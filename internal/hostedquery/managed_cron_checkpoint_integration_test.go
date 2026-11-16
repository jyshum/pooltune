//go:build integration

package hostedquery

import (
	"encoding/json"
	"pooltune/internal/supabaseproject"
	"strings"
	"testing"
	"time"
)

func TestManagedCronCheckpointSurvivesRestartAndNeverReplaysUnknownCommit(t *testing.T) {
	ctx, state, cfg := projectActivityTestState(t)
	const project = "managed-cron-checkpoint"
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,1)`, project); err != nil {
		t.Fatal(err)
	}
	evidence := managedCronPolicyExample(false)
	q := evidence.Periods[0].Busy.Target.Before
	envelope, _ := json.Marshal(managedCronQueue{JobID: evidence.JobID, Cycle: evidence.Periods[2].Cycle})
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence) VALUES($1,$2,$3,$4,$5::jsonb)`, project, q.Database, q.Role, q.Query, string(envelope)); err != nil {
		t.Fatal(err)
	}
	epoch := int64(1)
	claim, err := claimProjectInvestigation(ctx, state, project, &epoch, ownedIndexRecoveryScope{Managed: true})
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	target := ProjectCronTarget{Project: project, DatabaseOID: q.Database, OwnerOID: q.Role, JobID: evidence.JobID, WitnessJobID: 19, Name: "customer job", CommandDigest: strings.Repeat("a", 64)}
	a := managedCronAction{RunID: "managed-cron-1-1", Enrollment: ManagedCronEnrollment{Target: target, Window: evidence.Window}, Queries: ManagedCronQueries{Target: q}, Evidence: evidence, Intent: ProjectCronIntent{Target: target, Window: evidence.Window, RunID: "managed-cron-1-1", Before: ProjectCronSnapshot{Schedule: evidence.Original, Fingerprint: "native", Generation: "7"}, ProposedSchedule: evidence.Alternate, FirstFutureCycle: time.Now().Add(time.Minute)}}
	if err := recordOwnedProjectAction(ctx, state, project, epoch, *claim, "cron", a); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, `UPDATE pooltune_worker.investigations SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(ctx); err != nil {
		t.Fatal(err)
	}
	state = openMonitorConn(t, ctx, cfg)
	recovered, err := claimProjectInvestigation(ctx, state, project, &epoch, ownedIndexRecoveryScope{Managed: true})
	if err != nil || recovered == nil || !recovered.RecoveryOnly || recovered.MaintenanceOperation != "cron" {
		t.Fatalf("no Cron recovery claim: %+v %v", recovered, err)
	}
	saved, origin, err := readManagedCronAction(ctx, state, *recovered)
	if err != nil || saved == nil || saved.Receipt != nil || origin != claim.ID {
		t.Fatalf("unknown commit incorrectly acknowledged: %+v %d %v", saved, origin, err)
	}
	result := DispatchResult{JobID: recovered.ID, Attempt: recovered.Attempt, Outcome: "inconclusive", ManagedCron: managedCronReport(*saved, "inconclusive", "No durable acknowledgement; no replay.")}
	if err := publishManagedCronAction(ctx, state, project, epoch, *recovered, origin, *saved, result, nil, false); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := state.QueryRow(ctx, `SELECT outcome FROM pooltune_worker.owned_project_actions WHERE job_id=$1`, origin).Scan(&outcome); err != nil || outcome != "inconclusive" {
		t.Fatalf("unknown action not settled honestly: %s %v", outcome, err)
	}
}

func TestManagedCronReceiptAndFutureClaimAreAtomicAcrossRestart(t *testing.T) {
	ctx, state, cfg := projectActivityTestState(t)
	const project = "managed-cron-future"
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,1)`, project); err != nil {
		t.Fatal(err)
	}
	evidence := managedCronPolicyExample(false)
	q := evidence.Periods[0].Busy.Target.Before
	envelope, _ := json.Marshal(managedCronQueue{JobID: evidence.JobID, Cycle: evidence.Periods[2].Cycle})
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence) VALUES($1,$2,$3,$4,$5::jsonb)`, project, q.Database, q.Role, q.Query, string(envelope)); err != nil {
		t.Fatal(err)
	}
	epoch := int64(1)
	claim, err := claimProjectInvestigation(ctx, state, project, &epoch, ownedIndexRecoveryScope{Managed: true})
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	target := ProjectCronTarget{Project: project, DatabaseOID: q.Database, OwnerOID: q.Role, JobID: evidence.JobID, WitnessJobID: 19, Name: "customer job", CommandDigest: strings.Repeat("a", 64)}
	future := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	a := managedCronAction{RunID: "managed-cron-1-1", Enrollment: ManagedCronEnrollment{Target: target, Window: evidence.Window}, Queries: ManagedCronQueries{Target: q}, Evidence: evidence, Intent: ProjectCronIntent{Target: target, Window: evidence.Window, RunID: "managed-cron-1-1", Before: ProjectCronSnapshot{Schedule: evidence.Original, Fingerprint: "native", Generation: "7"}, ProposedSchedule: evidence.Alternate, FirstFutureCycle: future}}
	if err := recordOwnedProjectAction(ctx, state, project, epoch, *claim, "cron", a); err != nil {
		t.Fatal(err)
	}
	a.Receipt = &ProjectCronResult{After: ProjectCronSnapshot{Schedule: evidence.Alternate, Fingerprint: "native", Generation: "8"}, CommitAt: time.Now().UTC().Truncate(time.Microsecond), FirstFutureCycle: future}
	after := a.Enrollment
	after.After = true
	after.Schedule = evidence.Alternate
	after.Generation = "8"
	after.Fingerprint = strings.Repeat("b", 64)
	a.Followup = &after
	a.VerifyAfter = future.Add(3*time.Duration(evidence.Window.EveryMinutes)*time.Minute + 2*time.Second)
	result := DispatchResult{JobID: claim.ID, Attempt: claim.Attempt, Outcome: "awaiting_verification", ManagedCron: managedCronReport(a, "awaiting_verification", "Three future cycles required.")}
	if err := publishManagedCronAction(ctx, state, project, epoch+1, *claim, claim.ID, a, result, &a.VerifyAfter, true); err == nil {
		t.Fatal("stale epoch published schedule receipt")
	}
	var pending int
	if err := state.QueryRow(ctx, `SELECT count(*) FROM pooltune_worker.investigations WHERE parent_id=$1`, claim.ID).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("failed receipt created child: %d %v", pending, err)
	}
	if err := publishManagedCronAction(ctx, state, project, epoch, *claim, claim.ID, a, result, &a.VerifyAfter, true); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(ctx); err != nil {
		t.Fatal(err)
	}
	state = openMonitorConn(t, ctx, cfg)
	if early, err := claimProjectInvestigation(ctx, state, project, &epoch, ownedIndexRecoveryScope{Managed: true}); err != nil || early != nil {
		t.Fatalf("future trial claimed early: %+v %v", early, err)
	}
	if _, err := state.Exec(ctx, `UPDATE pooltune_worker.investigations SET not_before=clock_timestamp()-interval '1 second',attempts=2,lease_until=clock_timestamp()-interval '1 second' WHERE parent_id=$1`, claim.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := claimProjectInvestigation(ctx, state, project, &epoch, ownedIndexRecoveryScope{Managed: true}); err != nil {
		t.Fatal(err)
	}
	if err := ensureManagedCronContinuation(ctx, state, project, epoch); err != nil {
		t.Fatal(err)
	}
	follow, err := claimProjectInvestigation(ctx, state, project, &epoch, ownedIndexRecoveryScope{Managed: true})
	if err != nil || follow == nil {
		t.Fatalf("exhausted future trial stranded: %+v %v", follow, err)
	}
	saved, origin, err := readManagedCronAction(ctx, state, *follow)
	if err != nil || saved == nil || saved.Receipt == nil || origin != claim.ID || !saved.Receipt.FirstFutureCycle.Equal(future) {
		t.Fatalf("receipt lost: %+v %d %v", saved, origin, err)
	}
	terminal := DispatchResult{JobID: follow.ID, Attempt: follow.Attempt, Outcome: "inconclusive", ManagedCron: managedCronReport(*saved, "inconclusive", "Missing future observations.")}
	if err := publishManagedCronAction(ctx, state, project, epoch, *follow, origin, *saved, terminal, nil, false); err != nil {
		t.Fatal(err)
	}
}

func TestManagedCronRemovedFinalEnrollmentWakesAndSettlesWithoutNativeAction(t *testing.T) {
	ctx, state, _ := projectActivityTestState(t)
	identity, err := supabaseproject.New("osdjqwpzgyzwfaxyufze", "20139985697ea001")
	if err != nil {
		t.Fatal(err)
	}
	project := identity.ID()
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,1)`, project); err != nil {
		t.Fatal(err)
	}
	evidence := managedCronPolicyExample(false)
	q := evidence.Periods[0].Busy.Target.Before
	envelope, _ := json.Marshal(managedCronQueue{JobID: evidence.JobID, Cycle: evidence.Periods[2].Cycle})
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence) VALUES($1,$2,$3,$4,$5::jsonb)`, project, q.Database, q.Role, q.Query, string(envelope)); err != nil {
		t.Fatal(err)
	}
	epoch := int64(1)
	claim, err := claimProjectInvestigation(ctx, state, project, &epoch, ownedIndexRecoveryScope{Managed: true})
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	target := ProjectCronTarget{Project: project, DatabaseOID: q.Database, OwnerOID: q.Role, JobID: evidence.JobID, WitnessJobID: 19, Name: "customer job", CommandDigest: strings.Repeat("a", 64)}
	future := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	a := managedCronAction{RunID: "managed-cron-1-1", Enrollment: ManagedCronEnrollment{Target: target, Window: evidence.Window}, Queries: ManagedCronQueries{Target: q}, Evidence: evidence, Intent: ProjectCronIntent{Target: target, Window: evidence.Window, RunID: "managed-cron-1-1", Before: ProjectCronSnapshot{Schedule: evidence.Original, Fingerprint: "native", Generation: "7"}, ProposedSchedule: evidence.Alternate, FirstFutureCycle: future}}
	if err := recordOwnedProjectAction(ctx, state, project, epoch, *claim, "cron", a); err != nil {
		t.Fatal(err)
	}
	a.Receipt = &ProjectCronResult{After: ProjectCronSnapshot{Schedule: evidence.Alternate, Fingerprint: "native", Generation: "8"}, CommitAt: time.Now().UTC().Truncate(time.Microsecond), FirstFutureCycle: future}
	after := a.Enrollment
	after.After = true
	after.Schedule = evidence.Alternate
	after.Generation = "8"
	after.Fingerprint = strings.Repeat("b", 64)
	a.Followup = &after
	a.VerifyAfter = future.Add(3*time.Duration(evidence.Window.EveryMinutes)*time.Minute + 2*time.Second)
	result := DispatchResult{JobID: claim.ID, Attempt: claim.Attempt, Outcome: "awaiting_verification", ManagedCron: managedCronReport(a, "awaiting_verification", "Three future cycles required.")}
	if err := publishManagedCronAction(ctx, state, project, epoch+1, *claim, claim.ID, a, result, &a.VerifyAfter, true); err == nil {
		t.Fatal("stale epoch published schedule receipt")
	}
	var pending int
	if err := state.QueryRow(ctx, `SELECT count(*) FROM pooltune_worker.investigations WHERE parent_id=$1`, claim.ID).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("failed receipt created child: %d %v", pending, err)
	}
	if err := publishManagedCronAction(ctx, state, project, epoch, *claim, claim.ID, a, result, &a.VerifyAfter, true); err != nil {
		t.Fatal(err)
	}

	service := &ManagedProjectCron{}
	if err := service.wakeInactive(ctx, state, project, epoch); err != nil {
		t.Fatal(err)
	}
	follow, err := claimProjectInvestigation(ctx, state, project, &epoch, ownedIndexRecoveryScope{Managed: true})
	if err != nil || follow == nil {
		t.Fatalf("removed selection left child asleep: %+v %v", follow, err)
	}
	// No native connection or model is supplied. Removal settles only durable
	// state and cannot replay the action or invent a current catalog observation.
	finished, err := service.dispatch(ctx, state, identity, epoch, *follow)
	if err != nil || finished == nil || finished.Outcome != "inconclusive" {
		t.Fatalf("removed selection stranded repair: %+v %v", finished, err)
	}
	var pendingCount int
	if err := state.QueryRow(ctx, `SELECT count(*) FROM pooltune_worker.owned_project_actions WHERE project_id=$1 AND outcome='pending'`, project).Scan(&pendingCount); err != nil || pendingCount != 0 {
		t.Fatalf("other repairs still blocked: %d %v", pendingCount, err)
	}
}
