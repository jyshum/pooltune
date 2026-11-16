//go:build integration

package hostedquery

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

func TestManagedIndexRollbackBindsExactCreatedObject(t *testing.T) {
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
	exec(`CREATE SCHEMA billing; CREATE TABLE billing.orders(account integer);INSERT INTO billing.orders SELECT i FROM generate_series(1,10000)i`)
	target := ProjectIndexTarget{Schema: "billing", Relation: "orders"}
	if err := db.QueryRow(ctx, `SELECT oid,'billing.orders'::regclass::oid FROM pg_database WHERE datname=current_database()`).Scan(&target.DatabaseOID, &target.TableOID); err != nil {
		t.Fatal(err)
	}
	claim := investigationClaim{InvestigationJob: InvestigationJob{ID: 9, queryKey: queryKey{Database: target.DatabaseOID}}, Attempt: 1}
	intent := ProjectIndexActionIntent{RunID: "managed-index-9-1", Target: target, IndexName: fmt.Sprintf("pooltune_ix_%d_1", target.TableOID), Keys: []ProjectIndexKey{{Number: 1, Name: "account"}}, RelationBytes: 100000, RollbackRequested: true}
	create := func() {
		t.Helper()
		exec("CREATE INDEX " + intent.IndexName + " ON billing.orders(account)")
		if err := db.QueryRow(ctx, `SELECT to_regclass($1)::oid`, "billing."+intent.IndexName).Scan(&intent.AppliedIndexOID); err != nil {
			t.Fatal(err)
		}
	}
	create()
	if err := rollbackManagedIndex(ctx, db, claim, intent); err != nil {
		t.Fatal(err)
	}
	got, err := inspectManagedIndexIntent(ctx, db, claim, intent)
	if err != nil || got.Status != "index_absent" {
		t.Fatalf("rollback reconciliation: %+v %v", got, err)
	}
	create()
	original := intent.AppliedIndexOID
	exec("DROP INDEX billing." + intent.IndexName)
	create()
	replacement := intent.AppliedIndexOID
	intent.AppliedIndexOID = original
	if err := rollbackManagedIndex(ctx, db, claim, intent); !errors.Is(err, ErrProjectIndexTargetChanged) {
		t.Fatalf("replacement admitted: %v", err)
	}
	var stillPresent bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_class WHERE oid=$1)`, replacement).Scan(&stillPresent); err != nil || !stillPresent {
		t.Fatalf("customer replacement removed: %v", err)
	}
	intent.AppliedIndexOID = replacement
	blocker, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close(ctx)
	tx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE billing.orders IN ACCESS SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	if err := rollbackManagedIndex(ctx, db, claim, intent); err == nil {
		t.Fatal("blocked rollback succeeded")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = inspectManagedIndexIntent(ctx, db, claim, intent)
	if err != nil || got.Status != "exact_index_present" || got.IndexOID != replacement {
		t.Fatalf("interrupted rollback not observed: %+v %v", got, err)
	}
	if err := rollbackManagedIndex(ctx, db, claim, intent); err != nil {
		t.Fatal(err)
	}
}
