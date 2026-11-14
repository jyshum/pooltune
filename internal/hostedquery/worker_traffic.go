package hostedquery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

const trafficDuration = 7 * time.Second
const trafficCadence = 100 * time.Millisecond

type TrafficReport struct {
	Project       string            `json:"project"`
	Schema        string            `json:"schema"`
	ServerVersion string            `json:"server_version"`
	RowsPerTable  int               `json:"rows_per_table"`
	ObjectiveMS   float64           `json:"objective_ms"`
	PollInterval  time.Duration     `json:"poll_interval_ns"`
	Window        time.Duration     `json:"window_ns"`
	Healthy       TrafficCaseReport `json:"healthy"`
	Candidate     TrafficCaseReport `json:"candidate"`
	Polls         int               `json:"polls"`
	TargetP95MS   float64           `json:"target_snapshot_p95_ms"`
	PollP95MS     float64           `json:"worker_poll_p95_ms"`
	DetectionMS   float64           `json:"first_job_after_traffic_start_ms"`
	Jobs          int               `json:"jobs"`
	Investigation *DispatchResult   `json:"investigation,omitempty"`
}

type TrafficCaseReport struct {
	Calls          int64    `json:"calls"`
	Decision       Decision `json:"decision"`
	DatabaseMeanMS float64  `json:"database_mean_ms"`
	ClientP50MS    float64  `json:"client_p50_ms"`
	ClientP95MS    float64  `json:"client_p95_ms"`
}

// RunWorkerTraffic generates bounded reads on a dedicated session while a separate
// session observes only their statement counters. Fixture objects never commit.
func RunWorkerTraffic(ctx context.Context, traffic, target, state *pgx.Conn, identity string, investigate IndexInvestigator) (report TrafficReport, err error) {
	if traffic == target || traffic == state || target == state || identity == "" {
		return report, errors.New("traffic trial requires separate traffic, target and Pooltune state sessions")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := PrepareWorkerState(ctx, state); err != nil {
		return report, fmt.Errorf("prepare worker state: %w", err)
	}

	tx, err := traffic.Begin(ctx)
	if err != nil {
		return report, err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if rollbackErr := tx.Rollback(cleanup); !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	for _, setting := range []string{
		"SET LOCAL search_path='pg_catalog'",
		"SET LOCAL lock_timeout='250ms'",
		"SET LOCAL statement_timeout='5s'",
		"SET LOCAL idle_in_transaction_session_timeout='8s'",
		"SET LOCAL transaction_timeout='25s'",
		"SET LOCAL max_parallel_workers_per_gather=0",
	} {
		if _, err := tx.Exec(ctx, setting); err != nil {
			return report, err
		}
	}
	var acquired bool
	if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", trialLock).Scan(&acquired); err != nil {
		return report, err
	}
	if !acquired {
		return report, errors.New("another fixture owns the target lock")
	}
	if err := trialCapacity(ctx, traffic); err != nil {
		return report, err
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return report, err
	}
	runID := hex.EncodeToString(nonce[:])
	report.Project, report.Schema = identity+"/traffic/"+runID, "pooltune_traffic_"+runID
	report.ObjectiveMS, report.RowsPerTable = objectiveMS, fixtureRows
	policy := SamplingPolicy{Interval: 500 * time.Millisecond, Window: time.Second}
	report.PollInterval, report.Window = policy.Interval, policy.Window

	var statsSchema string
	if err := tx.QueryRow(ctx, `
		SELECT n.nspname, current_setting('server_version')
		FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
		WHERE e.extname='pg_stat_statements'
	`).Scan(&statsSchema, &report.ServerVersion); err != nil {
		return report, err
	}
	if _, err := tx.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{report.Schema}.Sanitize()); err != nil {
		return report, err
	}
	if err := seedFixtureTables(ctx, tx, report.Schema); err != nil {
		return report, err
	}
	healthyTable := qualified(report.Schema, "healthy")
	if _, err := tx.Exec(ctx, "CREATE INDEX ON "+healthyTable+" (lookup)"); err != nil {
		return report, err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout='750ms'"); err != nil {
		return report, err
	}
	tables := []string{healthyTable, qualified(report.Schema, "candidate")}
	queries := make([]*labCase, 0, len(tables))
	enrollment := Enrollment{Project: report.Project}
	before := make([]Counter, 0, len(tables))
	for _, table := range tables {
		query, err := enrollCase(ctx, tx, statsSchema, "SELECT id FROM "+table+" WHERE lookup=$1")
		if err != nil {
			return report, err
		}
		snapshot, err := query.snapshot(ctx, tx)
		if err != nil {
			return report, err
		}
		queries = append(queries, query)
		before = append(before, snapshot)
		enrollment.Queries = append(enrollment.Queries, QueryObjective{
			Database: snapshot.Database, Role: snapshot.Role, Query: snapshot.Query, MeanMS: objectiveMS,
		})
	}
	worker, err := NewWorker(ctx, target, state, enrollment, policy)
	if err != nil {
		return report, err
	}
	if _, err := worker.Poll(ctx); err != nil {
		return report, fmt.Errorf("establish worker baseline: %w", err)
	}
	started := time.Now()
	var stopAfterPoll = errors.New("traffic finished")
	trafficDone := make(chan struct{})
	done := make(chan error, 1)
	var pollTimes, targetTimes []float64
	summary := []*TrafficCaseReport{&report.Healthy, &report.Candidate}
	go func() {
		done <- worker.Run(ctx, func(poll PollResult) error {
			if !poll.Skipped {
				pollTimes = append(pollTimes, poll.TotalMS)
				targetTimes = append(targetTimes, poll.TargetMS)
			}
			for index, query := range queries {
				for _, window := range poll.Windows {
					if window.Query == query.queryID {
						summary[index].Decision = window.Decision
					}
				}
			}
			for range poll.Jobs {
				if report.Jobs == 0 {
					report.DetectionMS = elapsedMS(started)
				}
				report.Jobs++
			}
			select {
			case <-trafficDone:
				return stopAfterPoll
			default:
				return nil
			}
		})
	}()
	samples, trafficErr := driveWorkerTraffic(ctx, tx, queries, started)
	close(trafficDone)
	workerErr := <-done
	if workerErr != nil && !errors.Is(workerErr, stopAfterPoll) {
		return report, fmt.Errorf("observe traffic: %w", workerErr)
	}
	if trafficErr != nil {
		return report, trafficErr
	}
	for index, query := range queries {
		after, err := query.snapshot(ctx, tx)
		if err != nil {
			return report, err
		}
		count := int64(len(samples[index]))
		if count == 0 {
			return report, errors.New("traffic driver did not execute the query")
		}
		observed, err := Measure(before[index], after, count)
		if err != nil {
			return report, err
		}
		summary[index].Calls = count
		summary[index].DatabaseMeanMS = observed.MeanMS
		summary[index].ClientP50MS = percentileMS(samples[index], 50)
		summary[index].ClientP95MS = percentileMS(samples[index], 95)
	}
	if len(pollTimes) == 0 {
		return report, errors.New("worker did not record any traffic polls")
	}
	report.Polls = len(pollTimes)
	report.TargetP95MS = percentileMS(targetTimes, 95)
	report.PollP95MS = percentileMS(pollTimes, 95)
	if investigate != nil {
		if report.Jobs != 1 || report.Healthy.Decision != NoChange || report.Candidate.Decision != Investigate {
			return report, errors.New("expected one job for the slow fixture lookup")
		}
		report.Investigation, err = DispatchInvestigation(ctx, state, report.Project,
			func(attempt context.Context, job InvestigationJob) (IndexEvidence, error) {
				evidence, err := trafficEvidence(attempt, tx, report.Schema, queries[1], before[1], job)
				// End the fixture transaction before waiting on the model.
				return evidence, errors.Join(err, tx.Rollback(attempt))
			}, investigate)
		if err != nil {
			return report, err
		}
		if report.Investigation == nil {
			return report, errors.New("the queued fixture job could not be claimed")
		}
	}
	return report, nil
}

// Send at most one pair each tick. Alternating which executes first avoids a
// consistent ordering advantage. A delayed tick is skipped instead of replayed.
func driveWorkerTraffic(ctx context.Context, tx pgx.Tx, queries []*labCase, started time.Time) ([][]float64, error) {
	samples := make([][]float64, len(queries))
	ticker := time.NewTicker(trafficCadence)
	defer ticker.Stop()
	for i := 0; i < int(trafficDuration/trafficCadence) && time.Since(started) < trafficDuration; i++ {
		input := int32(1 + (i*7919)%fixtureRows)
		for offset := range queries {
			index := (i + offset) % len(queries)
			sent := time.Now()
			if err := queries[index].readID(ctx, tx, input); err != nil {
				return nil, fmt.Errorf("execute synthetic lookup: %w", err)
			}
			samples[index] = append(samples[index], elapsedMS(sent))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
	return samples, nil
}

func percentileMS(values []float64, percent int) float64 {
	slices.Sort(values)
	return values[(len(values)*percent+99)/100-1]
}
