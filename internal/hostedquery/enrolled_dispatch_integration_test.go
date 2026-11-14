//go:build integration

package hostedquery

import (
	"context"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
	"testing"
)

func TestEstimatedEvidenceDispatch(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, `
		INSERT INTO pooltune_worker.projects (project_id) VALUES ('estimate');
		INSERT INTO pooltune_worker.investigations (project_id,database_oid,role_oid,query_id,evidence)
		VALUES ('estimate',1,2,3,'{}'),('estimate',1,2,4,'{}');
	`); err != nil {
		t.Fatal(err)
	}
	result, err := DispatchEstimatedInvestigation(ctx, state, "estimate", func(_ context.Context, job InvestigationJob) (*EstimatedLookupEvidence, error) {
		if job.Query != 3 {
			t.Fatalf("wrong job: %+v", job)
		}
		return &EstimatedLookupEvidence{TableOID: 12, ColumnNumber: 1, Estimated: EstimatedPlan{Node: "Seq Scan", OutputRows: 1}}, nil
	})
	if err != nil || result == nil || result.Outcome != "needs_measured_evidence" || result.Estimate == nil || result.Evidence != nil || result.Proposal != nil {
		t.Fatalf("estimate result: %+v, %v", result, err)
	}
	result, err = DispatchEstimatedInvestigation(ctx, state, "estimate", func(_ context.Context, job InvestigationJob) (*EstimatedLookupEvidence, error) { return nil, nil })
	if err != nil || result == nil || result.Outcome != "insufficient_evidence" {
		t.Fatalf("missing result: %+v, %v", result, err)
	}
	var saved bool
	err = state.QueryRow(ctx, `SELECT result ? 'estimated_evidence' AND NOT (result ? 'evidence') FROM pooltune_worker.investigations WHERE query_id=3 AND finished_at IS NOT NULL`).Scan(&saved)
	if err != nil || !saved {
		t.Fatalf("estimate was not persisted distinctly: %v %v", saved, err)
	}
}
