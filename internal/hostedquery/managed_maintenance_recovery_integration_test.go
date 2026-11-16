//go:build integration

package hostedquery

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"pooltune/internal/testpg"
)

func TestManagedMaintenanceRecoveryNeverRepeatsCommand(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	db := openMonitorConn(t, ctx, config)
	if _, err := db.Exec(ctx, `CREATE TABLE public.customer_orders(account_id int);INSERT INTO public.customer_orders SELECT i FROM generate_series(1,2000)i`); err != nil {
		t.Fatal(err)
	}
	target := ProjectIndexTarget{Schema: "public", Relation: "customer_orders"}
	if err := db.QueryRow(ctx, `SELECT oid,'public.customer_orders'::regclass::oid FROM pg_database WHERE datname=current_database()`).Scan(&target.DatabaseOID, &target.TableOID); err != nil {
		t.Fatal(err)
	}
	claim := investigationClaim{InvestigationJob: InvestigationJob{ID: 42, Database: target.DatabaseOID}}
	intent := ProjectMaintenanceIntent{ProjectMaintenanceRequest: ProjectMaintenanceRequest{Target: target, RunID: "managed-maintenance-42-1", Operation: ProjectAnalyze, ObservedAt: time.Now().Add(-time.Minute), Columns: []int16{1}}, RelationBytes: 16384}
	locker := openMonitorConn(t, ctx, config)
	if _, err := locker.Exec(ctx, "SELECT pg_advisory_lock($1,$2)", projectIndexLockNamespace, int32(target.TableOID)); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectManagedMaintenanceIntent(ctx, db, claim, intent); !errors.Is(err, errOwnedIndexActionActive) {
		t.Fatalf("active action not deferred: %v", err)
	}
	if _, err := locker.Exec(ctx, "SELECT pg_advisory_unlock($1,$2)", projectIndexLockNamespace, int32(target.TableOID)); err != nil {
		t.Fatal(err)
	}
	report, err := inspectManagedMaintenanceIntent(ctx, db, claim, intent)
	if err != nil || report.RecoveryStatus != "completion_unconfirmed" || report.Outcome != "repair_interrupted" {
		t.Fatalf("unexpected no-action recovery: %+v %v", report, err)
	}
	var count int64
	if err := db.QueryRow(ctx, "SELECT analyze_count FROM pg_stat_all_tables WHERE relid=$1", target.TableOID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("recovery performed ANALYZE: %d %v", count, err)
	}
	if _, err := locker.Exec(ctx, `ANALYZE public.customer_orders;SELECT pg_stat_force_next_flush()`); err != nil {
		t.Fatal(err)
	}
	report, err = inspectManagedMaintenanceIntent(ctx, db, claim, intent)
	if err != nil || report.RecoveryStatus != "maintenance_observed" || report.Action.Confirmed {
		t.Fatalf("observed maintenance falsely claimed verified repair: %+v %v", report, err)
	}
	if _, err := db.Exec(ctx, `DROP TABLE public.customer_orders;CREATE TABLE public.customer_orders(account_id int)`); err != nil {
		t.Fatal(err)
	}
	report, err = inspectManagedMaintenanceIntent(ctx, db, claim, intent)
	if err != nil || report.RecoveryStatus != "target_changed" {
		t.Fatalf("replacement relation accepted: %+v %v", report, err)
	}
}

func TestManagedMaintenanceRestartReclaimsAndSettlesExactJournal(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state := openMonitorConn(t, ctx, config)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	const project = "managed-maintenance-recovery"
	epoch := int64(4)
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,$2);`, project, epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence) VALUES($1,1,2,3,'{}')`, project); err != nil {
		t.Fatal(err)
	}
	first, err := claimProjectInvestigation(ctx, state, project, &epoch)
	if err != nil || first == nil {
		t.Fatalf("initial claim: %+v %v", first, err)
	}
	intent := ProjectMaintenanceIntent{ProjectMaintenanceRequest: ProjectMaintenanceRequest{RunID: fmt.Sprintf("managed-maintenance-%d-1", first.ID), Operation: ProjectAnalyze}}
	if err := recordOwnedProjectAction(ctx, state, project, epoch, *first, "analyze", intent); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, `UPDATE pooltune_worker.investigations SET attempts=2,lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	restarted := openMonitorConn(t, ctx, config)
	claim, err := claimProjectInvestigation(ctx, restarted, project, &epoch, ownedIndexRecoveryScope{Managed: true})
	if err != nil || claim == nil || !claim.RecoveryOnly || claim.MaintenanceOperation != "analyze" || claim.Attempt != 3 {
		t.Fatalf("maintenance lost after ordinary retry exhaustion: %+v %v", claim, err)
	}
	raw, _ := json.Marshal(intent)
	report := &ManagedMaintenanceReport{Operation: ProjectAnalyze, Outcome: "repair_interrupted", RecoveryStatus: "completion_unconfirmed", Reason: "No completion evidence; command not repeated."}
	result := DispatchResult{Outcome: "repair_interrupted", ManagedMaintenance: report, ownedOperation: "analyze", managedRecoveryIntent: raw}
	for _, mismatch := range []json.RawMessage{nil, json.RawMessage(`{"RunID":"different"}`)} {
		bad := result
		bad.managedRecoveryIntent = mismatch
		if err := finishInvestigation(ctx, restarted, *claim, bad, epoch); err == nil {
			t.Fatal("mismatched journal finalized")
		}
	}
	if err := finishInvestigation(ctx, restarted, *claim, result, epoch-1); !errors.Is(err, errClaimLost) {
		t.Fatalf("stale epoch finalized: %v", err)
	}
	if err := finishInvestigation(ctx, restarted, *claim, result, epoch); err != nil {
		t.Fatal(err)
	}
	var outcome string
	var finished bool
	if err := state.QueryRow(ctx, `SELECT a.outcome,i.finished_at IS NOT NULL FROM pooltune_worker.owned_project_actions a JOIN pooltune_worker.investigations i ON i.id=a.job_id WHERE a.job_id=$1`, claim.ID).Scan(&outcome, &finished); err != nil || outcome != "repair_interrupted" || !finished {
		t.Fatalf("journal not settled honestly: %s %v %v", outcome, finished, err)
	}
}
