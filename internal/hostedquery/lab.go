package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	fixtureRows  = 100_000
	objectiveMS  = 1.0
	healthySQL   = "SELECT id FROM pooltune_m1.healthy WHERE id = $1"
	candidateSQL = "SELECT id FROM pooltune_m1.candidate WHERE lookup = $1"
)

var inputIDs = [...]int32{43, 8_015, 36_592, 97_043}

type Report struct {
	ServerVersion string     `json:"server_version"`
	StartedAt     time.Time  `json:"started_at"`
	RowsPerTable  int        `json:"rows_per_table"`
	ObjectiveMS   float64    `json:"mean_execution_objective_ms"`
	Healthy       CaseResult `json:"healthy"`
	Candidate     CaseResult `json:"candidate"`
}

type CaseResult struct {
	Observations []Observation `json:"observations"`
	Decision     Decision      `json:"decision"`
	Plan         PlanEvidence  `json:"measured_plan"`
}

type PlanEvidence struct {
	Node         string `json:"node"`
	ExaminedRows int64  `json:"examined_rows"`
	ReturnedRows int64  `json:"returned_rows"`
	SharedBlocks int64  `json:"shared_blocks"`
}

// RunLab runs a disposable fixture on a dedicated session and rolls back its DDL and data.
func RunLab(ctx context.Context, target *pgx.Conn) (report Report, err error) {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	tx, err := target.Begin(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("begin fixture: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if rollbackErr := tx.Rollback(cleanup); rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("rollback fixture: %w", rollbackErr))
		}
	}()

	for _, setting := range []string{
		"SET LOCAL search_path = pg_catalog",
		"SET LOCAL lock_timeout = '250ms'",
		"SET LOCAL statement_timeout = '5s'",
		"SET LOCAL idle_in_transaction_session_timeout = '8s'",
		"SET LOCAL transaction_timeout = '30s'",
		"SET LOCAL max_parallel_workers_per_gather = 0",
	} {
		if _, err := tx.Exec(ctx, setting); err != nil {
			return Report{}, fmt.Errorf("set fixture limits: %w", err)
		}
	}

	report = Report{StartedAt: time.Now().UTC(), RowsPerTable: fixtureRows, ObjectiveMS: objectiveMS}
	var acquired, exists, standby, lockWait, visibleActivity bool
	var active int
	if err := tx.QueryRow(ctx, `
		SELECT pg_try_advisory_xact_lock(761994345001),
		       to_regnamespace('pooltune_m1') IS NOT NULL, pg_is_in_recovery(),
		       EXISTS (SELECT FROM pg_locks WHERE NOT granted),
		       pg_has_role(current_user, 'pg_read_all_stats', 'USAGE'),
		       (SELECT count(*) FROM pg_stat_activity
		        WHERE backend_type = 'client backend' AND state = 'active' AND pid <> pg_backend_pid()),
		       current_setting('server_version')
	`).Scan(&acquired, &exists, &standby, &lockWait, &visibleActivity, &active, &report.ServerVersion); err != nil {
		return Report{}, fmt.Errorf("check fixture preflight: %w", err)
	}
	if !acquired || standby || lockWait || !visibleActivity || active > 2 {
		return Report{}, errors.New("fixture deferred: another run, standby, lock wait, or unavailable capacity")
	}
	if exists {
		return Report{}, errors.New("fixture schema already exists")
	}
	var statsSchema string
	if err := tx.QueryRow(ctx, `
		SELECT extnamespace::regnamespace::text
		FROM pg_extension WHERE extname = 'pg_stat_statements'
	`).Scan(&statsSchema); err != nil {
		return Report{}, fmt.Errorf("locate statement statistics: %w", err)
	}

	if err := setupFixture(ctx, tx); err != nil {
		return Report{}, err
	}
	healthy, err := enrollCase(ctx, tx, statsSchema, healthySQL)
	if err != nil {
		return Report{}, fmt.Errorf("enroll healthy query: %w", err)
	}
	candidate, err := enrollCase(ctx, tx, statsSchema, candidateSQL)
	if err != nil {
		return Report{}, fmt.Errorf("enroll candidate query: %w", err)
	}
	if healthy.queryID == candidate.queryID {
		return Report{}, errors.New("fixture query identifiers collide")
	}

	for range 5 {
		for _, query := range []*labCase{healthy, candidate} {
			if err := query.measure(ctx, tx); err != nil {
				return Report{}, fmt.Errorf("measure statement batch: %w", err)
			}
		}
	}
	report.Healthy, err = healthy.result(ctx, tx)
	if err != nil {
		return Report{}, fmt.Errorf("inspect healthy query: %w", err)
	}
	report.Candidate, err = candidate.result(ctx, tx)
	if err != nil {
		return Report{}, fmt.Errorf("inspect candidate query: %w", err)
	}
	return report, nil
}

func setupFixture(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, "CREATE SCHEMA pooltune_m1"); err != nil {
		return fmt.Errorf("create fixture schema: %w", err)
	}
	if err := seedFixtureTables(ctx, tx, "pooltune_m1"); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '750ms'")
	return err
}

func seedFixtureTables(ctx context.Context, tx pgx.Tx, schema string) error {
	for _, table := range []string{"healthy", "candidate"} {
		qualified := qualified(schema, table)
		if _, err := tx.Exec(ctx, "CREATE TABLE "+qualified+
			" (id integer PRIMARY KEY, lookup integer NOT NULL, padding text NOT NULL)"); err != nil {
			return fmt.Errorf("create fixture table: %w", err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO "+qualified+
			" SELECT n, n, repeat('p', 64) FROM generate_series(1, $1::integer) AS n", fixtureRows); err != nil {
			return fmt.Errorf("seed fixture table: %w", err)
		}
		if _, err := tx.Exec(ctx, "ANALYZE "+qualified); err != nil {
			return fmt.Errorf("analyze fixture table: %w", err)
		}
	}
	return nil
}

type labCase struct {
	sql          string
	statsSQL     string
	queryID      int64
	observations []Observation
}

func enrollCase(ctx context.Context, tx pgx.Tx, statsSchema, sql string) (*labCase, error) {
	plan, err := readPlan(ctx, tx, "EXPLAIN (FORMAT JSON, VERBOSE) "+sql)
	if err != nil {
		return nil, err
	}
	if plan.QueryID == 0 {
		return nil, errors.New("missing query identifier")
	}
	stats := pgx.Identifier{statsSchema, "pg_stat_statements"}.Sanitize()
	info := pgx.Identifier{statsSchema, "pg_stat_statements_info"}.Sanitize()
	query := &labCase{sql: sql, queryID: plan.QueryID, statsSQL: fmt.Sprintf(`
		SELECT s.dbid, s.userid, s.queryid, pg_postmaster_start_time(), i.stats_reset,
		       s.stats_since, s.calls, s.total_exec_time, s.shared_blks_hit + s.shared_blks_read
		FROM %s(false) AS s CROSS JOIN %s AS i
		WHERE s.toplevel AND s.queryid = $1
		  AND s.dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
		  AND s.userid = current_user::regrole::oid
	`, stats, info)}
	// Create the counter entry and warm the connection before taking a baseline.
	if err := query.readID(ctx, tx, inputIDs[0]); err != nil {
		return nil, err
	}
	return query, nil
}

func (q *labCase) readID(ctx context.Context, tx pgx.Tx, input int32) error {
	var id int32
	if err := tx.QueryRow(ctx, q.sql, input).Scan(&id); err != nil {
		return err
	}
	if id != input {
		return errors.New("fixture result differs from expected ID")
	}
	return nil
}

func (q *labCase) snapshot(ctx context.Context, tx pgx.Tx) (c Counter, err error) {
	err = tx.QueryRow(ctx, q.statsSQL, q.queryID).Scan(
		&c.Database, &c.Role, &c.Query, &c.Boot, &c.Reset, &c.Entry,
		&c.Calls, &c.ExecutionMS, &c.Blocks,
	)
	return c, err
}

func (q *labCase) measure(ctx context.Context, tx pgx.Tx) error {
	before, err := q.snapshot(ctx, tx)
	if err != nil {
		return fmt.Errorf("read start counters: %w", err)
	}
	for _, input := range inputIDs {
		if err := q.readID(ctx, tx, input); err != nil {
			return err
		}
	}
	after, err := q.snapshot(ctx, tx)
	if err != nil {
		return fmt.Errorf("read end counters: %w", err)
	}
	observed, err := Measure(before, after, int64(len(inputIDs)))
	if err != nil {
		return err
	}
	q.observations = append(q.observations, observed)
	return nil
}

func (q *labCase) result(ctx context.Context, tx pgx.Tx) (CaseResult, error) {
	decision, err := DetectObjective(q.observations, objectiveMS)
	if err != nil {
		return CaseResult{}, err
	}
	plan, err := readPlan(ctx, tx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, VERBOSE, TIMING OFF) "+q.sql)
	if err != nil {
		return CaseResult{}, err
	}
	node := plan.Node
	if plan.QueryID != q.queryID || node.Loops != 1 || node.Rows != 1 {
		return CaseResult{}, errors.New("measured plan does not match the enrolled single-row query")
	}
	return CaseResult{q.observations, decision, PlanEvidence{
		Node: node.Type, ExaminedRows: node.Rows + node.Filtered, ReturnedRows: node.Rows,
		SharedBlocks: node.Hit + node.Read,
	}}, nil
}

type explain struct {
	ExecutionMS float64 `json:"Execution Time"`
	QueryID     int64   `json:"Query Identifier"`
	Node        struct {
		Type     string `json:"Node Type"`
		Rows     int64  `json:"Actual Rows"`
		Loops    int64  `json:"Actual Loops"`
		Filtered int64  `json:"Rows Removed by Filter"`
		Hit      int64  `json:"Shared Hit Blocks"`
		Read     int64  `json:"Shared Read Blocks"`
	} `json:"Plan"`
}

func readPlan(ctx context.Context, tx pgx.Tx, sql string) (explain, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, sql, inputIDs[0]).Scan(&raw); err != nil {
		return explain{}, err
	}
	var plans []explain
	if err := json.Unmarshal(raw, &plans); err != nil {
		return explain{}, err
	}
	if len(plans) != 1 || plans[0].Node.Type == "" {
		return explain{}, errors.New("expected one plan")
	}
	return plans[0], nil
}
