//go:build integration

package hostedquery

import (
	"errors"
	"testing"
	"time"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestOwnedIndexIntentFencesExpiredAndReplacedClaims(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	const project = "synthetic-owned-index"
	if _, err := state.Exec(ctx, "INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,3)", project); err != nil {
		t.Fatal(err)
	}
	var claim investigationClaim
	claim.Project, claim.Attempt = project, 1
	err = state.QueryRow(ctx, `INSERT INTO pooltune_worker.investigations
		(project_id,database_oid,role_oid,query_id,evidence,attempts,lease_until)
		VALUES($1,1,2,3,'{}'::jsonb,1,clock_timestamp()+interval '30 seconds') RETURNING id`, project).Scan(&claim.ID)
	if err != nil {
		t.Fatal(err)
	}
	intent := ProjectIndexActionIntent{RunID: "exact"}
	if err := recordOwnedIndexIntent(ctx, state, project, 2, claim, intent); !errors.Is(err, errClaimLost) {
		t.Fatalf("stale registration recorded intent: %v", err)
	}
	if _, err := state.Exec(ctx, "UPDATE pooltune_worker.investigations SET lease_until=clock_timestamp() WHERE id=$1", claim.ID); err != nil {
		t.Fatal(err)
	}
	if err := recordOwnedIndexIntent(ctx, state, project, 3, claim, intent); !errors.Is(err, errClaimLost) {
		t.Fatalf("expired claim recorded intent: %v", err)
	}
	if _, err := state.Exec(ctx, "UPDATE pooltune_worker.investigations SET lease_until=clock_timestamp()+interval '30 seconds' WHERE id=$1", claim.ID); err != nil {
		t.Fatal(err)
	}
	if err := recordOwnedIndexIntent(ctx, state, project, 3, claim, intent); err != nil {
		t.Fatal(err)
	}
	if err := recordOwnedIndexIntent(ctx, state, project, 3, claim, intent); !errors.Is(err, errClaimLost) {
		t.Fatalf("duplicate action intent was accepted: %v", err)
	}
	fence, err := lockOwnedIndexEpoch(ctx, state, project, 3, claim)
	if err != nil {
		t.Fatal(err)
	}
	concurrent, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer concurrent.Close(ctx)
	if _, err := concurrent.Exec(ctx, "SET lock_timeout='100ms'"); err != nil {
		t.Fatal(err)
	}
	_, err = concurrent.Exec(ctx, "UPDATE pooltune_worker.projects SET monitor_epoch=4 WHERE project_id=$1", project)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("permission change was not fenced during action: %v", err)
	}
	if err := fence.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	result := DispatchResult{
		Outcome: "improved_tested_inputs",
		Repair: &IndexTrialReport{Measurement: "owned_single_input", IndexValid: true,
			Before: TrialSample{Reads: 1, ReadMS: 12, Blocks: 600, Plan: "Seq Scan"},
			After:  TrialSample{Reads: 1, ReadMS: 1, Blocks: 4, Plan: "Index Scan"}},
	}
	if err := finishInvestigation(ctx, state, claim, result, 2); !errors.Is(err, errClaimLost) {
		t.Fatalf("stale registration published action: %v", err)
	}
	if err := finishInvestigation(ctx, state, claim, result, 3); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := state.QueryRow(ctx, "SELECT outcome FROM pooltune_worker.owned_index_actions WHERE job_id=$1", claim.ID).Scan(&outcome); err != nil || outcome != result.Outcome {
		t.Fatalf("published result left intent unresolved: %q %v", outcome, err)
	}
	tx, err := state.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	jobs, err := readProjectJobs(ctx, tx, project, time.Now().Add(-time.Minute))
	if err != nil || len(jobs) != 1 || jobs[0].PlanSource != "owned_sample" || jobs[0].Repair == nil ||
		len(jobs[0].Repair.Metrics) != 2 || jobs[0].Repair.Metrics[0].Label != "Measured latency for one input" {
		t.Fatalf("published outcome lost measurement scope: %+v %v", jobs, err)
	}
}
