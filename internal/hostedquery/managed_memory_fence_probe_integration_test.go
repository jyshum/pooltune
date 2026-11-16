//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"pooltune/internal/testpg"
)

// Disposable native locking proof. This is not an enabled customer action.
func TestManagedMemoryRenameFenceProbe(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err = admin.Exec(ctx, `CREATE ROLE memory_owner LOGIN PASSWORD 'test-memory-owner';CREATE SCHEMA billing AUTHORIZATION memory_owner;SET ROLE memory_owner;CREATE FUNCTION billing.total() RETURNS bigint LANGUAGE plpgsql AS 'BEGIN RETURN 1; END';GRANT USAGE ON SCHEMA billing TO fixture_app;RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	ownerCfg := cfg.Copy()
	ownerCfg.User = "memory_owner"
	ownerCfg.Password = "test-memory-owner"
	owner, err := pgx.ConnectConfig(ctx, ownerCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close(ctx)
	second, err := pgx.ConnectConfig(ctx, ownerCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(ctx)
	appCfg := cfg.Copy()
	appCfg.User = "fixture_app"
	app, err := pgx.ConnectConfig(ctx, appCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(ctx)
	if _, err = second.Exec(ctx, `SET lock_timeout='75ms'`); err != nil {
		t.Fatal(err)
	}
	var catalogUpdate bool
	if err = owner.QueryRow(ctx, `SELECT has_table_privilege(current_user,'pg_catalog.pg_proc','UPDATE')`).Scan(&catalogUpdate); err != nil || catalogUpdate {
		t.Fatalf("unexpected catalog permission %v %v", catalogUpdate, err)
	}
	snapshot := func(db interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	}) (string, string) {
		t.Helper()
		var body, gen string
		err := db.QueryRow(ctx, `SELECT to_jsonb(p)::text,p.xmin::text FROM pg_proc p WHERE oid='billing.total()'::regprocedure`).Scan(&body, &gen)
		if err != nil {
			t.Fatal(err)
		}
		return body, gen
	}
	original, originalGen := snapshot(owner)
	tx, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `ALTER FUNCTION billing.total() RENAME TO pt_disposable_fence;ALTER FUNCTION billing.pt_disposable_fence() RENAME TO total`); err != nil {
		t.Fatal(err)
	}
	locked, lockedGen := snapshot(tx)
	if locked != original || lockedGen == originalGen {
		t.Fatalf("rename changed content or failed to mint own generation")
	}
	// The original committed function remains callable while its tuple is fenced.
	read, err := app.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	var result int64
	if err = read.QueryRow(ctx, `SELECT billing.total()`).Scan(&result); err != nil || result != 1 {
		t.Fatalf("ordinary read failed %d %v", result, err)
	}
	read.Rollback(ctx)
	for _, sql := range []string{`CREATE OR REPLACE FUNCTION billing.total() RETURNS bigint LANGUAGE plpgsql AS 'BEGIN RETURN 2; END'`, `ALTER FUNCTION billing.total() COST 123`, `REVOKE EXECUTE ON FUNCTION billing.total() FROM PUBLIC`} {
		_, err = second.Exec(ctx, sql)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "55P03" {
			t.Fatalf("owner edit was not fenced: %s %v", sql, err)
		}
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	restored, restoredGen := snapshot(owner)
	if restored != original || restoredGen != originalGen {
		t.Fatal("probe rollback changed original catalog")
	}
	// An edit committed between preflight and fencing remains visible in the
	// normalized row after rename; the fence never overwrites that edited field.
	for _, sql := range []string{`ALTER FUNCTION billing.total() COST 123`, `CREATE OR REPLACE FUNCTION billing.total() RETURNS bigint LANGUAGE plpgsql AS 'BEGIN RETURN 2; END'`, `REVOKE EXECUTE ON FUNCTION billing.total() FROM PUBLIC`} {
		before, _ := snapshot(owner)
		if _, err = second.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
		edited, editedGen := snapshot(owner)
		if before == edited {
			t.Fatalf("test edit changed nothing %s", sql)
		}
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `ALTER FUNCTION billing.total() RENAME TO pt_disposable_fence;ALTER FUNCTION billing.pt_disposable_fence() RENAME TO total`); err != nil {
			t.Fatal(err)
		}
		after, _ := snapshot(tx)
		if after != edited {
			t.Fatalf("fence overwrote owner edit %s", sql)
		}
		tx.Rollback(ctx)
		after, gen := snapshot(owner)
		if after != edited || gen != editedGen {
			t.Fatalf("rollback lost owner edit %s", sql)
		}
	}
	// A semantically identical owner save changes xmin but not the canonical row.
	before, gen := snapshot(owner)
	var definition string
	if err = owner.QueryRow(ctx, `SELECT pg_get_functiondef('billing.total()'::regprocedure)`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if _, err = second.Exec(ctx, definition); err != nil {
		t.Fatal(err)
	}
	same, newGen := snapshot(owner)
	if same != before || newGen == gen {
		t.Fatal("expected identical content with a new generation")
	}
}
