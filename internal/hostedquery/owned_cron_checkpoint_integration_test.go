//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

func newOwnedCronCheckpoint(t *testing.T, at time.Time) (context.Context, *pgx.ConnConfig, *pgx.Conn, int64, investigationClaim, OwnedCronRequest, OwnedCronAction) {
	t.Helper()
	ctx, cfg, _ := testpg.Start(t)
	worker, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Close(ctx) })
	if err := PrepareWorkerState(ctx, worker); err != nil {
		t.Fatal(err)
	}
	if err := PrepareOwnedCronCheckpoints(ctx, worker); err != nil {
		t.Fatal(err)
	}
	const project = "supabase/" + OwnedIndexRookeryRef + "/0123456789abcdef"
	epoch := int64(3)
	if _, err := worker.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,$2)`, project, epoch); err != nil {
		t.Fatal(err)
	}
	var jobID int64
	err = worker.QueryRow(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence)
		VALUES($1,11,22,-33,'{}') RETURNING id`, project).Scan(&jobID)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := claimProjectInvestigation(ctx, worker, project, &epoch)
	if err != nil || parent == nil || parent.ID != jobID {
		t.Fatalf("claim: %+v %v", parent, err)
	}
	first := cronCycle(at.UTC())
	future := first.Add(6 * time.Minute)
	due := future.Add(6*time.Minute + 2*time.Second)
	req := OwnedCronRequest{Target: ProjectCronTarget{Project: project, DatabaseOID: 11, OwnerOID: 22, JobID: 31, WitnessJobID: 32},
		Observed:   Counter{Database: 11, Role: 22, Query: -33, Boot: first, Reset: first, Entry: first},
		FirstCycle: first, Expires: first.Add(20 * time.Minute)}
	intent := OwnedCronAction{Intent: ProjectCronIntent{Target: req.Target, Window: ownedCronWindow(),
		Before:     ProjectCronSnapshot{Schedule: cronOriginal, Fingerprint: "frozen fields", Generation: "old"},
		Generation: CronGeneration{PID: 11, Started: first, Postmaster: first}, ProposedSchedule: cronAlternate,
		RunID: "owned-cron-test", FirstFutureCycle: future}, Observed: req.Observed,
		Report: CronReport{JobID: jobID, IntentAt: future.Add(-25 * time.Second), FirstFutureCycle: future, VerifyAfter: due}}
	if err := recordOwnedProjectAction(ctx, worker, project, epoch, *parent, "cron", intent); err != nil {
		t.Fatal(err)
	}
	return ctx, cfg, worker, epoch, *parent, req, intent
}

func TestOwnedCronCheckpointSurvivesRestartAndFencesSchedulePublication(t *testing.T) {
	for _, tc := range []struct {
		name   string
		offset time.Duration
	}{
		{name: "early"},
		{name: "due", offset: -14 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) { testOwnedCronCheckpoint(t, tc.offset) })
	}
}

func testOwnedCronCheckpoint(t *testing.T, offset time.Duration) {
	t.Helper()
	ctx, cfg, worker, epoch, parent, req, intent := newOwnedCronCheckpoint(t, time.Now().Add(offset))
	jobID, project := parent.ID, parent.Project
	future, due := intent.Report.FirstFutureCycle, intent.Report.VerifyAfter
	saved, err := readOwnedCronCheckpoint(ctx, worker, parent)
	if err != nil || saved == nil || saved.IsAcknowledged || saved.Origin != jobID {
		t.Fatalf("read-only recovery could not see independent intent: %+v %v", saved, err)
	}
	ack := intent
	ack.After = ProjectCronSnapshot{Schedule: cronAlternate, Fingerprint: "frozen fields", Generation: "new"}
	ack.Report.CommitAt = future.Add(-18 * time.Second)
	ack.Report.PostWitness = []CronRun{
		{ID: 91, JobID: req.Target.WitnessJobID, PID: 1, Start: future.Add(-17 * time.Second), End: future.Add(-16 * time.Second), Status: "succeeded"},
		{ID: 92, JobID: req.Target.WitnessJobID, PID: 2, Start: future.Add(-15 * time.Second), End: future.Add(-14 * time.Second), Status: "succeeded"},
	}
	if ownedCronAcknowledges(intent, ack) {
		t.Fatal("acknowledged a witness outside the approved 15-second handoff margin")
	}
	ack.Report.CommitAt = future.Add(-22 * time.Second)
	for index := range ack.Report.PostWitness {
		ack.Report.PostWitness[index].Start = ack.Report.PostWitness[index].Start.Add(-4 * time.Second)
		ack.Report.PostWitness[index].End = ack.Report.PostWitness[index].End.Add(-4 * time.Second)
	}
	waiting := deferOwnedCron(ack.Report, "awaiting_verification", due)
	if _, err := publishOwnedCronCheckpoint(ctx, worker, epoch+1, parent, waiting, &ack); !errors.Is(err, errClaimLost) {
		t.Fatalf("stale epoch published action: %v", err)
	}
	foreign := ack
	foreign.Observed.Query++
	if _, err := publishOwnedCronCheckpoint(ctx, worker, epoch, parent, waiting, &foreign); err == nil {
		t.Fatal("foreign acknowledgement published action")
	}
	child, err := publishOwnedCronCheckpoint(ctx, worker, epoch, parent, waiting, &ack)
	if err != nil || child <= 0 {
		t.Fatalf("publish continuation: %d %v", child, err)
	}
	if _, err := publishOwnedCronCheckpoint(ctx, worker, epoch, parent, waiting, &ack); !errors.Is(err, errClaimLost) {
		t.Fatalf("old lease republished after a lost acknowledgement: %v", err)
	}
	if err := worker.Close(ctx); err != nil {
		t.Fatal(err)
	}
	worker, err = pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Close(ctx) })
	if offset == 0 {
		if next, err := claimProjectInvestigation(ctx, worker, project, &epoch); err != nil || next != nil {
			t.Fatalf("future continuation ran before it was due: %+v %v", next, err)
		}
		if _, err := worker.Exec(ctx, `UPDATE pooltune_worker.investigations SET not_before=clock_timestamp() WHERE id=$1`, child); err != nil {
			t.Fatal(err)
		}
	}
	continued, err := claimProjectInvestigation(ctx, worker, project, &epoch)
	if err != nil || continued == nil || continued.ID != child {
		t.Fatalf("restart lost exact child: %+v %v", continued, err)
	}
	saved, err = readOwnedCronCheckpoint(ctx, worker, *continued)
	if err != nil || saved == nil || !saved.IsAcknowledged || !saved.Action.Report.CommitAt.Equal(ack.Report.CommitAt) {
		t.Fatalf("restart lost exact proof: %+v %v", saved, err)
	}
	after := ack.Report
	after.Outcome = "improved_tested_inputs"
	_, err = publishOwnedCronCheckpoint(ctx, worker, epoch, *continued, OwnedCronDecision{Report: after}, nil)
	if offset == 0 {
		if err == nil {
			t.Fatal("reported verification before the approved observation window elapsed")
		}
		return
	}
	if err != nil {
		t.Fatalf("finish verified child: %v", err)
	}
	var outcome, result string
	if err := worker.QueryRow(ctx, `SELECT a.outcome,i.result->>'outcome' FROM pooltune_worker.owned_project_actions a
		JOIN pooltune_worker.investigations i ON i.parent_id=a.job_id
		WHERE a.job_id=$1 AND i.id=$2`, jobID, child).Scan(&outcome, &result); err != nil || outcome != after.Outcome || result != outcome {
		t.Fatalf("exact action and result did not settle together: %q %q %v", outcome, result, err)
	}
}

func TestOwnedCronCheckpointRecoversExhaustedUnacknowledgedAction(t *testing.T) {
	ctx, _, state, epoch, origin, req, intent := newOwnedCronCheckpoint(t, time.Now())
	if _, err := state.Exec(ctx, `UPDATE pooltune_worker.investigations SET attempts=$2,lease_until=clock_timestamp()
		WHERE id=$1`, origin.ID, maxInvestigationAttempts); err != nil {
		t.Fatal(err)
	}
	if job, err := claimProjectInvestigation(ctx, state, origin.Project, &epoch); err != nil || job != nil {
		t.Fatalf("ordinary retry exhaustion: %+v %v", job, err)
	}
	if _, err := ensureOwnedCronCheckpointClaim(ctx, state, epoch+1, req.Target, req.Observed.Query); !errors.Is(err, errClaimLost) {
		t.Fatalf("stale epoch queued inspection: %v", err)
	}
	foreign := req.Target
	foreign.WitnessJobID++
	if id, err := ensureOwnedCronCheckpointClaim(ctx, state, epoch, foreign, req.Observed.Query); id != 0 || err != nil {
		t.Fatalf("foreign fixture queued inspection: %d %v", id, err)
	}
	child, err := ensureOwnedCronCheckpointClaim(ctx, state, epoch, req.Target, req.Observed.Query)
	if err != nil || child <= 0 {
		t.Fatalf("pending action was stranded after retry exhaustion: %d %v", child, err)
	}
	if id, err := ensureOwnedCronCheckpointClaim(ctx, state, epoch, req.Target, req.Observed.Query); id != 0 || err != nil {
		t.Fatalf("duplicate recovery: %d %v", id, err)
	}
	claim, err := claimProjectInvestigation(ctx, state, origin.Project, &epoch)
	if err != nil || claim == nil || claim.ID != child || claim.ParentID != origin.ID {
		t.Fatalf("wrong recovered claim: %+v %v", claim, err)
	}
	checkpoint, err := readOwnedCronCheckpoint(ctx, state, *claim)
	if err != nil || checkpoint == nil || checkpoint.IsAcknowledged || checkpoint.Origin != origin.ID {
		t.Fatalf("recovery lost the interrupted action: %+v %v", checkpoint, err)
	}
	intent.Report.Outcome = "improved_tested_inputs"
	if _, err := publishOwnedCronCheckpoint(ctx, state, epoch, *claim, OwnedCronDecision{Report: intent.Report}, nil); err == nil {
		t.Fatal("reported a verified repair without commit acknowledgement")
	}
	intent.Report.Outcome = "inconclusive"
	if _, err := publishOwnedCronCheckpoint(ctx, state, epoch, *claim, OwnedCronDecision{Report: intent.Report}, nil); err != nil {
		t.Fatalf("settle interrupted repair after inspection: %v", err)
	}
	if id, err := ensureOwnedCronCheckpointClaim(ctx, state, epoch, req.Target, req.Observed.Query); id != 0 || err != nil {
		t.Fatalf("settled repair reopened: %d %v", id, err)
	}
}
