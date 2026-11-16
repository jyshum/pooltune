package hostedquery

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type ManagedCronCounter struct {
	Counter Counter `json:"counter"`
	Kind    string  `json:"kind"`
}
type ManagedCronObservation struct {
	At         time.Time            `json:"at"`
	Generation CronGeneration       `json:"generation"`
	Counters   []ManagedCronCounter `json:"counters"`
	Runs       []CronRun            `json:"runs"`
	Blocks     []CronBlock          `json:"blocks"`
}

// ObserveManagedCron discovers blocked query identities and completed-work
// counters from the actual database. It never executes application statements.
// Install/permission epoch verification belongs to the owning service.
func ObserveManagedCron(ctx context.Context, db *pgx.Conn, enrolled ManagedCronEnrollment) (out ManagedCronObservation, err error) {
	history, err := ReadManagedCronHistory(ctx, db, enrolled)
	if err != nil {
		return out, err
	}
	if !history.ProviderReady || !history.SchedulerVisible {
		return out, errors.New("native scheduling observation prerequisites unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err = pgx.BeginTxFunc(ctx, db, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL search_path=pg_catalog;SET LOCAL statement_timeout='750ms';SET LOCAL lock_timeout='100ms'`); err != nil {
			return err
		}
		var err error
		out.At, out.Generation, err = readCronClock(ctx, tx)
		if err != nil {
			return err
		}
		if err := readManagedCronTraffic(ctx, tx, enrolled, &out); err != nil {
			return err
		}
		_, last, err := readCronClock(ctx, tx)
		if err != nil || !out.Generation.same(last) {
			return errors.Join(err, errors.New("scheduler generation changed during observation"))
		}
		return nil
	})
	return out, err
}

func readManagedCronTraffic(ctx context.Context, tx pgx.Tx, enrolled ManagedCronEnrollment, out *ManagedCronObservation) error {
	var schema string
	if err := tx.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pg_stat_statements'`).Scan(&schema); err != nil {
		return err
	}
	// PostgreSQL statement_timestamp binds the window to the counters themselves.
	// Earlier provider/clock reads may have taken multiple network round trips.
	rows, err := tx.Query(ctx, `SELECT statement_timestamp(),s.dbid,s.userid,s.queryid,pg_postmaster_start_time(),i.stats_reset,s.stats_since,s.calls,s.shared_blks_hit+s.shared_blks_read,s.total_exec_time,
 CASE WHEN ltrim(s.query)~*'^SELECT[[:space:]]' THEN 'read' ELSE 'write' END
 FROM `+qualified(schema, "pg_stat_statements")+` s CROSS JOIN `+qualified(schema, "pg_stat_statements_info")+` i
 WHERE s.dbid=$1 AND s.toplevel AND s.calls>0 AND ltrim(s.query)~*'^(SELECT|INSERT|UPDATE|DELETE)[[:space:]]'
 AND s.query NOT ILIKE '%pg_catalog%' AND s.query NOT ILIKE '%pg_stat_%' AND s.query NOT ILIKE '%cron.job%'
 ORDER BY s.calls DESC,s.userid,s.queryid LIMIT 64`, enrolled.Target.DatabaseOID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c ManagedCronCounter
		if err := rows.Scan(&out.At, &c.Counter.Database, &c.Counter.Role, &c.Counter.Query, &c.Counter.Boot, &c.Counter.Reset, &c.Counter.Entry, &c.Counter.Calls, &c.Counter.Blocks, &c.Counter.ExecutionMS, &c.Kind); err != nil {
			rows.Close()
			return err
		}
		out.Counters = append(out.Counters, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT clock_timestamp(),r.runid,r.jobid,r.job_pid,runner.backend_start,waiter.pid,waiter.backend_start,waiter.query_id,waiter.usesysid
 FROM cron.job_run_details r JOIN pg_stat_activity runner ON runner.pid=r.job_pid
 JOIN pg_stat_activity waiter ON runner.pid=ANY(pg_blocking_pids(waiter.pid))
 WHERE r.jobid=$1 AND r.start_time>=clock_timestamp()-interval '20 seconds' AND r.end_time IS NULL
 AND runner.datid=$2 AND waiter.datid=$2 AND waiter.query_id IS NOT NULL AND waiter.query_id<>0
 AND runner.backend_start<=r.start_time AND waiter.pid<>runner.pid
 ORDER BY waiter.pid LIMIT 16`, enrolled.Target.JobID, enrolled.Target.DatabaseOID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var b CronBlock
		if err := rows.Scan(&b.At, &b.RunID, &b.JobID, &b.PID, &b.BackendStart, &b.WaitingPID, &b.WaitingStart, &b.Query, &b.WaitingRole); err != nil {
			rows.Close()
			return err
		}
		out.Blocks = append(out.Blocks, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	out.Runs, err = projectCronRuns(ctx, tx, enrolled.Target.JobID, out.At.Add(-time.Duration(enrolled.Window.EveryMinutes)*time.Minute), out.At)
	return err
}
