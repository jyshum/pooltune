//go:build integration

package supabaseproject

import (
	"errors"
	"slices"
	"testing"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestDiscoveredIndexEvidenceBindsOrdinaryQueries(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	admin, monitor, project := installDiscoveryFixture(t, ctx, config)
	mustMonitorExec(t, ctx, admin, project.DiscoveredIndexEvidenceSQL())
	mustMonitorExec(t, ctx, admin, `CREATE SCHEMA app_data;
		CREATE TABLE app_data.workload (row_id integer PRIMARY KEY, customer integer, padding text, category integer);
		INSERT INTO app_data.workload VALUES (1, 7, 'private-row-value', 3);
		ANALYZE app_data.workload;
		GRANT USAGE ON SCHEMA app_data TO fixture_app;
		GRANT SELECT ON app_data.workload TO fixture_app`)
	appConfig := config.Copy()
	appConfig.User = "fixture_app"
	app := openMonitorConn(t, ctx, appConfig)
	var tableOID uint32
	if err := admin.QueryRow(ctx, "SELECT 'app_data.workload'::regclass::oid").Scan(&tableOID); err != nil {
		t.Fatal(err)
	}

	observe := func(statement string, args ...any) queryKey {
		t.Helper()
		var value int
		if err := app.QueryRow(ctx, statement, args...).Scan(&value); err != nil {
			t.Fatalf("normal application query failed: %v", err)
		}
		return fixtureDiscoveryKey(t, ctx, admin, "fixture_app", statement)
	}
	bound := func(key queryKey, expected []int16, only bool) {
		t.Helper()
		got, err := project.ReadDiscoveredIndexEvidence(ctx, monitor, key.Role, key.Query)
		if err != nil {
			t.Fatalf("bind discovered query: %v", err)
		}
		if got.DatabaseOID != key.Database || got.RoleOID != key.Role || got.QueryID != key.Query ||
			got.Schema != "app_data" || got.Relation != "workload" || got.TableOID != tableOID ||
			!slices.Equal(got.Columns, expected) || got.Only != only || got.PlanNode == "" ||
			got.Boot.IsZero() || got.ResetAt.IsZero() || got.EntrySince.IsZero() || got.ObjectiveMS <= 0 {
			t.Fatalf("incorrect query and catalog binding: %+v", got)
		}
	}
	unavailable := func(role uint32, query int64) {
		t.Helper()
		_, err := project.ReadDiscoveredIndexEvidence(ctx, monitor, role, query)
		if !errors.Is(err, ErrDiscoveredIndexEvidenceUnavailable) {
			t.Fatalf("expected evidence to be unavailable, got %v", err)
		}
	}
	compound := observe(`SELECT "customer" FROM ONLY "app_data"."workload" WHERE "customer" = $1 AND "category" = $2`, 7, 3)
	bound(compound, []int16{2, 4}, true)
	reversed := observe("SELECT category FROM app_data.workload WHERE category = $1 AND customer = $2", 3, 7)
	bound(reversed, []int16{4, 2}, false)
	healthy := observe("SELECT row_id FROM app_data.workload WHERE row_id = $1", 1)
	bound(healthy, []int16{1}, false)
	unsupported := observe("SELECT customer FROM app_data.workload WHERE customer = $1 AND category >= $2", 7, 3)
	unavailable(unsupported.Role, unsupported.Query)
	ambiguous := observe("SELECT customer FROM app_data.workload WHERE customer = $1 OR category = $2", 7, 3)
	unavailable(ambiguous.Role, ambiguous.Query)
	unavailable(0, compound.Query)
	unavailable(compound.Role, compound.Query+1)

	enrollments := qualified(project.Role(), "enrolled_queries")
	if _, err := admin.Exec(ctx, "INSERT INTO "+enrollments+" VALUES (1,$1,$2,1.0)", compound.Role, compound.Query); err != nil {
		t.Fatal(err)
	}
	unavailable(compound.Role, compound.Query)
	mustMonitorExec(t, ctx, admin, "DELETE FROM "+enrollments+" WHERE slot=1")
	bound(compound, []int16{2, 4}, true)

	mustMonitorExec(t, ctx, admin, `CREATE FUNCTION app_data.custom_eq(integer, integer) RETURNS boolean
		LANGUAGE SQL IMMUTABLE RETURN $1 OPERATOR(pg_catalog.=) $2;
		CREATE OPERATOR app_data.= (FUNCTION=app_data.custom_eq, LEFTARG=integer, RIGHTARG=integer)`)
	mustMonitorExec(t, ctx, app, "SET search_path=app_data,pg_catalog")
	shadowed := observe(`SELECT "category" FROM ONLY "app_data"."workload" WHERE "category" = $1 AND "customer" = $2`, 3, 7)
	mustMonitorExec(t, ctx, app, "RESET search_path")
	unavailable(shadowed.Role, shadowed.Query)
	bound(compound, []int16{2, 4}, true)

	var calls int64
	if err := admin.QueryRow(ctx, `SELECT calls FROM extensions.pg_stat_statements
		WHERE dbid=$1 AND userid=$2 AND queryid=$3 AND toplevel`, compound.Database, compound.Role, compound.Query).
		Scan(&calls); err != nil || calls != 1 {
		t.Fatalf("reading evidence changed ordinary application calls to %d: %v", calls, err)
	}
	if _, err := project.ReadDiscoveredIndexEvidence(ctx, app, compound.Role, compound.Query); !errors.Is(err, ErrDiscoveredIndexEvidenceUnavailable) {
		t.Fatalf("application role could read privileged evidence: %v", err)
	}
	mustMonitorExec(t, ctx, admin, "REVOKE SELECT ON app_data.workload FROM fixture_app")
	unavailable(compound.Role, compound.Query)
	mustMonitorExec(t, ctx, admin, "GRANT SELECT ON app_data.workload TO fixture_app")
	bound(compound, []int16{2, 4}, true)
	mustMonitorExec(t, ctx, admin, "DROP TABLE app_data.workload; CREATE TABLE app_data.workload (row_id integer PRIMARY KEY, customer integer, padding text, category integer); GRANT SELECT ON app_data.workload TO fixture_app")
	unavailable(compound.Role, compound.Query)

	mustMonitorExec(t, ctx, admin, "REVOKE EXECUTE ON FUNCTION "+qualified(project.Role(), "snapshot")+"() FROM "+pgx.Identifier{project.Role()}.Sanitize())
	unavailable(healthy.Role, healthy.Query)
}
