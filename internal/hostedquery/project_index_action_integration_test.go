//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestApplyProjectIndexKeepsAppWritesAndJournalsExactIntent(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	db, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE SCHEMA "Test Billing";
		CREATE TABLE "Test Billing".invoices (id int PRIMARY KEY, issued date, total int, "User ""ID""" bigint);
		CREATE TABLE "Test Billing".action_intent (index_name text PRIMARY KEY, table_oid oid, keys smallint[]);
		GRANT USAGE ON SCHEMA "Test Billing" TO fixture_app;
		GRANT SELECT, INSERT ON "Test Billing".invoices TO fixture_app;
		GRANT SELECT ON "Test Billing".action_intent TO fixture_app;
		INSERT INTO "Test Billing".invoices SELECT n, DATE '2026-01-01' + (n%366), n, n%1000 FROM generate_series(1, 50000) n`)
	target := ProjectIndexTarget{Schema: "Test Billing", Relation: "invoices"}
	if err := db.QueryRow(ctx, `SELECT oid, '"Test Billing".invoices'::regclass::oid FROM pg_catalog.pg_database WHERE datname=current_database()`).Scan(&target.DatabaseOID, &target.TableOID); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("pooltune_ix_%d_4_2", target.TableOID)
	key1, key2 := ProjectIndexKey{Number: 4, Name: `User "ID"`}, ProjectIndexKey{Number: 2, Name: "issued"}
	evidence := IndexEvidence{RunID: "owned-test", TableOID: target.TableOID, Column: 4, Columns: []int16{4, 2}, Decision: Investigate,
		Plan: PlanEvidence{Node: "Seq Scan", ExaminedRows: 50_000, ReturnedRows: 1}}
	proposal := ScriptedInvestigator
	deny := errors.New("denied by test guard")
	checks := []string{}
	guards := ProjectIndexActionGuards{
		CheckPermission: func(_ context.Context, intent ProjectIndexActionIntent) error {
			if intent.Target != target || intent.IndexName != name || !slices.Equal(intent.Keys, []ProjectIndexKey{key1, key2}) {
				return deny
			}
			checks = append(checks, "permission")
			return nil
		},
		CheckCapacity: func(_ context.Context, _ ProjectIndexActionIntent) error {
			checks = append(checks, "capacity")
			return nil
		},
		RecordIntent: func(ctx context.Context, intent ProjectIndexActionIntent) error {
			checks = append(checks, "journal")
			_, err := db.Exec(ctx, `INSERT INTO "Test Billing".action_intent VALUES ($1, $2, $3)`, intent.IndexName, intent.Target.TableOID, []int16{intent.Keys[0].Number, intent.Keys[1].Number})
			return err
		},
	}
	apply := func(guards ProjectIndexActionGuards) (ProjectIndexActionResult, error) {
		e := evidence
		e.At = time.Now().UTC()
		p, err := proposal(ctx, e)
		if err != nil {
			return ProjectIndexActionResult{}, err
		}
		return ApplyProjectIndex(ctx, config, target, e, p, guards)
	}
	assertIndexPresent := func(want bool) {
		t.Helper()
		var present bool
		if err := db.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, qualified(target.Schema, name)).Scan(&present); err != nil || present != want {
			t.Fatalf("index present=%v error=%v, want %v", present, err, want)
		}
	}
	if _, err := apply(ProjectIndexActionGuards{}); err == nil {
		t.Fatal("unconfigured action was admitted")
	}
	for _, reject := range []func(*ProjectIndexActionGuards){
		func(g *ProjectIndexActionGuards) {
			g.CheckPermission = func(context.Context, ProjectIndexActionIntent) error { return deny }
		},
		func(g *ProjectIndexActionGuards) {
			g.CheckCapacity = func(context.Context, ProjectIndexActionIntent) error { return deny }
		},
		func(g *ProjectIndexActionGuards) {
			g.RecordIntent = func(context.Context, ProjectIndexActionIntent) error { return deny }
		},
	} {
		bad := guards
		reject(&bad)
		if result, err := apply(bad); !errors.Is(err, deny) || result.WriteAttempted {
			t.Fatalf("guard denial reached write: %+v %v", result, err)
		}
		assertIndexPresent(false)
	}
	revoked := guards
	recorded := false
	revoked.CheckPermission = func(ctx context.Context, intent ProjectIndexActionIntent) error {
		if recorded {
			return deny
		}
		return guards.CheckPermission(ctx, intent)
	}
	revoked.RecordIntent = func(ctx context.Context, intent ProjectIndexActionIntent) error {
		err := guards.RecordIntent(ctx, intent)
		recorded = err == nil
		return err
	}
	if result, err := apply(revoked); !errors.Is(err, deny) || !recorded || result.WriteAttempted {
		t.Fatalf("revocation after journal commit reached write: %+v %v", result, err)
	}
	assertIndexPresent(false)
	exec(`DELETE FROM "Test Billing".action_intent`)
	exec(`ALTER TABLE "Test Billing".invoices OWNER TO fixture_app`)
	if result, err := apply(guards); err == nil || result.WriteAttempted {
		t.Fatalf("wrong PostgreSQL owner reached write: %+v %v", result, err)
	}
	exec(`ALTER TABLE "Test Billing".invoices OWNER TO postgres;
		GRANT SELECT, INSERT ON "Test Billing".invoices TO fixture_app;`)
	exec("CREATE TABLE " + qualified(target.Schema, name) + " (sentinel int)")
	if result, err := apply(guards); err == nil || result.WriteAttempted {
		t.Fatalf("occupied name reached write: %+v %v", result, err)
	}
	assertIndexPresent(true)
	exec("DROP TABLE " + qualified(target.Schema, name))
	checks = nil
	blocker, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close(ctx)
	block, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer block.Rollback(ctx)
	if _, err := block.Exec(ctx, `INSERT INTO "Test Billing".invoices VALUES (50001, DATE '2026-01-01', 11, 11)`); err != nil {
		t.Fatal(err)
	}
	appConfig := config.Copy()
	appConfig.User = "fixture_app"
	app, err := pgx.ConnectConfig(ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(ctx)
	observer, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close(ctx)
	completed := make(chan error, 1)
	resultReady := make(chan ProjectIndexActionResult, 1)
	go func() {
		result, err := apply(guards)
		resultReady <- result
		completed <- err
	}()
	observed := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if err := observer.QueryRow(ctx, `SELECT EXISTS (SELECT FROM pg_stat_activity WHERE state='active' AND wait_event_type='Lock' AND query LIKE 'CREATE INDEX CONCURRENTLY %')`).Scan(&observed); err != nil {
			t.Fatal(err)
		}
		if observed {
			break
		}
	}
	if !observed {
		t.Fatal("did not witness a concurrent index build waiting on a writer")
	}
	var journaled []int16
	if err := app.QueryRow(ctx, `SELECT keys FROM "Test Billing".action_intent WHERE index_name=$1 AND table_oid=$2`, name, target.TableOID).Scan(&journaled); err != nil || !slices.Equal(journaled, []int16{4, 2}) {
		t.Fatalf("exact intent was not durable before build: %v %v", journaled, err)
	}
	if _, err := app.Exec(ctx, `INSERT INTO "Test Billing".invoices VALUES (50002, DATE '2026-01-01', 22, 22)`); err != nil {
		t.Fatalf("app write during action: %v", err)
	}
	var total int
	if err := app.QueryRow(ctx, `SELECT total FROM "Test Billing".invoices WHERE id=50002`).Scan(&total); err != nil || total != 22 {
		t.Fatalf("app read during action = %d, %v", total, err)
	}
	if err := block.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	result, runErr := <-resultReady, <-completed
	if runErr != nil || !result.WriteAttempted || result.IndexOID == 0 || result.IndexBytes <= 0 || !slices.Equal(checks, []string{"permission", "capacity", "journal", "permission"}) {
		t.Fatalf("invalid index result: %+v checks=%v error=%v", result, checks, runErr)
	}
	var definition string
	if err := db.QueryRow(ctx, `SELECT pg_get_indexdef($1)`, result.IndexOID).Scan(&definition); err != nil || !strings.Contains(definition, `USING btree ("User ""ID""", issued)`) {
		t.Fatalf("definition: %q, %v", definition, err)
	}
	if result, err := apply(guards); err != nil || !result.AlreadyCovered || result.WriteAttempted {
		t.Fatalf("existing equivalent index did not end without a build: %+v %v", result, err)
	}
}
