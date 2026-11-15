//go:build integration

package hostedquery

import (
	"errors"
	"testing"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestOwnedProjectActionRequiresExactJournalAndEpoch(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	const project = "owned-repair-journal"
	if _, err := state.Exec(ctx, "INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,3)", project); err != nil {
		t.Fatal(err)
	}
	newClaim := func(query int64) investigationClaim {
		var claim investigationClaim
		claim.Project, claim.Attempt = project, 1
		err := state.QueryRow(ctx, `INSERT INTO pooltune_worker.investigations
		(project_id,database_oid,role_oid,query_id,evidence,attempts,lease_until)
		VALUES($1,1,2,$2,'{}'::jsonb,1,clock_timestamp()+interval '30 seconds') RETURNING id`, project, query).Scan(&claim.ID)
		if err != nil {
			t.Fatal(err)
		}
		return claim
	}
	claim, second := newClaim(3), newClaim(4)
	result := DispatchResult{Outcome: "improved_tested_inputs", ownedOperation: "memory"}
	if err := finishInvestigation(ctx, state, claim, result, 3); !errors.Is(err, errClaimLost) {
		t.Fatalf("publication without intent: %v", err)
	}
	if err := recordOwnedProjectAction(ctx, state, project, 2, claim, "memory", ProjectFunctionMemoryIntent{Phase: "apply"}); !errors.Is(err, errClaimLost) {
		t.Fatalf("old epoch: %v", err)
	}
	if err := recordOwnedProjectAction(ctx, state, project, 3, claim, "memory", ProjectFunctionMemoryIntent{Phase: "apply"}); err != nil {
		t.Fatal(err)
	}
	if err := recordOwnedProjectAction(ctx, state, project, 3, second, "analyze", ProjectMaintenanceIntent{}); !errors.Is(err, errClaimLost) {
		t.Fatalf("second pending action: %v", err)
	}
	if err := recordOwnedMemoryRestore(ctx, state, project, 3, claim, ProjectFunctionMemoryIntent{Phase: "restore"}); err != nil {
		t.Fatal(err)
	}
	if err := recordOwnedMemoryRestore(ctx, state, project, 3, claim, ProjectFunctionMemoryIntent{Phase: "restore"}); !errors.Is(err, errClaimLost) {
		t.Fatalf("duplicate restore: %v", err)
	}
	wrong := result
	wrong.ownedOperation = "analyze"
	if err := finishInvestigation(ctx, state, claim, wrong, 3); !errors.Is(err, errClaimLost) {
		t.Fatalf("wrong operation: %v", err)
	}
	if err := finishInvestigation(ctx, state, claim, result, 3); err != nil {
		t.Fatal(err)
	}
	var recorded, published, restored string
	err = state.QueryRow(ctx, `SELECT a.outcome,j.result->>'outcome',a.restore_intent->>'Phase'
	FROM pooltune_worker.owned_project_actions a JOIN pooltune_worker.investigations j ON a.job_id=j.id
	WHERE a.job_id=$1`, claim.ID).Scan(&recorded, &published, &restored)
	if err != nil || recorded != result.Outcome || published != result.Outcome || restored != "restore" {
		t.Fatalf("journal and result disagree: %q %q %q %v", recorded, published, restored, err)
	}
	if err := recordOwnedProjectAction(ctx, state, project, 3, second, "analyze", ProjectMaintenanceIntent{}); err != nil {
		t.Fatalf("settled action blocked unrelated claim: %v", err)
	}
}
