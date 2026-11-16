//go:build integration

package hostedquery

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"
)

func TestManagedStatisticsCheckboxRunsWithoutFixtureIdentity(t *testing.T) {
	for _, run := range []struct {
		variant          string
		restart, missing bool
	}{{"column_target", false, false}, {"joint", false, false}, {"column_target", true, false}, {"joint", true, false}, {"column_target", false, true}} {
		variant := run.variant
		t.Run(fmt.Sprintf("%s/restart=%v/missing=%v", variant, run.restart, run.missing), func(t *testing.T) {
			ctx, config, ca := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
			db := openMonitorConn(t, ctx, config)
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
			exec(fmt.Sprintf(`ALTER ROLE %s LOGIN PASSWORD 'local-test';COMMENT ON ROLE %s IS '%s';COMMENT ON SCHEMA %s IS '%s'`, project.Role(), project.Role(), marker, project.Role(), marker))
			exec(`CREATE SCHEMA billing;CREATE TABLE billing.orders(id integer,account integer,payload text) WITH(autovacuum_enabled=false);
 INSERT INTO billing.orders VALUES(6241,43,repeat('x',56));
 INSERT INTO billing.orders SELECT n,CASE WHEN n<=6240 THEN 42 WHEN n<=10080 THEN 43 ELSE n END,repeat('x',56) FROM generate_series(1,12000)n WHERE n<>6241 ORDER BY(n*7919::bigint)%12001;
 CREATE INDEX account_lookup ON billing.orders(account);ALTER TABLE billing.orders ALTER COLUMN account SET STATISTICS 1;
 CREATE TABLE billing.health(id integer PRIMARY KEY,n integer);INSERT INTO billing.health VALUES(1,0);
 GRANT USAGE ON SCHEMA billing TO fixture_app;GRANT SELECT ON ALL TABLES IN SCHEMA billing TO fixture_app;GRANT UPDATE ON billing.health TO fixture_app;SELECT pg_stat_force_next_flush()`)
			if variant == "joint" {
				exec(`TRUNCATE billing.orders;ALTER TABLE billing.orders ADD COLUMN category integer;ALTER TABLE billing.orders ALTER COLUMN account SET STATISTICS 100;DROP INDEX billing.account_lookup;
 INSERT INTO billing.orders VALUES(1,1,repeat('x',56),2),(101,2,repeat('x',56),1);
 INSERT INTO billing.orders SELECT n,CASE WHEN n<=100 OR n BETWEEN 401 AND 80000 THEN 1 ELSE 2 END,repeat('x',56),CASE WHEN n BETWEEN 101 AND 80000 THEN 1 ELSE 2 END FROM generate_series(1,160000)n WHERE n NOT IN(1,101) ORDER BY(n*7919::bigint)%160001;
 CREATE INDEX account_category ON billing.orders(account,category);SELECT pg_stat_force_next_flush()`)
			}
			exec(`ANALYZE billing.orders;SELECT pg_stat_force_next_flush()`)
			monitorConfig := config.Copy()
			monitorConfig.User = project.Role()
			monitorConfig.Password = "local-test"
			monitorConfig.RuntimeParams["default_transaction_read_only"] = "on"
			monitorConfig.RuntimeParams["statement_timeout"] = "2s"
			monitor := openMonitorConn(t, ctx, monitorConfig)
			appConfig := config.Copy()
			appConfig.User = "fixture_app"
			app := openMonitorConn(t, ctx, appConfig)
			query := `SELECT account FROM billing.orders WHERE account=$1`
			args := []any{int32(43)}
			if variant == "joint" {
				query += " AND category=$2"
				args = []any{int32(1), int32(2)}
			}
			rows, err := app.Query(ctx, query, args...)
			if err != nil {
				t.Fatal(err)
			}
			rows.Close()
			var role uint32
			var queryID int64
			if err := db.QueryRow(ctx, `SELECT userid,queryid FROM extensions.pg_stat_statements WHERE query=$1 AND userid='fixture_app'::regrole`, query).Scan(&role, &queryID); err != nil {
				t.Fatal(err)
			}
			observed, err := project.ReadDiscoveredIndexEvidence(ctx, monitor, role, queryID)
			if err != nil {
				t.Fatal(err)
			}
			exec("CREATE DATABASE pooltune_state")
			stateConfig := config.Copy()
			stateConfig.Database = "pooltune_state"
			state := openMonitorConn(t, ctx, stateConfig)
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
			exec(`CREATE ROLE test_control LOGIN SUPERUSER PASSWORD 'test-control-password'`)
			controlConfig := config.Copy()
			controlConfig.User = "test_control"
			controlConfig.Password = "test-control-password"
			control := openMonitorConn(t, ctx, controlConfig)
			if _, err := control.Exec(ctx, "ALTER ROLE postgres RENAME TO bootstrap_admin;CREATE ROLE postgres LOGIN PASSWORD '"+config.Password+"';GRANT fixture_app,pg_read_all_stats TO postgres;ALTER TABLE billing.orders OWNER TO postgres;GRANT USAGE,CREATE ON SCHEMA billing TO postgres", pgx.QueryExecModeSimpleProtocol); err != nil {
				t.Fatal(err)
			}
			// State uses its infrastructure login, separate from the newly
			// created non-superuser customer owner, including reconnects.
			if err := state.Close(ctx); err != nil {
				t.Fatal(err)
			}
			stateConfig.User, stateConfig.Password = "test_control", "test-control-password"
			state = openMonitorConn(t, ctx, stateConfig)
			trafficCtx, stop := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				for trafficCtx.Err() == nil {
					var n int
					if app.QueryRow(trafficCtx, `SELECT n FROM billing.health WHERE id=$1`, 1).Scan(&n) != nil {
						return
					}
					if _, err := app.Exec(trafficCtx, `UPDATE billing.health SET n=n+1 WHERE id=$1`, 1); err != nil {
						return
					}
					time.Sleep(5 * time.Millisecond)
				}
			}()
			defer func() { stop(); <-done }()
			worker := ManagedProjectIndex{Config: route, Capabilities: []string{"statistics"}, AdmitModel: func(context.Context) error { return nil }, Investigate: func(_ context.Context, e IndexEvidence) (IndexProposal, error) {
				if run.missing {
					stop()
					<-done
				}
				return IndexProposal{Outcome: "propose", Repair: "statistics", RunID: e.RunID, TableOID: e.TableOID, Column: e.Column, Reason: "measured statistics cause"}, nil
			}}
			result := DispatchResult{Outcome: "insufficient_evidence"}
			handled, err := worker.run(ctx, state, monitor, project, epoch, claim, &ObservedLookupLatency{ObjectiveMS: observed.ObjectiveMS}, &result)
			if err != nil || !handled || result.ManagedStatistics == nil {
				t.Fatalf("managed statistics not dispatched: handled=%v result=%+v err=%v", handled, result, err)
			}
			report := result.ManagedStatistics
			cases := 1
			if variant == "joint" {
				cases = 2
			}
			if (variant == "column_target" && report.ColumnTarget == nil) || report.Variant != variant || !report.Applied || report.Measurement != "managed_committed_statistics_trial" || len(report.Before) != cases || len(report.After) != cases {
				t.Fatalf("missing genuine trial evidence: %+v", report)
			}
			if result.Outcome == "improved_tested_inputs" {
				if !report.Retained || report.Restored || run.missing {
					t.Fatalf("false retained benefit: %+v", report)
				}
			} else if !report.Restored || !report.StatisticsRegenerated || report.Retained {
				t.Fatalf("unqualified committed trial was not restored: %+v", report)
			}
			if report.Deadline.IsZero() || report.VerificationScope != "committed_candidate_inputs_and_normal_traffic" {
				t.Fatalf("missing committed scope/deadline: %+v", report)
			}
			if run.restart {
				if _, err := state.Exec(ctx, `UPDATE pooltune_worker.investigations SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, claim.ID); err != nil {
					t.Fatal(err)
				}
				restarted, err := claimProjectInvestigation(ctx, state, project.ID(), &epoch, ownedIndexRecoveryScope{Managed: true})
				if err != nil || restarted == nil || !restarted.RecoveryOnly || restarted.MaintenanceOperation != "statistics" {
					t.Fatalf("restart claim missing: %+v %v", restarted, err)
				}
				var recovered DispatchResult
				handled, err := worker.recover(ctx, state, project, *restarted, &recovered)
				if err != nil || !handled || recovered.Outcome != "repair_interrupted" || recovered.ManagedMaintenance == nil {
					t.Fatalf("committed restart not reconciled: %+v %v", recovered, err)
				}
				if recovered.ManagedMaintenance.RecoveryStatus != "statistics_configuration_restored" && recovered.ManagedMaintenance.RecoveryStatus != "statistics_object_removed" {
					t.Fatalf("unexpected restoration status: %+v", recovered.ManagedMaintenance)
				}
				claim, result = *restarted, recovered
			}
			if err := finishInvestigation(ctx, state, claim, result, epoch); err != nil {
				t.Fatal(err)
			}
			var outcome string
			if err := state.QueryRow(ctx, `SELECT outcome FROM pooltune_worker.owned_project_actions WHERE job_id=$1`, claim.ID).Scan(&outcome); err != nil || outcome == "pending" {
				t.Fatalf("statistics journal was not settled: %s %v", outcome, err)
			}
		})
	}
}
