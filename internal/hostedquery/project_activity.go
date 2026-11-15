package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

const (
	projectActivityRetention   = 8 * 24 * time.Hour
	projectActivityPruneEvery  = 5 * time.Minute
	projectActivityPollLimit   = 12_000
	projectActivityWindowLimit = 64_000
	projectActivityJobLimit    = 200
)

const projectActivitySchema = `
	CREATE TABLE IF NOT EXISTS pooltune_worker.project_polls (
		project_id text NOT NULL REFERENCES pooltune_worker.projects(project_id) ON DELETE CASCADE,
		poll_at timestamptz NOT NULL,
		target_ms double precision NOT NULL,
		PRIMARY KEY (project_id, poll_at)
	);
	CREATE TABLE IF NOT EXISTS pooltune_worker.project_windows (
		project_id text NOT NULL REFERENCES pooltune_worker.projects(project_id) ON DELETE CASCADE,
		database_oid oid NOT NULL,
		role_oid oid NOT NULL,
		query_id bigint NOT NULL,
		objective_ms double precision NOT NULL,
		window_start timestamptz NOT NULL,
		window_end timestamptz NOT NULL,
		calls bigint,
		shared_blocks bigint,
		mean_ms double precision,
		decision text NOT NULL,
		PRIMARY KEY (project_id, window_end, database_oid, role_oid, query_id)
	);
	CREATE INDEX IF NOT EXISTS project_investigations_by_time
		ON pooltune_worker.investigations (project_id, created_at DESC, id DESC);
`

// ProjectActivity contains only bounded, displayable summaries for one project.
type ProjectActivity struct {
	LastPoll *time.Time      `json:"last_poll"`
	Polls    []ProjectPoll   `json:"polls"`
	Windows  []ProjectWindow `json:"windows"`
	Jobs     []ProjectJob    `json:"jobs"`
}

type ProjectPoll struct {
	At       time.Time `json:"at"`
	TargetMS float64   `json:"target_ms"`
}

// ProjectWindow keeps absent counter data separate from measured zero. A real
// window with zero executions has a call count and no mean execution time.
type ProjectWindow struct {
	Database     uint32    `json:"database_oid"`
	Role         uint32    `json:"role_oid"`
	Query        int64     `json:"query_id,string"`
	ObjectiveMS  float64   `json:"objective_ms"`
	Start        time.Time `json:"window_start"`
	End          time.Time `json:"window_end"`
	Calls        *int64    `json:"calls"`
	SharedBlocks *int64    `json:"shared_blocks"`
	MeanMS       *float64  `json:"mean_ms"`
	Decision     Decision  `json:"decision"`
}

type ProjectJob struct {
	ID           int64      `json:"id"`
	Database     uint32     `json:"database_oid"`
	Role         uint32     `json:"role_oid"`
	Query        int64      `json:"query_id,string"`
	CreatedAt    time.Time  `json:"created_at"`
	ClaimedAt    *time.Time `json:"claimed_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	Attempts     int        `json:"attempts"`
	Outcome      string     `json:"outcome"`
	Reason       string     `json:"reason"`
	PlanNode     string     `json:"plan_node"`
	PlanSource   string     `json:"plan_source"`
	ElapsedMS    *float64   `json:"elapsed_ms"`
	TargetPlanMS *float64   `json:"target_plan_ms"`
	GraphMS      *float64   `json:"graph_ms"`
	ModelMS      *float64   `json:"model_ms"`
}

func projectActivityWindow(objective QueryObjective, window WindowResult) ProjectWindow {
	result := ProjectWindow{
		Database: objective.Database, Role: objective.Role, Query: objective.Query,
		ObjectiveMS: objective.MeanMS, Start: window.Start, End: window.End, Decision: window.Decision,
	}
	if window.measurement != nil {
		result.Calls, result.SharedBlocks = &window.measurement.Calls, &window.measurement.Blocks
		if *result.Calls != 0 {
			result.MeanMS = &window.MeanMS
		}
	}
	return result
}

func recordProjectActivity(ctx context.Context, tx pgx.Tx, projectID string, at time.Time, last *time.Time, targetMS float64, windows []ProjectWindow) error {
	// jsonb_to_recordset expects an array even when no window has finalized.
	if windows == nil {
		windows = []ProjectWindow{}
	}
	payload, err := json.Marshal(windows)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		WITH poll AS (
			INSERT INTO pooltune_worker.project_polls (project_id, poll_at, target_ms)
			VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING
			RETURNING project_id
		)
		INSERT INTO pooltune_worker.project_windows
			(project_id, database_oid, role_oid, query_id, objective_ms, window_start, window_end, calls, shared_blocks, mean_ms, decision)
		SELECT poll.project_id, sample.*
		FROM poll CROSS JOIN jsonb_to_recordset($4::jsonb) AS sample(
			database_oid oid, role_oid oid, query_id bigint, objective_ms double precision,
			window_start timestamptz, window_end timestamptz,
			calls bigint, shared_blocks bigint, mean_ms double precision, decision text
		)
		ON CONFLICT DO NOTHING
	`, projectID, at, targetMS, string(payload))
	if err != nil {
		return err
	}

	// Both tables use a project/time index for range pruning. A separate row
	// limit keeps accelerated sampling bounded without shortening normal weeks.
	if last != nil && !at.Truncate(projectActivityPruneEvery).After(last.Truncate(projectActivityPruneEvery)) {
		return nil
	}
	_, err = tx.Exec(ctx, `
		WITH expired_polls AS (
			DELETE FROM pooltune_worker.project_polls
			WHERE project_id=$1 AND poll_at < GREATEST($2, (
				SELECT poll_at FROM pooltune_worker.project_polls WHERE project_id=$1
				ORDER BY poll_at DESC OFFSET $3 LIMIT 1
			))
		)
		DELETE FROM pooltune_worker.project_windows
		WHERE project_id=$1 AND window_end < GREATEST($2, (
			SELECT window_end FROM pooltune_worker.project_windows WHERE project_id=$1
			ORDER BY window_end DESC OFFSET $4 LIMIT 1
		))
	`, projectID, at.Add(-projectActivityRetention), projectActivityPollLimit-1, projectActivityWindowLimit-1)
	return err
}

// ReadProjectActivity returns the latest successful poll and recent project
// activity from one state snapshot. The caller authorizes the project first.
func ReadProjectActivity(ctx context.Context, db interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}, projectID string, since time.Time) (ProjectActivity, error) {
	activity := ProjectActivity{
		Polls: []ProjectPoll{}, Windows: []ProjectWindow{}, Jobs: []ProjectJob{},
	}
	err := pgx.BeginTxFunc(ctx, db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var now time.Time
		err := tx.QueryRow(ctx, `
			SELECT GREATEST(p.last_poll, (
				SELECT poll_at FROM pooltune_worker.project_polls
				WHERE project_id=p.project_id ORDER BY poll_at DESC LIMIT 1
			)), transaction_timestamp()
			FROM pooltune_worker.projects p WHERE p.project_id=$1 AND p.monitor_epoch IS NOT NULL
		`, projectID).Scan(&activity.LastPoll, &now)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}

		cutoff := now.Add(-projectActivityRetention)
		if since.After(cutoff) {
			cutoff = since
		}
		activity.Polls, err = readProjectPolls(ctx, tx, projectID, cutoff)
		if err != nil {
			return err
		}
		activity.Windows, err = readProjectWindows(ctx, tx, projectID, cutoff)
		if err != nil {
			return err
		}
		activity.Jobs, err = readProjectJobs(ctx, tx, projectID, cutoff)
		return err
	})
	if err != nil {
		return ProjectActivity{}, err
	}
	return activity, nil
}

func readProjectPolls(ctx context.Context, tx pgx.Tx, projectID string, cutoff time.Time) ([]ProjectPoll, error) {
	rows, err := tx.Query(ctx, `
		SELECT poll_at, target_ms FROM (
			SELECT poll_at, target_ms FROM pooltune_worker.project_polls
			WHERE project_id=$1 AND poll_at >= $2
			ORDER BY poll_at DESC LIMIT $3
		) recent ORDER BY poll_at
	`, projectID, cutoff, projectActivityPollLimit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ProjectPoll, error) {
		var poll ProjectPoll
		err := row.Scan(&poll.At, &poll.TargetMS)
		return poll, err
	})
}

func readProjectWindows(ctx context.Context, tx pgx.Tx, projectID string, cutoff time.Time) ([]ProjectWindow, error) {
	rows, err := tx.Query(ctx, `
		SELECT * FROM (
			SELECT database_oid, role_oid, query_id, objective_ms, window_start, window_end, calls, shared_blocks, mean_ms, decision
			FROM pooltune_worker.project_windows
			WHERE project_id=$1 AND window_end >= $2
			ORDER BY window_end DESC, database_oid DESC, role_oid DESC, query_id DESC LIMIT $3
		) recent ORDER BY window_end, database_oid, role_oid, query_id
	`, projectID, cutoff, projectActivityWindowLimit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ProjectWindow, error) {
		var window ProjectWindow
		err := row.Scan(&window.Database, &window.Role, &window.Query, &window.ObjectiveMS, &window.Start, &window.End,
			&window.Calls, &window.SharedBlocks, &window.MeanMS, &window.Decision)
		return window, err
	})
}

func readProjectJobs(ctx context.Context, tx pgx.Tx, projectID string, cutoff time.Time) ([]ProjectJob, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, database_oid, role_oid, query_id, created_at, claimed_at, finished_at, attempts,
			COALESCE(NULLIF(result->>'outcome', ''), CASE
				WHEN finished_at IS NOT NULL THEN 'finished'
				WHEN lease_until > transaction_timestamp() THEN 'running'
				WHEN not_before > transaction_timestamp() THEN 'scheduled'
				ELSE 'queued'
			END),
			LEFT(COALESCE(NULLIF(result->>'diagnostic_reason', ''), result#>>'{lookup_diagnosis,reason}', ''), 500),
			LEFT(COALESCE(result#>>'{traffic_plan,node}', result#>>'{lookup_plan,node}', result#>>'{estimated_evidence,estimated,node}', ''), 100),
			CASE
				WHEN result#>>'{traffic_plan,node}' IS NOT NULL THEN 'ordinary_traffic'
				WHEN result#>>'{lookup_plan,node}' IS NOT NULL THEN 'measured_lookup'
				WHEN result#>>'{estimated_evidence,estimated,node}' IS NOT NULL THEN 'estimate'
				ELSE ''
			END,
			CASE WHEN jsonb_typeof(result->'elapsed_ms')='number' THEN (result->>'elapsed_ms')::double precision END,
			CASE WHEN jsonb_typeof(result->'target_plan_ms')='number' THEN (result->>'target_plan_ms')::double precision END,
			CASE WHEN jsonb_typeof(result->'graph_ms')='number' THEN (result->>'graph_ms')::double precision END,
			CASE WHEN jsonb_typeof(result#>'{lookup_diagnosis,model_ms}')='number' THEN (result#>>'{lookup_diagnosis,model_ms}')::double precision END
		FROM pooltune_worker.investigations
		WHERE project_id=$1 AND created_at >= $2
		ORDER BY created_at DESC, id DESC LIMIT $3
	`, projectID, cutoff, projectActivityJobLimit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ProjectJob, error) {
		var job ProjectJob
		err := row.Scan(&job.ID, &job.Database, &job.Role, &job.Query, &job.CreatedAt, &job.ClaimedAt, &job.FinishedAt, &job.Attempts,
			&job.Outcome, &job.Reason, &job.PlanNode, &job.PlanSource, &job.ElapsedMS, &job.TargetPlanMS, &job.GraphMS, &job.ModelMS)
		job.Reason = activityText(job.Reason)
		job.PlanNode = activityText(job.PlanNode)
		return job, err
	})
}

func activityText(value string) string {
	plain := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, value)
	return strings.Join(strings.Fields(plain), " ")
}
