//go:build integration

package hostedquery

import (
	"context"
	"testing"
	"time"
)

// Reproduce the failure shape after a valid proposal when model/transport time
// consumes the existing handoff margin. This is a local provider adapter, not
// proof of the precise error returned by the retained hosted attempt.
func TestCronValidProposalCannotOutrunPostModelHandoffMargin(t *testing.T) {
	ctx, cfg, admin, state, fixture, f := cronProviderFixture(t, false)
	last := f.FirstCycle.Add(4 * time.Minute)
	if _, err := admin.Exec(ctx, "UPDATE cron_adapter.scheduler_clock SET at=$1", last.Add(89975*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	trace := &cronProviderTrace{}
	guarded := cfg.Copy()
	guarded.Tracer = trace
	calls := 0
	result, err := DispatchFixtureCron(ctx, state, guarded, "local", fixture, func(ctx context.Context, e IndexEvidence) (IndexProposal, error) {
		calls++
		// A 6.506s model response plus 0.619s process/transaction transport moves
		// the observed clock from +89.975s to +97.100s, past the +97s margin.
		if _, err := admin.Exec(ctx, "UPDATE cron_adapter.scheduler_clock SET at=$1", last.Add(97100*time.Millisecond)); err != nil {
			return IndexProposal{}, err
		}
		return IndexProposal{RunID: e.RunID, TableOID: e.TableOID, Column: e.Column, Outcome: "propose", Repair: "cron", Reason: "qualified test periods"}, nil
	})
	if err != nil || result == nil || result.Outcome != "inconclusive" || result.Cron.Reason != "eligible handoff window expired after investigation" || result.Proposal == nil || result.Proposal.Repair != "cron" || result.GraphMS <= 0 || result.Cron.GraphMS != result.GraphMS || calls != 1 || trace.apiCalls != 0 {
		t.Fatalf("wrong expired-margin failure: result=%+v err=%v model=%d api=%d", result, err, calls, trace.apiCalls)
	}
	current, err := readCronJob(ctx, admin, f.Target.Name)
	if err != nil || current.Schedule != cronOriginal {
		t.Fatalf("late proposal changed schedule: %+v %v", current, err)
	}
	var status string
	var saved bool
	if err := state.QueryRow(ctx, "SELECT status FROM "+journalTable+" WHERE id=$1", fixture.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := state.QueryRow(ctx, "SELECT finished_at IS NOT NULL AND result->>'outcome'='inconclusive' AND result->'proposal'->>'repair'='cron' AND (result->>'graph_ms')::float8 > 0 AND result->'cron'->>'intent_at'='0001-01-01T00:00:00Z' AND attempts=1 FROM pooltune_worker.investigations WHERE project_id=$1", fixture.Enrollment.Project).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if status != "cron_declined" || !saved {
		t.Fatalf("unexpected mutation intent or missing durable outcome: status=%s saved=%v", status, saved)
	}
}
