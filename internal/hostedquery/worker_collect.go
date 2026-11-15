package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Worker holds dedicated target and state sessions. Run and Poll must be called serially.
type Worker struct {
	target     *pgx.Conn
	state      *pgx.Conn
	enrollment Enrollment
	policy     SamplingPolicy
	counterSQL string
	roles      []uint32
	queries    []int64
}

func NewWorker(ctx context.Context, target, state *pgx.Conn, enrollment Enrollment, policy SamplingPolicy) (*Worker, error) {
	if err := enrollment.validate(policy); err != nil {
		return nil, err
	}
	var schema, version string
	var database uint32
	err := target.QueryRow(ctx, `
		SELECT n.nspname, e.extversion, db.oid
		FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
		JOIN pg_database db ON db.datname=current_database()
		WHERE e.extname='pg_stat_statements'
	`).Scan(&schema, &version, &database)
	if err != nil {
		return nil, fmt.Errorf("locate statement statistics: %w", err)
	}
	if enrollment.Queries[0].Database != database {
		return nil, errors.New("enrollment belongs to a different database")
	}
	worker := &Worker{target: target, state: state, enrollment: enrollment, policy: policy}
	for _, query := range enrollment.Queries {
		worker.roles = append(worker.roles, query.Role)
		worker.queries = append(worker.queries, query.Query)
	}
	worker.counterSQL = fmt.Sprintf(`
		SELECT s.dbid, s.userid, s.queryid, pg_postmaster_start_time(), i.stats_reset,
		       s.stats_since, s.calls, s.total_exec_time, s.shared_blks_hit+s.shared_blks_read
		FROM %s(false) AS s CROSS JOIN %s AS i
		JOIN unnest($2::oid[], $3::bigint[]) AS enrolled(role_oid,query_id) ON true
		WHERE s.dbid=$1 AND s.toplevel AND s.userid=enrolled.role_oid AND s.queryid=enrolled.query_id
	`, qualified(schema, "pg_stat_statements"), qualified(schema, "pg_stat_statements_info"))
	// Prepare once so unsupported extension versions fail before opening state history.
	if _, err := target.Prepare(ctx, "pooltune_worker_counters", worker.counterSQL); err != nil {
		return nil, fmt.Errorf("prepare statement statistics (extension %s): %w", version, err)
	}
	return worker, nil
}

func (w *Worker) collect(ctx context.Context) (map[queryKey]Counter, error) {
	rows, err := w.target.Query(ctx, "pooltune_worker_counters", w.enrollment.Queries[0].Database, w.roles, w.queries)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counters := make(map[queryKey]Counter, len(w.queries))
	for rows.Next() {
		var c Counter
		err := rows.Scan(&c.Database, &c.Role, &c.Query, &c.Boot, &c.Reset, &c.Entry, &c.Calls, &c.ExecutionMS, &c.Blocks)
		if err != nil {
			return nil, err
		}
		counters[c.key()] = c
	}
	return counters, rows.Err()
}

func (w *Worker) Poll(ctx context.Context) (PollResult, error) {
	return w.poll(ctx, time.Now())
}

func (w *Worker) poll(ctx context.Context, started time.Time) (PollResult, error) {
	timeout := min(2*time.Second, w.policy.Interval)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := recordPoll(ctx, w.state, w.enrollment, w.policy, started.UTC().Truncate(time.Microsecond), w.collect)
	result.TotalMS = elapsedMS(started)
	return result, err
}

func (w *Worker) Run(ctx context.Context, onPoll func(PollResult) error) error {
	return runPolls(ctx, w.policy.Interval, w.poll, onPoll)
}

func runPolls(ctx context.Context, interval time.Duration, poll func(context.Context, time.Time) (PollResult, error), onPoll func(PollResult) error) error {
	for ctx.Err() == nil {
		started := time.Now()
		result, err := poll(ctx, started)
		if err != nil {
			return err
		}
		if err := onPoll(result); err != nil {
			return err
		}
		timer := time.NewTimer(time.Until(started.Add(interval)))
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return ctx.Err()
}
