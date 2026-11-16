//go:build integration

package hostedquery

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"
	"testing"
	"time"
)

func TestManagedMemoryDispatchUsesSelectedFunctionAndOrdinaryTraffic(t *testing.T) {
	testManagedMemoryDispatch(t, false)
}

func TestManagedMemoryDispatchAcceptsUntypedNoChange(t *testing.T) {
	testManagedMemoryDispatch(t, true)
}

func testManagedMemoryDispatch(t *testing.T, decline bool) {
	t.Helper()
	ctx, config, ca := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	db, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	exec := func(sql string) {
		t.Helper()
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	project, _ := supabaseproject.New("abcdefghijklmnopqrst", "0123456789abcdef")
	exec(`CREATE SCHEMA extensions;CREATE EXTENSION pg_stat_statements WITH SCHEMA extensions;GRANT USAGE ON SCHEMA extensions TO PUBLIC`)
	exec(project.InstallSQL())
	exec(project.AutomaticDiscoverySQL())
	exec(project.DiscoveredIndexEvidenceSQL())
	marker := "Pooltune managed monitor v1 " + project.ID()
	exec(fmt.Sprintf(`ALTER ROLE %s LOGIN PASSWORD 'local-test'; COMMENT ON ROLE %s IS '%s';COMMENT ON SCHEMA %s IS '%s';CREATE SCHEMA billing;CREATE TABLE billing.orders(id integer,payload text);INSERT INTO billing.orders SELECT i,repeat(md5(i::text),4) FROM generate_series(1,100000)i;CREATE FUNCTION billing.order_total() RETURNS bigint LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER SET work_mem='4MB' SET search_path=pg_catalog SET max_parallel_workers_per_gather=0 SET jit=off AS 'BEGIN RETURN (SELECT sum(id) FROM (SELECT id FROM ONLY billing.orders ORDER BY payload,id OFFSET 0) AS sorted); END';CREATE TABLE billing.health(id integer PRIMARY KEY,n integer);INSERT INTO billing.health VALUES(1,0);GRANT USAGE ON SCHEMA billing TO fixture_app;GRANT SELECT ON ALL TABLES IN SCHEMA billing TO fixture_app;GRANT UPDATE ON billing.health TO fixture_app;ANALYZE billing.orders`, project.Role(), project.Role(), marker, project.Role(), marker))
	monitorConfig := config.Copy()
	monitorConfig.User = project.Role()
	monitorConfig.Password = "local-test"
	monitorConfig.RuntimeParams["default_transaction_read_only"] = "on"
	monitorConfig.RuntimeParams["statement_timeout"] = "2s"
	monitor, err := pgx.ConnectConfig(ctx, monitorConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close(ctx)
	appConfig := config.Copy()
	appConfig.User = "fixture_app"
	app, err := pgx.ConnectConfig(ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(ctx)
	const query = `SELECT billing.order_total()`
	var value int
	if err := app.QueryRow(ctx, query).Scan(&value); err != nil {
		t.Fatal(err)
	}
	var role uint32
	var queryID int64
	if err := db.QueryRow(ctx, `SELECT userid,queryid FROM extensions.pg_stat_statements WHERE query=$1 AND userid='fixture_app'::regrole`, query).Scan(&role, &queryID); err != nil {
		t.Fatal(err)
	}
	var observed struct {
		DatabaseOID               uint32
		Boot, ResetAt, EntrySince time.Time
		ObjectiveMS               float64
	}
	if err := monitor.QueryRow(ctx, "SELECT database_oid,boot,reset_at,entry_since,mean_execution_ms FROM "+qualified(project.Role(), "snapshot")+"() WHERE role_oid=$1 AND query_id=$2", role, queryID).Scan(&observed.DatabaseOID, &observed.Boot, &observed.ResetAt, &observed.EntrySince, &observed.ObjectiveMS); err != nil {
		t.Fatal(err)
	}
	exec("CREATE DATABASE pooltune_state")
	stateConfig := config.Copy()
	stateConfig.Database = "pooltune_state"
	state, err := pgx.ConnectConfig(ctx, stateConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	epoch, err := ActivateProjectMonitor(ctx, state, project, false)
	if err != nil {
		t.Fatal(err)
	}
	claim := investigationClaim{Project: project.ID(), Attempt: 1, InvestigationJob: InvestigationJob{queryKey: queryKey{Database: observed.DatabaseOID, Role: role, Query: queryID}, Generation: []time.Time{observed.Boot, observed.ResetAt, observed.EntrySince}}}
	if err := state.QueryRow(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,attempts,lease_until)VALUES($1,$2,$3,$4,'{}',1,clock_timestamp()+interval '30 seconds')RETURNING id`, project.ID(), observed.DatabaseOID, role, queryID).Scan(&claim.ID); err != nil {
		t.Fatal(err)
	}
	route := managedLocalRoute(t, ctx, db, config, ca, project)
	exec("CREATE ROLE test_control LOGIN SUPERUSER PASSWORD 'test-control-password'")
	controlConfig := config.Copy()
	controlConfig.User = "test_control"
	controlConfig.Password = "test-control-password"
	control, err := pgx.ConnectConfig(ctx, controlConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close(ctx)
	if _, err := control.Exec(ctx, "ALTER ROLE postgres RENAME TO bootstrap_admin; CREATE ROLE postgres LOGIN PASSWORD '"+config.Password+"'; GRANT fixture_app,pg_read_all_stats TO postgres; ALTER TABLE billing.orders OWNER TO postgres; ALTER FUNCTION billing.order_total() OWNER TO postgres; GRANT CREATE ON DATABASE postgres TO postgres; GRANT USAGE,CREATE ON SCHEMA billing TO postgres", pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	trafficCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		for {
			if trafficCtx.Err() != nil {
				done <- nil
				return
			}
			var n int
			if err := app.QueryRow(trafficCtx, `SELECT n FROM billing.health WHERE id=$1`, 1).Scan(&n); err != nil {
				done <- err
				return
			}
			if _, err := app.Exec(trafficCtx, `UPDATE billing.health SET n=n+1 WHERE id=$1`, 1); err != nil {
				done <- err
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	defer func() { stop(); <-done }()
	owner, err := pgx.ConnectConfig(ctx, route)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close(ctx)
	target := ProjectFunctionMemoryTarget{Project: project.ID(), Schema: "billing", Function: "order_total", DatabaseOID: observed.DatabaseOID}
	if err := owner.QueryRow(ctx, `SELECT 'billing.order_total()'::regprocedure::oid`).Scan(&target.FunctionOID); err != nil {
		t.Fatal(err)
	}
	fn, err := ReadManagedMemoryFunction(ctx, owner, ManagedMemoryEnrollment{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	worker := &ManagedProjectIndex{Config: route, Capabilities: []string{"memory"}, MemoryEnrollments: []ManagedMemoryEnrollment{{Target: target, Fingerprint: fn.CatalogFingerprint, Generation: fn.Snapshot.Generation}}, MemoryHeadroom: func(context.Context) (supabaseproject.MemoryHeadroom, error) {
		return supabaseproject.MemoryHeadroom{ProjectRef: project.Ref(), ObservedAt: time.Now(), AvailableBytes: 768 << 20, TotalBytes: 1 << 30}, nil
	}, AdmitModel: func(context.Context) error { return nil }, Investigate: func(ctx context.Context, e IndexEvidence) (IndexProposal, error) {
		calls++
		if decline {
			return IndexProposal{RunID: e.RunID, TableOID: e.TableOID, Column: e.Column, Outcome: "no_change", Reason: "observations do not justify changing memory"}, nil
		}
		return IndexProposal{RunID: e.RunID, TableOID: e.TableOID, Column: e.Column, Repair: "memory", Outcome: "propose", Reason: "matched function spill with available memory"}, nil
	}}
	result := DispatchResult{Outcome: "insufficient_evidence"}
	handled, err := worker.run(ctx, state, monitor, project, epoch, claim, &ObservedLookupLatency{ObjectiveMS: observed.ObjectiveMS}, &result)
	if decline {
		if err != nil || !handled || result.Outcome != "no_change" || calls != 1 || result.ManagedMemory != nil {
			t.Fatalf("valid no-change retried or acted: handled=%v outcome=%s err=%v calls=%d", handled, result.Outcome, err, calls)
		}
		if err := finishInvestigation(ctx, state, claim, result, epoch); err != nil {
			t.Fatal(err)
		}
		var outcome string
		var finished bool
		var actions int
		if err := state.QueryRow(ctx, `SELECT result->>'outcome',finished_at IS NOT NULL,(SELECT count(*) FROM pooltune_worker.owned_project_actions WHERE job_id=$1) FROM pooltune_worker.investigations WHERE id=$1`, claim.ID).Scan(&outcome, &finished, &actions); err != nil || outcome != "no_change" || !finished || actions != 0 {
			t.Fatalf("decline did not finish without action: %s %v %d %v", outcome, finished, actions, err)
		}
		current, err := ReadManagedMemoryFunction(ctx, owner, ManagedMemoryEnrollment{Target: target})
		if err != nil || current != fn {
			t.Fatalf("model decline changed selected function: %v", err)
		}
		return
	}
	if err != nil || !handled || result.ManagedMemory == nil || calls != 1 {
		t.Fatalf("managed memory dispatch: handled%v result%+v err%v calls%d", handled, result, err, calls)
	}
	if result.ManagedMemory.Before.OuterTempBytes < memoryMinSpill || result.ManagedMemory.After == nil {
		t.Fatalf("missing matched evidence: %+v", result.ManagedMemory)
	}
	if result.Outcome != "improved_tested_inputs" && !result.ManagedMemory.Restored {
		t.Fatalf("unqualified change retained: %+v", result.ManagedMemory)
	}
	if err := finishInvestigation(ctx, state, claim, result, epoch); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := state.QueryRow(ctx, `SELECT outcome FROM pooltune_worker.owned_project_actions WHERE job_id=$1`, claim.ID).Scan(&outcome); err != nil || outcome == "pending" {
		t.Fatalf("action journal unsettled: %s %v", outcome, err)
	}
}
