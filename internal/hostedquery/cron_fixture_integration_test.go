//go:build integration

package hostedquery

import (
	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
	"testing"
	"time"
)

func TestCronPrerequisitesUseSupportedAPIsBeforeFixtureCreation(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	// Real ACL checks; only extension/settings discovery and API bodies are adapted.
	_, err = admin.Exec(ctx, `CREATE SCHEMA cron;
 CREATE TABLE cron.job(jobid bigint PRIMARY KEY,jobname text,username text,database text,command text,schedule text,active boolean);
 INSERT INTO cron.job VALUES(7,'existing','fixture_app','postgres','SELECT 1','2 seconds',false);
 CREATE TABLE cron.job_run_details(runid bigint,jobid bigint,job_pid integer,start_time timestamptz,end_time timestamptz,status text);
 CREATE FUNCTION cron.schedule(text,text,text) RETURNS bigint LANGUAGE sql AS $$SELECT 7::bigint$$;
 CREATE FUNCTION cron.alter_job(bigint,schedule text DEFAULT NULL,command text DEFAULT NULL,database text DEFAULT NULL,username text DEFAULT NULL,active boolean DEFAULT NULL) RETURNS void LANGUAGE sql AS $$SELECT$$;
 CREATE FUNCTION cron.unschedule(bigint) RETURNS boolean LANGUAGE sql AS $$SELECT true$$;
 CREATE SCHEMA cron_adapter; CREATE VIEW cron_adapter.pg_extension AS SELECT 'pg_cron'::name AS extname,'1.6.4'::text AS extversion;
 GRANT USAGE ON SCHEMA cron,cron_adapter TO fixture_app;
 GRANT SELECT ON cron.job,cron.job_run_details,cron_adapter.pg_extension TO fixture_app;
 REVOKE ALL ON ALL FUNCTIONS IN SCHEMA cron FROM PUBLIC;
 GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA cron TO fixture_app`)
	if err != nil {
		t.Fatal(err)
	}
	restricted := cfg.Copy()
	restricted.User = "fixture_app"
	restricted.RuntimeParams["search_path"] = "cron_adapter,pg_catalog"
	for key, value := range map[string]string{"cron.timezone": "UTC", "cron.log_run": "on", "cron.use_background_workers": "off", "cron.database_name": cfg.Database} {
		restricted.RuntimeParams[key] = value
	}
	target, err := pgx.ConnectConfig(ctx, restricted)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close(ctx)
	var directUpdate bool
	if err = target.QueryRow(ctx, "SELECT has_any_column_privilege(current_user,'cron.job','UPDATE')").Scan(&directUpdate); err != nil || directUpdate {
		t.Fatalf("adapter must have no catalog UPDATE: %v %v", directUpdate, err)
	}
	if err = cronPrerequisites(ctx, target); err != nil {
		t.Fatalf("supported API role without catalog UPDATE rejected: %v", err)
	}
	for _, privilege := range []string{"SELECT ON cron.job", "SELECT ON cron.job_run_details", "USAGE ON SCHEMA cron", "EXECUTE ON FUNCTION cron.schedule(text,text,text)", "EXECUTE ON FUNCTION cron.alter_job(bigint,text,text,text,text,boolean)", "EXECUTE ON FUNCTION cron.unschedule(bigint)"} {
		t.Run(privilege, func(t *testing.T) {
			if _, err = admin.Exec(ctx, "REVOKE "+privilege+" FROM fixture_app"); err != nil {
				t.Fatal(err)
			}
			f, e := CreateCronFixture(ctx, target, admin, "local")
			if e == nil || f.ID != "" {
				t.Fatalf("missing capability admitted creation: %+v %v", f, e)
			}
			var untouched bool
			if err = admin.QueryRow(ctx, `SELECT to_regnamespace('pooltune_lab_state') IS NULL AND NOT EXISTS(SELECT FROM pg_namespace WHERE nspname LIKE 'pooltune_trial_%') AND (SELECT count(*)=1 AND min(jobid)=7 FROM cron.job)`).Scan(&untouched); err != nil || !untouched {
				t.Fatalf("preflight mutated state: %v %v", untouched, err)
			}
			if _, err = admin.Exec(ctx, "GRANT "+privilege+" TO fixture_app"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// This exercises real PostgreSQL transaction control and executor timing.
// It does not emulate or qualify the pg_cron scheduler.
func TestCronProcedureCommitsAndOuterCounterIncludesLockWait(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	_, err = db.Exec(ctx, `CREATE EXTENSION pg_stat_statements; CREATE SCHEMA cron_test; CREATE TABLE cron_test.candidate(id integer PRIMARY KEY); INSERT INTO cron_test.candidate VALUES(43)`)
	if err != nil {
		t.Fatal(err)
	}
	if err = createCronRoutines(ctx, db, "cron_test", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A top-level CALL with two iterations proves the second transaction runs.
	start := time.Now()
	if _, err = db.Exec(ctx, `CALL cron_test.pooltune_cron_hold(2)`); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatal("procedure returned before required holds")
	}
	for _, count := range []int{0, 201} {
		if _, err = db.Exec(ctx, `CALL cron_test.pooltune_cron_hold($1)`, count); err == nil {
			t.Fatal("unbounded hold count accepted")
		}
	}
	var answer int
	if err = db.QueryRow(ctx, cronQuery("cron_test")).Scan(&answer); err != nil || answer != 43 {
		t.Fatalf("read %d %v", answer, err)
	}
	blocker, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close(ctx)
	_, err = blocker.Exec(ctx, "BEGIN; LOCK TABLE cron_test.candidate IN ACCESS EXCLUSIVE MODE")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- db.QueryRow(ctx, cronQuery("cron_test")).Scan(&answer) }()
	time.Sleep(100 * time.Millisecond)
	if _, err = blocker.Exec(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	var ms float64
	err = db.QueryRow(ctx, `SELECT total_exec_time FROM pg_stat_statements WHERE query='SELECT "cron_test"."pooltune_cron_read"() AS id' AND toplevel`).Scan(&ms)
	if err != nil || ms < 80 {
		t.Fatalf("outer execution clock omitted lock wait: %.3f %v", ms, err)
	}
	t.Logf("actual outer pg_stat_statements execution: %.3f ms", ms)
	if _, err = db.Exec(ctx, "SET statement_timeout='1s'"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `CALL cron_test.pooltune_cron_hold(2)`); err == nil {
		t.Fatal("inherited job-session timeout below full-work allowance admitted")
	}
	if _, err = db.Exec(ctx, "RESET statement_timeout"); err != nil {
		t.Fatal(err)
	}
	// CALL inside an explicit transaction cannot silently report completed work.
	_, err = db.Exec(ctx, "BEGIN")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `CALL cron_test.pooltune_cron_hold(2)`); err == nil {
		t.Fatal("nested transaction unexpectedly committed")
	}
	db.Exec(ctx, "ROLLBACK")
}

func TestCronFullProcedureCompletesRequired200Holds(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	if _, err = db.Exec(ctx, "CREATE SCHEMA cron_full;CREATE TABLE cron_full.candidate(id integer)"); err != nil {
		t.Fatal(err)
	}
	if err = createCronRoutines(ctx, db, "cron_full", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err = db.Exec(ctx, "CALL cron_full.pooltune_cron_hold(200)"); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 16*time.Second || elapsed >= 20*time.Second {
		t.Fatalf("full work outside declared budget: %s", elapsed)
	}
	t.Logf("200 committing 80ms holds completed in %s", elapsed)
}
