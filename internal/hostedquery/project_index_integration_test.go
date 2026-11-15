//go:build integration

package hostedquery

import (
	"errors"
	"slices"
	"testing"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestResolveProjectIndexOnOrdinaryTable(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
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
	exec(`CREATE SCHEMA "Billing Team";
		CREATE SCHEMA billing_shadow;
		CREATE TABLE "Billing Team"."Recent Orders" (id bigint PRIMARY KEY, "Placed, at" timestamptz, removed int, "Account ""Name""" varchar(64), extra text);
		ALTER TABLE "Billing Team"."Recent Orders" DROP COLUMN removed;
		CREATE TABLE billing_shadow."Recent Orders" (id bigint PRIMARY KEY, "Placed, at" timestamptz, removed int, "Account ""Name""" varchar(64));
		CREATE VIEW "Billing Team".orders_view AS SELECT * FROM "Billing Team"."Recent Orders"`)
	target := ProjectIndexTarget{Schema: "Billing Team", Relation: "Recent Orders"}
	var shadowOID, viewOID uint32
	if err := db.QueryRow(ctx, `SELECT oid,'"Billing Team"."Recent Orders"'::regclass::oid,
		'billing_shadow."Recent Orders"'::regclass::oid, '"Billing Team".orders_view'::regclass::oid
		FROM pg_catalog.pg_database WHERE datname=pg_catalog.current_database()`).Scan(&target.DatabaseOID, &target.TableOID, &shadowOID, &viewOID); err != nil {
		t.Fatal(err)
	}
	assertPair := func(wantCovering bool) {
		t.Helper()
		got, err := ResolveProjectIndex(ctx, db, target, 4, []int16{4, 2})
		wantKeys := []ProjectIndexKey{{Number: 4, Name: `Account "Name"`}, {Number: 2, Name: "Placed, at"}}
		if err != nil || got.Target != target || !slices.Equal(got.Keys, wantKeys) || got.Covering != wantCovering {
			t.Fatalf("resolve %+v, error %v, want keys %+v and covering %v", got, err, wantKeys, wantCovering)
		}
	}
	assertPair(false)
	scalar, err := ResolveProjectIndex(ctx, db, target, 1, nil)
	if err != nil || !scalar.Covering || !slices.Equal(scalar.Keys, []ProjectIndexKey{{Number: 1, Name: "id"}}) {
		t.Fatalf("primary key should cover this scalar: %+v, %v", scalar, err)
	}
	for _, sql := range []string{
		`CREATE INDEX shadow_pair ON billing_shadow."Recent Orders" ("Account ""Name""", "Placed, at")`,
		`CREATE INDEX wrong_order ON "Billing Team"."Recent Orders" ("Placed, at", "Account ""Name""")`,
		`CREATE INDEX partial_pair ON "Billing Team"."Recent Orders" ("Account ""Name""", "Placed, at") WHERE id > 0`,
		`CREATE INDEX included_column ON "Billing Team"."Recent Orders" ("Account ""Name""") INCLUDE ("Placed, at")`,
	} {
		exec(sql)
		assertPair(false)
	}
	exec(`CREATE INDEX actual_pair ON "Billing Team"."Recent Orders" ("Account ""Name""", "Placed, at", id)`)
	assertPair(true)

	for _, tc := range []struct {
		name   string
		change func(*ProjectIndexTarget)
	}{
		{"wrong database", func(p *ProjectIndexTarget) { p.DatabaseOID++ }},
		{"wrong schema", func(p *ProjectIndexTarget) { p.Schema = "billing_shadow" }},
		{"wrong relation name", func(p *ProjectIndexTarget) { p.Relation = "Other Orders" }},
		{"other schema's OID", func(p *ProjectIndexTarget) { p.TableOID = shadowOID }},
		{"view", func(p *ProjectIndexTarget) { p.TableOID, p.Relation = viewOID, "orders_view" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := target
			tc.change(&bad)
			if _, err := ResolveProjectIndex(ctx, db, bad, 4, []int16{4, 2}); !errors.Is(err, ErrProjectIndexTargetChanged) {
				t.Fatalf("accepted target outside enrollment: %v", err)
			}
		})
	}
	for _, key := range []int16{3, 6} {
		if _, err := ResolveProjectIndex(ctx, db, target, 4, []int16{4, key}); !errors.Is(err, ErrProjectIndexTargetChanged) {
			t.Fatalf("accepted dropped or missing key %d: %v", key, err)
		}
	}
	if _, err := ResolveProjectIndex(ctx, db, target, 2, []int16{4, 2}); err == nil {
		t.Fatal("accepted mismatched anchor")
	}
}
