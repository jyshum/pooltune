//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"testing"
	"time"

	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"
)

func TestProjectRepairAccessUsesExactInstallationAndDatabaseAuthority(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	owner := openMonitorConn(t, ctx, config)
	project, err := supabaseproject.New("abcdefghijklmnopqrst", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `CREATE SCHEMA app;
		CREATE TABLE app.orders (id int, account_id int);
		CREATE FUNCTION app.report() RETURNS int LANGUAGE sql SET max_parallel_workers_per_gather=0 AS 'SELECT 1';
		GRANT USAGE ON SCHEMA app TO fixture_app;
		GRANT MAINTAIN ON app.orders TO fixture_app;
		CREATE SCHEMA cron;
		CREATE TABLE cron.job (jobid bigint PRIMARY KEY, jobname name, username name, database name, command text, schedule text, active bool);
		ALTER TABLE cron.job ENABLE ROW LEVEL SECURITY;
		CREATE POLICY own_jobs ON cron.job USING (username = current_user);
		CREATE FUNCTION cron.alter_job(bigint,text,text,text,text,boolean) RETURNS void LANGUAGE plpgsql AS 'BEGIN RETURN; END';
		REVOKE ALL ON FUNCTION cron.alter_job(bigint,text,text,text,text,boolean) FROM PUBLIC;
		GRANT USAGE ON SCHEMA cron TO fixture_app;
		GRANT SELECT ON cron.job TO fixture_app;
		GRANT EXECUTE ON FUNCTION cron.alter_job(bigint,text,text,text,text,boolean) TO fixture_app;
		INSERT INTO cron.job VALUES (7,'orders_refresh','fixture_app',current_database(),'SELECT 42','*/2 * * * *',true),
		  (8,'repair_witness','fixture_app',current_database(),'SELECT 1','2 seconds',true)`); err != nil {
		t.Fatal(err)
	}
	// Install the same identity markers that automatic onboarding writes. The
	// preflight must never trust a project reference from the scope file alone.
	marker := "Pooltune managed monitor v1 " + project.ID()
	if _, err := owner.Exec(ctx, `CREATE ROLE `+project.Role()+` LOGIN NOINHERIT CONNECTION LIMIT 2;
		CREATE SCHEMA `+project.Role()+`;
		COMMENT ON ROLE `+project.Role()+` IS '`+marker+`';
		COMMENT ON SCHEMA `+project.Role()+` IS '`+marker+`';`); err != nil {
		t.Fatal(err)
	}

	var database, table, function, maintainerOID uint32
	if err := owner.QueryRow(ctx, `SELECT oid, 'app.orders'::regclass::oid, 'app.report()'::regprocedure::oid,
		'fixture_app'::regrole::oid FROM pg_database WHERE datname=current_database()`).Scan(&database, &table, &function, &maintainerOID); err != nil {
		t.Fatal(err)
	}
	scope := ProjectRepairAccessScope{
		Project: project.ID(), Capabilities: []string{"index", "analyze", "statistics", "vacuum", "memory", "cron"},
		Table:    &ProjectIndexTarget{DatabaseOID: database, TableOID: table, Schema: "app", Relation: "orders"},
		Function: &ProjectFunctionMemoryTarget{Project: project.ID(), DatabaseOID: database, FunctionOID: function, Schema: "app", Function: "report"},
		Cron: &ProjectCronTarget{Project: project.ID(), DatabaseOID: database, JobID: 7, Name: "orders_refresh", OwnerOID: maintainerOID,
			CommandDigest: ownedCronDigest("SELECT 42"), WitnessJobID: 8},
		CronWindow: &ProjectCronWindowPlan{EveryMinutes: 2, CurrentMinute: 0, ProposedMinute: 1, DeadlineSeconds: 120, MaxRuntimeSeconds: 20},
	}
	logger := config.Copy()
	logger.User = "fixture_app"
	maintainer := openMonitorConn(t, ctx, logger)
	access, err := ReadProjectRepairAccess(ctx, maintainer, project, scope)
	if err != nil {
		t.Fatal(err)
	}
	got := repairAccessStatuses(access)
	for _, operation := range []string{"analyze", "vacuum", "cron"} {
		if got[operation] != "granted" {
			t.Errorf("explicit native privilege for %s: %+v", operation, got)
		}
	}
	for _, operation := range []string{"index", "joint_statistics", "column_statistics", "cleanup_trigger", "function_memory"} {
		if got[operation] != "database_permission_missing" {
			t.Errorf("maintainer should lack %s: %+v", operation, got)
		}
	}
	access, err = ReadProjectRepairAccess(ctx, owner, project, scope)
	if err != nil {
		t.Fatal(err)
	}
	got = repairAccessStatuses(access)
	for _, operation := range []string{"index", "joint_statistics", "column_statistics", "cleanup_trigger", "function_memory"} {
		if got[operation] != "granted" {
			t.Errorf("owner should have %s on the exact object: %+v", operation, got)
		}
	}
	if got["cron"] != "database_permission_missing" {
		t.Errorf("worker requires the bound job-owner login: %+v", got)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO app.orders SELECT n,n%11 FROM generate_series(1,100) n;
		CREATE TABLE app.permission_test_intents (run_id text PRIMARY KEY, target oid NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	request := ProjectMaintenanceRequest{Target: *scope.Table, RunID: "authorize-orders-analyze", Operation: ProjectAnalyze,
		ObservedAt: time.Now(), Columns: []int16{2}}
	guards := ProjectMaintenanceGuards{
		CheckPermission: func(ctx context.Context, intent ProjectMaintenanceIntent) error {
			if intent.Target != *scope.Table || intent.Operation != ProjectAnalyze {
				return errors.New("repair target or operation differs from the approved scope")
			}
			report, err := ReadProjectRepairAccess(ctx, maintainer, project, scope)
			if err != nil {
				return err
			}
			if repairAccessStatuses(report)["analyze"] != "granted" {
				return errors.New("the exact MAINTAIN grant is unavailable")
			}
			return nil
		},
		CheckCapacity: func(context.Context, ProjectMaintenanceIntent) error { return nil },
		RecordIntent: func(ctx context.Context, intent ProjectMaintenanceIntent) error {
			_, err := owner.Exec(ctx, "INSERT INTO app.permission_test_intents VALUES ($1,$2)", intent.RunID, intent.Target.TableOID)
			return err
		},
	}
	result, err := ApplyProjectMaintenance(ctx, logger, request, guards)
	if err != nil || !result.Confirmed || result.After.Count != result.Before.Count+1 {
		t.Fatalf("existing repair failed with the live permission guard: %+v, %v", result, err)
	}

	scope.Capabilities = []string{"analyze", "cron"}
	scope.CronWindow.CurrentMinute, scope.CronWindow.ProposedMinute = 1, 0
	if _, err := owner.Exec(ctx, "REVOKE MAINTAIN ON app.orders FROM fixture_app"); err != nil {
		t.Fatal(err)
	}
	access, err = ReadProjectRepairAccess(ctx, maintainer, project, scope)
	if err != nil {
		t.Fatal(err)
	}
	got = repairAccessStatuses(access)
	if got["analyze"] != "database_permission_missing" || got["cron"] != "cron_window_mismatch" || got["vacuum"] != "not_selected" {
		t.Errorf("revocation, schedule drift, and unselected action: %+v", got)
	}
	retry, err := ApplyProjectMaintenance(ctx, logger, request, guards)
	if err == nil || retry.WriteAttempted {
		t.Fatalf("revoked grant reached a write: %+v, %v", retry, err)
	}
	scope.Table.TableOID++
	access, err = ReadProjectRepairAccess(ctx, owner, project, scope)
	if err != nil || repairAccessStatuses(access)["analyze"] != "target_changed" {
		t.Fatalf("replaced table must lose permission: %+v, %v", access, err)
	}
	foreign, _ := supabaseproject.New(project.Ref(), "aaaaaaaaaaaaaaaa")
	scope.Project = foreign.ID()
	if _, err := ReadProjectRepairAccess(ctx, owner, foreign, scope); err == nil {
		t.Fatal("a different installation without matching markers was accepted")
	}
}

func repairAccessStatuses(report ProjectRepairAccessReport) map[string]string {
	statuses := make(map[string]string)
	for _, access := range report.Operations {
		statuses[access.Operation] = access.Status
	}
	return statuses
}
