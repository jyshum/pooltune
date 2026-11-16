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

func TestManagedIndexRollbackIntentSurvivesFailure(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	const project = "managed-rollback-intent"
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch)VALUES($1,4)`, project); err != nil {
		t.Fatal(err)
	}
	claim := investigationClaim{Project: project, Attempt: 1, InvestigationJob: InvestigationJob{queryKey: queryKey{Database: 1}}}
	if err := state.QueryRow(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,attempts,lease_until)VALUES($1,1,2,3,'{}',1,clock_timestamp()+interval '30 seconds')RETURNING id`, project).Scan(&claim.ID); err != nil {
		t.Fatal(err)
	}
	original := ProjectIndexActionIntent{RunID: fmt.Sprintf("managed-index-%d-1", claim.ID), Target: ProjectIndexTarget{DatabaseOID: 1, TableOID: 9}, IndexName: "pooltune_ix_9_1", Keys: []ProjectIndexKey{{Number: 1, Name: "account"}}, RelationBytes: 100000}
	if err := recordOwnedIndexIntent(ctx, state, project, 4, claim, original); err != nil {
		t.Fatal(err)
	}
	if _, err := requestManagedIndexRollback(ctx, state, project, 3, claim, 24); err == nil {
		t.Fatal("stale epoch recorded rollback")
	}
	got, err := requestManagedIndexRollback(ctx, state, project, 4, claim, 24)
	if err != nil || !got.RollbackRequested || got.AppliedIndexOID != 24 {
		t.Fatalf("rollback intent: %+v %v", got, err)
	}
	restarted, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(ctx)
	var pending bool
	var oid uint32
	if err := restarted.QueryRow(ctx, `SELECT outcome='pending', (intent->>'AppliedIndexOID')::oid FROM pooltune_worker.owned_index_actions WHERE job_id=$1`, claim.ID).Scan(&pending, &oid); err != nil || !pending || oid != 24 {
		t.Fatalf("uncertain rollback lost intent: %v %d %v", pending, oid, err)
	}
	if _, err := requestManagedIndexRollback(ctx, restarted, project, 4, claim, 25); err == nil {
		t.Fatal("repeated rollback changed exact created OID")
	}
}
