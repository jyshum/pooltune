package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	ID           int64          `json:"id"`
	Database     uint32         `json:"database_oid"`
	Role         uint32         `json:"role_oid"`
	Query        int64          `json:"query_id,string"`
	CreatedAt    time.Time      `json:"created_at"`
	ClaimedAt    *time.Time     `json:"claimed_at"`
	FinishedAt   *time.Time     `json:"finished_at"`
	Attempts     int            `json:"attempts"`
	Outcome      string         `json:"outcome"`
	Reason       string         `json:"reason"`
	PlanNode     string         `json:"plan_node"`
	PlanSource   string         `json:"plan_source"`
	ElapsedMS    *float64       `json:"elapsed_ms"`
	TargetPlanMS *float64       `json:"target_plan_ms"`
	GraphMS      *float64       `json:"graph_ms"`
	ModelMS      *float64       `json:"model_ms"`
	Repair       *ProjectRepair `json:"repair,omitempty"`
}

type ProjectRepair struct {
	AutomaticCleaned      bool                  `json:"automatic_cleaned,omitempty"`
	StatisticsRegenerated bool                  `json:"statistics_regenerated,omitempty"`
	Restored              bool                  `json:"restored,omitempty"`
	Kind                  string                `json:"kind"`
	Action                string                `json:"action"`
	RecoveryStatus        string                `json:"recovery_status,omitempty"`
	Metrics               []ProjectRepairMetric `json:"metrics,omitempty"`
	AnalysisMS            float64               `json:"analysis_wall_ms,omitempty"`
}

type ProjectRepairMetric struct {
	Label  string  `json:"label"`
	Unit   string  `json:"unit"`
	Before float64 `json:"before"`
	After  float64 `json:"after"`
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
			CASE WHEN owned.pending_operation IS NOT NULL THEN 'repair_interrupted' ELSE COALESCE(NULLIF(result->>'outcome', ''), CASE
				WHEN finished_at IS NOT NULL THEN 'finished'
				WHEN lease_until > transaction_timestamp() THEN 'running'
				WHEN not_before > transaction_timestamp() THEN 'scheduled'
				ELSE 'queued'
			END) END,
			LEFT(COALESCE(CASE WHEN owned.pending_operation IS NOT NULL THEN 'A recorded repair needs reconciliation' END,
				NULLIF(result->>'diagnostic_reason', ''), NULLIF(result#>>'{lookup_diagnosis,reason}', ''),
				result#>>'{proposal,reason}', ''), 500),
			LEFT(COALESCE(result#>>'{traffic_plan,node}', result#>>'{lookup_plan,node}', result#>>'{estimated_evidence,estimated,node}',
				NULLIF(concat_ws(' → ',result#>>'{repair,before,plan}',result#>>'{repair,after,plan}'),''), ''), 100),
			CASE
				WHEN result#>>'{traffic_plan,node}' IS NOT NULL THEN 'ordinary_traffic'
				WHEN result#>>'{lookup_plan,node}' IS NOT NULL THEN 'measured_lookup'
				WHEN result#>>'{estimated_evidence,estimated,node}' IS NOT NULL THEN 'estimate'
				WHEN result#>>'{repair,measurement_scope}' = 'owned_single_input' THEN 'owned_sample'
                WHEN result#>>'{repair,measurement_scope}' = 'managed_sampled_input' THEN 'managed_sample'
				ELSE ''
			END,
			CASE WHEN jsonb_typeof(result->'elapsed_ms')='number' THEN (result->>'elapsed_ms')::double precision END,
			CASE WHEN jsonb_typeof(result->'target_plan_ms')='number' THEN (result->>'target_plan_ms')::double precision END,
			CASE WHEN jsonb_typeof(result->'graph_ms')='number' THEN (result->>'graph_ms')::double precision END,
			CASE
				WHEN jsonb_typeof(result#>'{lookup_diagnosis,model_ms}')='number' THEN (result#>>'{lookup_diagnosis,model_ms}')::double precision
				WHEN jsonb_typeof(result#>'{proposal,model_call,duration_ms}')='number' THEN (result#>>'{proposal,model_call,duration_ms}')::double precision
			END,
			CASE WHEN owned.pending_operation IS NOT NULL THEN jsonb_build_object(owned.pending_operation,'{}'::jsonb)
			WHEN finished_at IS NOT NULL THEN jsonb_strip_nulls(jsonb_build_object(
				'index', COALESCE(result->'index_recovery',result->'repair'),
				'analyze', result->'analyze', 'statistics', result->'statistics',
				'vacuum', result->'vacuum', 'memory', result->'memory', 'cron', result->'cron',
                'managed', result->'managed_maintenance', 'managed_statistics', result->'managed_statistics', 'managed_cron', result->'managed_cron', 'managed_memory', result->'managed_memory', 'managed_cleanup', result->'managed_cleanup'
			)) END
		FROM pooltune_worker.investigations j
		CROSS JOIN LATERAL (
			SELECT CASE WHEN (j.finished_at IS NOT NULL OR j.lease_until<=transaction_timestamp())
			  AND NOT (COALESCE(j.result->>'outcome','')='awaiting_verification' AND EXISTS (
				SELECT FROM pooltune_worker.investigations child WHERE child.project_id=j.project_id AND child.parent_id=j.id
			  )) THEN COALESCE(
				(SELECT 'index' FROM pooltune_worker.owned_index_actions a
				  WHERE a.project_id=j.project_id AND a.job_id=j.id AND a.outcome='pending'),
				(SELECT a.operation FROM pooltune_worker.owned_project_actions a
				  WHERE a.project_id=j.project_id AND a.job_id=j.id AND a.outcome='pending')
			) END AS pending_operation
		) owned
		WHERE project_id=$1 AND created_at >= $2
		ORDER BY created_at DESC, id DESC LIMIT $3
	`, projectID, cutoff, projectActivityJobLimit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ProjectJob, error) {
		var job ProjectJob
		var reports []byte
		err := row.Scan(&job.ID, &job.Database, &job.Role, &job.Query, &job.CreatedAt, &job.ClaimedAt, &job.FinishedAt, &job.Attempts,
			&job.Outcome, &job.Reason, &job.PlanNode, &job.PlanSource, &job.ElapsedMS, &job.TargetPlanMS, &job.GraphMS, &job.ModelMS, &reports)
		if err == nil {
			readProjectRepair(&job, reports)
		}
		job.Reason = activityText(job.Reason)
		job.PlanNode = activityText(job.PlanNode)
		return job, err
	})
}

// Journaled results carry explicit action witnesses. Reports can also describe
// declines, which contribute a reason but no repair, and interrupted actions.
func readProjectRepair(job *ProjectJob, encoded []byte) {
	var reports map[string]json.RawMessage
	if json.Unmarshal(encoded, &reports) != nil || len(reports) != 1 {
		return
	}
	for kind, raw := range reports {
		if kind == "managed_statistics" {
			readManagedStatisticsActivity(job, raw)
			return
		}
		if kind == "managed_cron" {
			readManagedCronActivity(job, raw)
			return
		}
		if kind == "managed_memory" {
			readManagedMemoryActivity(job, raw)
			return
		}
		if kind == "managed_cleanup" {
			readManagedCleanupActivity(job, raw)
			return
		}
		if kind == "managed" {
			readManagedMaintenanceActivity(job, raw)
			return
		}
		var report struct {
			Reason      string          `json:"reason"`
			Status      string          `json:"status"`
			Measurement string          `json:"measurement_scope"`
			Before      json.RawMessage `json:"before"`
			After       json.RawMessage `json:"after"`
			IndexOK     bool            `json:"index_valid"`
			Reverted    bool            `json:"reverted"`
			Analyze     float64         `json:"analyze_wall_ms"`
			Vacuum      float64         `json:"vacuum_wall_ms"`
			Memory      float64         `json:"memory_wall_ms"`
			Restored    bool            `json:"restored"`
			Commit      time.Time       `json:"commit_at"`

			ColumnTarget *StatisticsColumnTargetReport `json:"column_target"`
			Cleanup      *CleanupTriggerReport         `json:"cleanup_trigger"`
		}
		if json.Unmarshal(raw, &report) != nil {
			return
		}
		if reason := []rune(report.Reason); len(reason) > 0 {
			job.Reason = string(reason[:min(500, len(reason))])
		}
		if job.Outcome == "no_change" || job.Outcome == "propose" {
			return
		}
		if job.Outcome == "repair_interrupted" {
			job.Repair = &ProjectRepair{Kind: kind, Action: "interrupted"}
			if kind == "index" {
				job.Repair.RecoveryStatus = report.Status
			}
			if kind == "memory" && report.Restored {
				job.Repair.Action = "restored"
			}
			return
		}
		summary := ProjectRepair{Kind: kind, Action: "recorded"}
		switch kind {
		case "statistics":
			var before, after struct {
				Applied   bool                  `json:"applied"`
				Retained  *bool                 `json:"retained"`
				Samples   []projectRepairSample `json:"samples"`
				AnalyzeMS float64               `json:"analyze_wall_ms"`
			}
			if json.Unmarshal(report.After, &after) != nil || !after.Applied {
				return
			}
			if after.Retained != nil && !*after.Retained {
				summary.Action = "removed"
				if report.ColumnTarget != nil && report.ColumnTarget.Restored {
					summary.Action = "restored"
				}
			}
			if report.ColumnTarget != nil && nonnegativeFinite(after.AnalyzeMS) {
				summary.AnalysisMS = after.AnalyzeMS
			}
			if json.Unmarshal(report.Before, &before) == nil && len(before.Samples) == len(after.Samples) {
				for i := 0; i < min(4, len(after.Samples)); i++ {
					summary.Metrics = append(summary.Metrics, before.Samples[i].compare(after.Samples[i], fmt.Sprintf("Input %d · ", i+1))...)
				}
			}
		case "cron":
			if report.Commit.IsZero() {
				return
			}
			summary.Metrics = projectCronRepairMetrics(report.Before, report.After)
		case "index", "analyze", "vacuum", "memory":
			if kind == "vacuum" && report.Cleanup != nil {
				change := report.Cleanup
				if !change.SettingChanged {
					return
				}
				if change.Restored {
					summary.Action = "restored"
				}
				if !change.Before.At.IsZero() && change.After.At.After(change.Before.At) {
					summary.Metrics = append(summary.Metrics,
						ProjectRepairMetric{Label: "Dead tuples", Before: float64(change.Before.DeadTuples), After: float64(change.After.DeadTuples)},
						ProjectRepairMetric{Label: "Automatic cleanups", Before: float64(change.Before.AutomaticCleanups), After: float64(change.After.AutomaticCleanups)})
				}
				var before, after projectRepairSample
				if json.Unmarshal(report.Before, &before) == nil && json.Unmarshal(report.After, &after) == nil {
					summary.Metrics = append(summary.Metrics, before.compare(after, "")...)
				}
				break
			}
			if !(kind == "index" && (report.IndexOK || report.Reverted) || kind == "analyze" && report.Analyze > 0 ||
				kind == "vacuum" && report.Vacuum > 0 || kind == "memory" && report.Memory > 0) {
				return
			}
			if kind == "memory" && report.Restored {
				summary.Action = "restored"
			}
			if kind == "index" && report.Reverted {
				summary.Action = "removed"
			}
			var before, after projectRepairSample
			if json.Unmarshal(report.Before, &before) == nil && json.Unmarshal(report.After, &after) == nil {
				summary.Metrics = before.compare(after, "")
				if report.Measurement == "managed_sampled_input" {
					// Normal-traffic window means need positive samples, not identical request counts.
					summary.Metrics = slices.DeleteFunc(summary.Metrics, func(metric ProjectRepairMetric) bool {
						return metric.Label == "Protected read mean latency" || metric.Label == "Protected write mean latency"
					})
					for _, pair := range []struct {
						label    string
						b, a     *float64
						measured bool
					}{
						{"Protected normal read mean latency", before.HealthyMS, after.HealthyMS, before.HealthyReads > 0 && after.HealthyReads > 0},
						{"Protected normal write mean latency", before.WriteMS, after.WriteMS, before.Writes > 0 && after.Writes > 0},
					} {
						if pair.measured && pair.b != nil && pair.a != nil && nonnegativeFinite(*pair.b) && nonnegativeFinite(*pair.a) {
							summary.Metrics = append(summary.Metrics, ProjectRepairMetric{Label: pair.label, Unit: "ms", Before: *pair.b, After: *pair.a})
						}
					}
				}
				if kind == "index" && (report.Measurement == "owned_single_input" || report.Measurement == "managed_sampled_input") {
					for i := range summary.Metrics {
						switch summary.Metrics[i].Label {
						case "Query mean latency":
							summary.Metrics[i].Label = "Measured latency for one input"
						case "Block accesses per read":
							summary.Metrics[i].Label = "Block accesses for one input"
						}
					}
				}
			}
		}
		job.Repair = &summary
	}
}

type projectRepairSample struct {
	Reads         int      `json:"read_calls"`
	HealthyReads  int      `json:"healthy_read_calls"`
	Writes        int      `json:"write_calls"`
	Errors        int      `json:"errors"`
	ReadMS        *float64 `json:"read_mean_ms"`
	HealthyMS     *float64 `json:"healthy_mean_ms"`
	WriteMS       *float64 `json:"write_mean_ms"`
	Blocks        *float64 `json:"blocks_per_read"`
	HeapFetches   *float64 `json:"heap_fetches"`
	EstimateError *float64 `json:"estimate_error_ratio"`
	TempBytes     *float64 `json:"temp_bytes"`
}

func (before projectRepairSample) compare(after projectRepairSample, prefix string) []ProjectRepairMetric {
	if before.Errors != 0 || after.Errors != 0 {
		return nil
	}
	var metrics []ProjectRepairMetric
	for _, m := range []struct {
		label, unit   string
		before, after *float64
		matched       bool
	}{
		{"Query mean latency", "ms", before.ReadMS, after.ReadMS, before.Reads > 0 && before.Reads == after.Reads},
		{"Protected read mean latency", "ms", before.HealthyMS, after.HealthyMS, before.HealthyReads > 0 && before.HealthyReads == after.HealthyReads},
		{"Protected write mean latency", "ms", before.WriteMS, after.WriteMS, before.Writes > 0 && before.Writes == after.Writes},
		{"Block accesses per read", "", before.Blocks, after.Blocks, before.Reads > 0 && before.Reads == after.Reads},
		{"Heap fetches", "", before.HeapFetches, after.HeapFetches, before.Reads > 0 && before.Reads == after.Reads},
		{"Row estimate error ratio", "×", before.EstimateError, after.EstimateError, before.Reads > 0 && before.Reads == after.Reads},
		{"Temporary storage", "bytes", before.TempBytes, after.TempBytes, before.Reads > 0 && before.Reads == after.Reads},
	} {
		if m.matched && m.before != nil && m.after != nil && nonnegativeFinite(*m.before) && nonnegativeFinite(*m.after) {
			metrics = append(metrics, ProjectRepairMetric{Label: prefix + m.label, Unit: m.unit, Before: *m.before, After: *m.after})
		}
	}
	return metrics
}

func projectCronRepairMetrics(beforeRaw, afterRaw json.RawMessage) []ProjectRepairMetric {
	var before, after []CronPeriod
	if json.Unmarshal(beforeRaw, &before) != nil || json.Unmarshal(afterRaw, &after) != nil ||
		validateCronPeriods(before, true) != nil || validateCronPeriods(after, false) != nil {
		return nil
	}
	var metrics []ProjectRepairMetric
	for _, group := range []struct {
		label   string
		measure func(CronWindow) CronMeasure
	}{
		{"Query mean latency during the busy window", func(w CronWindow) CronMeasure { return w.Target }},
		{"Protected read mean latency during the busy window", func(w CronWindow) CronMeasure { return w.Healthy }},
		{"Protected write mean latency during the busy window", func(w CronWindow) CronMeasure { return w.Write }},
	} {
		if !sameCronCounter(group.measure(before[0].Busy).Before, group.measure(after[0].Busy).Before) {
			continue
		}
		mean := func(periods []CronPeriod) float64 {
			var total float64
			for _, period := range periods {
				ms, _ := cronMean(group.measure(period.Busy)) // Period validation checked this measurement.
				total += ms / float64(len(periods))
			}
			return total
		}
		metrics = append(metrics, ProjectRepairMetric{Label: group.label, Unit: "ms", Before: mean(before), After: mean(after)})
	}
	return metrics
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

// A sampled experiment and ordinary traffic windows have different meanings.
// Preserve that distinction and never infer success from a proposal alone.
func readManagedMaintenanceActivity(job *ProjectJob, raw []byte) {
	var report ManagedMaintenanceReport
	if json.Unmarshal(raw, &report) != nil || job.Outcome == "no_change" || job.Outcome == "propose" {
		return
	}
	maintenance := report.Operation == ProjectAnalyze || report.Operation == ProjectVacuum
	committedStatisticsRecovery := report.Operation == "statistics" && report.Measurement == "interrupted_committed_statistics_recovery"
	interrupted := maintenance && report.Measurement == "interrupted_maintenance_catalog_observation" || report.Operation == "statistics" && report.Measurement == "interrupted_statistics_catalog_observation" || committedStatisticsRecovery
	if job.Outcome == "repair_interrupted" && interrupted {
		job.Repair = &ProjectRepair{Kind: string(report.Operation), Action: "interrupted", RecoveryStatus: activityText(report.RecoveryStatus)}
		if committedStatisticsRecovery && (report.RecoveryStatus == "statistics_configuration_restored" || report.RecoveryStatus == "statistics_object_removed") {
			job.Repair.Action = "restored"
			if report.RecoveryStatus == "statistics_object_removed" {
				job.Repair.Action = "removed"
			}
			job.Repair.Restored, job.Repair.StatisticsRegenerated = true, true
		}
		if reason := []rune(report.Reason); len(reason) > 0 {
			job.Reason = string(reason[:min(500, len(reason))])
		}
		// Catalog recovery cannot establish which input, plan or performance
		// result survived the interruption.
		job.PlanNode, job.PlanSource = "", ""
		return
	}
	if !maintenance || report.Measurement != "managed_sampled_input_and_normal_traffic_windows" {
		return
	}
	if !report.Action.Confirmed {
		if report.Action.WriteAttempted {
			job.Repair = &ProjectRepair{Kind: string(report.Operation), Action: "interrupted"}
		}
		return
	}
	summary := &ProjectRepair{Kind: string(report.Operation), Action: "recorded"}
	job.PlanSource = "managed_sample"
	job.PlanNode = report.Before.Sample.Plan
	if report.After != nil {
		job.PlanNode += " → " + report.After.Sample.Plan
		if report.Before.Sample.Reads > 0 && report.After.Sample.Reads > 0 {
			for _, metric := range []ProjectRepairMetric{
				{Label: "Measured latency for one input", Unit: "ms", Before: report.Before.Sample.ReadMS, After: report.After.Sample.ReadMS},
				{Label: "Block accesses for one input", Before: report.Before.Sample.Blocks, After: report.After.Sample.Blocks},
			} {
				if nonnegativeFinite(metric.Before) && nonnegativeFinite(metric.After) {
					summary.Metrics = append(summary.Metrics, metric)
				}
			}
		}
	}
	if b, a := report.BeforeTraffic, report.AfterTraffic; b != nil && a != nil && b.ReadCalls > 0 && b.WriteCalls > 0 && a.ReadCalls > 0 && a.WriteCalls > 0 {
		for _, metric := range []ProjectRepairMetric{
			{Label: "Protected normal read mean latency", Unit: "ms", Before: b.ReadMeanMS, After: a.ReadMeanMS},
			{Label: "Protected normal write mean latency", Unit: "ms", Before: b.WriteMeanMS, After: a.WriteMeanMS},
		} {
			if nonnegativeFinite(metric.Before) && nonnegativeFinite(metric.After) {
				summary.Metrics = append(summary.Metrics, metric)
			}
		}
	}
	job.Repair = summary
}

// Keep committed and historical uncommitted trials distinct. Only a retained,
// committed trial can carry a verified benefit outcome.
func readManagedStatisticsActivity(job *ProjectJob, raw []byte) {
	type sample struct {
		EstimatedRows *int64 `json:"estimated_rows"`
		Sample        struct {
			projectRepairSample
			Plan         string `json:"plan"`
			ReturnedRows *int64 `json:"returned_rows"`
		} `json:"sample"`
	}
	type traffic struct {
		ReadMeanMS  *float64 `json:"read_mean_ms"`
		WriteMeanMS *float64 `json:"write_mean_ms"`
		ReadCalls   int64    `json:"read_calls"`
		WriteCalls  int64    `json:"write_calls"`
	}
	var report struct {
		VerificationScope     string                   `json:"verification_scope"`
		Measurement           string                   `json:"measurement"`
		Variant               string                   `json:"variant"`
		Applied               bool                     `json:"applied"`
		Restored              bool                     `json:"restored"`
		Retained              bool                     `json:"retained"`
		StatisticsRegenerated bool                     `json:"statistics_regenerated"`
		OwnerChanged          bool                     `json:"owner_changed"`
		Reason                string                   `json:"reason"`
		Before                []sample                 `json:"before"`
		After                 []sample                 `json:"after"`
		BeforeTraffic         *traffic                 `json:"before_traffic"`
		AfterTraffic          *traffic                 `json:"after_traffic"`
		ColumnTarget          *ProjectStatisticsResult `json:"column_target"`
	}
	if json.Unmarshal(raw, &report) != nil || (report.Variant != "column_target" && report.Variant != "joint") || job.Outcome == "no_change" || job.Outcome == "propose" {
		return
	}
	committed := report.Measurement == "managed_committed_statistics_trial" && report.VerificationScope == "committed_candidate_inputs_and_normal_traffic"
	uncommitted := report.Measurement == "managed_sampled_inputs_and_normal_traffic_windows" && report.VerificationScope == "uncommitted_candidate_inputs_and_concurrent_load"
	if !committed && !uncommitted {
		return
	}
	retained := committed && report.Applied && report.Retained && !report.Restored && !report.OwnerChanged && job.Outcome == "improved_tested_inputs"
	// Even an inconsistent stored payload must not turn a temporary trial into
	// a successful deployed repair in the customer activity feed.
	if !retained && job.Outcome != "regressed" && job.Outcome != "repair_interrupted" {
		job.Outcome = "inconclusive"
	}
	if !report.Applied {
		return
	}
	summary := &ProjectRepair{Kind: "statistics", Action: "interrupted"}
	job.Repair = summary
	if reason := []rune(report.Reason); len(reason) > 0 {
		job.Reason = string(reason[:min(500, len(reason))])
	}
	restored := report.Restored && !report.Retained
	if committed {
		restored = restored && report.StatisticsRegenerated
	}
	if report.Variant == "column_target" {
		restored = restored && report.ColumnTarget != nil && report.ColumnTarget.Confirmed && report.ColumnTarget.Restored
	}
	if (!restored && !retained) || report.OwnerChanged || job.Outcome == "repair_interrupted" {
		job.Reason = "The temporary statistics trial has no confirmed restoration or verified repair result."
		if report.OwnerChanged {
			job.Reason = "The owner changed the statistics configuration during this trial. That change was preserved; no verified repair is claimed."
		}
		job.PlanNode, job.PlanSource = "", ""
		return
	}
	summary.Restored = restored
	summary.StatisticsRegenerated = committed && restored && report.StatisticsRegenerated
	summary.Action = "removed"
	if report.Variant == "column_target" {
		summary.Action = "restored"
		if report.ColumnTarget.AnalyzeDuration > 0 {
			summary.AnalysisMS = float64(report.ColumnTarget.AnalyzeDuration) / float64(time.Millisecond)
		}
	}
	if retained {
		summary.Action = "recorded"
	}
	notice := "Measurements describe the temporary trial. Original statistics were restored; no repair was retained."
	if committed {
		notice = "Measurements describe sampled inputs and observed normal traffic after the change was committed."
		if restored {
			notice = "Prior configuration was restored and statistics regenerated; the exact previous sample was not restored. No repair was retained."
		}
	}
	if job.Reason == "" {
		job.Reason = notice
	} else {
		job.Reason = notice + " " + job.Reason
	}
	reason := []rune(job.Reason)
	job.Reason = string(reason[:min(500, len(reason))])
	job.PlanNode, job.PlanSource = "", "managed_sample"
	cases := 1
	if report.Variant == "joint" {
		cases = 2
	}
	if len(report.Before) == cases && len(report.After) == cases {
		for i, b := range report.Before {
			a := report.After[i]
			if b.Sample.Errors != 0 || a.Sample.Errors != 0 || b.Sample.Reads <= 0 || a.Sample.Reads <= 0 || b.Sample.Reads != a.Sample.Reads || b.Sample.ReturnedRows == nil || a.Sample.ReturnedRows == nil || *b.Sample.ReturnedRows <= 0 || *b.Sample.ReturnedRows != *a.Sample.ReturnedRows {
				continue
			}
			prefix := fmt.Sprintf("Trial input %d · ", i+1)
			if committed {
				prefix = fmt.Sprintf("Committed trial input %d · ", i+1)
			}
			for _, metric := range []struct {
				label, unit string
				b, a        *float64
			}{
				{"Measured latency", "ms", b.Sample.ReadMS, a.Sample.ReadMS},
				{"Block accesses", "", b.Sample.Blocks, a.Sample.Blocks},
			} {
				if metric.b != nil && metric.a != nil && nonnegativeFinite(*metric.b) && nonnegativeFinite(*metric.a) {
					summary.Metrics = append(summary.Metrics, ProjectRepairMetric{Label: prefix + metric.label, Unit: metric.unit, Before: *metric.b, After: *metric.a})
				}
			}
			if b.EstimatedRows != nil && a.EstimatedRows != nil && *b.EstimatedRows > 0 && *a.EstimatedRows > 0 {
				summary.Metrics = append(summary.Metrics, ProjectRepairMetric{Label: prefix + "Row estimate error ratio", Unit: "×", Before: rowEstimateError(*b.EstimatedRows, *b.Sample.ReturnedRows), After: rowEstimateError(*a.EstimatedRows, *a.Sample.ReturnedRows)})
			}
			if i == 0 && slices.Contains([]string{"Seq Scan", "Index Scan", "Index Only Scan"}, b.Sample.Plan) && slices.Contains([]string{"Seq Scan", "Index Scan", "Index Only Scan"}, a.Sample.Plan) {
				job.PlanNode = "Trial: " + b.Sample.Plan + " → " + a.Sample.Plan
			}
		}
	}
	if b, a := report.BeforeTraffic, report.AfterTraffic; b != nil && a != nil {
		readLabel, writeLabel := "Concurrent normal read mean latency during trial", "Concurrent normal write mean latency during trial"
		if committed {
			readLabel, writeLabel = "Protected normal read mean latency after commit", "Protected normal write mean latency after commit"
		}
		for _, metric := range []struct {
			label    string
			b, a     *float64
			measured bool
		}{
			{readLabel, b.ReadMeanMS, a.ReadMeanMS, b.ReadCalls > 0 && a.ReadCalls > 0},
			{writeLabel, b.WriteMeanMS, a.WriteMeanMS, b.WriteCalls > 0 && a.WriteCalls > 0},
		} {
			if metric.measured && metric.b != nil && metric.a != nil && nonnegativeFinite(*metric.b) && nonnegativeFinite(*metric.a) {
				summary.Metrics = append(summary.Metrics, ProjectRepairMetric{Label: metric.label, Unit: "ms", Before: *metric.b, After: *metric.a})
			}
		}
	}
}

func readManagedCronActivity(job *ProjectJob, raw []byte) {
	var report ManagedCronReport
	if json.Unmarshal(raw, &report) != nil || report.JobID <= 0 || report.ApplicationRequestVisibility != "unobserved" {
		return
	}
	job.PlanNode, job.PlanSource = "", ""
	job.Reason = "Scheduling verification covers completed database work. Application-wide request counts and errors remain unobserved. " + activityText(report.Reason)
	reason := []rune(job.Reason)
	job.Reason = string(reason[:min(500, len(reason))])
	verified, _ := VerifyManagedCronPeriods(report.Before, report.After, report.Window)
	if job.Outcome == "improved_database_observed" && (!report.Applied || report.CommitAt.IsZero() || verified != "improved_database_observed") {
		job.Outcome = "inconclusive"
	}
	if !report.Applied || report.CommitAt.IsZero() {
		return
	}
	job.Repair = &ProjectRepair{Kind: "cron", Action: "recorded"}
	if len(report.Before) != 3 || len(report.After) != 3 || verified == "inconclusive" {
		return
	}
	for i, label := range []string{"Affected completed database work mean latency", "Protected completed database reads mean latency", "Protected completed database writes mean latency"} {
		var sums [2]float64
		var calls [2]int64
		for side, periods := range [][]ManagedCronPeriod{report.Before, report.After} {
			for _, p := range periods {
				measure := []ManagedCronMeasure{p.Busy.Target, p.Busy.Read, p.Busy.Write}[i]
				mean, count, err := managedCronMean(measure)
				if err != nil || count <= 0 {
					return
				}
				sums[side] += mean * float64(count)
				calls[side] += count
			}
		}
		job.Repair.Metrics = append(job.Repair.Metrics, ProjectRepairMetric{Label: label, Unit: "ms", Before: sums[0] / float64(calls[0]), After: sums[1] / float64(calls[1])})
	}
}

func readManagedMemoryActivity(job *ProjectJob, raw []byte) {
	var report ManagedMemoryReport
	if json.Unmarshal(raw, &report) != nil || report.FunctionOID == 0 {
		return
	}
	recovery := report.Measurement == "interrupted_function_catalog_observation"
	if !recovery && report.Measurement != "matched_read_only_function_experiment" {
		return
	}
	job.PlanNode, job.PlanSource = "", ""
	job.Reason = activityText(report.Reason)
	if recovery {
		job.Outcome = "repair_interrupted"
		job.Repair = &ProjectRepair{Kind: "memory", Action: "interrupted", Restored: report.Restored, RecoveryStatus: activityText(report.RecoveryStatus)}
		if report.Restored {
			job.Repair.Action = "restored"
		}
		return
	}
	job.Repair = &ProjectRepair{Kind: "memory", Action: "recorded", Restored: report.Restored}
	if report.Restored {
		job.Repair.Action = "restored"
	}
	a := report.After
	b := report.Before
	measured := a != nil && b.Calls == 10 && a.Calls == 10 && b.SortRows > 0 && a.SortRows == b.SortRows
	if job.Outcome == "improved_tested_inputs" && (!measured || report.Restored || report.Outcome != "improved_tested_inputs") {
		job.Outcome = "inconclusive"
	}
	notice := "Extra memory is limited by a short allowance; other calls use the function's original memory. "
	if report.Restored {
		notice = "The original function definition was restored. "
	}
	job.Reason = notice + job.Reason
	reason := []rune(job.Reason)
	job.Reason = string(reason[:min(500, len(reason))])
	if measured {
		for _, metric := range []ProjectRepairMetric{
			{Label: "Measured function mean latency", Unit: "ms", Before: b.MeanMS, After: a.MeanMS},
			{Label: "Function temporary writes per call", Unit: "bytes", Before: b.OuterTempBytes, After: a.OuterTempBytes},
			{Label: "Matched sort temporary writes", Unit: "bytes", Before: b.InnerTempBytes, After: a.InnerTempBytes},
		} {
			if nonnegativeFinite(metric.Before) && nonnegativeFinite(metric.After) {
				job.Repair.Metrics = append(job.Repair.Metrics, metric)
			}
		}
	}
	if b, a := report.BeforeTraffic, report.AfterTraffic; b != nil && a != nil && b.ReadCalls > 0 && b.WriteCalls > 0 && a.ReadCalls > 0 && a.WriteCalls > 0 {
		for _, metric := range []ProjectRepairMetric{
			{Label: "Protected normal read mean latency", Unit: "ms", Before: b.ReadMeanMS, After: a.ReadMeanMS},
			{Label: "Protected normal write mean latency", Unit: "ms", Before: b.WriteMeanMS, After: a.WriteMeanMS},
		} {
			if nonnegativeFinite(metric.Before) && nonnegativeFinite(metric.After) {
				job.Repair.Metrics = append(job.Repair.Metrics, metric)
			}
		}
	}
}

func readManagedCleanupActivity(job *ProjectJob, raw []byte) {
	var report ManagedCleanupReport
	if json.Unmarshal(raw, &report) != nil || report.Measurement != "same_sampled_input_and_normal_database_traffic" {
		return
	}
	job.PlanNode, job.PlanSource = "", ""
	job.Reason = activityText(report.Reason)
	t := report.Trigger
	if job.Outcome == "repair_interrupted" {
		job.Repair = &ProjectRepair{Kind: "vacuum", Action: "interrupted", Restored: t.Restored}
		return
	}
	matched := report.After != nil && report.Before.Sample.Reads > 0 && report.After.Sample.Reads == report.Before.Sample.Reads && report.Before.Sample.PlanReturned > 0 && report.Before.Sample.PlanReturned == report.After.Sample.PlanReturned
	if job.Outcome == "improved_tested_inputs" && (!t.SettingChanged || !t.AutomaticCleaned || t.Restored || !matched || report.Outcome != "improved_tested_inputs") {
		job.Outcome = "inconclusive"
	}
	if !t.SettingChanged && !t.Restored {
		return
	}
	summary := &ProjectRepair{Kind: "vacuum", Action: "recorded", Restored: t.Restored, AutomaticCleaned: t.AutomaticCleaned}
	if t.Restored {
		summary.Action = "restored"
	}
	job.Repair = summary
	if t.AutomaticCleaned {
		job.Reason = "PostgreSQL automatically cleaned the measured backlog after the setting change. " + job.Reason
		if !t.Before.At.IsZero() && !t.After.At.IsZero() && sameOwnedCleanupGeneration(t.Before, t.After) && t.Before.DeadTuples >= 0 && t.After.DeadTuples >= 0 {
			summary.Metrics = append(summary.Metrics, ProjectRepairMetric{Label: "Estimated dead rows", Unit: "rows", Before: float64(t.Before.DeadTuples), After: float64(t.After.DeadTuples)})
		}
	}
	reason := []rune(job.Reason)
	job.Reason = string(reason[:min(500, len(reason))])
	if matched {
		for _, metric := range []ProjectRepairMetric{
			{Label: "Measured latency for the same sampled input", Unit: "ms", Before: report.Before.Sample.ReadMS, After: report.After.Sample.ReadMS},
			{Label: "Block accesses for the same sampled input", Before: report.Before.Sample.Blocks, After: report.After.Sample.Blocks},
		} {
			if nonnegativeFinite(metric.Before) && nonnegativeFinite(metric.After) {
				summary.Metrics = append(summary.Metrics, metric)
			}
		}
	}
	if b, a := report.BeforeTraffic, report.AfterTraffic; b != nil && a != nil && b.ReadCalls > 0 && b.WriteCalls > 0 && a.ReadCalls > 0 && a.WriteCalls > 0 {
		for _, metric := range []ProjectRepairMetric{
			{Label: "Protected normal read mean latency", Unit: "ms", Before: b.ReadMeanMS, After: a.ReadMeanMS},
			{Label: "Protected normal write mean latency", Unit: "ms", Before: b.WriteMeanMS, After: a.WriteMeanMS},
		} {
			if nonnegativeFinite(metric.Before) && nonnegativeFinite(metric.After) {
				summary.Metrics = append(summary.Metrics, metric)
			}
		}
	}
}
