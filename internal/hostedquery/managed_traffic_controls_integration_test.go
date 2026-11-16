//go:build integration

package hostedquery

import (
	"context"
	"pooltune/internal/testpg"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagedTrafficControlsExcludeOwnCatalogAndWitnessReads(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	db := openMonitorConn(t, ctx, cfg)
	traffic := openMonitorConn(t, ctx, cfg)
	if _, err := db.Exec(ctx, `CREATE EXTENSION pg_stat_statements;CREATE TABLE customer_health(id integer PRIMARY KEY,n integer);INSERT INTO customer_health VALUES(1,0)`); err != nil {
		t.Fatal(err)
	}
	var database, role uint32
	if err := db.QueryRow(ctx, `SELECT oid,current_user::regrole::oid FROM pg_database WHERE datname=current_database()`).Scan(&database, &role); err != nil {
		t.Fatal(err)
	}
	work, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	var applicationReads atomic.Bool
	go func() {
		defer close(done)
		for work.Err() == nil {
			var n int
			if traffic.QueryRow(work, `SELECT count(*) FROM pg_catalog.pg_class`).Scan(&n) != nil {
				return
			}
			if traffic.QueryRow(work, `SELECT 1`).Scan(&n) != nil {
				return
			}
			if applicationReads.Load() {
				if traffic.QueryRow(work, `SELECT n FROM customer_health WHERE id=$1`, 1).Scan(&n) != nil {
					return
				}
			}
			if _, err := traffic.Exec(work, `UPDATE customer_health SET n=n+1 WHERE id=$1`, 1); err != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	defer func() { stop(); <-done }()
	if _, err := observeManagedControls(ctx, db, database, role, -123, nil); err == nil {
		t.Fatal("catalog/witness traffic masqueraded as protected application reads")
	}
	applicationReads.Store(true)
	time.Sleep(20 * time.Millisecond)
	controls, err := observeManagedControls(ctx, db, database, role, -123, nil)
	if err != nil || controls.ReadCalls == 0 || controls.WriteCalls == 0 {
		t.Fatal("genuine application traffic not accepted", err)
	}
	var catalogID, witnessID int64
	if err := db.QueryRow(ctx, `SELECT queryid FROM pg_stat_statements WHERE query='SELECT count(*) FROM pg_catalog.pg_class'`).Scan(&catalogID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT queryid FROM pg_stat_statements WHERE query='SELECT $1'`).Scan(&witnessID); err != nil {
		t.Fatal(err)
	}
	if _, found := controls.means[catalogID]; found {
		t.Fatal("catalog query remained in protection baseline")
	}
	if _, found := controls.means[witnessID]; found {
		t.Fatal("witness query remained in protection baseline")
	}
}
