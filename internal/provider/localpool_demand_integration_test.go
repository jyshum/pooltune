//go:build integration

package provider

import (
	"context"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpool"
	"testing"
	"time"
)

// Omitting the pooler's waiting clients would make a saturated pool appear quiet.
func TestLocalPoolObservesDirectClientDemand(t *testing.T) {
	ctx, admin, app, monitor, _ := testpool.Start(t)
	p, err := NewLocalPool(LocalPoolConfig{Target: "db", Admin: admin, Application: app, Monitor: monitor, Allocation: 8, Journal: &memoryJournal{}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err = p.Observe(ctx); err != nil {
		t.Fatal(err)
	}
	var txs []pgx.Tx
	for i := 0; i < 2; i++ {
		c, e := pgx.ConnectConfig(ctx, app)
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close(context.Background())
		tx, e := c.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		txs = append(txs, tx)
	}
	waiter, e := pgx.ConnectConfig(ctx, app)
	if e != nil {
		t.Fatal(e)
	}
	defer waiter.Close(context.Background())
	done := make(chan error, 1)
	go func() { _, e := waiter.Exec(ctx, "SELECT 1"); done <- e }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		o, e := p.Observe(ctx)
		if e == nil && o.Waiting == 1 && o.Working-o.Idle == 2 {
			if o.Transactions == nil {
				t.Fatal("local adapter omitted measured counter")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing direct demand: %+v %v", o, e)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, tx := range txs {
		if e := tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
