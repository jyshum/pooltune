//go:build integration

package hostedquery

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"
)

func TestManagedLookupMeasuresDiscoveredRoleAndReusesSample(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
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
	exec(`CREATE EXTENSION pg_stat_statements; CREATE SCHEMA customer_data;
 CREATE TABLE customer_data.orders (row_id integer, account_id integer, padding text, category integer);
 INSERT INTO customer_data.orders SELECT i,i,'x',i%10 FROM generate_series(1,30000)i;
 GRANT USAGE ON SCHEMA customer_data TO fixture_app; GRANT SELECT ON customer_data.orders TO fixture_app;
 ANALYZE customer_data.orders;`)
	appConfig := config.Copy()
	appConfig.User = "fixture_app"
	app, err := pgx.ConnectConfig(ctx, appConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(ctx)
	const query = `SELECT account_id FROM ONLY customer_data.orders WHERE account_id=$1 AND category=$2`
	var value int
	if err := app.QueryRow(ctx, query, 7, 7).Scan(&value); err != nil {
		t.Fatal(err)
	}
	e := supabaseproject.DiscoveredIndexEvidence{Schema: "customer_data", Relation: "orders", Columns: []int16{2, 4}, Only: true}
	if err := db.QueryRow(ctx, `SELECT dbid,userid,queryid,'customer_data.orders'::regclass::oid FROM pg_stat_statements WHERE userid='fixture_app'::regrole AND query=$1`, query).Scan(&e.DatabaseOID, &e.RoleOID, &e.QueryID, &e.TableOID); err != nil {
		t.Fatal(err)
	}
	before, err := MeasureManagedIndexLookup(ctx, db, e, nil)
	if err != nil {
		t.Fatal(err)
	}
	if before.Sample.Plan != "Seq Scan" || before.Sample.PlanExamined < 30000 || len(before.Inputs) != 2 || before.Sample.PlanReturned != 1 {
		t.Fatalf("unexpected measurement %+v", before)
	}
	encoded, _ := json.Marshal(before)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["Inputs"] != nil || fields["inputs"] != nil || fields["plan"] == nil || fields["sample"] == nil || before.Sample.Inputs != nil {
		t.Fatal("sample values escaped JSON result")
	}
	exec(`CREATE INDEX account_category ON customer_data.orders(account_id,category)`)
	after, err := MeasureManagedIndexLookup(ctx, db, e, before.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	if after.Sample.PlanReturned != 1 || after.Sample.Blocks >= before.Sample.Blocks {
		t.Fatalf("index did not improve same sample: %+v -> %+v", before.Sample, after.Sample)
	}
	const scalarQuery = "SELECT row_id FROM customer_data.orders WHERE row_id=$1"
	if err := app.QueryRow(ctx, scalarQuery, 7).Scan(&value); err != nil {
		t.Fatal(err)
	}
	scalar := e
	scalar.Columns = []int16{1}
	scalar.Only = false
	if err := db.QueryRow(ctx, `SELECT queryid FROM pg_stat_statements WHERE userid='fixture_app'::regrole AND query=$1`, scalarQuery).Scan(&scalar.QueryID); err != nil {
		t.Fatal(err)
	}
	sample, err := MeasureManagedIndexLookup(ctx, db, scalar, nil)
	if err != nil || len(sample.Inputs) != 1 || sample.Sample.PlanReturned != 1 {
		t.Fatalf("scalar sample failed: %+v %v", sample, err)
	}
	wrong := e
	wrong.QueryID++
	if _, err := MeasureManagedIndexLookup(ctx, db, wrong, before.Inputs); err == nil {
		t.Fatal("accepted different query identity")
	}
	exec(`ALTER TABLE customer_data.orders ENABLE ROW LEVEL SECURITY`)
	if _, err := MeasureManagedIndexLookup(ctx, db, e, before.Inputs); err == nil {
		t.Fatal("accepted RLS relation without request context")
	}
	exec(`ALTER TABLE customer_data.orders DISABLE ROW LEVEL SECURITY; REVOKE SELECT ON customer_data.orders FROM fixture_app`)
	if _, err := MeasureManagedIndexLookup(ctx, db, e, before.Inputs); err == nil {
		t.Fatal("bypassed app role revocation")
	}
	exec(`GRANT SELECT ON customer_data.orders TO fixture_app; DROP TABLE customer_data.orders; CREATE TABLE customer_data.orders(account_id integer,category integer); GRANT SELECT ON customer_data.orders TO fixture_app`)
	if _, err := MeasureManagedIndexLookup(ctx, db, e, before.Inputs); err == nil {
		t.Fatal("accepted replacement table")
	}
}
