//go:build integration

package hostedquery

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type cronPeriodReadTrace struct {
	read   chan struct{}
	once   sync.Once
	onRead func()
}

func (tr *cronPeriodReadTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(d.SQL, "SELECT payload FROM pooltune_lab_state.cron_periods") {
		tr.once.Do(func() {
			if tr.onRead != nil {
				tr.onRead()
			}
			close(tr.read)
		})
	}
	return ctx
}

type cronQueryCountTrace struct{ queries, prepares, batches, connections int }

func (tr *cronQueryCountTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	tr.queries++
	return ctx
}
func (*cronQueryCountTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (tr *cronQueryCountTrace) TracePrepareStart(ctx context.Context, _ *pgx.Conn, _ pgx.TracePrepareStartData) context.Context {
	tr.prepares++
	return ctx
}
func (*cronQueryCountTrace) TracePrepareEnd(context.Context, *pgx.Conn, pgx.TracePrepareEndData) {}
func (tr *cronQueryCountTrace) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	tr.batches++
	return ctx
}
func (*cronQueryCountTrace) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}
func (*cronQueryCountTrace) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData)     {}
func (tr *cronQueryCountTrace) TraceConnectStart(ctx context.Context, _ pgx.TraceConnectStartData) context.Context {
	tr.connections++
	return ctx
}
func (*cronQueryCountTrace) TraceConnectEnd(context.Context, pgx.TraceConnectEndData) {}

// Compare target round trips after the first missing-period read. The durable
// path has already paid its initial setup, so only its fresh child is counted.
// The polling path includes every added post-wait validation before the model.
func TestCronPublicationWaitReducesTargetHandoffWork(t *testing.T) {
	counts := map[string]int{}
	prepares := map[string]int{}
	batches := map[string]int{}
	connections := map[string]int{}
	for _, mode := range []string{"same_attempt", "durable_child"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cfg, _, state, fixture, f := cronProviderFixture(t, false)
			var third []byte
			cycle := f.FirstCycle.Add(4 * time.Minute)
			if err := state.QueryRow(ctx, "DELETE FROM pooltune_lab_state.cron_periods WHERE fixture=$1 AND cycle=$2 RETURNING payload", fixture.ID, cycle).Scan(&third); err != nil {
				t.Fatal(err)
			}
			counter := &cronQueryCountTrace{}
			guarded := cfg.Copy()
			guarded.Tracer = counter
			atRead := cronQueryCountTrace{}
			read := &cronPeriodReadTrace{read: make(chan struct{}), onRead: func() { atRead = *counter }}
			sc := state.Config().Copy()
			sc.Tracer = read
			consumer, err := pgx.ConnectConfig(ctx, sc)
			if err != nil {
				t.Fatal(err)
			}
			defer consumer.Close(ctx)
			publish := func() error {
				_, err := state.Exec(ctx, "INSERT INTO pooltune_lab_state.cron_periods(fixture,cycle,payload) VALUES($1,$2,$3::jsonb)", fixture.ID, cycle, string(third))
				return err
			}
			published := make(chan error, 1)
			if mode == "same_attempt" {
				go func() { <-read.read; time.Sleep(350 * time.Millisecond); published <- publish() }()
			}
			calls := 0
			investigate := func(_ context.Context, e IndexEvidence) (IndexProposal, error) {
				calls++
				counts[mode] = counter.queries
				prepares[mode] = counter.prepares
				batches[mode] = counter.batches
				connections[mode] = counter.connections
				if mode == "same_attempt" {
					counts[mode] -= atRead.queries
					prepares[mode] -= atRead.prepares
					batches[mode] -= atRead.batches
					connections[mode] -= atRead.connections
				}
				return IndexProposal{RunID: e.RunID, TableOID: e.TableOID, Column: e.Column, Outcome: "no_change", Reason: "measure admission work only"}, nil
			}
			result, err := DispatchFixtureCron(ctx, consumer, guarded, "local", fixture, investigate)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "same_attempt" {
				if err := <-published; err != nil {
					t.Fatal(err)
				}
			} else {
				if result == nil || result.Outcome != "awaiting_evidence" || calls != 0 {
					t.Fatalf("missing durable child: %+v", result)
				}
				if err := publish(); err != nil {
					t.Fatal(err)
				}
				if _, err := state.Exec(ctx, "UPDATE pooltune_worker.investigations SET not_before=clock_timestamp()-interval '1 second' WHERE project_id=$1 AND finished_at IS NULL", fixture.Enrollment.Project); err != nil {
					t.Fatal(err)
				}
				*counter = cronQueryCountTrace{}
				result, err = DispatchFixtureCron(ctx, consumer, guarded, "local", fixture, investigate)
			}
			if err != nil || result == nil || result.Outcome != "no_change" || calls != 1 {
				t.Fatalf("admission failed: %+v %v calls=%d", result, err, calls)
			}
		})
	}
	sameExchanges := counts["same_attempt"] + batches["same_attempt"] + prepares["same_attempt"]
	childExchanges := counts["durable_child"] + batches["durable_child"] + prepares["durable_child"]
	if sameExchanges >= childExchanges || connections["same_attempt"] != 0 || connections["durable_child"] != 1 {
		t.Fatalf("wait failed to reduce target work: queries=%+v batches=%+v prepares=%+v connections=%+v", counts, batches, prepares, connections)
	}
	t.Logf("target exchanges after missing-period read to model: same attempt=%d, durable child=%d; saved=%d exchanges plus one target connection", sameExchanges, childExchanges, childExchanges-sameExchanges)
	t.Logf("query calls=%+v batch sends=%+v preparations=%+v connections=%+v", counts, batches, prepares, connections)
}
func (*cronPeriodReadTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestCronEvidencePublicationContinuesSameAttempt(t *testing.T) {
	for _, name := range []string{"arrives", "times_out", "schedule_changed", "admission_expired"} {
		t.Run(name, func(t *testing.T) {
			publish := name != "times_out"
			ctx, cfg, admin, state, fixture, f := cronProviderFixture(t, false)
			var third []byte
			cycle := f.FirstCycle.Add(4 * time.Minute)
			if err := state.QueryRow(ctx, "DELETE FROM pooltune_lab_state.cron_periods WHERE fixture=$1 AND cycle=$2 RETURNING payload", fixture.ID, cycle).Scan(&third); err != nil {
				t.Fatal(err)
			}
			var original int64
			if err := state.QueryRow(ctx, "SELECT id FROM pooltune_worker.investigations WHERE project_id=$1", fixture.Enrollment.Project).Scan(&original); err != nil {
				t.Fatal(err)
			}
			read := &cronPeriodReadTrace{read: make(chan struct{})}
			sc := state.Config().Copy()
			sc.Tracer = read
			consumer, err := pgx.ConnectConfig(ctx, sc)
			if err != nil {
				t.Fatal(err)
			}
			defer consumer.Close(ctx)
			published := make(chan error, 1)
			if publish {
				go func() {
					select {
					case <-read.read:
					case <-ctx.Done():
						published <- ctx.Err()
						return
					}
					time.Sleep(350 * time.Millisecond)
					if name == "schedule_changed" {
						if _, err := admin.Exec(ctx, "UPDATE cron.job SET schedule='* * * * *' WHERE jobid=$1", f.Target.ID); err != nil {
							published <- err
							return
						}
					}
					if name == "admission_expired" {
						if _, err := admin.Exec(ctx, "UPDATE cron_adapter.scheduler_clock SET at=$1", cycle.Add(96*time.Second)); err != nil {
							published <- err
							return
						}
					}
					_, err := state.Exec(ctx, "INSERT INTO pooltune_lab_state.cron_periods(fixture,cycle,payload) VALUES($1,$2,$3::jsonb)", fixture.ID, cycle, string(third))
					published <- err
				}()
			}
			trace := &cronProviderTrace{}
			trace.afterClock = func(ctx context.Context, _ *pgx.Conn) {
				if _, err := admin.Exec(ctx, `INSERT INTO cron.job_run_details SELECT 20,2,42,at+interval '1 second',at+interval '2 seconds','succeeded' FROM cron_adapter.scheduler_clock;INSERT INTO cron.job_run_details SELECT 21,2,42,at+interval '3 seconds',at+interval '4 seconds','succeeded' FROM cron_adapter.scheduler_clock;UPDATE cron_adapter.scheduler_clock SET at=at+interval '5 seconds'`); err != nil {
					t.Fatal(err)
				}
			}
			guarded := cfg.Copy()
			guarded.Tracer = trace
			calls := 0
			started := time.Now()
			_, result, err := cronProviderRun(t, ctx, guarded, consumer, fixture, false, &calls)
			elapsed := time.Since(started)
			if publish {
				if e := <-published; e != nil {
					t.Fatal(e)
				}
			}
			if err != nil || result == nil || result.JobID != original {
				t.Fatalf("lost same attempt: %+v %v", result, err)
			}
			childrenWanted := 1
			if name == "schedule_changed" || name == "admission_expired" {
				childrenWanted = 0
				if result.Outcome != "inconclusive" || calls != 0 || trace.apiCalls != 0 {
					t.Fatalf("changed state admitted model/action: %+v model=%d api=%d", result, calls, trace.apiCalls)
				}
			} else if publish {
				if result.Outcome != "awaiting_verification" || len(result.Cron.Before) != 3 || calls != 1 || trace.apiCalls != 2 {
					t.Fatalf("imminent evidence was deferred: %+v model=%d api=%d", result, calls, trace.apiCalls)
				}
			} else if result.Outcome != "awaiting_evidence" || calls != 0 || trace.apiCalls != 0 || elapsed < 1500*time.Millisecond || elapsed > 3*time.Second {
				t.Fatalf("wait not bounded or unsafe: outcome=%s elapsed=%s model=%d api=%d", result.Outcome, elapsed, calls, trace.apiCalls)
			}
			var children int
			if err := consumer.QueryRow(ctx, "SELECT count(*) FROM pooltune_worker.investigations WHERE parent_id=$1 AND finished_at IS NULL", original).Scan(&children); err != nil || children != childrenWanted {
				t.Fatalf("missing durable continuation: %d %v", children, err)
			}
		})
	}
}
