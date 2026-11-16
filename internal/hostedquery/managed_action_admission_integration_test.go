//go:build integration

package hostedquery

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

func TestPendingRepairsExcludeOtherFamilies(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	const project = "managed-exclusive"
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch)VALUES($1,1)`, project); err != nil {
		t.Fatal(err)
	}
	query := int64(3)
	claim := func() investigationClaim {
		query++
		t.Helper()
		c := investigationClaim{Project: project, Attempt: 1}
		if err := state.QueryRow(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,attempts,lease_until)VALUES($1,1,2,$2,'{}',1,clock_timestamp()+interval '30 seconds')RETURNING id`, project, query).Scan(&c.ID); err != nil {
			t.Fatal(err)
		}
		return c
	}
	first, second := claim(), claim()
	if err := recordOwnedProjectAction(ctx, state, project, 1, first, "analyze", ProjectMaintenanceIntent{}); err != nil {
		t.Fatal(err)
	}
	if err := recordOwnedIndexIntent(ctx, state, project, 1, second, ProjectIndexActionIntent{}); !errors.Is(err, errClaimLost) {
		t.Fatalf("pending analyze admitted index: %v", err)
	}
	if _, err := state.Exec(ctx, `DELETE FROM pooltune_worker.owned_project_actions`); err != nil {
		t.Fatal(err)
	}
	if err := recordOwnedIndexIntent(ctx, state, project, 1, first, ProjectIndexActionIntent{}); err != nil {
		t.Fatal(err)
	}
	if err := recordOwnedProjectAction(ctx, state, project, 1, second, "vacuum", ProjectMaintenanceIntent{}); !errors.Is(err, errClaimLost) {
		t.Fatalf("pending index admitted vacuum: %v", err)
	}
}
