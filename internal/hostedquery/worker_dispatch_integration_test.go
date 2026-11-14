//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"testing"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestInvestigationClaimAndRecovery(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, "INSERT INTO pooltune_worker.projects(project_id) VALUES ('trial')"); err != nil {
		t.Fatal(err)
	}
	var id int64
	err = state.QueryRow(ctx, `INSERT INTO pooltune_worker.investigations
		(project_id,database_oid,role_oid,query_id,evidence) VALUES ('trial',1,2,3,'{}') RETURNING id`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	first, err := claimInvestigation(ctx, state, "trial")
	if err != nil || first == nil || first.ID != id || first.Attempt != 1 {
		t.Fatalf("first claim: %+v %v", first, err)
	}
	otherConfig := config.Copy()
	other, err := pgx.ConnectConfig(ctx, otherConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close(ctx)
	for _, project := range []string{"other", "trial"} {
		job, err := claimInvestigation(ctx, other, project)
		if err != nil || job != nil {
			t.Fatalf("competing claim in %s: %+v %v", project, job, err)
		}
	}
	producer, err := other.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := producer.Exec(ctx, "SELECT FROM pooltune_worker.projects WHERE project_id='trial' FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	// A finishing job must wait for the producer's check of both the cooldown
	// record and the open-job constraint. Otherwise neither can see the job.
	if _, err := state.Exec(ctx, "SET lock_timeout='100ms'"); err != nil {
		t.Fatal(err)
	}
	err = finishInvestigation(ctx, state, *first, DispatchResult{Outcome: "no_change"})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("completion bypassed project coordination: %v", err)
	}
	if err := producer.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, "RESET lock_timeout"); err != nil {
		t.Fatal(err)
	}

	expire := func() {
		t.Helper()
		if _, err := state.Exec(ctx, "UPDATE pooltune_worker.investigations SET lease_until=now()-interval '1 second' WHERE id=$1", id); err != nil {
			t.Fatal(err)
		}
	}
	expire()
	second, err := claimInvestigation(ctx, other, "trial")
	if err != nil || second == nil || second.Attempt != 2 {
		t.Fatalf("second claim: %+v %v", second, err)
	}
	if err := finishInvestigation(ctx, state, *first, DispatchResult{Outcome: "insufficient_evidence"}); !errors.Is(err, errClaimLost) {
		t.Fatalf("stale claim completed: %v", err)
	}
	expire()
	third, err := claimInvestigation(ctx, state, "trial")
	if err != nil || third != nil {
		t.Fatalf("exhausted claim: %+v %v", third, err)
	}
	var outcome string
	if err := state.QueryRow(ctx, "SELECT result->>'outcome' FROM pooltune_worker.investigations WHERE id=$1 AND finished_at IS NOT NULL", id).Scan(&outcome); err != nil || outcome != "retry_exhausted" {
		t.Fatalf("missing exhausted result: %q %v", outcome, err)
	}

	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.investigations
		(project_id,database_oid,role_oid,query_id,evidence) VALUES ('trial',1,2,4,'{}')`); err != nil {
		t.Fatal(err)
	}
	diagnosed, err := DispatchInvestigation(ctx, state, "trial",
		func(context.Context, InvestigationJob) (IndexEvidence, error) { return IndexEvidence{}, nil },
		func(context.Context, IndexEvidence) (IndexProposal, error) {
			t.Fatal("model called without evidence")
			return IndexProposal{}, nil
		})
	if err != nil || diagnosed.Outcome != "insufficient_evidence" {
		t.Fatalf("insufficient evidence: %+v %v", diagnosed, err)
	}
}
