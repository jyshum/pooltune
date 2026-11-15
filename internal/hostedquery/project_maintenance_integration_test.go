//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestApplyProjectMaintenanceAsExactMaintainer(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	owner := openMonitorConn(t, ctx, config)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE SCHEMA "Project Data";
		CREATE TABLE "Project Data"."Due Invoices" (id int PRIMARY KEY, total int, note text) WITH (autovacuum_enabled=false);
		CREATE TABLE "Project Data".untouched (id int PRIMARY KEY) WITH (autovacuum_enabled=false);
		CREATE TABLE "Project Data".inherited () INHERITS ("Project Data".untouched) WITH (autovacuum_enabled=false);
		CREATE TABLE "Project Data".ungranted (id int) WITH (autovacuum_enabled=false);
		CREATE TABLE "Project Data".intent_log (run_id text, kind text, relation oid, columns smallint[]);
		GRANT USAGE ON SCHEMA "Project Data" TO fixture_app;
		GRANT MAINTAIN ON "Project Data"."Due Invoices", "Project Data".untouched, "Project Data".inherited TO fixture_app;
		INSERT INTO "Project Data"."Due Invoices" SELECT n,n%100,'fixture' FROM generate_series(1,4000) n;
		DELETE FROM "Project Data"."Due Invoices" WHERE id%2=0;
		INSERT INTO "Project Data".untouched VALUES (1);
		INSERT INTO "Project Data".inherited VALUES (2);
		INSERT INTO "Project Data".ungranted VALUES (3)`)
	maintainer := config.Copy()
	maintainer.User = "fixture_app"
	readTarget := func(schema, name string) ProjectIndexTarget {
		t.Helper()
		target := ProjectIndexTarget{Schema: schema, Relation: name}
		if err := owner.QueryRow(ctx, `SELECT oid, to_regclass($1)::oid FROM pg_catalog.pg_database WHERE datname=current_database()`, qualified(schema, name)).Scan(&target.DatabaseOID, &target.TableOID); err != nil {
			t.Fatal(err)
		}
		return target
	}
	target := readTarget("Project Data", "Due Invoices")
	verifyCounts := func(target ProjectIndexTarget, analyzes, vacuums int64) {
		t.Helper()
		var gotAnalyzes, gotVacuums int64
		if err := owner.QueryRow(ctx, `SELECT analyze_count,vacuum_count FROM pg_stat_all_tables WHERE relid=$1`, target.TableOID).Scan(&gotAnalyzes, &gotVacuums); err != nil || gotAnalyzes != analyzes || gotVacuums != vacuums {
			t.Fatalf("%v maintenance counts: got %d/%d, wanted %d/%d: %v", target, gotAnalyzes, gotVacuums, analyzes, vacuums, err)
		}
	}
	var hasSelect bool
	if err := owner.QueryRow(ctx, `SELECT has_table_privilege('fixture_app',$1::oid,'SELECT')`, target.TableOID).Scan(&hasSelect); err != nil || hasSelect {
		t.Fatalf("fixture maintain role has SELECT: %t, %v", hasSelect, err)
	}
	request := ProjectMaintenanceRequest{Target: target, RunID: "ordinary-analyze", Operation: ProjectAnalyze, Columns: []int16{2}}
	var stages []string
	guards := ProjectMaintenanceGuards{
		CheckPermission: func(_ context.Context, i ProjectMaintenanceIntent) error {
			if i.Target != target || i.Operation != request.Operation || !slices.Equal(i.Columns, request.Columns) {
				return errors.New("permission received a different target")
			}
			stages = append(stages, "permission")
			return nil
		},
		CheckCapacity: func(_ context.Context, i ProjectMaintenanceIntent) error {
			if i.RelationBytes <= 0 {
				return errors.New("missing table sizing")
			}
			stages = append(stages, "capacity")
			return nil
		},
		RecordIntent: func(ctx context.Context, i ProjectMaintenanceIntent) error {
			stages = append(stages, "intent")
			_, err := owner.Exec(ctx, `INSERT INTO "Project Data".intent_log VALUES ($1,$2,$3,$4)`, i.RunID, string(i.Operation), i.Target.TableOID, i.Columns)
			return err
		},
	}
	apply := func(cfg *pgx.ConnConfig, req ProjectMaintenanceRequest, g ProjectMaintenanceGuards) (ProjectMaintenanceResult, error) {
		req.ObservedAt = time.Now()
		return ApplyProjectMaintenance(ctx, cfg, req, g)
	}
	deny := errors.New("owner grant withdrawn")
	for _, replace := range []func(*ProjectMaintenanceGuards){
		func(g *ProjectMaintenanceGuards) {
			g.CheckPermission = func(context.Context, ProjectMaintenanceIntent) error { return deny }
		},
		func(g *ProjectMaintenanceGuards) {
			g.CheckCapacity = func(context.Context, ProjectMaintenanceIntent) error { return deny }
		},
		func(g *ProjectMaintenanceGuards) {
			g.RecordIntent = func(context.Context, ProjectMaintenanceIntent) error { return deny }
		},
	} {
		g := guards
		replace(&g)
		if result, err := apply(maintainer, request, g); !errors.Is(err, deny) || result.WriteAttempted {
			t.Fatalf("guard denial reached maintenance SQL: %+v %v", result, err)
		}
	}
	permitted := ProjectMaintenanceGuards{CheckPermission: func(context.Context, ProjectMaintenanceIntent) error { return nil },
		CheckCapacity: func(context.Context, ProjectMaintenanceIntent) error { return nil }, RecordIntent: func(context.Context, ProjectMaintenanceIntent) error { return nil }}
	for _, invalid := range []ProjectIndexTarget{
		{DatabaseOID: target.DatabaseOID, TableOID: target.TableOID, Schema: target.Schema, Relation: "untouched"},
		{DatabaseOID: target.DatabaseOID + 1, TableOID: target.TableOID, Schema: target.Schema, Relation: target.Relation},
		readTarget("Project Data", "ungranted"), readTarget("Project Data", "untouched"), readTarget("Project Data", "inherited"),
		readTarget("pg_catalog", "pg_class"),
	} {
		bad := request
		bad.Target = invalid
		if result, err := apply(maintainer, bad, permitted); err == nil || result.WriteAttempted {
			t.Fatalf("wrong target or authority passed: %+v, %v", result, err)
		}
	}
	verifyCounts(target, 0, 0)
	stages = nil
	result, err := apply(maintainer, request, guards)
	if err != nil || !result.WriteAttempted || !result.Confirmed || result.Before.Count != 0 || result.After.Count != 1 || !slices.Equal(stages, []string{"permission", "capacity", "intent", "permission"}) {
		t.Fatalf("wrong ANALYZE result: %+v stages=%v err=%v", result, stages, err)
	}
	var analyzed []string
	if err := owner.QueryRow(ctx, `SELECT array_agg(attname ORDER BY attname) FROM pg_stats WHERE schemaname=$1 AND tablename=$2`, target.Schema, target.Relation).Scan(&analyzed); err != nil || !slices.Equal(analyzed, []string{"total"}) {
		t.Fatalf("ANALYZE changed other columns: %v %v", analyzed, err)
	}
	request.Operation, request.Columns, request.RunID = ProjectVacuum, nil, "ordinary-vacuum"
	stages = nil
	baseRecord := guards.RecordIntent
	guards.RecordIntent = func(ctx context.Context, i ProjectMaintenanceIntent) error {
		// A normal writer proceeds while the VACUUM identity pin is held.
		exec(`INSERT INTO "Project Data"."Due Invoices" VALUES (9000,1,'concurrent writer')`)
		return baseRecord(ctx, i)
	}
	result, err = apply(maintainer, request, guards)
	if err != nil || !result.WriteAttempted || !result.Confirmed || result.Before.Count != 0 || result.After.Count != 1 || !slices.Equal(stages, []string{"permission", "capacity", "intent", "permission"}) {
		t.Fatalf("wrong plain VACUUM result: %+v stages=%v err=%v", result, stages, err)
	}
	verifyCounts(target, 1, 1)
	verifyCounts(readTarget("Project Data", "untouched"), 0, 0)
	verifyCounts(readTarget("Project Data", "inherited"), 0, 0)
	verifyCounts(readTarget("Project Data", "ungranted"), 0, 0)
	var journaled int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM "Project Data".intent_log WHERE relation=$1`, target.TableOID).Scan(&journaled); err != nil || journaled != 2 {
		t.Fatalf("expected two intents for exact bound actions, got %d: %v", journaled, err)
	}
	request.Operation, request.RunID = ProjectAnalyze, "revoked"
	permitted.RecordIntent = func(_ context.Context, _ ProjectMaintenanceIntent) error {
		exec(`REVOKE MAINTAIN ON "Project Data"."Due Invoices" FROM fixture_app`)
		return nil
	}
	if result, err := apply(maintainer, request, permitted); err == nil || result.WriteAttempted {
		t.Fatalf("native permission revocation reached maintenance SQL: %+v %v", result, err)
	}
	verifyCounts(target, 1, 1)
	permitted.RecordIntent = func(context.Context, ProjectMaintenanceIntent) error { return nil }
	request.RunID = "owner-analyze"
	if result, err := apply(config, request, permitted); err != nil || !result.Confirmed || result.After.Count != 2 {
		t.Fatalf("exact owner could not refresh statistics: %+v %v", result, err)
	}
	exec(`ALTER TABLE "Project Data"."Due Invoices" OWNER TO fixture_app`)
	if result, err := apply(config, request, permitted); err == nil || result.WriteAttempted {
		t.Fatalf("implicit database-wide authority reached maintenance SQL: %+v %v", result, err)
	}
	verifyCounts(target, 2, 1)

	blocker := openMonitorConn(t, ctx, config)
	block, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer block.Rollback(ctx)
	if _, err := block.Exec(ctx, `LOCK TABLE ONLY "Project Data"."Due Invoices" IN SHARE UPDATE EXCLUSIVE MODE NOWAIT`); err != nil {
		t.Fatal(err)
	}
	for _, op := range []ProjectMaintenanceOperation{ProjectAnalyze, ProjectVacuum} {
		request.Operation, request.RunID = op, "conflicting-lock"
		result, err := apply(maintainer, request, permitted)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "55P03" || result.Confirmed {
			t.Fatalf("%s did not defer to another maintenance lock: %+v %v", op, result, err)
		}
	}
	if err := block.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	verifyCounts(target, 2, 1)

	request.Operation, request.RunID = ProjectVacuum, "namespace-swap"
	permitted.RecordIntent = func(context.Context, ProjectMaintenanceIntent) error {
		exec(`ALTER SCHEMA "Project Data" RENAME TO "Original Project Data";
			CREATE SCHEMA "Project Data";
			CREATE TABLE "Project Data"."Due Invoices" (id int) WITH (autovacuum_enabled=false);
			GRANT USAGE ON SCHEMA "Project Data" TO fixture_app;
			GRANT MAINTAIN ON "Project Data"."Due Invoices" TO fixture_app;
			INSERT INTO "Project Data"."Due Invoices" VALUES (1)`)
		return nil
	}
	if result, err := apply(maintainer, request, permitted); !errors.Is(err, ErrProjectMaintenanceTarget) || result.WriteAttempted {
		t.Fatalf("schema replacement reached maintenance SQL: %+v %v", result, err)
	}
	verifyCounts(target, 2, 1)
	verifyCounts(readTarget("Project Data", "Due Invoices"), 0, 0)
}
