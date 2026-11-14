//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

func TestVacuumRestoresVisibilityWithoutChangingResults(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "compute_query_id=on")
	target, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close(ctx)
	if _, err := target.Exec(ctx, "CREATE DATABASE vacuum_state"); err != nil {
		t.Fatal(err)
	}
	sc := config.Copy()
	sc.Database = "vacuum_state"
	state, err := pgx.ConnectConfig(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	f, err := CreateVacuumFixture(ctx, target, state, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := RemoveConsumerFixture(ctx, target, state, "local", f.ID); err != nil {
			t.Error(err)
		}
	}()
	objects, err := expectedFixture(ctx, state, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	index, err := vacuumIndexIdentity(ctx, state, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := sampleVacuum(ctx, target, f.ID, objects, index)
	if err != nil {
		t.Fatal(err)
	}
	if healthy.HeapFetches != 0 || healthy.ScanRows != 50000 {
		t.Fatalf("healthy: %+v", healthy)
	}
	if _, err := target.Exec(ctx, "UPDATE ONLY "+qualified(f.Schema, "candidate")+" SET padding=repeat('q',64)"); err != nil {
		t.Fatal(err)
	}
	dirty, err := sampleVacuum(ctx, target, f.ID, objects, index)
	if err != nil {
		t.Fatal(err)
	}
	if dirty.HeapFetches < 1000 || dirty.HeapFetches/float64(dirty.ScanRows) < .5 {
		t.Fatalf("fault missing: %+v", dirty)
	}
	applied, err := applyFixtureVacuum(ctx, target, config, f.ID, objects, index)
	if err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	after, err := sampleVacuum(ctx, target, f.ID, objects, index)
	if err != nil {
		t.Fatal(err)
	}
	if after.HeapFetches > dirty.HeapFetches*.5 || after.ScanRows != 50000 {
		t.Fatalf("repair missing: %+v", after)
	}
	outcome, reason := verifyVacuum(dirty, after)
	t.Logf("healthy %.3fms dirty %.3fms repaired %.3fms heap %.0f -> %.0f; %s: %s", healthy.ReadMS, dirty.ReadMS, after.ReadMS, dirty.HeapFetches, after.HeapFetches, outcome, reason)
	applied, err = applyFixtureVacuum(ctx, target, config, f.ID, objects, index)
	if err != nil || applied {
		t.Fatalf("healthy repeat: %v %v", applied, err)
	}
	// An active maintenance-compatible table lock and an old snapshot independently defer cleanup.
	blocker, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close(ctx)
	for _, mode := range []string{"maintenance", "snapshot", "idle writer"} {
		tx, err := blocker.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if mode == "maintenance" {
			_, err = tx.Exec(ctx, "LOCK TABLE "+qualified(f.Schema, "candidate")+" IN SHARE UPDATE EXCLUSIVE MODE")
		} else if mode == "idle writer" {
			_, err = tx.Exec(ctx, "SELECT pg_current_xact_id()")
			time.Sleep(5100 * time.Millisecond)
		} else {
			_, err = tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ; SELECT 1")
			if err == nil {
				_, err = tx.Exec(ctx, "SELECT 1 FROM "+qualified(f.Schema, "candidate")+" LIMIT 1")
			}
			time.Sleep(5100 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		clear, err := vacuumConditions(ctx, target, objects.Candidate, 0)
		if err != nil || clear {
			t.Fatalf("%s was not deferred: %v %v", mode, clear, err)
		}
		tx.Rollback(ctx)
	}
	if _, err := target.Exec(ctx, "DROP INDEX "+qualified(f.Schema, trialIndex)); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(ctx, "CREATE INDEX "+pgx.Identifier{trialIndex}.Sanitize()+" ON "+qualified(f.Schema, "candidate")+" (lookup)"); err != nil {
		t.Fatal(err)
	}
	if _, err := sampleVacuum(ctx, target, f.ID, objects, index); err == nil {
		t.Fatal("replacement index accepted")
	}
}

// The dispatch test uses real query counters for identity and synthetic queued latency
// windows only to make detector admission independent of the test host's speed.
func TestQueuedVacuumRejectsUnsafeOutcomesAndReconcilesWithoutRepeat(t *testing.T) {
	for _, mode := range []string{"apply", "healthy", "wrong repair", "wrong identity", "decline", "provider failure", "changed index", "external vacuum", "idle writer before", "idle writer during"} {
		t.Run(mode, func(t *testing.T) { exerciseQueuedVacuum(t, mode) })
	}
}
func exerciseQueuedVacuum(t *testing.T, mode string) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	target, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close(ctx)
	for _, sql := range []string{"CREATE EXTENSION pg_stat_statements", "CREATE DATABASE vacuum_state"} {
		if _, err := target.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	sc := config.Copy()
	sc.Database = "vacuum_state"
	state, err := pgx.ConnectConfig(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	f, err := CreateVacuumFixture(ctx, target, state, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := RemoveConsumerFixture(ctx, target, state, "local", f.ID); err != nil {
			t.Error(err)
		}
	}()

	var pinned pgx.Tx
	blocker, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close(ctx)
	defer func() {
		if pinned != nil {
			pinned.Rollback(ctx)
		}
	}()
	pinWriter := func() {
		var err error
		pinned, err = blocker.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pinned.Exec(ctx, "SELECT pg_current_xact_id()"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5100 * time.Millisecond)
	}
	if mode == "idle writer before" {
		pinWriter()
	}
	if mode != "healthy" {
		if _, err := target.Exec(ctx, "UPDATE ONLY "+qualified(f.Schema, "candidate")+" SET padding=repeat('q',64)"); err != nil {
			t.Fatal(err)
		}
	}
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(ctx, target, state, f.Enrollment, SamplingPolicy{Interval: time.Second, Window: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	if err := target.QueryRow(ctx, vacuumQuery(f.Schema), int32(1), int32(50000)).Scan(&sum); err != nil || sum != 1250025000 {
		t.Fatalf("result %d %v", sum, err)
	}
	counters, err := worker.collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := f.Enrollment.Queries[1].key()
	c := counters[key]
	if c.Calls != 1 {
		t.Fatalf("missing query identity: %+v", c)
	}
	start := time.Now().Add(-5 * time.Second)
	for tick := range 6 {
		c.Calls++
		c.ExecutionMS += 100
		if _, err := recordPoll(ctx, state, f.Enrollment, worker.policy, start.Add(time.Duration(tick)*time.Second), func(context.Context) (map[queryKey]Counter, error) { return map[queryKey]Counter{key: c}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	countSQL := "SELECT coalesce(sum(calls),0) FROM public.pg_stat_statements WHERE query LIKE 'VACUUM %' AND query LIKE $1"
	count := func() int64 {
		var n int64
		if err := target.QueryRow(ctx, countSQL, "%"+f.Schema+"%").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	modelCalled := false
	result, err := DispatchFixtureVacuum(ctx, state, config, "local", f, func(_ context.Context, e IndexEvidence) (IndexProposal, error) {
		modelCalled = true
		if mode == "healthy" || mode == "idle writer before" {
			t.Fatal("healthy or deferred evidence reached model")
		}
		if !vacuumCause(e) {
			t.Fatalf("weak model evidence: %+v", e)
		}
		p := IndexProposal{Outcome: "propose", Repair: "vacuum", RunID: e.RunID, TableOID: e.TableOID, Column: e.Column, Reason: "lost visibility"}
		switch mode {
		case "idle writer during":
			pinWriter()
		case "wrong repair":
			p.Repair = "analyze"
		case "wrong identity":
			p.TableOID++
		case "decline":
			p.Outcome, p.Repair = "no_change", ""
		case "provider failure":
			return IndexProposal{}, context.DeadlineExceeded
		case "changed index":
			if _, err := target.Exec(ctx, "ALTER INDEX "+qualified(f.Schema, trialIndex)+" SET (fillfactor=80)"); err != nil {
				t.Fatal(err)
			}
		case "external vacuum":
			if _, err := target.Exec(ctx, "VACUUM (TRUNCATE FALSE,PARALLEL 0) "+qualified(f.Schema, "candidate")); err != nil {
				t.Fatal(err)
			}
		}
		return p, nil
	})
	if mode != "healthy" && mode != "idle writer before" && !modelCalled {
		t.Fatalf("expected eligible evidence to reach model: %v", err)
	}
	if mode == "wrong repair" || mode == "wrong identity" || mode == "provider failure" || mode == "changed index" {
		if err == nil || result != nil {
			t.Fatalf("invalid outcome accepted: %+v %v", result, err)
		}
		if count() != before {
			t.Fatal("invalid outcome caused VACUUM")
		}
		return
	}
	if err != nil || result == nil || result.Vacuum == nil {
		t.Fatalf("dispatch: %+v %v", result, err)
	}
	if mode != "apply" {
		wantOutcome := "no_change"
		if mode == "idle writer before" {
			wantOutcome = "deferred"
		}
		if result.Outcome != wantOutcome || result.Vacuum.VacuumMS != 0 {
			t.Fatalf("unexpected repair %+v", result)
		}
		want := before
		if mode == "external vacuum" {
			want++
		}
		if count() != want {
			t.Fatal("unexpected repeated VACUUM")
		}
		return
	}
	r := result.Vacuum
	if r.After.HeapFetches > r.Before.HeapFetches*.5 || count() != before+1 {
		t.Fatalf("missing repair: %+v", r)
	}
	for _, status := range []string{"vacuum_verified", "vacuum_pending"} {
		if err := writeVacuumStatus(ctx, state, status, r); err != nil {
			t.Fatal(err)
		}
		if _, err := state.Exec(ctx, "UPDATE pooltune_worker.investigations SET attempts=1,finished_at=NULL,lease_until=now()-interval '1 second',result=NULL WHERE id=$1", result.JobID); err != nil {
			t.Fatal(err)
		}
		retry, err := DispatchFixtureVacuum(ctx, state, config, "local", f, func(context.Context, IndexEvidence) (IndexProposal, error) {
			t.Fatal("recovery called model")
			return IndexProposal{}, nil
		})
		if err != nil || retry == nil || retry.Vacuum == nil {
			t.Fatalf("retry: %+v %v", retry, err)
		}
		if status == "vacuum_pending" && (retry.Outcome != "repair_interrupted" || retry.Vacuum.After.Reads != 10) {
			t.Fatalf("pending not inspected %+v", retry)
		}
		if count() != before+1 {
			t.Fatal("recovery repeated VACUUM")
		}
	}
}

func vacuumIndexIdentity(ctx context.Context, state *pgx.Conn, id string) (uint32, error) {
	var oid uint32
	err := state.QueryRow(ctx, "SELECT (report->>'index_oid')::oid FROM "+journalTable+" WHERE id=$1", id).Scan(&oid)
	if err == nil && oid == 0 {
		err = errors.New("vacuum fixture has no durable index identity")
	}
	return oid, err
}

func TestVacuumPreparationFailureAndCrashAreRecoverable(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "compute_query_id=on")
	target, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close(ctx)
	if _, err := target.Exec(ctx, "CREATE DATABASE preparation_state"); err != nil {
		t.Fatal(err)
	}
	sc := config.Copy()
	sc.Database = "preparation_state"
	state, err := pgx.ConnectConfig(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	// Interrupt exactly when the committed seed is about to enter nontransaction VACUUM.
	trace := &interruptVacuumTrace{}
	failingConfig := config.Copy()
	failingConfig.Tracer = trace
	failingTarget, err := pgx.ConnectConfig(ctx, failingConfig)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := CreateVacuumFixture(ctx, failingTarget, state, "local")
	failingTarget.Close(ctx)
	if err == nil || failed.ID == "" || !trace.seen {
		t.Fatalf("expected interruption at setup VACUUM, got %+v %v, seen=%v", failed, err, trace.seen)
	}
	var gone bool
	if err := target.QueryRow(ctx, "SELECT to_regnamespace($1) IS NULL", failed.Schema).Scan(&gone); err != nil || !gone {
		t.Fatalf("failed setup leaked committed fixture: %v %v", gone, err)
	}
	// Recreate the durable state of a process dying after seed commit. Next startup
	// must recover only the unfinished preparation, leaving a ready fixture intact.
	crashed, err := CreateVacuumFixture(ctx, target, state, "local")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, "UPDATE "+journalTable+" SET status='vacuum_preparing' WHERE id=$1", crashed.ID); err != nil {
		t.Fatal(err)
	}
	active, err := CreateVacuumFixture(ctx, target, state, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := RemoveConsumerFixture(ctx, target, state, "local", active.ID); err != nil {
			t.Error(err)
		}
	}()
	if err := target.QueryRow(ctx, "SELECT to_regnamespace($1) IS NULL", crashed.Schema).Scan(&gone); err != nil || !gone {
		t.Fatalf("startup did not reconcile interrupted preparation: %v %v", gone, err)
	}
	next, err := CreateVacuumFixture(ctx, target, state, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := RemoveConsumerFixture(ctx, target, state, "local", next.ID); err != nil {
			t.Error(err)
		}
	}()
	if err := target.QueryRow(ctx, "SELECT to_regnamespace($1) IS NULL", active.Schema).Scan(&gone); err != nil || gone {
		t.Fatalf("startup removed healthy ready fixture: %v %v", gone, err)
	}
	// Ownership drift must prevent automatic cleanup of a preparation.
	if _, err := state.Exec(ctx, "UPDATE "+journalTable+" SET status='vacuum_preparing' WHERE id=$1", active.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(ctx, "COMMENT ON SCHEMA "+pgx.Identifier{active.Schema}.Sanitize()+" IS 'replacement owner marker'"); err != nil {
		t.Fatal(err)
	}
	_, err = CreateVacuumFixture(ctx, target, state, "local")
	if err == nil {
		t.Fatal("startup accepted changed ownership")
	}
	if err := target.QueryRow(ctx, "SELECT to_regnamespace($1) IS NULL", active.Schema).Scan(&gone); err != nil || gone {
		t.Fatalf("startup removed changed fixture: %v %v", gone, err)
	}
	if _, err := target.Exec(ctx, "COMMENT ON SCHEMA "+pgx.Identifier{active.Schema}.Sanitize()+" IS 'pooltune trial "+active.ID+"'"); err != nil {
		t.Fatal(err)
	}
}

// A test-owned pgx tracing hook injects process interruption at the actual query
// boundary, after seed commit. It adds no test-only hook to production code.
type interruptVacuumTrace struct{ seen bool }

func (t *interruptVacuumTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "VACUUM ") {
		t.seen = true
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		return ctx
	}
	return ctx
}
func (*interruptVacuumTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
