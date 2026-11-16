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

func TestManagedStatisticsRecoveryOnlyObservesExactCatalogState(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	db := openMonitorConn(t, ctx, config)
	exec := func(sql string) {
		t.Helper()
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE public.customer_stats(account_id int,category int);INSERT INTO public.customer_stats SELECT i,i%10 FROM generate_series(1,2000)i;ALTER TABLE public.customer_stats ALTER COLUMN account_id SET STATISTICS 1`)
	target := ProjectIndexTarget{Schema: "public", Relation: "customer_stats"}
	if err := db.QueryRow(ctx, `SELECT oid,'public.customer_stats'::regclass::oid FROM pg_database WHERE datname=current_database()`).Scan(&target.DatabaseOID, &target.TableOID); err != nil {
		t.Fatal(err)
	}
	claim := investigationClaim{InvestigationJob: InvestigationJob{ID: 42, Database: target.DatabaseOID}}
	intent := ProjectStatisticsIntent{ProjectStatisticsRequest: ProjectStatisticsRequest{Target: target, RunID: "managed-statistics-42-1", ObservedAt: time.Now(), Column: 1, NextTarget: 2}, Key: ProjectIndexKey{Number: 1, Name: "account_id"}, Prior: ProjectStatisticsSetting{Value: 1}, PriorEffective: 1, RelationBytes: 16384}
	raw, _ := json.Marshal(intent)
	assert := func(raw []byte, want string) {
		t.Helper()
		r, err := inspectManagedStatisticsIntent(ctx, db, claim, raw)
		if err != nil || r.RecoveryStatus != want || r.Outcome != "repair_interrupted" || r.Action.Confirmed {
			t.Fatalf("recovery: %+v %v want %s", r, err, want)
		}
	}
	assert(raw, "prior_statistics_setting")
	exec(`ALTER TABLE public.customer_stats ALTER COLUMN account_id SET STATISTICS 2`)
	assert(raw, "trial_statistics_setting")
	exec(`ALTER TABLE public.customer_stats ALTER COLUMN account_id SET STATISTICS 3`)
	assert(raw, "statistics_setting_changed")
	lock := openMonitorConn(t, ctx, config)
	if _, err := lock.Exec(ctx, "SELECT pg_advisory_lock($1,$2)", projectIndexLockNamespace, int32(target.TableOID)); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectManagedStatisticsIntent(ctx, db, claim, raw); !errors.Is(err, errOwnedIndexActionActive) {
		t.Fatalf("active action finalized: %v", err)
	}
	if _, err := lock.Exec(ctx, "SELECT pg_advisory_unlock($1,$2)", projectIndexLockNamespace, int32(target.TableOID)); err != nil {
		t.Fatal(err)
	}
	joint := OwnedExtendedStatisticsIntent{RunID: intent.RunID, Target: target, ColumnNumbers: []int16{1, 2}, ObjectName: managedJointStatisticsName(target.TableOID, []int16{1, 2}), RelationBytes: 16384}
	raw, _ = json.Marshal(joint)
	assert(raw, "statistics_object_absent")
	exec("CREATE STATISTICS public." + joint.ObjectName + " (mcv) ON account_id,category FROM public.customer_stats")
	assert(raw, "matching_statistics_object_present")
	exec("DROP STATISTICS public." + joint.ObjectName + ";CREATE STATISTICS public." + joint.ObjectName + " (dependencies) ON account_id,category FROM public.customer_stats")
	assert(raw, "statistics_object_changed")
}

func TestManagedStatisticsPendingIntentReclaimsAndFinalizesAfterRestart(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state := openMonitorConn(t, ctx, config)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	const project = "managed-statistics-restart"
	epoch := int64(2)
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,$2)`, project, epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence) VALUES($1,1,2,3,'{}')`, project); err != nil {
		t.Fatal(err)
	}
	first, err := claimProjectInvestigation(ctx, state, project, &epoch)
	if err != nil || first == nil {
		t.Fatal(err)
	}
	intent := ProjectStatisticsIntent{ProjectStatisticsRequest: ProjectStatisticsRequest{RunID: fmt.Sprintf("managed-statistics-%d-1", first.ID)}}
	if err := recordOwnedProjectAction(ctx, state, project, epoch, *first, "statistics", intent); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, `UPDATE pooltune_worker.investigations SET attempts=2,lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	restarted := openMonitorConn(t, ctx, config)
	claim, err := claimProjectInvestigation(ctx, restarted, project, &epoch, ownedIndexRecoveryScope{Managed: true})
	if err != nil || claim == nil || !claim.RecoveryOnly || claim.MaintenanceOperation != "statistics" || claim.Attempt != 3 {
		t.Fatalf("statistics intent not recovered: %+v %v", claim, err)
	}
	raw, _ := json.Marshal(intent)
	result := DispatchResult{Outcome: "repair_interrupted", ownedOperation: "statistics", managedRecoveryIntent: raw, ManagedMaintenance: &ManagedMaintenanceReport{Operation: "statistics", Outcome: "repair_interrupted", RecoveryStatus: "prior_statistics_setting", Reason: "Catalog observed; benefit unverified."}}
	bad := result
	bad.managedRecoveryIntent = json.RawMessage(`{"RunID":"changed"}`)
	if err := finishInvestigation(ctx, restarted, *claim, bad, epoch); err == nil {
		t.Fatal("different journal finalized")
	}
	if err := finishInvestigation(ctx, restarted, *claim, result, epoch); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := state.QueryRow(ctx, `SELECT outcome FROM pooltune_worker.owned_project_actions WHERE job_id=$1`, claim.ID).Scan(&outcome); err != nil || outcome != "repair_interrupted" {
		t.Fatalf("statistics journal not settled: %s %v", outcome, err)
	}
}
