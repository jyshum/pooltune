//go:build integration

package hostedquery

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

func TestManagedIndexRecoveryPinsOrdinaryTableAndJournal(t *testing.T) {
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
	exec(`CREATE SCHEMA billing; CREATE TABLE billing.orders(account integer, region integer); INSERT INTO billing.orders SELECT i,i%10 FROM generate_series(1,10000)i`)
	target := ProjectIndexTarget{Schema: "billing", Relation: "orders"}
	if err := db.QueryRow(ctx, `SELECT oid,'billing.orders'::regclass::oid FROM pg_database WHERE datname=current_database()`).Scan(&target.DatabaseOID, &target.TableOID); err != nil {
		t.Fatal(err)
	}
	claim := investigationClaim{InvestigationJob: InvestigationJob{ID: 9, queryKey: queryKey{Database: target.DatabaseOID}}, Attempt: 2}
	intent := ProjectIndexActionIntent{RunID: "managed-index-9-1", Target: target, IndexName: fmt.Sprintf("pooltune_ix_%d_1_2", target.TableOID), Keys: []ProjectIndexKey{{Number: 1, Name: "account"}, {Number: 2, Name: "region"}}, RelationBytes: 100000}
	assert := func(want string) {
		t.Helper()
		got, err := inspectManagedIndexIntent(ctx, db, claim, intent)
		if err != nil || got.Status != want {
			t.Fatalf("got %+v, %v, want %s", got, err, want)
		}
	}
	assert("index_absent")
	exec("CREATE INDEX " + intent.IndexName + " ON billing.orders(account,region)")
	assert("exact_index_present")
	prior := claim.ID
	claim.ID++
	assert("target_changed")
	claim.ID = prior
	exec("ALTER TABLE billing.orders OWNER TO fixture_app")
	assert("target_changed")
	exec("ALTER TABLE billing.orders OWNER TO postgres; DROP INDEX billing." + intent.IndexName + "; CREATE INDEX equivalent ON billing.orders(account,region)")
	assert("equivalent_index_present")
	exec("DROP TABLE billing.orders; CREATE TABLE billing.orders(account integer,region integer)")
	assert("target_changed")
}
