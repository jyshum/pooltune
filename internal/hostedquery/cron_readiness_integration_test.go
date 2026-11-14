//go:build integration

package hostedquery

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Real queue/journal commits and fresh consumer connections exercise the save
// interleaving. The reused provider adapter's periods/scheduler are synthetic.
func TestCronEvidenceReadinessWaitsForCommittedThirdPeriod(t *testing.T) {
	ctx, cfg, admin, state, fixture, f := cronProviderFixture(t, false)
	var third []byte
	cycle := f.FirstCycle.Add(4 * time.Minute)
	if err := state.QueryRow(ctx, "DELETE FROM pooltune_lab_state.cron_periods WHERE fixture=$1 AND cycle=$2 RETURNING payload", fixture.ID, cycle).Scan(&third); err != nil {
		t.Fatal(err)
	}
	trace := &cronProviderTrace{}
	guarded := cfg.Copy()
	guarded.Tracer = trace
	calls := 0
	var prior int64
	for attempt := 0; attempt < 2; attempt++ {
		_, result, err := cronProviderRun(t, ctx, guarded, state, fixture, false, &calls)
		if err != nil || result == nil || result.Outcome != "awaiting_evidence" || len(result.Cron.Before) != 2 {
			t.Fatalf("partial save became terminal: %+v %v", result, err)
		}
		if calls != 0 || trace.apiCalls != 0 {
			t.Fatal("incomplete evidence invoked model or action")
		}
		if prior != 0 && result.JobID != prior {
			t.Fatal("did not resume the linked child")
		}
		var child, parent int64
		var due time.Time
		var finished bool
		if err = state.QueryRow(ctx, `SELECT c.id,c.parent_id,c.not_before,p.finished_at IS NOT NULL FROM pooltune_worker.investigations c JOIN pooltune_worker.investigations p ON p.id=c.parent_id WHERE c.finished_at IS NULL AND c.project_id=$1`, fixture.Enrollment.Project).Scan(&child, &parent, &due, &finished); err != nil {
			t.Fatal(err)
		}
		var now time.Time
		if err = admin.QueryRow(ctx, "SELECT at FROM cron_adapter.scheduler_clock").Scan(&now); err != nil {
			t.Fatal(err)
		}
		if parent != result.JobID || !finished || !due.Equal(now.Add(time.Second)) {
			t.Fatalf("wrong atomic readiness continuation: parent=%d due=%s finished=%v", parent, due, finished)
		}
		prior = child
		if _, err = admin.Exec(ctx, "UPDATE cron_adapter.scheduler_clock SET at=at+interval '1 second'"); err != nil {
			t.Fatal(err)
		}
		// The adapter uses a fixed historical DB clock; explicitly make that saved
		// deadline due in the independent real state clock, without changing ancestry.
		if _, err = state.Exec(ctx, "UPDATE pooltune_worker.investigations SET not_before=clock_timestamp()-interval '1 second' WHERE id=$1", child); err != nil {
			t.Fatal(err)
		}
	}
	stateConfig := state.Config().Copy()
	state.Close(ctx)
	resumed, err := pgx.ConnectConfig(ctx, stateConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close(ctx)
	if _, err = resumed.Exec(ctx, "INSERT INTO pooltune_lab_state.cron_periods(fixture,cycle,payload) VALUES($1,$2,$3::jsonb)", fixture.ID, cycle, string(third)); err != nil {
		t.Fatal(err)
	}
	trace.afterClock = func(ctx context.Context, _ *pgx.Conn) {
		if _, err := admin.Exec(ctx, `INSERT INTO cron.job_run_details SELECT 20,2,42,at+interval '1 second',at+interval '2 seconds','succeeded' FROM cron_adapter.scheduler_clock;INSERT INTO cron.job_run_details SELECT 21,2,42,at+interval '3 seconds',at+interval '4 seconds','succeeded' FROM cron_adapter.scheduler_clock;UPDATE cron_adapter.scheduler_clock SET at=at+interval '5 seconds'`); err != nil {
			t.Fatal(err)
		}
	}
	_, result, err := cronProviderRun(t, ctx, guarded, resumed, fixture, false, &calls)
	if err != nil || result == nil || result.JobID != prior || result.Outcome != "awaiting_verification" || len(result.Cron.Before) != 3 || calls != 1 || trace.apiCalls != 2 {
		t.Fatalf("fresh consumer missed committed evidence: %+v %v model=%d api=%d", result, err, calls, trace.apiCalls)
	}
	if _, err = resumed.Exec(ctx, "UPDATE pooltune_worker.investigations SET not_before=clock_timestamp()-interval '1 second' WHERE finished_at IS NULL"); err != nil {
		t.Fatal(err)
	}
	_, followup, err := cronProviderRun(t, ctx, guarded, resumed, fixture, false, &calls)
	if err != nil || followup == nil || followup.Outcome != "awaiting_verification" || calls != 1 || trace.apiCalls != 2 {
		t.Fatalf("followup repeated action/model: %+v %v model=%d api=%d", followup, err, calls, trace.apiCalls)
	}
}

func TestCronEvidenceReadinessStopsAtAdmissionCutoff(t *testing.T) {
	for _, complete := range []bool{false, true} {
		name := "missing"
		if complete {
			name = "complete"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cfg, admin, state, fixture, f := cronProviderFixture(t, false)
			cutoff := f.FirstCycle.Add(4*time.Minute + 96*time.Second)
			if !complete {
				if _, err := state.Exec(ctx, "DELETE FROM pooltune_lab_state.cron_periods WHERE fixture=$1 AND cycle=$2", fixture.ID, f.FirstCycle.Add(4*time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := admin.Exec(ctx, "UPDATE cron_adapter.scheduler_clock SET at=$1", cutoff.Add(-250*time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			calls := 0
			trace := &cronProviderTrace{}
			guarded := cfg.Copy()
			guarded.Tracer = trace
			if !complete {
				_, result, err := cronProviderRun(t, ctx, guarded, state, fixture, false, &calls)
				if err != nil || result == nil || result.Outcome != "awaiting_evidence" {
					t.Fatalf("premature terminal result: %+v %v", result, err)
				}
				var due time.Time
				if err = state.QueryRow(ctx, "SELECT not_before FROM pooltune_worker.investigations WHERE project_id=$1 AND finished_at IS NULL", fixture.Enrollment.Project).Scan(&due); err != nil || !due.Equal(cutoff) {
					t.Fatalf("readiness continuation crossed cutoff: %s %v", due, err)
				}
				if _, err = state.Exec(ctx, "UPDATE pooltune_worker.investigations SET not_before=clock_timestamp()-interval '1 second' WHERE finished_at IS NULL"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := admin.Exec(ctx, "UPDATE cron_adapter.scheduler_clock SET at=$1", cutoff); err != nil {
				t.Fatal(err)
			}
			_, result, err := cronProviderRun(t, ctx, guarded, state, fixture, false, &calls)
			if err != nil || result == nil || result.Outcome != "inconclusive" || result.Cron.Reason != "eligible handoff window expired before model admission" || calls != 0 || trace.apiCalls != 0 {
				t.Fatalf("cutoff admitted or silently rescheduled work: %+v %v model=%d api=%d", result, err, calls, trace.apiCalls)
			}
			var open int
			if err = state.QueryRow(ctx, "SELECT count(*) FROM pooltune_worker.investigations WHERE project_id=$1 AND finished_at IS NULL", fixture.Enrollment.Project).Scan(&open); err != nil || open != 0 {
				t.Fatalf("cutoff left another readiness child: %d %v", open, err)
			}
		})
	}
}

func TestCronEvidenceReadinessRefusesMalformedCompletePeriods(t *testing.T) {
	ctx, cfg, _, state, fixture, _ := cronProviderFixture(t, false)
	if _, err := state.Exec(ctx, `UPDATE pooltune_lab_state.cron_periods SET payload=jsonb_set(payload,'{busy,target,errors}','1') WHERE fixture=$1`, fixture.ID); err != nil {
		t.Fatal(err)
	}
	calls := 0
	trace := &cronProviderTrace{}
	guarded := cfg.Copy()
	guarded.Tracer = trace
	_, result, err := cronProviderRun(t, ctx, guarded, state, fixture, false, &calls)
	if err != nil || result == nil || result.Outcome != "inconclusive" || calls != 0 || trace.apiCalls != 0 {
		t.Fatalf("malformed complete evidence admitted: %+v %v model=%d api=%d", result, err, calls, trace.apiCalls)
	}
	var open int
	if err = state.QueryRow(ctx, "SELECT count(*) FROM pooltune_worker.investigations WHERE project_id=$1 AND finished_at IS NULL", fixture.Enrollment.Project).Scan(&open); err != nil || open != 0 {
		t.Fatalf("malformed evidence was deferred: %d %v", open, err)
	}
}
