//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"
)

func TestManagedColumnStatisticsRestoresOriginalSampleOnInconclusiveTrial(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	db := openMonitorConn(t, ctx, config)
	exec := func(sql string) {
		t.Helper()
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE EXTENSION pg_stat_statements;CREATE SCHEMA customer_data;
 CREATE TABLE customer_data.orders(row_id integer, account_id integer,payload text) WITH(autovacuum_enabled=false);
 INSERT INTO customer_data.orders VALUES(6241,43,repeat('x',56));
 INSERT INTO customer_data.orders SELECT n,CASE WHEN n<=6240 THEN 42 WHEN n<=10080 THEN 43 ELSE n END,repeat('x',56) FROM generate_series(1,12000)n WHERE n<>6241 ORDER BY(n*7919::bigint)%12001;
 CREATE INDEX account_lookup ON customer_data.orders(account_id);
 ALTER TABLE customer_data.orders ALTER COLUMN account_id SET STATISTICS 1;
 ANALYZE customer_data.orders;GRANT USAGE ON SCHEMA customer_data TO fixture_app;GRANT SELECT ON customer_data.orders TO fixture_app;
 SELECT pg_stat_force_next_flush()`)
	exec(`ANALYZE customer_data.orders; SELECT pg_stat_force_next_flush()`)
	observed, first := managedStatisticsTestMeasurement(t, ctx, config, db, []int16{2})
	var original string
	if err := db.QueryRow(ctx, `SELECT most_common_vals::text FROM pg_stats WHERE schemaname='customer_data' AND tablename='orders' AND attname='account_id'`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	var before managedStatisticsObservation
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var err error
		before, err = readManagedStatistics(ctx, tx, observed, first, nil, "managed-statistics-42-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if before.next != 2 || !before.evidence.Statistics.SupportsRepair(true) {
		t.Fatalf("cause not eligible: %+v", before.evidence)
	}
	guard := func(context.Context, ProjectStatisticsIntent) error { return nil }
	verified := false
	action, err := applyProjectStatisticsTarget(ctx, config, ProjectStatisticsRequest{Target: first.Catalog.Target, RunID: before.evidence.RunID, ObservedAt: time.Now(), Column: 2, NextTarget: 2}, ProjectStatisticsGuards{CheckPermission: guard, CheckCapacity: guard, RecordIntent: guard}, projectStatisticsTrial{verify: func(ctx context.Context, tx pgx.Tx, _ ProjectStatisticsIntent) (bool, error) {
		after, err := MeasureManagedIndexLookupTx(ctx, tx, observed, first.Catalog, first.Inputs)
		if err != nil {
			return false, err
		}
		verified = after.Sample.PlanReturned == first.Sample.PlanReturned && rowEstimateError(after.EstimatedRows, after.Sample.PlanReturned) <= 3
		return false, nil
	}})
	if err != nil || !action.Restored || !verified {
		t.Fatalf("failed real rollback trial: %+v verified=%v err=%v", action, verified, err)
	}
	var restored string
	var target int32
	if err := db.QueryRow(ctx, `SELECT most_common_vals::text,(SELECT attstattarget::int FROM pg_attribute WHERE attrelid='customer_data.orders'::regclass AND attname='account_id') FROM pg_stats WHERE schemaname='customer_data' AND tablename='orders' AND attname='account_id'`).Scan(&restored, &target); err != nil || restored != original || target != 1 {
		t.Fatalf("did not restore exact prior sample: %s/%s target=%d err=%v", original, restored, target, err)
	}
	exec(`DROP INDEX customer_data.account_lookup`)
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		_, err := readManagedStatistics(ctx, tx, observed, first, nil, "stale-index")
		return err
	})
	if !errors.Is(err, errOwnedStatisticsEvidence) {
		t.Fatalf("accepted stale covering index evidence: %v", err)
	}
}

func TestManagedJointStatisticsUsesActualDiscoveredKeysAndRollsBack(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	db := openMonitorConn(t, ctx, config)
	exec := func(sql string) {
		t.Helper()
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE EXTENSION pg_stat_statements;CREATE SCHEMA customer_data;
 CREATE TABLE customer_data.orders(row_id integer,account_id integer,payload text,category integer) WITH(autovacuum_enabled=false);
 INSERT INTO customer_data.orders VALUES(1,1,repeat('x',56),2),(101,2,repeat('x',56),1);
 INSERT INTO customer_data.orders SELECT n,CASE WHEN n<=100 OR n BETWEEN 401 AND 80000 THEN 1 ELSE 2 END,repeat('x',56),CASE WHEN n BETWEEN 101 AND 80000 THEN 1 ELSE 2 END FROM generate_series(1,160000)n WHERE n NOT IN(1,101) ORDER BY(n*7919::bigint)%160001;
 CREATE INDEX account_category ON customer_data.orders(account_id,category);
 ANALYZE customer_data.orders;GRANT USAGE ON SCHEMA customer_data TO fixture_app;GRANT SELECT ON customer_data.orders TO fixture_app;
 SELECT pg_stat_force_next_flush()`)
	exec(`ANALYZE customer_data.orders; SELECT pg_stat_force_next_flush()`)
	observed, first := managedStatisticsTestMeasurement(t, ctx, config, db, []int16{2, 4})
	var before managedStatisticsObservation
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var err error
		before, err = readManagedStatistics(ctx, tx, observed, first, nil, "managed-statistics-42-1")
		return err
	}); err != nil {
		t.Fatalf("statistics cause: first=%+v err=%v", first, err)
	}
	if len(before.samples) != 2 || !before.evidence.Statistics.Correlated {
		t.Fatalf("missing two measured cases: %+v", before.evidence)
	}
	guard := func(context.Context) error { return nil }
	recheck := func(ctx context.Context, tx pgx.Tx) error {
		_, err := readManagedStatistics(ctx, tx, observed, first, managedStatisticsInputs(before.samples), before.evidence.RunID)
		return err
	}
	verified := false
	applied, retained, err := applyManagedJointStatistics(ctx, config, observed, first.Catalog, time.Now(), before.evidence.RunID, recheck, func(ctx context.Context, tx pgx.Tx) (bool, error) {
		verified = true
		for _, b := range before.samples {
			a, err := MeasureManagedIndexLookupTx(ctx, tx, observed, first.Catalog, b.Inputs)
			if err != nil {
				return false, err
			}
			verified = verified && a.Sample.PlanReturned == b.Sample.PlanReturned && rowEstimateError(a.EstimatedRows, a.Sample.PlanReturned) <= 3
		}
		return false, nil
	}, guard, guard, func(context.Context, any) error { return nil })
	if err != nil || !applied || retained || !verified {
		t.Fatalf("joint trial: applied=%v retained=%v verified=%v err=%v", applied, retained, verified, err)
	}
	var existing int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM pg_statistic_ext WHERE stxrelid=$1", observed.TableOID).Scan(&existing); err != nil || existing != 0 {
		t.Fatalf("inconclusive joint stats survived: %d %v", existing, err)
	}
}

func managedStatisticsTestMeasurement(t *testing.T, ctx context.Context, config *pgx.ConnConfig, db *pgx.Conn, columns []int16) (supabaseproject.DiscoveredIndexEvidence, ManagedIndexMeasurement) {
	t.Helper()
	appConfig := config.Copy()
	appConfig.User = "fixture_app"
	app := openMonitorConn(t, ctx, appConfig)
	query := `SELECT account_id FROM ONLY customer_data.orders WHERE account_id=$1`
	args := []any{int32(43)}
	if len(columns) == 2 {
		query += " AND category=$2"
		args = []any{int32(1), int32(2)}
	}
	rows, err := app.Query(ctx, query, args...)
	if err != nil {
		t.Fatal(err)
	}
	rows.Close()
	observed := supabaseproject.DiscoveredIndexEvidence{Schema: "customer_data", Relation: "orders", Columns: columns, Only: true}
	if err := db.QueryRow(ctx, `SELECT dbid,userid,queryid,'customer_data.orders'::regclass::oid FROM pg_stat_statements WHERE userid='fixture_app'::regrole AND query=$1`, query).Scan(&observed.DatabaseOID, &observed.RoleOID, &observed.QueryID, &observed.TableOID); err != nil {
		t.Fatal(err)
	}
	first, err := MeasureManagedIndexLookup(ctx, db, observed, nil)
	if err != nil {
		t.Fatal(err)
	}
	return observed, first
}
