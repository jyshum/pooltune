package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const maxWorkerQueries = 20

type queryKey struct {
	Database uint32 `json:"database_oid"`
	Role     uint32 `json:"role_oid"`
	Query    int64  `json:"query_id,string"`
}

func (c Counter) key() queryKey { return queryKey{c.Database, c.Role, c.Query} }

type QueryObjective struct {
	Database uint32  `json:"database_oid"`
	Role     uint32  `json:"role_oid"`
	Query    int64   `json:"query_id,string"`
	MeanMS   float64 `json:"mean_execution_ms"`
}

func (q QueryObjective) key() queryKey { return queryKey{q.Database, q.Role, q.Query} }

type Enrollment struct {
	Project string           `json:"project"`
	Queries []QueryObjective `json:"queries"`
}

func (e Enrollment) validate(policy SamplingPolicy) error {
	if len(e.Project) == 0 || len(e.Project) > 128 || len(e.Queries) == 0 || len(e.Queries) > maxWorkerQueries {
		return errors.New("worker needs a project and 1 to 20 enrolled queries")
	}
	if policy.Interval < 100*time.Millisecond || policy.Window < policy.Interval ||
		policy.Window > time.Hour || policy.Window%policy.Interval != 0 {
		return errors.New("window must be a multiple of the sampling interval, from 100ms through 1h")
	}
	seen := make(map[queryKey]bool, len(e.Queries))
	for _, query := range e.Queries {
		key := query.key()
		if key.Database == 0 || key.Database != e.Queries[0].Database || key.Role == 0 || key.Query == 0 ||
			!nonnegativeFinite(query.MeanMS) || query.MeanMS == 0 || seen[key] {
			return errors.New("worker needs distinct query identities in one database and positive mean latency objectives")
		}
		seen[key] = true
	}
	return nil
}

type QueryWindow struct {
	queryKey
	WindowResult
}

type InvestigationJob struct {
	ID int64 `json:"id"`
	queryKey
}

type PollResult struct {
	TargetMS float64            `json:"target_ms,omitempty"`
	TotalMS  float64            `json:"total_ms"`
	Skipped  bool               `json:"skipped,omitempty"`
	Windows  []QueryWindow      `json:"windows,omitempty"`
	Jobs     []InvestigationJob `json:"investigations,omitempty"`
}

type savedPoll struct {
	Policy SamplingPolicy
	States []savedQuery
}

type savedQuery struct {
	Objective QueryObjective
	Window    windowState
}

// PrepareWorkerState initializes the queue and sampling history on Pooltune's own database.
func PrepareWorkerState(ctx context.Context, state *pgx.Conn) error {
	return pgx.BeginFunc(ctx, state, func(tx pgx.Tx) error {
		// Serialize first-run schema creation when several workers start together.
		_, err := tx.Exec(ctx, `
			SELECT pg_advisory_xact_lock(761994345003);
			CREATE SCHEMA IF NOT EXISTS pooltune_worker;
			CREATE TABLE IF NOT EXISTS pooltune_worker.projects (
				project_id text PRIMARY KEY,
				last_poll timestamptz,
				evidence jsonb NOT NULL DEFAULT '{}'
			);
			CREATE TABLE IF NOT EXISTS pooltune_worker.investigations (
				id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
				project_id text NOT NULL,
				database_oid oid NOT NULL,
				role_oid oid NOT NULL,
				query_id bigint NOT NULL,
				created_at timestamptz NOT NULL DEFAULT now(),
				finished_at timestamptz,
				evidence jsonb NOT NULL
			);
			ALTER TABLE pooltune_worker.investigations
				ADD COLUMN IF NOT EXISTS attempts integer NOT NULL DEFAULT 0,
				ADD COLUMN IF NOT EXISTS lease_until timestamptz,
				ADD COLUMN IF NOT EXISTS result jsonb;
			CREATE UNIQUE INDEX IF NOT EXISTS one_open_investigation
				ON pooltune_worker.investigations (project_id, database_oid, role_oid, query_id)
				WHERE finished_at IS NULL;
		`)
		return err
	})
}

type collectCounters func(context.Context) (map[queryKey]Counter, error)

func recordPoll(ctx context.Context, state *pgx.Conn, enrollment Enrollment, policy SamplingPolicy, at time.Time, collect collectCounters) (PollResult, error) {
	var result PollResult
	err := pgx.BeginFunc(ctx, state, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO pooltune_worker.projects (project_id) VALUES ($1) ON CONFLICT DO NOTHING", enrollment.Project)
		if err != nil {
			return err
		}
		var last *time.Time
		var raw []byte
		err = tx.QueryRow(ctx, "SELECT last_poll, evidence FROM pooltune_worker.projects WHERE project_id=$1 FOR UPDATE", enrollment.Project).Scan(&last, &raw)
		if err != nil {
			return err
		}
		// A second worker cannot spend another target round trip for the same interval.
		if last != nil && at.Sub(*last) < 3*policy.Interval/4 {
			result.Skipped = true
			return nil
		}
		var previous savedPoll
		if err := json.Unmarshal(raw, &previous); err != nil {
			return fmt.Errorf("decode previous worker poll: %w", err)
		}

		started := time.Now()
		samples, err := collect(ctx)
		result.TargetMS = elapsedMS(started)
		if err != nil {
			return err
		}

		prior := make(map[QueryObjective]windowState, len(previous.States))
		if previous.Policy == policy {
			for _, state := range previous.States {
				prior[state.Objective] = state.Window
			}
		}
		next := savedPoll{Policy: policy, States: make([]savedQuery, 0, len(enrollment.Queries))}
		for _, objective := range enrollment.Queries {
			history := prior[objective]
			key := objective.key()
			var sample *Counter
			if counter, found := samples[key]; found {
				sample = &counter
			}
			window := history.advance(at, sample, policy, objective.MeanMS)
			next.States = append(next.States, savedQuery{Objective: objective, Window: history})
			if window == nil {
				continue
			}
			result.Windows = append(result.Windows, QueryWindow{key, *window})
			if window.Decision == Investigate {
				job, err := enqueueInvestigation(ctx, tx, enrollment.Project, objective, history, *window)
				if err != nil {
					return err
				}
				if job != nil {
					result.Jobs = append(result.Jobs, *job)
				}
			}
		}
		raw, err = json.Marshal(next)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE pooltune_worker.projects SET last_poll=$2, evidence=$3::jsonb WHERE project_id=$1", enrollment.Project, at, string(raw))
		return err
	})
	if err != nil {
		return PollResult{}, err
	}
	return result, nil
}

func enqueueInvestigation(ctx context.Context, tx pgx.Tx, project string, objective QueryObjective, history windowState, latest WindowResult) (*InvestigationJob, error) {
	means := make([]float64, 0, len(history.Recent))
	for _, window := range history.Recent {
		means = append(means, window.observation(*history.Last).MeanMS)
	}
	evidence, err := json.Marshal(struct {
		Generation  [3]time.Time `json:"counter_generation"`
		ObjectiveMS float64      `json:"objective_ms"`
		RecentMS    []float64    `json:"recent_window_mean_ms"`
		Latest      WindowResult `json:"latest_window"`
	}{[3]time.Time{history.Last.Boot.UTC(), history.Last.Reset.UTC(), history.Last.Entry.UTC()}, objective.MeanMS, means, latest})
	if err != nil {
		return nil, err
	}
	job := InvestigationJob{queryKey: objective.key()}
	err = tx.QueryRow(ctx, `
		INSERT INTO pooltune_worker.investigations (project_id,database_oid,role_oid,query_id,evidence)
		SELECT $1,$2,$3,$4,$5::jsonb
		WHERE NOT EXISTS (
			SELECT FROM pooltune_worker.investigations
			WHERE project_id=$1 AND database_oid=$2 AND role_oid=$3 AND query_id=$4
			  AND finished_at > statement_timestamp()-CASE
			    WHEN result->>'outcome' IN ('propose','no_change') THEN interval '60 minutes'
			    ELSE interval '5 minutes'
			  END
			  AND evidence->'objective_ms'=($5::jsonb)->'objective_ms'
			  AND evidence->'counter_generation'=($5::jsonb)->'counter_generation'
		)
		ON CONFLICT (project_id,database_oid,role_oid,query_id) WHERE finished_at IS NULL
		DO NOTHING RETURNING id
	`, project, objective.Database, objective.Role, objective.Query, string(evidence)).Scan(&job.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &job, err
}
