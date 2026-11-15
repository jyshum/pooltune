//go:build integration

package supabaseproject

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestSupabaseAutomaticDiscoveryReturnsOnlyApplicationCounters(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements", "-c", "pg_stat_statements.track=all")
	admin, monitor, project := installDiscoveryFixture(t, ctx, config)
	snapshot := "SELECT * FROM " + qualified(project.Role(), "snapshot") + "()"
	if rows := discoveryRows(t, ctx, monitor, snapshot); len(rows) != 0 {
		t.Fatalf("empty project discovered %d statements", len(rows))
	}

	mustMonitorExec(t, ctx, admin, `
		CREATE ROLE anon NOLOGIN;
		CREATE ROLE authenticated NOLOGIN;
		CREATE ROLE service_role NOLOGIN BYPASSRLS;
		CREATE ROLE authenticator LOGIN NOINHERIT PASSWORD 'disposable-local-fixture-only';
		GRANT anon, authenticated TO authenticator;
		CREATE ROLE supabase_auth_admin LOGIN;
		CREATE ROLE pt_monitor_other LOGIN;
		CREATE ROLE diagnostics LOGIN;
		GRANT pg_read_all_stats TO diagnostics;
		CREATE SCHEMA app_data;
		CREATE TABLE app_data.items (id integer PRIMARY KEY, token text);
		INSERT INTO app_data.items VALUES (1, 'private-row-value');
		GRANT USAGE ON SCHEMA app_data TO fixture_app, anon, authenticated, service_role, authenticator,
			supabase_auth_admin, pt_monitor_other, diagnostics;
		GRANT SELECT ON app_data.items TO fixture_app, anon, authenticated, service_role, authenticator,
			supabase_auth_admin, pt_monitor_other, diagnostics;
		GRANT INSERT ON app_data.items TO authenticated;
		CREATE FUNCTION app_data.nested_read() RETURNS integer LANGUAGE plpgsql AS $$
		BEGIN
			RETURN (SELECT max(id) FROM app_data.items);
		END
		$$;
	`)
	appQuery := "SELECT token FROM app_data.items WHERE id=1 /* private-sql-comment */"
	apiQuery := "WITH pgrst_source AS (SELECT id FROM app_data.items) SELECT pg_catalog.count(*) FROM pgrst_source"
	apiWrite := "WITH pgrst_source AS (INSERT INTO app_data.items VALUES (2, 'private-literal') RETURNING id) SELECT pg_catalog.count(*) FROM pgrst_source"
	for _, role := range []string{"postgres", "service_role", "authenticator", "supabase_auth_admin", "pt_monitor_other", "diagnostics"} {
		mustMonitorExec(t, ctx, admin, "SET ROLE "+role)
		mustMonitorExec(t, ctx, admin, appQuery)
	}
	mustMonitorExec(t, ctx, admin, "RESET ROLE")
	appConfig := config.Copy()
	appConfig.User = "fixture_app"
	app := openMonitorConn(t, ctx, appConfig)
	for _, query := range []string{
		appQuery, appQuery,
		"SELECT 1",
		"SELECT pid FROM pg_catalog.pg_stat_activity",
		"SELECT role_name FROM information_schema.applicable_roles",
		"SELECT id INTO TEMP local_items FROM app_data.items",
		"WITH cte AS (SELECT id FROM app_data.items) SELECT id INTO TEMP cte_items FROM cte",
		"CREATE TEMP TABLE scratch (id integer)",
		"SELECT app_data.nested_read()",
	} {
		mustMonitorExec(t, ctx, app, query)
	}
	apiConfig := config.Copy()
	apiConfig.User, apiConfig.Password = "authenticator", "disposable-local-fixture-only"
	api := openMonitorConn(t, ctx, apiConfig)
	mustMonitorExec(t, ctx, api, "SET ROLE anon")
	mustMonitorExec(t, ctx, api, apiQuery)
	mustMonitorExec(t, ctx, api, "SET ROLE authenticated")
	mustMonitorExec(t, ctx, api, apiQuery)
	mustMonitorExec(t, ctx, api, apiWrite)
	// pg_stat_statements records other databases as well as the monitored one.
	mustMonitorExec(t, ctx, admin, "CREATE DATABASE other_workload OWNER fixture_app")
	otherConfig := appConfig.Copy()
	otherConfig.Database = "other_workload"
	other := openMonitorConn(t, ctx, otherConfig)
	mustMonitorExec(t, ctx, other, "CREATE TABLE private_to_other_db AS SELECT 12 AS id")
	mustMonitorExec(t, ctx, other, "SELECT id FROM private_to_other_db")

	want := map[queryKey]int64{
		fixtureDiscoveryKey(t, ctx, admin, "fixture_app", "SELECT token FROM app_data.items WHERE id=$1 /* private-sql-comment */"): 2,
		fixtureDiscoveryKey(t, ctx, admin, "anon", apiQuery):                                                                        1,
		fixtureDiscoveryKey(t, ctx, admin, "authenticated", apiQuery):                                                               1,
		fixtureDiscoveryKey(t, ctx, admin, "authenticated", strings.ReplaceAll(apiWrite, "(2, 'private-literal')", "($1, $2)")):     1,
	}
	rows := discoveryRows(t, ctx, monitor, snapshot)
	if len(rows) != len(want) {
		t.Fatalf("discovered %d statements, want %d", len(rows), len(want))
	}
	for _, row := range rows {
		calls, found := want[row.objective.key()]
		if !found || row.calls == nil || *row.calls != calls || row.objective.MeanMS != 1000 {
			t.Fatalf("unexpected discovered identity, objective, or calls: %+v", row)
		}
		if row.boot.IsZero() || row.entry == nil || row.entry.IsZero() || row.execution == nil || *row.execution < 0 || row.blocks == nil || *row.blocks < 0 {
			t.Fatalf("incomplete discovered counter: %+v", row)
		}
		delete(want, row.objective.key())
	}
	var snapshotJSON string
	if err := monitor.QueryRow(ctx, "SELECT jsonb_agg(s)::text FROM ("+snapshot+") s").Scan(&snapshotJSON); err != nil {
		t.Fatal(err)
	}
	for _, privateValue := range []string{"private-row-value", "private-sql-comment", "private-literal", "app_data", "token"} {
		if strings.Contains(snapshotJSON, privateValue) {
			t.Fatal("discovery returned private application content")
		}
	}
	if _, err := monitor.Exec(ctx, "SELECT * FROM app_data.items"); monitorSQLState(err) != "42501" {
		t.Fatalf("monitor can read application rows: %v", err)
	}
	var visibleSQL int
	if err := monitor.QueryRow(ctx, `
		SELECT count(*) FROM extensions.pg_stat_statements
		WHERE userid='fixture_app'::regrole AND queryid IS NOT NULL
	`).Scan(&visibleSQL); err != nil || visibleSQL != 0 {
		t.Fatalf("monitor gained unrestricted cross-role statistics: %d, %v", visibleSQL, err)
	}
	if _, err := app.Exec(ctx, snapshot); monitorSQLState(err) != "42501" {
		t.Fatalf("application role can call the private reader: %v", err)
	}
	plainConfig := monitor.Config()
	plainConfig.TLSConfig = nil
	plain := openMonitorConn(t, ctx, plainConfig)
	if _, err := plain.Exec(ctx, snapshot); monitorSQLState(err) != "42501" {
		t.Fatalf("monitor accepted a non-TLS connection: %v", err)
	}
	mustMonitorExec(t, ctx, monitor, "SET default_transaction_read_only=off")
	if _, err := monitor.Exec(ctx, snapshot); monitorSQLState(err) != "42501" {
		t.Fatalf("monitor accepted a read-write transaction: %v", err)
	}
	if _, err := monitor.Exec(ctx, "DELETE FROM app_data.items WHERE false"); monitorSQLState(err) != "42501" {
		t.Fatalf("monitor gained application write permission: %v", err)
	}
	mustMonitorExec(t, ctx, monitor, "SET default_transaction_read_only=on")
	mustMonitorExec(t, ctx, admin, "ALTER ROLE "+project.Role()+" NOLOGIN")
	if _, err := monitor.Exec(ctx, snapshot); monitorSQLState(err) != "42501" {
		t.Fatalf("monitor kept discovering after login was revoked: %v", err)
	}
}

func TestSupabaseAutomaticDiscoveryBoundsCandidatesAndPreservesManualObjectives(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	admin, monitor, project := installDiscoveryFixture(t, ctx, config)
	snapshot := "SELECT * FROM " + qualified(project.Role(), "snapshot") + "()"
	mustMonitorExec(t, ctx, admin, "CREATE SCHEMA app_data AUTHORIZATION fixture_app")
	mustMonitorExec(t, ctx, admin, "SET ROLE fixture_app")
	for i := 0; i < 25; i++ {
		table := fmt.Sprintf("app_data.item_%d", i)
		mustMonitorExec(t, ctx, admin, "CREATE TABLE "+table+" AS SELECT 1 AS id")
		mustMonitorExec(t, ctx, admin, "SELECT id FROM "+table)
	}
	for i := 0; i < 5; i++ {
		mustMonitorExec(t, ctx, admin, "SELECT pg_sleep(0.01) FROM app_data.item_0")
	}
	mustMonitorExec(t, ctx, admin, "RESET ROLE")
	manual := fixtureDiscoveryKey(t, ctx, admin, "fixture_app", "SELECT id FROM app_data.item_0")
	slow := fixtureDiscoveryKey(t, ctx, admin, "fixture_app", "SELECT pg_sleep($1) FROM app_data.item_0")
	newest := fixtureDiscoveryKey(t, ctx, admin, "fixture_app", "SELECT id FROM app_data.item_24")
	if _, err := admin.Exec(ctx, "INSERT INTO "+qualified(project.Role(), "enrolled_queries")+" VALUES (1,$1,$2,2.5),(2,$1,17,5)", manual.Role, manual.Query); err != nil {
		t.Fatal(err)
	}
	rows := discoveryRows(t, ctx, monitor, snapshot)
	if len(rows) != 20 {
		t.Fatalf("discovered %d identities, want 20", len(rows))
	}
	if rows[0].objective.key() != manual || rows[0].objective.MeanMS != 2.5 || rows[0].calls == nil || *rows[0].calls != 1 {
		t.Fatal("discovery changed the manual query identity, objective, or counter")
	}
	if rows[1].objective.Query != 17 || rows[1].objective.MeanMS != 5 || rows[1].calls != nil {
		t.Fatal("discovery dropped a manual query without counters")
	}
	seen := make(map[queryKey]bool)
	for _, row := range rows {
		key := row.objective.key()
		if seen[key] {
			t.Fatalf("duplicate query identity: %+v", key)
		}
		seen[key] = true
	}
	if !seen[slow] || !seen[newest] {
		t.Fatal("discovery failed to retain both a high-impact and a newly tracked identity")
	}
}

func installDiscoveryFixture(t *testing.T, ctx context.Context, config *pgx.ConnConfig) (*pgx.Conn, *pgx.Conn, Project) {
	t.Helper()
	admin := openMonitorConn(t, ctx, config)
	project, err := New("abcdefghijklmnopqrst", "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	mustMonitorExec(t, ctx, admin, "CREATE SCHEMA extensions; CREATE EXTENSION pg_stat_statements WITH SCHEMA extensions; GRANT USAGE ON SCHEMA extensions TO PUBLIC")
	mustMonitorExec(t, ctx, admin, project.InstallSQL())
	mustMonitorExec(t, ctx, admin, project.AutomaticDiscoverySQL())
	const password = "disposable-local-fixture-only"
	mustMonitorExec(t, ctx, admin, "ALTER ROLE "+project.Role()+" LOGIN PASSWORD '"+password+"'")
	monitorConfig := config.Copy()
	monitorConfig.User, monitorConfig.Password = project.Role(), password
	monitorConfig.RuntimeParams["default_transaction_read_only"] = "on"
	monitorConfig.RuntimeParams["statement_timeout"] = "2s"
	monitor := openMonitorConn(t, ctx, monitorConfig)
	return admin, monitor, project
}

type discoveryRow struct {
	objective     discoveryObjective
	boot, reset   time.Time
	entry         *time.Time
	calls, blocks *int64
	execution     *float64
}

type queryKey struct {
	Database, Role uint32
	Query          int64
}

type discoveryObjective struct {
	queryKey
	MeanMS float64
}

func (q discoveryObjective) key() queryKey { return q.queryKey }

func discoveryRows(t *testing.T, ctx context.Context, target *pgx.Conn, snapshot string) []discoveryRow {
	t.Helper()
	rows, err := target.Query(ctx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []discoveryRow
	for rows.Next() {
		var row discoveryRow
		if err := rows.Scan(&row.objective.Database, &row.objective.Role, &row.objective.Query, &row.objective.MeanMS,
			&row.boot, &row.reset, &row.entry, &row.calls, &row.execution, &row.blocks); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func fixtureDiscoveryKey(t *testing.T, ctx context.Context, admin *pgx.Conn, role, text string) queryKey {
	t.Helper()
	var key queryKey
	err := admin.QueryRow(ctx, `
		SELECT dbid,userid,queryid FROM extensions.pg_stat_statements
		WHERE toplevel AND userid=$1::regrole AND query=$2
	`, role, text).Scan(&key.Database, &key.Role, &key.Query)
	if err != nil {
		t.Fatalf("expected fixture counter for %s: %v", role, err)
	}
	return key
}

func qualified(schema, name string) string { return pgx.Identifier{schema, name}.Sanitize() }

func openMonitorConn(t *testing.T, ctx context.Context, config *pgx.ConnConfig) *pgx.Conn {
	t.Helper()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func mustMonitorExec(t *testing.T, ctx context.Context, conn *pgx.Conn, sql string) {
	t.Helper()
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
}

func monitorSQLState(err error) string {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return postgresError.Code
	}
	return ""
}
