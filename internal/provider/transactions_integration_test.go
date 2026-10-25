//go:build integration

package provider

import (
	"context"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpool"
	"testing"
)

func TestRealPoolTransactionCounter(t *testing.T) {
	ctx, admin, app, _, _ := testpool.Start(t)
	a, e := pgx.ConnectConfig(ctx, admin)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close(context.Background())
	c, e := pgx.ConnectConfig(ctx, app)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	if _, e = c.Exec(ctx, "SELECT 1"); e != nil {
		t.Fatal(e)
	}
	before := ReadTransactions(ctx, a, app.Database, "test-epoch")
	if before == nil {
		rows, err := poolRows(ctx, a, "SHOW STATS")
		t.Fatalf("missing counter: database=%s rows=%v err=%v", app.Database, rows, err)
	}
	for i := 0; i < 5; i++ {
		if _, e = c.Exec(ctx, "SELECT 1"); e != nil {
			t.Fatal(e)
		}
	}
	tx, e := c.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		if _, e = tx.Exec(ctx, "SELECT 1"); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if PollTransactions(canceled, admin, app.Database, "test-epoch") != nil {
		t.Fatal("canceled optional poll returned data")
	}
	if _, e = a.Exec(ctx, "SHOW POOLS"); e != nil {
		t.Fatal("optional telemetry damaged control connection", e)
	}
	after := ReadTransactions(ctx, a, app.Database, "test-epoch")
	if after == nil || after.Total-before.Total != 6 {
		t.Fatalf("expected 6 transactions, not query count: before=%+v after=%+v", before, after)
	}
	if after.Epoch != before.Epoch || !after.At.After(before.At) {
		t.Fatal("counter continuity invalid")
	}
	if ReadTransactions(ctx, a, "missing", "test-epoch") != nil {
		t.Fatal("invented missing counter")
	}
}
