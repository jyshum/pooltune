//go:build integration

package hostedquery

import (
	"context"
	"testing"
	"time"

	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"
)

func TestManagedMaintenanceUsesDiscoveredOrdinaryTableAndSameInput(t *testing.T) {
	for _, operation := range []ProjectMaintenanceOperation{ProjectAnalyze, ProjectVacuum} {
		t.Run(string(operation), func(t *testing.T) {
			ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
			db := openMonitorConn(t, ctx, config)
			exec := func(sql string) {
				t.Helper()
				if _, err := db.Exec(ctx, sql); err != nil {
					t.Fatal(err)
				}
			}
			exec(`CREATE EXTENSION pg_stat_statements; CREATE SCHEMA customer_data;
   CREATE TABLE customer_data.orders(row_id integer,unused integer,account_id integer,padding text) WITH(autovacuum_enabled=false);
   INSERT INTO customer_data.orders SELECT i,0,43,repeat('x',100) FROM generate_series(1,30000)i;
   CREATE INDEX account_lookup ON customer_data.orders(account_id);
   GRANT USAGE ON SCHEMA customer_data TO fixture_app;GRANT SELECT ON customer_data.orders TO fixture_app;
   ANALYZE customer_data.orders;`)
			if operation == ProjectAnalyze {
				exec(`UPDATE customer_data.orders SET account_id=row_id+100 WHERE row_id>10`)
			} else {
				exec(`UPDATE customer_data.orders SET account_id=row_id/1500; ANALYZE customer_data.orders`)
			}
			appConfig := config.Copy()
			appConfig.User = "fixture_app"
			app := openMonitorConn(t, ctx, appConfig)
			query := `SELECT account_id FROM ONLY customer_data.orders WHERE account_id=$1`
			rows, err := app.Query(ctx, query, int32(43))
			if err != nil {
				t.Fatal(err)
			}
			rows.Close()
			observed := supabaseproject.DiscoveredIndexEvidence{Schema: "customer_data", Relation: "orders", Columns: []int16{3}, Only: true}
			if err := db.QueryRow(ctx, `SELECT dbid,userid,queryid,'customer_data.orders'::regclass::oid FROM pg_stat_statements WHERE userid='fixture_app'::regrole AND query=$1`, query).Scan(&observed.DatabaseOID, &observed.RoleOID, &observed.QueryID, &observed.TableOID); err != nil {
				t.Fatal(err)
			}
			before, err := MeasureManagedIndexLookup(ctx, db, observed, nil)
			if err != nil {
				t.Fatal(err)
			}
			managed := ManagedProjectIndex{Capabilities: []string{string(operation)}}
			evidence, kind, err := managed.maintenanceEvidence(ctx, db, observed, before, "test-managed-maintenance")
			if err != nil || kind != operation {
				t.Fatalf("missing %s cause: kind=%s plan=%+v estimate=%d heap=%v err=%v", operation, kind, before.Evidence, before.EstimatedRows, before.HeapFetches, err)
			}
			proposal := IndexProposal{Outcome: "propose", Repair: string(operation), RunID: evidence.RunID, TableOID: observed.TableOID, Column: 3, Reason: "observed mechanism"}
			if err := ValidateDiagnosticProposal(evidence, proposal, time.Now()); err != nil {
				t.Fatal(err)
			}
			request := ProjectMaintenanceRequest{Target: before.Catalog.Target, RunID: evidence.RunID, Operation: operation, ObservedAt: time.Now()}
			if operation == ProjectAnalyze {
				request.Columns = []int16{3}
			}
			guard := func(context.Context, ProjectMaintenanceIntent) error { return nil }
			action, err := ApplyProjectMaintenance(ctx, config, request, ProjectMaintenanceGuards{CheckPermission: guard, CheckCapacity: guard, RecordIntent: guard})
			if err != nil || !action.Confirmed {
				t.Fatalf("action %+v err=%v", action, err)
			}
			after, err := MeasureManagedIndexLookup(ctx, db, observed, before.Inputs)
			if err != nil {
				t.Fatal(err)
			}
			if before.Sample.PlanReturned != after.Sample.PlanReturned {
				t.Fatal("maintenance changed result count")
			}
			if operation == ProjectAnalyze && rowEstimateError(after.EstimatedRows, after.Sample.PlanReturned) >= rowEstimateError(before.EstimatedRows, before.Sample.PlanReturned) {
				t.Fatalf("planner did not improve: %d -> %d", before.EstimatedRows, after.EstimatedRows)
			}
			if operation == ProjectVacuum && (after.HeapFetches == nil || *after.HeapFetches >= *before.HeapFetches) {
				t.Fatalf("visibility did not improve: %v -> %v", before.HeapFetches, after.HeapFetches)
			}
			managed.Capabilities = nil
			_, kind, err = managed.maintenanceEvidence(ctx, db, observed, before, "disabled")
			if err != nil || kind != "" {
				t.Fatal("unchecked repair still eligible")
			}
		})
	}
}
