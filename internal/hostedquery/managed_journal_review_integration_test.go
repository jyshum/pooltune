//go:build integration

package hostedquery

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

// An interrupted maintenance action must not disappear into ordinary retry
// exhaustion, nor may a different repair family bypass its pending journal.
func TestManagedPendingMaintenanceSurvivesRestart(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	const project = "managed-maintenance-restart"
	const epoch int64 = 3
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,$2)`, project, epoch); err != nil {
		t.Fatal(err)
	}
	claim := investigationClaim{Project: project, Attempt: maxInvestigationAttempts}
	if err := state.QueryRow(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,attempts,lease_until)
	VALUES($1,1,2,3,'{}',$2,clock_timestamp()+interval '30 seconds') RETURNING id`, project, claim.Attempt).Scan(&claim.ID); err != nil {
		t.Fatal(err)
	}
	if err := recordOwnedProjectAction(ctx, state, project, epoch, claim, "analyze", ProjectMaintenanceIntent{}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, `UPDATE pooltune_worker.investigations SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	_, err = claimProjectInvestigation(ctx, state, project, new(epoch), ownedIndexRecoveryScope{Managed: true})
	if err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := state.QueryRow(ctx, `SELECT COALESCE(result->>'outcome','') FROM pooltune_worker.investigations WHERE id=$1`, claim.ID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome == "retry_exhausted" {
		t.Error("pending maintenance was discarded as ordinary retry exhaustion")
	}
	second := investigationClaim{Project: project, Attempt: 1}
	if err := state.QueryRow(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,attempts,lease_until)
	VALUES($1,1,2,4,'{}',1,clock_timestamp()+interval '30 seconds') RETURNING id`, project).Scan(&second.ID); err != nil {
		t.Fatal(err)
	}
	if err := recordOwnedIndexIntent(ctx, state, project, epoch, second, ProjectIndexActionIntent{}); err == nil {
		t.Error("new index intent bypassed a pending maintenance action in the same project")
	}
}
