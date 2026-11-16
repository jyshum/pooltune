//go:build integration

package hostedquery

import (
	"testing"
	"time"
)

func TestManagedCleanupReceiptAndFollowupSurviveRestart(t *testing.T) {
	ctx, state, cfg := projectActivityTestState(t)
	project := "managed-cleanup-checkpoint"
	epoch := int64(1)
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch)VALUES($1,1);`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence)VALUES($1,1,2,3,'{}')`, project); err != nil {
		t.Fatal(err)
	}
	claim, err := claimProjectInvestigation(ctx, state, project, &epoch, ownedIndexRecoveryScope{Managed: true})
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	a, _ := managedCleanupExample()
	if err := recordOwnedProjectAction(ctx, state, project, epoch, *claim, "vacuum", a); err != nil {
		t.Fatal(err)
	}
	receipt := a.Before
	receipt.At = receipt.At.Add(time.Second)
	receipt.ScaleFactor = .15
	receipt.ScaleOption = "0.15"
	a.Receipt = &receipt
	next := time.Now().Add(time.Minute)
	a.Deadline = next.Add(time.Minute)
	// A different deadline must not overwrite the original bounded action.
	result := DispatchResult{JobID: claim.ID, Attempt: claim.Attempt, Outcome: "awaiting_verification", ManagedCleanup: a.report("awaiting_verification", "waiting")}
	if err := publishManagedCleanup(ctx, state, epoch, *claim, result, managedCleanupPublication{Action: a, Origin: claim.ID, Next: &next, Ack: true}); err == nil {
		t.Fatal("changed action deadline published")
	}
	a, _ = managedCleanupExample()
	a.Receipt = &receipt
	next = receipt.At.Add(time.Second)
	result.ManagedCleanup = a.report("awaiting_verification", "waiting")
	if err := publishManagedCleanup(ctx, state, epoch+1, *claim, result, managedCleanupPublication{Action: a, Origin: claim.ID, Next: &next, Ack: true}); err == nil {
		t.Fatal("stale epoch published acknowledgement")
	}
	if err := publishManagedCleanup(ctx, state, epoch, *claim, result, managedCleanupPublication{Action: a, Origin: claim.ID, Next: &next, Ack: true}); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(ctx); err != nil {
		t.Fatal(err)
	}
	state = openMonitorConn(t, ctx, cfg)
	if _, err := state.Exec(ctx, `UPDATE pooltune_worker.investigations SET not_before=clock_timestamp()-interval '1 second' WHERE parent_id=$1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	child, err := claimProjectInvestigation(ctx, state, project, &epoch, ownedIndexRecoveryScope{Managed: true})
	if err != nil || child == nil || child.ParentID != claim.ID {
		t.Fatalf("missing durable follow-up %+v %v", child, err)
	}
	saved, origin, err := readManagedCleanupAction(ctx, state, *child)
	if err != nil || saved == nil || saved.Receipt == nil || origin != claim.ID {
		t.Fatalf("lost exact action/receipt %+v %v", saved, err)
	}
	forged := saved.report("improved_tested_inputs", "automatic cleanup claimed without observation")
	if err := publishManagedCleanup(ctx, state, epoch, *child, DispatchResult{Outcome: forged.Outcome, ManagedCleanup: forged}, managedCleanupPublication{Action: *saved, Origin: origin}); err == nil {
		t.Fatal("invented automatic cleanup accepted")
	}
	restore := ProjectCleanupSettingsIntent{Target: a.Intent.Target, RunID: a.RunID, BeforeOption: a.Intent.AfterOption, AfterOption: a.Intent.BeforeOption, Restoring: true}
	if err := recordManagedCleanupRestore(ctx, state, epoch, *child, origin, *saved, restore); err != nil {
		t.Fatal(err)
	}
	saved, _, err = readManagedCleanupAction(ctx, state, *child)
	if err != nil || saved.Restore == nil {
		t.Fatal("restore intent did not survive", err)
	}
	if err := recordManagedCleanupRestore(ctx, state, epoch, *child, origin, *saved, restore); err == nil {
		t.Fatal("repeated restore intent accepted")
	}
	report := saved.report("repair_interrupted", "uncertain restoration was inspected without another write")
	if err := publishManagedCleanup(ctx, state, epoch, *child, DispatchResult{Outcome: report.Outcome, ManagedCleanup: report}, managedCleanupPublication{Action: *saved, Origin: origin}); err != nil {
		t.Fatal(err)
	}
}
