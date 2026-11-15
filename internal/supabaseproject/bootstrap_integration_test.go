//go:build integration

package supabaseproject

import (
	"testing"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestBootstrapSQLAndReconnect(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE EXTENSION pg_stat_statements"); err != nil {
		t.Fatal(err)
	}
	project, err := New("abcdefghijklmnopqrst", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	const firstPassword = "test password from a managed bootstrap"
	script, err := project.bootstrapSQL(firstPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, script, pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	monitor := config.Copy()
	monitor.User, monitor.Password = project.Role(), firstPassword
	monitor.RuntimeParams["default_transaction_read_only"] = "on"
	monitor.RuntimeParams["statement_timeout"] = "2s"
	readSnapshot := func() {
		t.Helper()
		conn, err := pgx.ConnectConfig(ctx, monitor)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		var n int
		err = conn.QueryRow(ctx, "SELECT count(*) FROM "+project.Role()+".snapshot()").Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
	}
	readSnapshot()
	if _, err := admin.Exec(ctx, `CREATE SCHEMA app_data;
		CREATE TABLE app_data.items (lookup integer);
		INSERT INTO app_data.items VALUES (17);
		GRANT USAGE ON SCHEMA app_data TO fixture_app;
		GRANT SELECT ON app_data.items TO fixture_app;`, pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	workload := config.Copy()
	workload.User = "fixture_app"
	app, err := pgx.ConnectConfig(ctx, workload)
	if err != nil {
		t.Fatal(err)
	}
	var value int
	err = app.QueryRow(ctx, "SELECT lookup FROM app_data.items WHERE lookup = $1", 17).Scan(&value)
	app.Close(ctx)
	if err != nil || value != 17 {
		t.Fatalf("application lookup = %d, %v", value, err)
	}
	read, err := pgx.ConnectConfig(ctx, monitor)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	err = read.QueryRow(ctx, "SELECT count(*) FROM "+project.Role()+".snapshot() observed CROSS JOIN LATERAL "+
		project.Role()+".read_discovered_lookup(observed.role_oid, observed.query_id) matched").Scan(&n)
	read.Close(ctx)
	if err != nil || n != 1 {
		t.Fatalf("expected one discovered evidence row, got %d, %v", n, err)
	}
	monitor.Password = "test password after reauthorizing the managed installation"
	scram, err := passwordVerifier(monitor.Password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, project.reconnectSQL(scram), pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	readSnapshot()
	if _, err := admin.Exec(ctx, "COMMENT ON SCHEMA "+project.Role()+" IS NULL"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, project.reconnectSQL(scram), pgx.QueryExecModeSimpleProtocol); err == nil {
		t.Fatal("unrecognized installation could rotate credentials")
	}
}

func TestDiscoveredLookupVerifiesStatementAndCatalogIdentity(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	admin, monitor, project := installDiscoveryFixture(t, ctx, config)
	mustMonitorExec(t, ctx, admin, project.discoveredLookupSQL())
	mustMonitorExec(t, ctx, admin, `CREATE SCHEMA app_data;
		GRANT USAGE ON SCHEMA app_data TO fixture_app;
		CREATE FUNCTION app_data.custom_eq(integer, integer) RETURNS boolean
			LANGUAGE SQL IMMUTABLE RETURN $1 OPERATOR(pg_catalog.=) $2;
		CREATE OPERATOR app_data.= (FUNCTION=app_data.custom_eq, LEFTARG=integer, RIGHTARG=integer);`)
	appConfig := config.Copy()
	appConfig.User = "fixture_app"
	app := openMonitorConn(t, ctx, appConfig)
	lookup := "SELECT * FROM " + qualified(project.Role(), "read_discovered_lookup") + "($1::oid,$2::bigint)"
	createTable := func(t *testing.T, name string) {
		t.Helper()
		mustMonitorExec(t, ctx, admin, "CREATE TABLE app_data."+name+" (lookup integer)")
		mustMonitorExec(t, ctx, admin, "INSERT INTO app_data."+name+" VALUES (17)")
		mustMonitorExec(t, ctx, admin, "GRANT SELECT ON app_data."+name+" TO fixture_app")
	}
	executeLookup := func(t *testing.T, name string) queryKey {
		t.Helper()
		statement := "SELECT lookup FROM app_data." + name + " WHERE lookup = $1"
		var value int
		if err := app.QueryRow(ctx, statement, 17).Scan(&value); err != nil || value != 17 {
			t.Fatalf("application lookup = %d, %v", value, err)
		}
		return fixtureDiscoveryKey(t, ctx, admin, "fixture_app", statement)
	}
	assertNoEvidence := func(t *testing.T, key queryKey) {
		t.Helper()
		var found int
		if err := monitor.QueryRow(ctx, "SELECT count(*) FROM ("+lookup+") e", key.Role, key.Query).Scan(&found); err != nil || found != 0 {
			t.Fatalf("unsafe lookup returned %d evidence rows, %v", found, err)
		}
	}
	assertBound := func(t *testing.T, key queryKey, name string) {
		t.Helper()
		var wantOID, dbOID, tableOID uint32
		var column int16
		if err := admin.QueryRow(ctx, "SELECT $1::regclass::oid", "app_data."+name).Scan(&wantOID); err != nil {
			t.Fatal(err)
		}
		if err := monitor.QueryRow(ctx, "SELECT database_oid,table_oid,column_number FROM ("+lookup+") e", key.Role, key.Query).
			Scan(&dbOID, &tableOID, &column); err != nil || dbOID != key.Database || tableOID != wantOID || column != 1 {
			t.Fatalf("lookup bound to database %d, table %d, column %d, %v", dbOID, tableOID, column, err)
		}
	}
	t.Run("built-in equality", func(t *testing.T) {
		createTable(t, "plain_lookup")
		mustMonitorExec(t, ctx, admin, "CREATE INDEX ON app_data.plain_lookup (lookup)")
		assertBound(t, executeLookup(t, "plain_lookup"), "plain_lookup")
	})
	t.Run("explicit ordinary table and operator", func(t *testing.T) {
		statement := "SELECT lookup FROM ONLY app_data.plain_lookup WHERE lookup OPERATOR(pg_catalog.=) $1"
		if _, err := app.Exec(ctx, statement, 17); err != nil {
			t.Fatal(err)
		}
		assertBound(t, fixtureDiscoveryKey(t, ctx, admin, "fixture_app", statement), "plain_lookup")
	})
	t.Run("recreated table", func(t *testing.T) {
		createTable(t, "replaced_lookup")
		stale := executeLookup(t, "replaced_lookup")
		mustMonitorExec(t, ctx, admin, "DROP TABLE app_data.replaced_lookup")
		createTable(t, "replaced_lookup")
		assertNoEvidence(t, stale)
		var current queryKey
		if _, err := app.Exec(ctx, "SELECT lookup FROM app_data.replaced_lookup WHERE lookup = $1", 17); err != nil {
			t.Fatal(err)
		}
		if err := admin.QueryRow(ctx, `SELECT dbid,userid,queryid FROM extensions.pg_stat_statements
			WHERE toplevel AND userid=$1 AND queryid<>$2 AND query='SELECT lookup FROM app_data.replaced_lookup WHERE lookup = $1'`,
			stale.Role, stale.Query).Scan(&current.Database, &current.Role, &current.Query); err != nil {
			t.Fatal(err)
		}
		assertBound(t, current, "replaced_lookup")
	})
	t.Run("custom equality", func(t *testing.T) {
		createTable(t, "operator_lookup")
		mustMonitorExec(t, ctx, app, "SET search_path=app_data,pg_catalog")
		key := executeLookup(t, "operator_lookup")
		mustMonitorExec(t, ctx, app, "RESET search_path")
		assertNoEvidence(t, key)
	})
	t.Run("expression index", func(t *testing.T) {
		createTable(t, "expression_lookup")
		key := executeLookup(t, "expression_lookup")
		mustMonitorExec(t, ctx, admin, "CREATE INDEX ON app_data.expression_lookup ((lookup + 1))")
		assertNoEvidence(t, key)
	})
	t.Run("predicate index", func(t *testing.T) {
		createTable(t, "predicate_lookup")
		key := executeLookup(t, "predicate_lookup")
		mustMonitorExec(t, ctx, admin, "CREATE INDEX ON app_data.predicate_lookup (lookup) WHERE lookup > 0")
		assertNoEvidence(t, key)
	})
	t.Run("busy table", func(t *testing.T) {
		createTable(t, "busy_lookup")
		key := executeLookup(t, "busy_lookup")
		lock, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback(ctx)
		if _, err := lock.Exec(ctx, "LOCK TABLE app_data.busy_lookup IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatal(err)
		}
		assertNoEvidence(t, key)
	})
	t.Run("released planning locks", func(t *testing.T) {
		createTable(t, "released_lookup")
		key := executeLookup(t, "released_lookup")
		read, err := monitor.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer read.Rollback(ctx)
		var found int
		if err := read.QueryRow(ctx, "SELECT count(*) FROM ("+lookup+") e", key.Role, key.Query).Scan(&found); err != nil || found != 1 {
			t.Fatalf("expected evidence before checking lock release, got %d, %v", found, err)
		}
		lock, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback(ctx)
		if _, err := lock.Exec(ctx, "LOCK TABLE app_data.released_lookup IN ACCESS EXCLUSIVE MODE NOWAIT"); err != nil {
			t.Fatalf("reader left a lock in the open transaction: %v", err)
		}
	})
}
