//go:build integration

package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

func TestCommittedStatisticsReceiptSurvivesEpochFenceRollback(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state := openMonitorConn(t, ctx, config)
	journal := openMonitorConn(t, ctx, config)
	initial := setupCommittedStatisticsTable(t, ctx, state, "column_target")
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	const project = "committed-statistics-journal"
	epoch := int64(7)
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,$2)`, project, epoch); err != nil {
		t.Fatal(err)
	}
	claim := investigationClaim{Project: project, Attempt: 1}
	if err := state.QueryRow(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,attempts,lease_until) VALUES($1,$2,2,3,'{}',1,clock_timestamp()+interval '30 seconds') RETURNING id`, project, initial.Target.DatabaseOID).Scan(&claim.ID); err != nil {
		t.Fatal(err)
	}
	initial.RunID = fmt.Sprintf("managed-statistics-%d-1", claim.ID)
	var fence pgx.Tx
	var recorded ManagedStatisticsIntent
	guards := managedStatisticsCommitGuards{Permission: func(context.Context) error { return nil }, Capacity: func(context.Context) error { return nil }, Recheck: func(context.Context, pgx.Tx) error { return nil }, Record: func(ctx context.Context, i ManagedStatisticsIntent) error {
		recorded = i
		if err := recordOwnedProjectAction(ctx, state, project, epoch, claim, "statistics", i); err != nil {
			return err
		}
		var err error
		fence, err = lockOwnedIndexEpoch(ctx, state, project, epoch, claim)
		return err
	}, Save: func(ctx context.Context, prior, next ManagedStatisticsIntent) error {
		if err := saveManagedStatisticsIntent(ctx, state, project, epoch, claim, prior, next); err == nil {
			t.Fatal("receipt accepted uncommitted epoch-fence connection")
		}
		return saveManagedStatisticsIntent(ctx, journal, project, epoch, claim, prior, next)
	}}
	action, err := applyManagedStatisticsCommitted(ctx, config, initial, guards, func(ctx context.Context) (string, string, error) {
		var raw []byte
		if err := journal.QueryRow(ctx, `SELECT intent FROM pooltune_worker.owned_project_actions WHERE job_id=$1`, claim.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var receipt ManagedStatisticsIntent
		if json.Unmarshal(raw, &receipt) != nil || receipt.TrialVersion == "" || receipt.Phase != "commit_ready" {
			t.Fatalf("receipt not durably visible before verification: %s", raw)
		}
		return "inconclusive", "missing sufficient benefit", nil
	})
	if fence != nil {
		err = errors.Join(err, releaseOwnedIndexEpoch(state, fence))
	}
	if err != nil || !action.Restored {
		t.Fatalf("guarded committed trial: %+v %v", action, err)
	}
	restarted := openMonitorConn(t, ctx, config)
	var raw []byte
	if err := restarted.QueryRow(ctx, `SELECT intent FROM pooltune_worker.owned_project_actions WHERE job_id=$1`, claim.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var durable ManagedStatisticsIntent
	if json.Unmarshal(raw, &durable) != nil || durable.Phase != "restore_ready" || durable.RestoredVersion == "" {
		t.Fatalf("receipt rolled back with epoch fence: %s", raw)
	}
	if err := saveManagedStatisticsIntent(ctx, journal, project, epoch, claim, recorded, action.Intent); err == nil {
		t.Fatal("stale receipt overwrote later stage")
	}
	if err := saveManagedStatisticsIntent(ctx, journal, project, epoch-1, claim, durable, durable); err == nil {
		t.Fatal("stale permission epoch updated receipt")
	}
}
