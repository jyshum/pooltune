//go:build integration

package hostedquery

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

func TestManagedMemoryFallbackPreservesCallerAndOriginalErrors(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	body, err := managedMemoryBody(`pg_catalog.current_setting('work_mem')`, `(SELECT 32768 AS candidate_kb,3 AS slots) allowance`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `CREATE FUNCTION public.memory_probe() RETURNS text LANGUAGE plpgsql SECURITY INVOKER PARALLEL UNSAFE SET work_mem='4MB' SET search_path=pg_catalog AS `+quoteMemoryBody(body)); err != nil {
		t.Fatal(err)
	}
	var conns []*pgx.Conn
	var txs []pgx.Tx
	defer func() {
		for _, c := range conns {
			c.Close(context.Background())
		}
	}()
	for i := 0; i < 4; i++ {
		db, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, db)
		if _, err := db.Exec(ctx, `SET work_mem='2MB'`); err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		txs = append(txs, tx)
		var inside, after string
		if err := tx.QueryRow(ctx, `SELECT public.memory_probe(),current_setting('work_mem')`).Scan(&inside, &after); err != nil {
			t.Fatal(err)
		}
		want := "32MB"
		if i == 3 {
			want = "4MB"
		}
		if inside != want || after != "2MB" {
			t.Fatalf("call%d got %s caller %s, want %s/2MB", i, inside, after, want)
		}
	}
	// Ending one call's transaction releases its extra-memory slot. The fourth
	// transaction then gets the allowance without reconnecting or changing SQL.
	if err := txs[0].Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var boosted string
	if err := txs[3].QueryRow(ctx, `SELECT public.memory_probe()`).Scan(&boosted); err != nil || boosted != "32MB" {
		t.Fatalf("released slot not reusable: %s %v", boosted, err)
	}
	for _, tx := range txs {
		_ = tx.Rollback(ctx)
	}
	bad, err := managedMemoryBody(`1/0`, `(SELECT 32768 AS candidate_kb,3 AS slots) allowance`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `CREATE FUNCTION public.memory_error() RETURNS integer LANGUAGE plpgsql SECURITY INVOKER PARALLEL UNSAFE SET work_mem='4MB' AS `+quoteMemoryBody(bad)); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `SELECT public.memory_error()`); err == nil || !strings.Contains(err.Error(), "22012") {
		t.Fatalf("original error was hidden: %v", err)
	}
	var setting string
	if err := admin.QueryRow(ctx, `SHOW work_mem`).Scan(&setting); err != nil || setting != "4MB" {
		t.Fatalf("error leaked setting: %s %v", setting, err)
	}
	missing, err := managedMemoryBody(`pg_catalog.current_setting('work_mem')`, `public.missing_allowance()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `CREATE OR REPLACE FUNCTION public.memory_probe() RETURNS text LANGUAGE plpgsql SECURITY INVOKER PARALLEL UNSAFE SET work_mem='4MB' AS `+quoteMemoryBody(missing)); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `SELECT public.memory_probe()`).Scan(&setting); err != nil || setting != "4MB" {
		t.Fatalf("missing allowance must use original: %s %v", setting, err)
	}
}

func quoteMemoryBody(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
