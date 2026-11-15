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
)

func TestApplyProjectStatisticsTargetPreservesPriorAndAnalyzesExactColumn(t *testing.T) {
	ctx, admin, _, owner, target := projectStatisticsFixture(t)
	request := ProjectStatisticsRequest{Target: target, RunID: "first-column-statistics", ObservedAt: time.Now(), Column: 2, NextTarget: 160}
	var checks []string
	guards := ProjectStatisticsGuards{
		CheckPermission: func(_ context.Context, got ProjectStatisticsIntent) error {
			checks = append(checks, "permission")
			if got.Target != target || got.RunID != request.RunID || got.Key != (ProjectIndexKey{Number: 2, Name: "Measured Score"}) || got.NextTarget != 160 {
				return errors.New("permission check received a different action")
			}
			return nil
		},
		CheckCapacity: func(_ context.Context, got ProjectStatisticsIntent) error {
			checks = append(checks, "capacity")
			if got.RelationBytes <= 0 || got.PriorEffective != 100 {
				return errors.New("capacity check did not receive the bound size and original effective target")
			}
			return nil
		},
		RecordIntent: func(ctx context.Context, got ProjectStatisticsIntent) error {
			checks = append(checks, "intent")
			if !got.Prior.UsesDefault || readProjectStatisticsSetting(t, ctx, admin, got.Target, got.Column) != got.Prior {
				return errors.New("the prior value was lost or changed before durable intent")
			}
			if _, err := admin.Exec(ctx, `UPDATE "Statistics App"."Candidate Scores" SET note = 'ordinary concurrent write' WHERE id=1`); err != nil {
				return err
			}
			_, err := admin.Exec(ctx, `INSERT INTO "Statistics App".intents VALUES ($1,$2,$3,$4,$5)`, got.RunID,
				got.Target.TableOID, got.Column, got.Prior.UsesDefault, got.NextTarget)
			return err
		},
	}
	result, err := ApplyProjectStatisticsTarget(ctx, owner, request, guards)
	if err != nil || !result.WriteAttempted || !result.Confirmed || !result.Prior.UsesDefault ||
		result.Current != (ProjectStatisticsSetting{Value: 160}) || result.AnalyzeDuration <= 0 ||
		!slices.Equal(checks, []string{"permission", "capacity", "intent", "permission"}) {
		t.Fatalf("unexpected statistics action: %+v, checks %v, %v", result, checks, err)
	}
	if value := readProjectStatisticsSetting(t, ctx, admin, target, request.Column); value != result.Current {
		t.Fatalf("catalog value after setting change = %+v", value)
	}
	var columns []string
	if err := admin.QueryRow(ctx, `SELECT array_agg(attname ORDER BY attname) FROM pg_catalog.pg_stats
		WHERE schemaname=$1 AND tablename=$2 AND NOT inherited`, target.Schema, target.Relation).Scan(&columns); err != nil || !slices.Equal(columns, []string{"Measured Score"}) {
		t.Fatalf("ANALYZE refreshed different columns: %v, %v", columns, err)
	}
	var intentCount int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM "Statistics App".intents
		WHERE run_id=$1 AND relation_oid=$2 AND field_number=2 AND was_default AND next_target=160`, request.RunID, target.TableOID).
		Scan(&intentCount); err != nil || intentCount != 1 {
		t.Fatalf("missing exact saved intent: %d, %v", intentCount, err)
	}
	var note string
	if err := admin.QueryRow(ctx, `SELECT note FROM "Statistics App"."Candidate Scores" WHERE id=1`).Scan(&note); err != nil || note != "ordinary concurrent write" {
		t.Fatalf("application write was not preserved: %q, %v", note, err)
	}

	request.RunID, request.ObservedAt, request.NextTarget = "increase-explicit-target", time.Now(), 200
	guards = allowingProjectStatistics()
	guards.RecordIntent = func(_ context.Context, got ProjectStatisticsIntent) error {
		if got.Prior != (ProjectStatisticsSetting{Value: 160}) || got.PriorEffective != 160 {
			return errors.New("exact explicit prior value was not recorded")
		}
		return nil
	}
	result, err = ApplyProjectStatisticsTarget(ctx, owner, request, guards)
	if err != nil || !result.Confirmed || result.Prior != (ProjectStatisticsSetting{Value: 160}) ||
		result.Current != (ProjectStatisticsSetting{Value: 200}) {
		t.Fatalf("explicit target was not preserved: %+v, %v", result, err)
	}
	result, err = ApplyProjectStatisticsTarget(ctx, owner, request, guards)
	if err == nil || result.WriteAttempted {
		t.Fatalf("already applied request attempted another write: %+v, %v", result, err)
	}
}

func TestApplyProjectStatisticsTargetRefusesUnapprovedOrUnsupportedRequests(t *testing.T) {
	ctx, admin, adminConfig, owner, target := projectStatisticsFixture(t)
	request := ProjectStatisticsRequest{Target: target, RunID: "column-statistics", ObservedAt: time.Now(), Column: 2, NextTarget: 160}
	denied := errors.New("operation denied")
	for _, test := range []struct {
		name   string
		mutate func(*ProjectStatisticsGuards)
	}{
		{"permission", func(g *ProjectStatisticsGuards) {
			g.CheckPermission = func(context.Context, ProjectStatisticsIntent) error { return denied }
		}},
		{"capacity", func(g *ProjectStatisticsGuards) {
			g.CheckCapacity = func(context.Context, ProjectStatisticsIntent) error { return denied }
		}},
		{"intent", func(g *ProjectStatisticsGuards) {
			g.RecordIntent = func(context.Context, ProjectStatisticsIntent) error { return denied }
		}},
		{"revoked after intent", func(g *ProjectStatisticsGuards) {
			var recorded bool
			g.RecordIntent = func(context.Context, ProjectStatisticsIntent) error { recorded = true; return nil }
			g.CheckPermission = func(context.Context, ProjectStatisticsIntent) error {
				if recorded {
					return denied
				}
				return nil
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			guards := allowingProjectStatistics()
			test.mutate(&guards)
			if result, err := ApplyProjectStatisticsTarget(ctx, owner, request, guards); !errors.Is(err, denied) || result.WriteAttempted {
				t.Fatalf("denied operation reached write: %+v, %v", result, err)
			}
		})
	}
	for _, test := range []struct {
		name   string
		mutate func(*ProjectStatisticsRequest)
	}{
		{"wrong database", func(r *ProjectStatisticsRequest) { r.Target.DatabaseOID++ }},
		{"wrong table", func(r *ProjectStatisticsRequest) { r.Target.Relation = "Untouched" }},
		{"wrong column", func(r *ProjectStatisticsRequest) { r.Column = 999 }},
		{"unsupported type", func(r *ProjectStatisticsRequest) { r.Column = 3 }},
		{"excess target", func(r *ProjectStatisticsRequest) { r.NextTarget = 201 }},
		{"no increase", func(r *ProjectStatisticsRequest) { r.NextTarget = 100 }},
		{"stale observation", func(r *ProjectStatisticsRequest) { r.ObservedAt = time.Now().Add(-time.Minute) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			other := request
			test.mutate(&other)
			if result, err := ApplyProjectStatisticsTarget(ctx, owner, other, allowingProjectStatistics()); err == nil || result.WriteAttempted {
				t.Fatalf("unsupported operation reached write: %+v, %v", result, err)
			}
		})
	}
	if result, err := ApplyProjectStatisticsTarget(ctx, adminConfig, request, allowingProjectStatistics()); err == nil || result.WriteAttempted {
		t.Fatalf("login other than table owner reached write: %+v, %v", result, err)
	}
	if value := readProjectStatisticsSetting(t, ctx, admin, target, request.Column); !value.UsesDefault {
		t.Fatalf("a refused action changed column statistics: %+v", value)
	}
}

func TestApplyProjectStatisticsTargetRollsBackFailedChange(t *testing.T) {
	ctx, admin, _, owner, target := projectStatisticsFixture(t)
	mustMonitorExec(t, ctx, admin, `CREATE FUNCTION public.reject_fixture_alter() RETURNS event_trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'fixture rejected ALTER TABLE' USING ERRCODE='P0001';
		END
		$$;
		CREATE EVENT TRIGGER reject_fixture_alter ON ddl_command_end WHEN TAG IN ('ALTER TABLE') EXECUTE FUNCTION public.reject_fixture_alter()`)
	request := ProjectStatisticsRequest{Target: target, RunID: "failed-column-statistics", ObservedAt: time.Now(), Column: 2, NextTarget: 160}
	result, err := ApplyProjectStatisticsTarget(ctx, owner, request, allowingProjectStatistics())
	if monitorSQLState(err) != "P0001" || !result.WriteAttempted || result.Confirmed {
		t.Fatalf("expected failed write: %+v, %v", result, err)
	}
	if value := readProjectStatisticsSetting(t, ctx, admin, target, request.Column); !value.UsesDefault {
		t.Fatalf("failed transaction persisted a statistics target: %+v", value)
	}
}

func projectStatisticsFixture(t *testing.T) (context.Context, *pgx.Conn, *pgx.ConnConfig, *pgx.ConnConfig, ProjectIndexTarget) {
	t.Helper()
	ctx, config, _ := testpg.Start(t, "-c", "default_statistics_target=100")
	admin := openMonitorConn(t, ctx, config)
	mustMonitorExec(t, ctx, admin, `CREATE SCHEMA "Statistics App";
		GRANT USAGE ON SCHEMA "Statistics App" TO fixture_app;
		CREATE TABLE "Statistics App"."Candidate Scores" (id integer, "Measured Score" integer, note text) WITH (autovacuum_enabled=false);
		CREATE TABLE "Statistics App"."Untouched" (id integer);
		CREATE TABLE "Statistics App".intents (run_id text, relation_oid oid, field_number smallint, was_default boolean, next_target integer);
		INSERT INTO "Statistics App"."Candidate Scores" SELECT n, n%20, 'initial' FROM generate_series(1,2000) n;
		ALTER TABLE "Statistics App"."Candidate Scores" OWNER TO fixture_app`)
	target := ProjectIndexTarget{Schema: "Statistics App", Relation: "Candidate Scores"}
	if err := admin.QueryRow(ctx, `SELECT oid, pg_catalog.to_regclass($1)::oid FROM pg_catalog.pg_database WHERE datname=current_database()`,
		qualified(target.Schema, target.Relation)).Scan(&target.DatabaseOID, &target.TableOID); err != nil {
		t.Fatal(err)
	}
	owner := config.Copy()
	owner.User = "fixture_app"
	return ctx, admin, config, owner, target
}

func readProjectStatisticsSetting(t *testing.T, ctx context.Context, db *pgx.Conn, target ProjectIndexTarget, column int16) ProjectStatisticsSetting {
	t.Helper()
	var value *int32
	if err := db.QueryRow(ctx, `SELECT attstattarget::integer FROM pg_catalog.pg_attribute WHERE attrelid=$1 AND attnum=$2`, target.TableOID, column).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value == nil {
		return ProjectStatisticsSetting{UsesDefault: true}
	}
	return ProjectStatisticsSetting{Value: *value}
}

func allowingProjectStatistics() ProjectStatisticsGuards {
	allow := func(context.Context, ProjectStatisticsIntent) error { return nil }
	return ProjectStatisticsGuards{CheckPermission: allow, CheckCapacity: allow, RecordIntent: allow}
}
