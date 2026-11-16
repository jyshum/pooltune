//go:build integration

package hostedapp

import (
	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
	"testing"
)

func TestEnrollmentCatalogReadsIdentityWithoutCallingFunction(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	if _, err := db.Exec(ctx, `CREATE SCHEMA customer;CREATE TABLE customer.never_write(n int);
CREATE FUNCTION customer.report(integer) RETURNS integer LANGUAGE plpgsql AS $$BEGIN INSERT INTO customer.never_write VALUES(1); RETURN $1; END$$;
CREATE FUNCTION customer.hidden() RETURNS int LANGUAGE sql SECURITY DEFINER AS $$SELECT 1$$;
CREATE FUNCTION customer.other() RETURNS int LANGUAGE sql AS $$SELECT 2$$;ALTER FUNCTION customer.other() OWNER TO fixture_app;`); err != nil {
		t.Fatal(err)
	}
	catalog, err := readRepairEnrollmentCatalog(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Functions) != 1 || catalog.Functions[0].Name != "report" || catalog.Functions[0].Signature != "customer.report(integer)" {
		t.Fatalf("unexpected eligible catalog: %+v", catalog.Functions)
	}
	first := catalog.Functions[0]
	if _, err := db.Exec(ctx, `CREATE OR REPLACE FUNCTION customer.report(integer) RETURNS integer LANGUAGE plpgsql AS $$BEGIN RETURN $1+1; END$$`); err != nil {
		t.Fatal(err)
	}
	catalog, err = readRepairEnrollmentCatalog(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Functions[0].ObjectOID != first.ObjectOID || catalog.Functions[0].Fingerprint == first.Fingerprint || catalog.Functions[0].Generation == first.Generation {
		t.Fatal("same-OID function replacement not detected")
	}
	if sameEnrollmentIdentity(first, catalog.Functions[0]) {
		t.Fatal("replaced function accepted as original selection")
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM customer.never_write`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("catalog discovery called customer code: %d %v", count, err)
	}
	if _, err := db.Exec(ctx, `DROP FUNCTION customer.report(integer);CREATE FUNCTION customer.report(integer) RETURNS int LANGUAGE sql AS $$SELECT $1$$`); err != nil {
		t.Fatal(err)
	}
	catalog, err = readRepairEnrollmentCatalog(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if sameEnrollmentIdentity(first, catalog.Functions[0]) {
		t.Fatal("replacement OID accepted")
	}
}
