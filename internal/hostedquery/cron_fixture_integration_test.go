//go:build integration

package hostedquery

import (
	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
	"testing"
	"time"
)

// This exercises real PostgreSQL transaction control and executor timing.
// It does not emulate or qualify the pg_cron scheduler.
func TestCronProcedureCommitsAndOuterCounterIncludesLockWait(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	_, err = db.Exec(ctx, `CREATE EXTENSION pg_stat_statements; CREATE SCHEMA cron_test; CREATE TABLE cron_test.candidate(id integer PRIMARY KEY); INSERT INTO cron_test.candidate VALUES(43)`)
	if err != nil {
		t.Fatal(err)
	}
	if err = createCronRoutines(ctx, db, "cron_test", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A top-level CALL with two iterations proves the second transaction runs.
	start := time.Now()
	if _, err = db.Exec(ctx, `CALL cron_test.pooltune_cron_hold(2)`); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatal("procedure returned before required holds")
	}
	for _, count := range []int{0, 201} {
		if _, err = db.Exec(ctx, `CALL cron_test.pooltune_cron_hold($1)`, count); err == nil {
			t.Fatal("unbounded hold count accepted")
		}
	}
	var answer int
	if err = db.QueryRow(ctx, cronQuery("cron_test")).Scan(&answer); err != nil || answer != 43 {
		t.Fatalf("read %d %v", answer, err)
	}
	blocker, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close(ctx)
	_, err = blocker.Exec(ctx, "BEGIN; LOCK TABLE cron_test.candidate IN ACCESS EXCLUSIVE MODE")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- db.QueryRow(ctx, cronQuery("cron_test")).Scan(&answer) }()
	time.Sleep(100 * time.Millisecond)
	if _, err = blocker.Exec(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	var ms float64
	err = db.QueryRow(ctx, `SELECT total_exec_time FROM pg_stat_statements WHERE query='SELECT "cron_test"."pooltune_cron_read"() AS id' AND toplevel`).Scan(&ms)
	if err != nil || ms < 80 {
		t.Fatalf("outer execution clock omitted lock wait: %.3f %v", ms, err)
	}
	t.Logf("actual outer pg_stat_statements execution: %.3f ms", ms)
	if _, err = db.Exec(ctx, "SET statement_timeout='1s'"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `CALL cron_test.pooltune_cron_hold(2)`); err == nil {
		t.Fatal("inherited job-session timeout below full-work allowance admitted")
	}
	if _, err = db.Exec(ctx, "RESET statement_timeout"); err != nil {
		t.Fatal(err)
	}
	// CALL inside an explicit transaction cannot silently report completed work.
	_, err = db.Exec(ctx, "BEGIN")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `CALL cron_test.pooltune_cron_hold(2)`); err == nil {
		t.Fatal("nested transaction unexpectedly committed")
	}
	db.Exec(ctx, "ROLLBACK")
}

func TestCronFullProcedureCompletesRequired200Holds(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	if _, err = db.Exec(ctx, "CREATE SCHEMA cron_full;CREATE TABLE cron_full.candidate(id integer)"); err != nil {
		t.Fatal(err)
	}
	if err = createCronRoutines(ctx, db, "cron_full", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err = db.Exec(ctx, "CALL cron_full.pooltune_cron_hold(200)"); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 16*time.Second || elapsed >= 20*time.Second {
		t.Fatalf("full work outside declared budget: %s", elapsed)
	}
	t.Logf("200 committing 80ms holds completed in %s", elapsed)
}
