//go:build integration

package provider

import (
	"context"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpool"
	"testing"
	"time"
)

func TestLocalPoolPhysicalGrowthAndDrain(t *testing.T) {
	ctx, admin, app, monitor, _ := testpool.Start(t)
	journal := &memoryJournal{}
	pool, err := NewLocalPool(LocalPoolConfig{Target: "db", Admin: admin, Application: app, Monitor: monitor, Allocation: 8, Journal: journal})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	wait := func(n int) LocalPoolStatus {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for {
			obs, e := pool.Observe(ctx)
			if e == nil && obs.Working == n && !obs.Pending {
				return obs
			}
			if time.Now().After(deadline) {
				t.Fatalf("physical capacity %d unavailable: %+v %v", n, obs, e)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	before := wait(2)
	if err = pool.Apply(ctx, "grow-1", before.Version, 4); err != nil {
		t.Fatal(err)
	}
	after := wait(4)
	txs := make([]pgx.Tx, 4)
	clients := make([]*pgx.Conn, 4)
	for i := range clients {
		clients[i], err = pgx.ConnectConfig(ctx, app)
		if err != nil {
			t.Fatal(err)
		}
		defer clients[i].Close(context.Background())
		txs[i], err = clients[i].Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = txs[i].Exec(ctx, "SELECT 1"); err != nil {
			t.Fatal(err)
		}
	}
	if err = pool.Apply(ctx, "shrink-1", after.Version, 2); err != nil {
		t.Fatal(err)
	}
	obs, err := pool.Observe(ctx)
	if err != nil || !obs.Pending || obs.Working != 4 {
		t.Fatalf("busy pool was reclaimed: %+v %v", obs, err)
	}
	for _, tx := range txs {
		if _, err = tx.Exec(ctx, "SELECT 1"); err != nil {
			t.Fatal("transaction interrupted", err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	wait(2)
	t.Log("real PgBouncer/PostgreSQL physical capacity 2 -> 4 -> 2; four live transactions survived lower target and completed")
}

func TestLocalPoolRejectsDriftForeignWorkAndRetainsIntent(t *testing.T) {
	ctx, admin, app, monitor, _ := testpool.Start(t)
	j := &memoryJournal{}
	cfg := LocalPoolConfig{Target: "db", Admin: admin, Application: app, Monitor: monitor, Allocation: 8, Journal: j}
	pool, err := NewLocalPool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var obs LocalPoolStatus
	for deadline := time.Now().Add(5 * time.Second); ; {
		obs, err = pool.Observe(ctx)
		if err == nil && obs.Working == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no verified pool", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pool.Apply(ctx, "bad-version", "stale", 3) != ErrStale || pool.Apply(ctx, "over-budget", obs.Version, 9) != ErrInvalid {
		t.Fatal("unguarded physical mutation")
	}
	foreign, err := pgx.ConnectConfig(ctx, monitor)
	if err != nil {
		t.Fatal(err)
	}
	if pool.Apply(ctx, "external-work", obs.Version, 3) == nil {
		t.Fatal("ignored foreign backend")
	}
	foreign.Close(ctx)
	if err = pool.Apply(ctx, "retained", obs.Version, 4); err != nil {
		t.Fatal(err)
	}
	if pool.Apply(ctx, "duplicate", obs.Version, 5) != ErrPending {
		t.Fatal("compounded physical intent")
	}
	pool.Close()
	restarted, err := NewLocalPool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	for deadline := time.Now().Add(5 * time.Second); ; {
		obs, err = restarted.Observe(ctx)
		if err == nil && !obs.Pending && obs.Working == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("readback did not reconcile retained intent", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	j.fail = true
	if restarted.Apply(ctx, "storage-fault", obs.Version, 5) != ErrStorage {
		t.Fatal("mutation without durable intent")
	}
	c, err := pgx.ConnectConfig(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	rows, err := poolRows(ctx, c, "SHOW CONFIG")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if (r["key"] == "default_pool_size" || r["key"] == "min_pool_size") && r["value"] != "4" {
			t.Fatal("storage failure changed pool")
		}
	}
	if restarted.Apply(ctx, "later", obs.Version, 6) != ErrPending {
		t.Fatal("storage uncertainty lost")
	}
}
