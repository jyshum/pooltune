//go:build integration

package hostedquery

import (
	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
	"testing"
	"time"
)

func TestManagedCronObserverReadsRealCompletedWorkAndBlockingWithoutCallingJob(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements", "-c", "compute_query_id=on")
	admin := openMonitorConn(t, ctx, cfg)
	locker := openMonitorConn(t, ctx, cfg)
	traffic := openMonitorConn(t, ctx, cfg)
	if _, err := admin.Exec(ctx, `CREATE EXTENSION pg_stat_statements;CREATE TABLE app_orders(id int PRIMARY KEY,n int);INSERT INTO app_orders VALUES(1,10);CREATE SCHEMA cron;CREATE TABLE cron.job_run_details(runid bigint,jobid bigint,job_pid integer,start_time timestamptz,end_time timestamptz,status text)`); err != nil {
		t.Fatal(err)
	}
	var count int
	for range 4 {
		if err := traffic.QueryRow(ctx, `SELECT n FROM app_orders WHERE id=1 FOR UPDATE`).Scan(&count); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := traffic.Exec(ctx, `UPDATE app_orders SET n=n+1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	var qid int64
	if err := admin.QueryRow(ctx, `SELECT queryid FROM pg_stat_statements WHERE query LIKE 'SELECT n FROM app_orders%'`).Scan(&qid); err != nil {
		t.Fatal(err)
	}
	lock, err := locker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, `UPDATE app_orders SET n=n WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	var oid uint32
	if err := admin.QueryRow(ctx, `SELECT oid FROM pg_database WHERE datname=current_database()`).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO cron.job_run_details VALUES(101,17,$1,clock_timestamp(),NULL,'running')`, locker.PgConn().PID()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		var n int
		done <- traffic.QueryRow(ctx, `SELECT n FROM app_orders WHERE id=1 FOR UPDATE`).Scan(&n)
	}()
	joined := false
	defer func() {
		_ = lock.Rollback(ctx)
		if !joined {
			<-done
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	var blocked bool
	for time.Now().Before(deadline) {
		if err := admin.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1))>0`, traffic.PgConn().PID()).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("real customer query did not block")
	}
	e := ManagedCronEnrollment{Target: ProjectCronTarget{JobID: 17, DatabaseOID: oid}, Window: ProjectCronWindowPlan{EveryMinutes: 5}}
	read := func() ManagedCronObservation {
		t.Helper()
		o := ManagedCronObservation{At: time.Unix(1, 0)}
		if err := pgx.BeginTxFunc(ctx, admin, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error { return readManagedCronTraffic(ctx, tx, e, &o) }); err != nil {
			t.Fatal(err)
		}
		return o
	}
	observed := read()
	if time.Since(observed.At) > 2*time.Second {
		t.Fatal("counter snapshot kept a stale preflight timestamp")
	}
	if len(observed.Blocks) != 1 || observed.Blocks[0].Query != qid || observed.Blocks[0].RunID != 101 || observed.Blocks[0].PID != int32(locker.PgConn().PID()) {
		t.Fatalf("native job/query attribution missing: %+v", observed.Blocks)
	}
	var calls int64
	for _, c := range observed.Counters {
		if c.Counter.Query == qid {
			calls = c.Counter.Calls
		}
	}
	if calls != 4 {
		t.Fatalf("observer replayed a query or counted an unfinished call: %d", calls)
	}
	// Ended history cannot attribute a still-blocking process to that old run.
	if _, err := admin.Exec(ctx, `UPDATE cron.job_run_details SET end_time=clock_timestamp(),status='succeeded'`); err != nil {
		t.Fatal(err)
	}
	if got := read(); len(got.Blocks) != 0 {
		t.Fatal("completed history attributed live blocking")
	}
	if err := lock.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	result := <-done
	joined = true
	if err := result; err != nil {
		t.Fatal(err)
	}
}
