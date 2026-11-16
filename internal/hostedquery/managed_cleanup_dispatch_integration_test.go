//go:build integration

package hostedquery

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagedCleanupUsesOrdinaryTableAndActualAutomaticCleanup(t *testing.T) {
	ctx, cfg, ca := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements", "-c", "autovacuum_naptime=1s")
	admin := openMonitorConn(t, ctx, cfg)
	exec := func(sql string) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	project, _ := supabaseproject.New("abcdefghijklmnopqrst", "0123456789abcdef")
	exec(`CREATE SCHEMA extensions;CREATE EXTENSION pg_stat_statements WITH SCHEMA extensions;GRANT USAGE ON SCHEMA extensions TO PUBLIC`)
	exec(project.InstallSQL())
	exec(project.AutomaticDiscoverySQL())
	exec(project.DiscoveredIndexEvidenceSQL())
	marker := "Pooltune managed monitor v1 " + project.ID()
	exec(fmt.Sprintf(`ALTER ROLE %s LOGIN PASSWORD 'local-test';COMMENT ON ROLE %s IS '%s';COMMENT ON SCHEMA %s IS '%s'`, project.Role(), project.Role(), marker, project.Role(), marker))
	exec(`CREATE SCHEMA billing;CREATE TABLE billing.orders(id integer PRIMARY KEY,account integer,n integer,padding text) WITH(autovacuum_enabled=false,autovacuum_vacuum_insert_threshold=-1,autovacuum_analyze_threshold=100000000);
 INSERT INTO billing.orders SELECT i,i/3000,0,repeat('x',100) FROM generate_series(1,30000)i;CREATE INDEX account_lookup ON billing.orders(account);ANALYZE billing.orders;
 CREATE TABLE billing.health(id integer PRIMARY KEY,n integer);INSERT INTO billing.health VALUES(1,0);GRANT USAGE ON SCHEMA billing TO fixture_app;GRANT SELECT,UPDATE ON ALL TABLES IN SCHEMA billing TO fixture_app;`)
	exec(`VACUUM billing.orders`)
	exec(`UPDATE billing.orders SET n=n+1 WHERE id<=5000;SELECT pg_stat_force_next_flush()`)
	monitorCfg := cfg.Copy()
	monitorCfg.User = project.Role()
	monitorCfg.Password = "local-test"
	monitorCfg.RuntimeParams["default_transaction_read_only"] = "on"
	monitorCfg.RuntimeParams["statement_timeout"] = "2s"
	monitor := openMonitorConn(t, ctx, monitorCfg)
	appCfg := cfg.Copy()
	appCfg.User = "fixture_app"
	app := openMonitorConn(t, ctx, appCfg)
	query := `SELECT account FROM billing.orders WHERE account=$1`
	rows, err := app.Query(ctx, query, int32(0))
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if _, err := app.Exec(ctx, `SELECT pg_stat_force_next_flush()`); err != nil {
		t.Fatal(err)
	}
	exec(`ALTER TABLE billing.orders SET(autovacuum_enabled=true)`)
	var role uint32
	var qid int64
	if err := admin.QueryRow(ctx, `SELECT userid,queryid FROM extensions.pg_stat_statements WHERE query=$1 AND userid='fixture_app'::regrole`, query).Scan(&role, &qid); err != nil {
		t.Fatal(err)
	}
	observed, err := project.ReadDiscoveredIndexEvidence(ctx, monitor, role, qid)
	if err != nil {
		t.Fatal(err)
	}
	exec(`CREATE DATABASE pooltune_state`)
	stateCfg := cfg.Copy()
	stateCfg.Database = "pooltune_state"
	state := openMonitorConn(t, ctx, stateCfg)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	epoch, err := ActivateProjectMonitor(ctx, state, project, false)
	if err != nil {
		t.Fatal(err)
	}
	route := managedLocalRoute(t, ctx, admin, cfg, ca, project)
	exec(`CREATE ROLE test_control LOGIN SUPERUSER PASSWORD 'test-control-password'`)
	controlCfg := cfg.Copy()
	controlCfg.User = "test_control"
	controlCfg.Password = "test-control-password"
	control := openMonitorConn(t, ctx, controlCfg)
	if _, err := control.Exec(ctx, "ALTER ROLE postgres RENAME TO bootstrap_admin;CREATE ROLE postgres LOGIN PASSWORD '"+cfg.Password+"';GRANT fixture_app,pg_read_all_stats TO postgres;ALTER TABLE billing.orders OWNER TO postgres;GRANT USAGE ON SCHEMA billing TO postgres", pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	_ = state.Close(ctx)
	stateCfg.User = "test_control"
	stateCfg.Password = "test-control-password"
	state = openMonitorConn(t, ctx, stateCfg)
	owner := openMonitorConn(t, ctx, route)
	trafficCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan struct{})
	var updates atomic.Int64
	go func() {
		defer close(done)
		i := 5001
		for trafficCtx.Err() == nil {
			var n int
			if app.QueryRow(trafficCtx, `SELECT n FROM billing.health WHERE id=$1`, 1).Scan(&n) != nil {
				return
			}
			if _, err := app.Exec(trafficCtx, `UPDATE billing.health SET n=n+1 WHERE id=$1`, 1); err != nil {
				return
			}
			if i < 5500 {
				if _, err := app.Exec(trafficCtx, `UPDATE billing.orders SET n=n+1 WHERE id=$1`, i); err != nil {
					return
				}
				i++
				updates.Add(1)
			}
			if _, err := app.Exec(trafficCtx, `SELECT pg_stat_force_next_flush()`); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	defer func() { stop(); <-done }()
	before, err := MeasureManagedIndexLookup(ctx, owner, observed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if before.HeapFetches == nil || *before.HeapFetches < 1000 {
		t.Fatalf("test did not create real visibility backlog %+v", before)
	}
	claim := investigationClaim{Project: project.ID(), Generation: []time.Time{observed.Boot, observed.ResetAt, observed.EntrySince}}
	if err := state.QueryRow(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,attempts,lease_until)VALUES($1,$2,$3,$4,'{}',1,clock_timestamp()+interval '25 seconds')RETURNING id`, project.ID(), observed.DatabaseOID, role, qid).Scan(&claim.ID); err != nil {
		t.Fatal(err)
	}
	claim.Database, claim.Role, claim.Query, claim.Attempt = observed.DatabaseOID, role, qid, 1
	modelCalls := 0
	worker := ManagedProjectIndex{Config: route, Capabilities: []string{"vacuum"}, AdmitModel: func(context.Context) error { return nil }, Investigate: func(_ context.Context, e IndexEvidence) (IndexProposal, error) {
		modelCalls++
		if e.Vacuum == nil || e.Vacuum.CleanupTrigger == nil || e.Vacuum.CleanupTrigger.NextScaleFactor != .15 || !e.Vacuum.CleanupTrigger.Valid(e.TableOID, e.RunID, time.Now()) {
			t.Fatal("model did not receive the actual justified cleanup trigger")
		}
		return IndexProposal{Outcome: "propose", Repair: "vacuum", RunID: e.RunID, TableOID: e.TableOID, Column: e.Column, Reason: "measured growing visibility backlog"}, nil
	}}
	result := DispatchResult{JobID: claim.ID, Attempt: claim.Attempt}
	initialCleanup, _ := ReadProjectCleanupSettings(ctx, owner, before.Catalog.Target)
	t.Logf("starting cleanup dead=%d cleanups=%d scale=%f rows=%f last=%s", initialCleanup.DeadTuples, initialCleanup.Cleanups, initialCleanup.ScaleFactor, initialCleanup.EstimatedRows, initialCleanup.LastAutovacuum)
	handled, err := worker.runCleanupSettings(ctx, state, monitor, project, epoch, claim, &ObservedLookupLatency{ObjectiveMS: observed.ObjectiveMS}, &result, observed, owner, before)
	if err != nil || !handled || result.managedCleanupPublication == nil {
		current, _ := ReadProjectCleanupSettings(ctx, owner, before.Catalog.Target)
		t.Logf("ending cleanup dead=%d cleanups=%d scale=%f rows=%f last=%s", current.DeadTuples, current.Cleanups, current.ScaleFactor, current.EstimatedRows, current.LastAutovacuum)
		t.Fatalf("managed cleanup not applied: handled=%v model=%d updates=%d result=%+v err=%v", handled, modelCalls, updates.Load(), result, err)
	}
	if err := publishManagedCleanup(ctx, state, epoch, claim, result, *result.managedCleanupPublication); err != nil {
		t.Fatal(err)
	}
	// Restart only the state/dispatcher connection. Native autovacuum and normal
	// traffic keep running; the model must not be called again.
	_ = state.Close(ctx)
	state = openMonitorConn(t, ctx, stateCfg)
	var final *DispatchResult
	for ctx.Err() == nil {
		child, err := claimProjectInvestigation(ctx, state, project.ID(), &epoch, ownedIndexRecoveryScope{Managed: true})
		if err != nil {
			t.Fatal(err)
		}
		if child == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		final, err = worker.dispatchCleanupSettings(ctx, state, project, epoch, *child, func(context.Context) (*pgx.Conn, func(), error) { return monitor, func() {}, nil })
		if err != nil {
			t.Fatal(err)
		}
		if final != nil && final.Outcome != "awaiting_verification" {
			break
		}
	}
	if final == nil || final.ManagedCleanup == nil || !final.ManagedCleanup.Trigger.AutomaticCleaned || modelCalls != 1 {
		t.Fatalf("automatic cleanup not proven across restart: model=%d result=%+v", modelCalls, final)
	}
	if final.Outcome != "improved_tested_inputs" && !final.ManagedCleanup.Trigger.Restored {
		t.Fatal("insufficient benefit left changed option")
	}
	t.Logf("genuine automatic cleanup reduced dead tuples %d -> %d; outcome=%s restored=%v", final.ManagedCleanup.Trigger.Before.DeadTuples, final.ManagedCleanup.Trigger.After.DeadTuples, final.Outcome, final.ManagedCleanup.Trigger.Restored)
}
