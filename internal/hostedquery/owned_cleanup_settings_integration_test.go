//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"testing"
	"time"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestOwnedCleanupTriggerRequiresActualBacklogAndAutovacuum(t *testing.T) {
	ctx, admin, _ := testpg.Start(t, "-c", "compute_query_id=on", "-c", "autovacuum_naptime=1s")
	const project = "supabase/osdjqwpzgyzwfaxyufze/a551222333444557"
	const schema = "pt_hosted_vc_a551222333444557"
	config, db := ownedTestWriter(t, ctx, admin, schema)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`COMMENT ON SCHEMA ` + schema + ` IS 'Pooltune hosted vacuum fixture ` + project + `';
		CREATE TABLE ` + schema + `.candidate(id integer PRIMARY KEY, lookup integer NOT NULL, padding text NOT NULL) WITH
			(autovacuum_vacuum_scale_factor=0.20, autovacuum_analyze_threshold=100000000, autovacuum_vacuum_insert_threshold=-1);
		INSERT INTO ` + schema + `.candidate SELECT n,n,repeat('p',64) FROM generate_series(1,100000) n ORDER BY (n::bigint*7919)%100003;
		CREATE INDEX candidate_lookup ON ` + schema + `.candidate(lookup);
		ANALYZE ` + schema + `.candidate;
		CREATE TABLE ` + schema + `.healthy(id integer PRIMARY KEY, padding text NOT NULL);
		INSERT INTO ` + schema + `.healthy SELECT n,'a' FROM generate_series(1,20) n;
		SET max_parallel_workers_per_gather=0; SET random_page_cost=1;`)
	req := OwnedVacuumRequest{Workload: OwnedRepairWorkload{Project: project, RunID: "owned-cleanup-trigger", Decision: Investigate,
		Target: ProjectIndexTarget{Schema: schema, Relation: "candidate"}}, IndexName: "candidate_lookup"}
	if err := db.QueryRow(ctx, `SELECT (SELECT oid FROM pg_database WHERE datname=current_database()), current_user::regrole::oid,
		to_regclass($1)::oid,to_regclass($2)::oid,to_regclass($3)::oid`,
		schema+".candidate", schema+".healthy", schema+".candidate_lookup").Scan(&req.Workload.Target.DatabaseOID,
		&req.Workload.RoleOID, &req.Workload.Target.TableOID, &req.Workload.HealthyOID, &req.IndexOID); err != nil {
		t.Fatal(err)
	}
	plan, err := scanTrialPlan(db.QueryRow(ctx, ownedMeasuredSQL+vacuumQuery(schema), int32(1), int32(50000)))
	if err != nil {
		t.Fatal(err)
	}
	req.Workload.QueryID = plan.QueryID
	read := func() ProjectCleanupSettingsObservation {
		t.Helper()
		n, err := ReadProjectCleanupSettings(ctx, db, req.Workload.Target)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	state := openMonitorConn(t, ctx, admin)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	const epoch = int64(1)
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,$2)`, project, epoch); err != nil {
		t.Fatal(err)
	}
	var origin int64
	if err := state.QueryRow(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence)
		VALUES($1,$2,$3,$4,'{}') RETURNING id`, project, req.Workload.Target.DatabaseOID, req.Workload.RoleOID,
		req.Workload.QueryID).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	active := epoch
	claim, err := claimProjectInvestigation(ctx, state, project, &active)
	if err != nil || claim == nil || claim.ID != origin {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	publish := func(decision OwnedCleanupSettingsDecision) int64 {
		t.Helper()
		result := DispatchResult{JobID: claim.ID, Attempt: claim.Attempt, Outcome: decision.Report.Outcome, Vacuum: &decision.Report}
		child, err := publishOwnedCleanupCheckpoint(ctx, state, active, *claim, result, decision)
		if err != nil {
			t.Fatal(err)
		}
		return child
	}
	var saved *OwnedCleanupSettingsAction
	allowed := func(context.Context, ProjectCleanupSettingsIntent) error { return nil }
	guards := OwnedCleanupSettingsGuards{
		CheckPermission: allowed,
		CheckCapacity:   func(context.Context, int64, ProjectCleanupSettingsIntent) error { return nil },
		RecordIntent: func(ctx context.Context, action OwnedCleanupSettingsAction) error {
			if saved != nil || action.Intent.Target != req.Workload.Target || read().ScaleOption != action.Intent.BeforeOption {
				return errors.New("exact cleanup change was not recorded before ALTER")
			}
			saved = &action
			return recordOwnedProjectAction(ctx, state, project, active, *claim, "vacuum", action)
		},
	}
	modelCalls := 0
	model := func(_ context.Context, e IndexEvidence) (IndexProposal, error) {
		modelCalls++
		return IndexProposal{RunID: e.RunID, TableOID: e.TableOID, Column: e.Column, Outcome: "propose", Repair: "vacuum", Reason: "Visibility work is high"}, nil
	}
	out, err := RunOwnedProjectCleanupSettings(ctx, config, req, nil, model, guards)
	if err != nil || out.Report.Outcome != "insufficient_evidence" || saved != nil || modelCalls != 0 {
		t.Fatalf("healthy backlog reached model or DDL: out=%+v intent=%+v calls=%d err=%v", out, saved, modelCalls, err)
	}
	exec(`UPDATE ` + schema + `.candidate SET padding=repeat('x', 64) WHERE id BETWEEN 1 AND 16000`)
	exec(`SELECT pg_stat_force_next_flush()`)
	writer, err := pgx.ConnectConfig(ctx, config.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer closeVacuumConnection(writer)
	writerFinished := make(chan error, 1)
	go func() {
		time.Sleep(450 * time.Millisecond)
		_, err := writer.Exec(ctx, `UPDATE `+schema+`.candidate SET padding=repeat('x',64) WHERE id BETWEEN 16001 AND 16750`)
		if err == nil {
			_, err = writer.Exec(ctx, `SELECT pg_stat_force_next_flush()`)
		}
		writerFinished <- err
	}()
	out, err = RunOwnedProjectCleanupSettings(ctx, config, req, nil, model, guards)
	if writerErr := <-writerFinished; writerErr != nil {
		t.Fatal(writerErr)
	}
	if err != nil || out.Report.Outcome != "awaiting_verification" || saved == nil || out.Action == nil || out.NextAt.IsZero() ||
		read().ScaleOption != "0.15" || modelCalls != 1 ||
		saved.Before.DeadTuples <= saved.Earlier.DeadTuples || saved.Before.AutomaticCleanups != 0 {
		t.Fatalf("cleanup was not acknowledged with exact growing backlog: %+v saved=%+v err=%v", out, saved, err)
	}
	unacknowledged, err := readOwnedCleanupCheckpoint(ctx, state, *claim)
	if err != nil || unacknowledged == nil || !unacknowledged.Action.Receipt.At.IsZero() {
		t.Fatalf("typed intent existed without its separate commit acknowledgement: %+v %v", unacknowledged, err)
	}
	interrupted, err := RunOwnedProjectCleanupSettings(ctx, config, req, &unacknowledged.Action, nil,
		OwnedCleanupSettingsGuards{CheckPermission: allowed})
	if err != nil || interrupted.Report.Outcome != "repair_interrupted" || !interrupted.Report.CleanupTrigger.SettingChanged ||
		read().ScaleOption != "0.15" {
		t.Fatalf("unacknowledged action was not inspected safely: %+v %v", interrupted, err)
	}
	next := publish(out)
	if next <= origin {
		t.Fatal("commit did not publish the linked successor")
	}
	if err := state.Close(ctx); err != nil {
		t.Fatal(err)
	}
	state = openMonitorConn(t, ctx, admin)
	var afterCheckpoint *ownedCleanupCheckpoint
	restored := 0
	verifyGuards := OwnedCleanupSettingsGuards{CheckPermission: allowed, CheckCapacity: guards.CheckCapacity,
		RecordRestore: func(ctx context.Context, intent ProjectCleanupSettingsIntent) error {
			restored++
			return recordOwnedCleanupRestore(ctx, state, active, *claim, afterCheckpoint, intent)
		}}
	for out.Report.Outcome == "awaiting_verification" {
		if err := waitOwnedCleanup(ctx, time.Until(out.NextAt)); err != nil {
			t.Fatal(err)
		}
		claim, err = claimProjectInvestigation(ctx, state, project, &active)
		if err != nil || claim == nil || claim.ID != next {
			t.Fatalf("worker restart lost cleanup successor: %+v %v", claim, err)
		}
		afterCheckpoint, err = readOwnedCleanupCheckpoint(ctx, state, *claim)
		if err != nil || afterCheckpoint == nil || afterCheckpoint.Origin != origin || afterCheckpoint.Action.Receipt.At.IsZero() {
			t.Fatalf("worker restart lost original action and acknowledgement: %+v %v", afterCheckpoint, err)
		}
		out, err = RunOwnedProjectCleanupSettings(ctx, config, req, &afterCheckpoint.Action, nil, verifyGuards)
		if err != nil {
			t.Fatal(err)
		}
		next = publish(out)
	}
	var settled string
	if err := state.QueryRow(ctx, `SELECT outcome FROM pooltune_worker.owned_project_actions WHERE job_id=$1`, origin).Scan(&settled); err != nil ||
		settled != out.Report.Outcome || next != 0 || modelCalls != 1 {
		t.Fatalf("worker did not atomically settle the original action: %s %+v %v", settled, out, err)
	}
	if settled == "inconclusive" && (restored != 1 || read().ScaleOption != "0.20") ||
		settled == "improved_tested_inputs" && (restored != 0 || read().ScaleOption != "0.15") {
		t.Fatalf("final option was inconsistent with the verified outcome: %s, restores=%d, option=%q", settled, restored, read().ScaleOption)
	}
	cleanup := out.Report.CleanupTrigger
	if cleanup == nil || cleanup.After.AutomaticCleanups <= saved.Before.AutomaticCleanups ||
		cleanup.After.LastAutovacuum.Before(saved.Before.At) || cleanup.After.DeadTuples >= saved.Before.DeadTuples/2 ||
		out.Report.After.HeapFetches > out.Report.Before.HeapFetches/2 || modelCalls != 1 ||
		out.Report.Outcome != "improved_tested_inputs" && out.Report.Outcome != "inconclusive" {
		t.Fatalf("unverified natural cleanup: %+v trigger=%+v", out.Report, cleanup)
	}
	t.Logf("automatic PostgreSQL cleanup reduced backlog %d -> %d, heap fetches %.0f -> %.0f, and target reads %.2f -> %.2f ms (%s)",
		saved.Before.DeadTuples, cleanup.After.DeadTuples, out.Report.Before.HeapFetches, out.Report.After.HeapFetches,
		out.Report.Before.ReadMS, out.Report.After.ReadMS, out.Report.Outcome)
}
