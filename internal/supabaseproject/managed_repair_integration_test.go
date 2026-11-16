//go:build integration

package supabaseproject

import (
	"context"
	"testing"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestManagedRepairValidatesNativeOwnerAndInstallation(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	project, err := New("abcdefghijklmnopqrst", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	marker := project.bootstrapMarker()
	_, err = admin.Exec(ctx, `CREATE ROLE `+project.Role()+` LOGIN NOINHERIT CONNECTION LIMIT 2;
        CREATE SCHEMA `+project.Role()+`;
        COMMENT ON ROLE `+project.Role()+` IS '`+marker+`';
        COMMENT ON SCHEMA `+project.Role()+` IS '`+marker+`';
        CREATE ROLE test_control LOGIN SUPERUSER PASSWORD 'local-control-password';`, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.VerifyManagedRepair(ctx, admin); err == nil {
		t.Fatal("managed repair admitted a superuser")
	}
	controlConfig := config.Copy()
	controlConfig.User, controlConfig.Password = "test_control", "local-control-password"
	control, err := pgx.ConnectConfig(ctx, controlConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close(context.Background())
	if _, err := control.Exec(ctx, "ALTER ROLE postgres RENAME TO bootstrap_admin; CREATE ROLE postgres LOGIN PASSWORD '"+config.Password+"';", pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	admin, err = pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if err := project.VerifyManagedRepair(ctx, admin); err != nil {
		t.Fatalf("ordinary postgres login rejected: %v", err)
	}
	foreign, _ := New("abcdefghijklmnopqrst", "fedcba9876543210")
	if err := foreign.VerifyManagedRepair(ctx, admin); err == nil {
		t.Fatal("foreign installation admitted")
	}
	if _, err := admin.Exec(ctx, "SET default_transaction_read_only=on"); err != nil {
		t.Fatal(err)
	}
	if err := project.VerifyManagedRepair(ctx, admin); err == nil {
		t.Fatal("read-only session admitted")
	}
	if _, err := admin.Exec(ctx, "SET default_transaction_read_only=off"); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Exec(ctx, "COMMENT ON SCHEMA "+project.Role()+" IS NULL"); err != nil {
		t.Fatal(err)
	}
	if err := project.VerifyManagedRepair(ctx, admin); err == nil {
		t.Fatal("replaced installation marker admitted")
	}
	if _, err := control.Exec(ctx, "COMMENT ON SCHEMA "+project.Role()+" IS '"+marker+"'"); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Exec(ctx, "SET ROLE postgres"); err != nil {
		t.Fatal(err)
	}
	if err := project.VerifyManagedRepair(ctx, control); err == nil {
		t.Fatal("assumed role admitted as native login")
	}
}

func TestManagedIndexReaderInstallIsBoundAndRetryable(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if _, err := admin.Exec(ctx, "CREATE EXTENSION pg_stat_statements"); err != nil {
		t.Fatal(err)
	}
	project, _ := New("abcdefghijklmnopqrst", "0123456789abcdef")
	if _, err := admin.Exec(ctx, project.managedIndexReaderSQL(), pgx.QueryExecModeSimpleProtocol); err == nil {
		t.Fatal("reader installed without connected monitor")
	}
	_, _ = admin.Exec(ctx, "ROLLBACK")
	setup, err := project.bootstrapSQL("disposable-monitor-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, setup, pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := admin.Exec(ctx, project.managedIndexReaderSQL(), pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatal(err)
		}
	}
	var publicAccess bool
	err = admin.QueryRow(ctx, `SELECT has_function_privilege('fixture_app', $1, 'EXECUTE')`, project.Role()+".read_discovered_index(oid,bigint)").Scan(&publicAccess)
	if err != nil || publicAccess {
		t.Fatalf("public reader exposure: %v, %v", publicAccess, err)
	}
	if _, err := admin.Exec(ctx, "COMMENT ON SCHEMA "+project.Role()+" IS 'foreign installation'"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, project.managedIndexReaderSQL(), pgx.QueryExecModeSimpleProtocol); err == nil {
		t.Fatal("reader replaced in foreign installation")
	}
}

func TestAutomaticDiscoveryObjectivePersistsAcrossReconnect(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	if _, err := admin.Exec(ctx, "CREATE EXTENSION pg_stat_statements"); err != nil {
		t.Fatal(err)
	}
	project, _ := New("abcdefghijklmnopqrst", "0123456789abcdef")
	script, err := project.bootstrapSQL("first-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, script, pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	goalSQL, err := project.automaticObjectiveSQL(25)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := admin.Exec(ctx, goalSQL, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatal(err)
		}
	}
	verifier, err := passwordVerifier("second-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, project.reconnectSQL(verifier), pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `CREATE TABLE public.managed_goal_test(id integer);
        INSERT INTO public.managed_goal_test VALUES(1);
        GRANT SELECT ON public.managed_goal_test TO fixture_app`, pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	appConfig := config.Copy()
	appConfig.User = "fixture_app"
	app, err := pgx.ConnectConfig(ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	var id int
	err = app.QueryRow(ctx, "SELECT id FROM public.managed_goal_test WHERE id=$1", 1).Scan(&id)
	app.Close(context.Background())
	if err != nil || id != 1 {
		t.Fatalf("ordinary traffic failed: %d %v", id, err)
	}
	monitorConfig := config.Copy()
	monitorConfig.User, monitorConfig.Password = project.Role(), "second-password"
	monitorConfig.RuntimeParams["default_transaction_read_only"] = "on"
	monitor, err := pgx.ConnectConfig(ctx, monitorConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close(context.Background())
	var goal float64
	err = monitor.QueryRow(ctx, "SELECT mean_execution_ms FROM "+project.Role()+".snapshot() WHERE role_oid='fixture_app'::regrole::oid").Scan(&goal)
	if err != nil || goal != 25 {
		t.Fatalf("normal discovered traffic lost objective after reconnect: %v %v", goal, err)
	}
	if _, err := admin.Exec(ctx, "COMMENT ON SCHEMA "+project.Role()+" IS NULL"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, goalSQL, pgx.QueryExecModeSimpleProtocol); err == nil {
		t.Fatal("objective changed for an unverified installation")
	}
	for _, goal := range []int{-1, 0, 1001} {
		if _, err := project.automaticObjectiveSQL(goal); err == nil {
			t.Fatalf("accepted goal %d", goal)
		}
	}
}
