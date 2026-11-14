//go:build integration

package hostedquery

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

// A real PostgreSQL calibration of the manual fault only. The request count and
// 500ms combined target cadence match the driver; this is not pg_cron, healthy
// workload protection, detector qualification or a replacement for the live run.
func TestCronManualSpikeBreachesFrozenObjectiveWith40CadencedReads(t *testing.T) {
	_, cfg, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	ctx, cancel := context.WithTimeout(context.Background(), 28*time.Second)
	defer cancel()
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	fault, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer fault.Close(ctx)
	if _, err = db.Exec(ctx, "CREATE EXTENSION pg_stat_statements;CREATE SCHEMA cron_spike;CREATE TABLE cron_spike.candidate(id integer PRIMARY KEY);INSERT INTO cron_spike.candidate VALUES(43)"); err != nil {
		t.Fatal(err)
	}
	if err = createCronRoutines(ctx, db, "cron_spike", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	query := cronQuery("cron_spike")
	read := func() {
		t.Helper()
		var id int
		if err := db.QueryRow(ctx, query).Scan(&id); err != nil || id != 43 {
			t.Fatalf("read: %d %v", id, err)
		}
	}
	counter := func() (int64, float64) {
		t.Helper()
		var calls int64
		var ms float64
		if err := db.QueryRow(ctx, "SELECT calls,total_exec_time FROM pg_stat_statements WHERE query=$1 AND toplevel", query).Scan(&calls, &ms); err != nil {
			t.Fatal(err)
		}
		return calls, ms
	}
	read()
	_, warmMS := counter()
	for i := 0; i < 10; i++ {
		read()
	}
	beforeCalls, beforeMS := counter()
	objective := max(3., 1.5*(beforeMS-warmMS)/10)
	done := make(chan error, 1)
	started := time.Now()
	go func() { _, err := fault.Exec(ctx, "CALL cron_spike.pooltune_cron_hold(60)"); done <- err }()
	// Model the hosted snapshot/request transit before the first target call.
	anchor := started.Add(450 * time.Millisecond)
	for i := 0; i < 40; i++ {
		if delay := time.Until(anchor.Add(time.Duration(i) * 500 * time.Millisecond)); delay > 0 {
			time.Sleep(delay)
		}
		read()
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	calls, ms := counter()
	mean := (ms - beforeMS) / float64(calls-beforeCalls)
	if calls-beforeCalls != 40 || mean <= objective {
		t.Fatalf("manual fault insufficient: calls=%d mean=%.3f objective=%.3f", calls-beforeCalls, mean, objective)
	}
	t.Logf("manual 60x80ms fault, 40 reads at 2/s with 450ms initial transit: mean %.3fms > frozen objective %.3fms", mean, objective)
}
