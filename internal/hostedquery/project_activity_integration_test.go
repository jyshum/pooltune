//go:build integration

package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestProjectActivityRetainsScopedWindowsAndDiagnosisAcrossReconnect(t *testing.T) {
	ctx, state, config := projectActivityTestState(t)
	project, err := supabaseproject.New("osdjqwpzgyzwfaxyufze", "20139985697ea001")
	if err != nil {
		t.Fatal(err)
	}
	other := "supabase/osdjqwpzgyzwfaxyufze/20139985697ea002"
	if _, err := state.Exec(ctx, "INSERT INTO pooltune_worker.projects(project_id, monitor_epoch) VALUES ($1,1),($2,1)", project.ID(), other); err != nil {
		t.Fatal(err)
	}
	const (
		healthyID = 42
		slowID    = -4567890123456789012
		idleID    = 44
		missingID = 45
	)
	at := time.Now().UTC().Add(-10 * time.Second).Truncate(time.Microsecond)
	policy := SamplingPolicy{Interval: time.Second, Window: time.Second}
	enrollment := Enrollment{Project: project.ID(), Queries: []QueryObjective{
		{Database: 7, Role: 9, Query: healthyID, MeanMS: 2},
		{Database: 7, Role: 9, Query: slowID, MeanMS: 2},
		{Database: 7, Role: 9, Query: idleID, MeanMS: 2},
		{Database: 7, Role: 9, Query: missingID, MeanMS: 2},
	}}
	record := func(db *pgx.Conn, enrollment Enrollment, sequence int64, epoch int64) (PollResult, error) {
		return recordProjectPoll(ctx, db, enrollment.Project, policy, at.Add(time.Duration(sequence)*time.Second), epoch,
			func(context.Context) (Enrollment, map[queryKey]Counter, error) {
				samples := make(map[queryKey]Counter)
				for _, entry := range []struct {
					query, calls, blocks int64
					execution            float64
				}{
					{healthyID, 10 + sequence, 10 + sequence, 10},
					{slowID, 10 + sequence, 10 + 3*sequence, 10 + float64(5*sequence)},
					{idleID, 10, 10, 10},
				} {
					counter := Counter{Database: 7, Role: 9, Query: entry.query, Boot: at, Reset: at, Entry: at,
						Calls: entry.calls, Blocks: entry.blocks, ExecutionMS: entry.execution}
					samples[counter.key()] = counter
				}
				return enrollment, samples, nil
			})
	}
	for interval := int64(0); interval <= 5; interval++ {
		if _, err := record(state, enrollment, interval, 1); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := record(state, enrollment, 5, 1); err != nil || !result.Skipped {
		t.Fatalf("duplicate poll must be skipped: %+v, %v", result, err)
	}
	if _, err := record(state, Enrollment{Project: other, Queries: enrollment.Queries}, 0, 1); err != nil {
		t.Fatal(err)
	}
	var epoch int64 = 1
	job, err := claimProjectInvestigation(ctx, state, project.ID(), &epoch)
	if err != nil || job == nil || job.Query != slowID {
		t.Fatalf("expected the slow investigation: %+v, %v", job, err)
	}
	if err := finishInvestigation(ctx, state, *job, DispatchResult{
		Outcome: "diagnosed", ElapsedMS: 1200, TargetPlanMS: 100,
		LookupDiagnosis: &LookupDiagnosis{Reason: "One\n  reason.", ModelMS: 800},
		TrafficPlan:     &TrafficPlan{Node: "Seq Scan"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := DisconnectProjectMonitor(ctx, state, project); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(ctx); err != nil {
		t.Fatal(err)
	}
	state = openMonitorConn(t, ctx, config)
	activity, err := ReadProjectActivity(ctx, state, project.ID(), at)
	if err != nil || activity.LastPoll == nil || !activity.LastPoll.Equal(at.Add(5*time.Second)) || len(activity.Polls) != 6 {
		t.Fatalf("disconnect and new state connection must preserve six successful polls: %+v, %v", activity, err)
	}
	if len(activity.Windows) != 20 || len(activity.Jobs) != 1 {
		t.Fatalf("wanted 20 finalized query windows and one diagnosis: windows=%d jobs=%d", len(activity.Windows), len(activity.Jobs))
	}
	var latest = make(map[int64]ProjectWindow)
	for _, window := range activity.Windows {
		latest[window.Query] = window
	}
	healthy, slow := latest[healthyID], latest[slowID]
	if healthy.Decision != NoChange || healthy.MeanMS == nil || *healthy.MeanMS != 0 ||
		slow.Decision != Investigate || slow.MeanMS == nil || *slow.MeanMS != 5 ||
		slow.SharedBlocks == nil || *slow.SharedBlocks != 3 || slow.ObjectiveMS != 2 {
		t.Fatalf("wrong healthy or slow metrics: %+v %+v", healthy, slow)
	}
	idle, missing := latest[idleID], latest[missingID]
	if idle.Decision != InsufficientEvidence || idle.Calls == nil || *idle.Calls != 0 ||
		idle.SharedBlocks == nil || *idle.SharedBlocks != 0 || idle.MeanMS != nil ||
		missing.Decision != InsufficientEvidence || missing.Calls != nil ||
		missing.SharedBlocks != nil || missing.MeanMS != nil {
		t.Fatalf("zero calls confused with an absent counter: idle=%+v missing=%+v", idle, missing)
	}
	saved := activity.Jobs[0]
	if saved.ID != job.ID || saved.Attempts != 1 || saved.ClaimedAt == nil || saved.FinishedAt == nil ||
		saved.ClaimedAt.Before(saved.CreatedAt) || saved.FinishedAt.Before(*saved.ClaimedAt) ||
		saved.Outcome != "diagnosed" || saved.Reason != "One reason." ||
		saved.PlanNode != "Seq Scan" || saved.PlanSource != "ordinary_traffic" ||
		saved.ModelMS == nil || *saved.ModelMS != 800 || saved.ElapsedMS == nil || *saved.ElapsedMS != 1200 {
		t.Fatalf("wrong typed investigation summary: %+v", saved)
	}
	foreign, err := ReadProjectActivity(ctx, state, other, at)
	if err != nil || len(foreign.Polls) != 1 || len(foreign.Windows) != 0 || len(foreign.Jobs) != 0 {
		t.Fatalf("activity crossed project boundaries: %+v, %v", foreign, err)
	}
	unset, err := ReadProjectActivity(ctx, state, "unregistered", at)
	if err != nil || unset.LastPoll != nil || len(unset.Polls)+len(unset.Windows)+len(unset.Jobs) != 0 {
		t.Fatalf("unregistered project should have no activity: %+v, %v", unset, err)
	}

	if _, err := state.Exec(ctx, "UPDATE pooltune_worker.projects SET monitor_epoch=2 WHERE project_id=$1", project.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := record(state, enrollment, 7, 2); err != nil {
		t.Fatal(err)
	}
	after, err := ReadProjectActivity(ctx, state, project.ID(), at)
	if err != nil || len(after.Polls) != 7 || !after.Polls[6].At.Equal(at.Add(7*time.Second)) ||
		len(after.Windows) != 20 || after.LastPoll == nil || !after.LastPoll.Equal(after.Polls[6].At) {
		t.Fatalf("reconnect should preserve the gap and start a new window: %+v, %v", after, err)
	}
}

func TestProjectActivityPrunesOldSuccessfulPolls(t *testing.T) {
	ctx, state, _ := projectActivityTestState(t)
	enrollment := Enrollment{Project: "supabase/osdjqwpzgyzwfaxyufze/20139985697ea003",
		Queries: []QueryObjective{{Database: 7, Role: 9, Query: 3, MeanMS: 2}}}
	if _, err := state.Exec(ctx, "INSERT INTO pooltune_worker.projects(project_id, monitor_epoch) VALUES ($1,1)", enrollment.Project); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	policy := SamplingPolicy{Interval: time.Second, Window: time.Second}
	collect := func(context.Context) (Enrollment, map[queryKey]Counter, error) {
		return enrollment, nil, nil
	}
	for _, at := range []time.Time{now.Add(-9 * 24 * time.Hour), now.Add(-9*24*time.Hour + time.Second), now.Add(-7 * 24 * time.Hour), now} {
		if _, err := recordProjectPoll(ctx, state, enrollment.Project, policy, at, 1, collect); err != nil {
			t.Fatal(err)
		}
	}
	var polls, windows int
	err := state.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM pooltune_worker.project_polls WHERE project_id=$1),
		(SELECT count(*) FROM pooltune_worker.project_windows WHERE project_id=$1)`, enrollment.Project).Scan(&polls, &windows)
	if err != nil || polls != 2 || windows != 2 {
		t.Fatalf("expected old polls and windows to be removed and week-old history retained: polls=%d windows=%d err=%v", polls, windows, err)
	}
	pollErr := errors.New("target error")
	_, err = recordProjectPoll(ctx, state, enrollment.Project, policy, now.Add(time.Second), 1,
		func(context.Context) (Enrollment, map[queryKey]Counter, error) { return Enrollment{}, nil, pollErr })
	if !errors.Is(err, pollErr) {
		t.Fatalf("expected target error, got %v", err)
	}
	recent, err := ReadProjectActivity(ctx, state, enrollment.Project, now.Add(-time.Hour))
	if err != nil || len(recent.Polls) != 1 || len(recent.Windows) != 1 || !recent.Polls[0].At.Equal(now) {
		t.Fatalf("a failed poll must leave a gap and explicit ranges must filter history: %+v, %v", recent, err)
	}
}

func TestProjectActivityReportsAutomaticCleanupSeparately(t *testing.T) {
	now := time.Now().UTC()
	report := VacuumReport{CleanupTrigger: &CleanupTriggerReport{
		SettingChanged: true, AutomaticCleaned: true,
		Before: ProjectCleanupSettingsObservation{At: now, DeadTuples: 16000, AutomaticCleanups: 2},
		After:  ProjectCleanupSettingsObservation{At: now.Add(time.Minute), DeadTuples: 300, AutomaticCleanups: 3},
	}}
	raw, err := json.Marshal(map[string]VacuumReport{"vacuum": report})
	if err != nil {
		t.Fatal(err)
	}
	job := ProjectJob{Outcome: "improved_tested_inputs"}
	readProjectRepair(&job, raw)
	if job.Repair == nil || job.Repair.Action != "recorded" || len(job.Repair.Metrics) != 2 {
		t.Fatalf("automatic cleanup result missing: %+v", job.Repair)
	}
	if job.Repair.Metrics[0].Label != "Dead tuples" || job.Repair.Metrics[0].After != 300 ||
		job.Repair.Metrics[1].Label != "Automatic cleanups" || job.Repair.Metrics[1].After != 3 {
		t.Fatalf("automatic cleanup values changed: %+v", job.Repair.Metrics)
	}
}

func TestProjectActivityReportsOnlyRecordedRepairMeasurements(t *testing.T) {
	ctx, state, _ := projectActivityTestState(t)
	const project = "supabase/osdjqwpzgyzwfaxyufze/20139985697ea004"
	if _, err := state.Exec(ctx, "INSERT INTO pooltune_worker.projects(project_id, monitor_epoch) VALUES ($1,1)", project); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		result, kind, action, reason string
		firstMetric                  []float64
		pending                      bool
		analysisMS                   float64
	}{
		{result: `{"outcome":"regressed","repair":{"measurement_scope":"managed_sampled_input","index_valid":false,"reverted":true,"before":{"read_calls":1,"read_mean_ms":12},"after":{"read_calls":1,"read_mean_ms":3}}}`, kind: "index", action: "removed", firstMetric: []float64{12, 3}},
		{result: `{"outcome":"repair_interrupted","managed_maintenance":{"operation":"vacuum","measurement":"interrupted_maintenance_catalog_observation","recovery_status":"maintenance_observed","action":{}}}`, kind: "vacuum", action: "interrupted"},
		{result: `{"outcome":"improved_tested_inputs","managed_maintenance":{"operation":"analyze","measurement":"managed_sampled_input_and_normal_traffic_windows","action":{"Confirmed":true},"before":{"sample":{"read_calls":1,"read_mean_ms":12}},"after":{"sample":{"read_calls":1,"read_mean_ms":3}}}}`, kind: "analyze", action: "recorded", firstMetric: []float64{12, 3}},
		{result: `{"outcome":"inconclusive","managed_maintenance":{"operation":"vacuum","measurement":"managed_sampled_input_and_normal_traffic_windows","action":{"WriteAttempted":true,"Confirmed":false},"before":{"sample":{"read_calls":1,"read_mean_ms":12}}}}`, kind: "vacuum", action: "interrupted"},
		{result: `{"outcome":"propose","proposal":{"repair":"index","reason":"Try an index"}}`, reason: "Try an index"},
		{result: `{"outcome":"no_change","repair":{"index_valid":false,"reason":"Missing evidence","before":{"read_calls":10,"read_mean_ms":12}}}`, reason: "Missing evidence"},
		{result: `{"outcome":"improved_tested_inputs","repair":{"index_valid":true,"reason":"Measured result","before":{"read_calls":10,"read_mean_ms":12},"after":{"read_calls":10,"read_mean_ms":0}}}`, kind: "index", action: "recorded", reason: "Measured result", firstMetric: []float64{12, 0}},
		{result: `{"outcome":"inconclusive","analyze":{"analyze_wall_ms":10,"reason":"Incomplete sample","before":{"read_calls":10,"read_mean_ms":12},"after":{"read_calls":10}}}`, kind: "analyze", action: "recorded", reason: "Incomplete sample"},
		{result: `{"outcome":"regressed","statistics":{"reason":"Candidate regressed","before":{"samples":[{"read_calls":10,"read_mean_ms":12}]},"after":{"applied":true,"retained":false,"samples":[{"read_calls":10,"read_mean_ms":15}]}}}`, kind: "statistics", action: "removed", reason: "Candidate regressed", firstMetric: []float64{12, 15}},
		{result: `{"outcome":"inconclusive","statistics":{"reason":"Original target restored","column_target":{"prior_target":50,"trial_target":100,"restored":true},"before":{"samples":[{"read_calls":10,"read_mean_ms":12}]},"after":{"applied":true,"retained":false,"analyze_wall_ms":20,"samples":[{"read_calls":10,"read_mean_ms":11}]}}}`, kind: "statistics", action: "restored", reason: "Original target restored", firstMetric: []float64{12, 11}, analysisMS: 20},
		{result: `{"outcome":"improved_tested_inputs","vacuum":{"vacuum_wall_ms":10,"before":{"read_calls":10,"read_mean_ms":12},"after":{"read_calls":10,"read_mean_ms":4}}}`, kind: "vacuum", action: "recorded", firstMetric: []float64{12, 4}},
		{result: `{"outcome":"inconclusive","memory":{"memory_wall_ms":10,"restored":true,"before":{"read_calls":10,"read_mean_ms":12},"after":{"read_calls":10,"read_mean_ms":4}}}`, kind: "memory", action: "restored", firstMetric: []float64{12, 4}},
		{result: `{"outcome":"awaiting_verification","cron":{"commit_at":"2026-11-14T00:00:01Z"}}`, kind: "cron", action: "recorded"},
		{result: `{"outcome":"repair_interrupted","repair":{"index_valid":true,"before":{"read_calls":10,"read_mean_ms":12},"after":{"read_calls":10,"read_mean_ms":4}}}`, kind: "index", action: "interrupted"},
		{result: `{"outcome":"inconclusive","cron":{"commit_at":"0001-01-01T00:00:00Z","reason":"Window expired"}}`, reason: "Window expired"},
		{result: `{"outcome":"investigate","repair":{"index_valid":true}}`, pending: true},
	}
	for i, test := range tests {
		_, err := state.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id, database_oid, role_oid, query_id, finished_at, evidence, result)
			VALUES ($1, 7, 9, $2, CASE WHEN $3 THEN NULL ELSE statement_timestamp() END, '{}', $4::jsonb)`, project, i+1, test.pending, test.result)
		if err != nil {
			t.Fatal(err)
		}
	}
	activity, err := ReadProjectActivity(ctx, state, project, time.Now().Add(-time.Hour))
	if err != nil || len(activity.Jobs) != len(tests) {
		t.Fatalf("wrong activity rows: %d, %v", len(activity.Jobs), err)
	}
	for _, job := range activity.Jobs {
		test := tests[job.Query-1]
		raw, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		var display struct {
			Reason string
			Repair *struct {
				Kind, Action string
				Metrics      []struct{ Before, After float64 }
				AnalysisMS   float64 `json:"analysis_wall_ms"`
			}
		}
		if err := json.Unmarshal(raw, &display); err != nil {
			t.Fatal(err)
		}
		if display.Reason != test.reason {
			t.Errorf("query %d: got reason %q, want %q", job.Query, display.Reason, test.reason)
		}
		if test.kind == "" {
			if display.Repair != nil {
				t.Errorf("query %d: proposal or no-change incorrectly showed a repair: %s", job.Query, raw)
			}
			continue
		}
		if display.Repair == nil || display.Repair.Kind != test.kind || display.Repair.Action != test.action {
			t.Errorf("query %d: missing %s %s repair: %s", job.Query, test.kind, test.action, raw)
			continue
		}
		if display.Repair.AnalysisMS != test.analysisMS {
			t.Errorf("query %d: got analysis wall time %.2f, want %.2f", job.Query, display.Repair.AnalysisMS, test.analysisMS)
		}
		var first []float64
		if len(display.Repair.Metrics) != 0 {
			first = []float64{display.Repair.Metrics[0].Before, display.Repair.Metrics[0].After}
		}
		if !reflect.DeepEqual(first, test.firstMetric) {
			t.Errorf("query %d: got metric %v, want %v", job.Query, first, test.firstMetric)
		}
	}
}

func projectActivityTestState(t *testing.T) (context.Context, *pgx.Conn, *pgx.ConnConfig) {
	t.Helper()
	ctx, config, _ := testpg.Start(t)
	state := openMonitorConn(t, ctx, config)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	return ctx, state, config
}

func TestProjectActivityKeepsManagedStatisticsTrialsSeparateFromRetainedRepairs(t *testing.T) {
	ctx, state, _ := projectActivityTestState(t)
	const project = "managed-statistics-activity"
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,1)`, project); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		result, outcome, action string
		restored                bool
		metrics                 int
		firstLabel              string
		firstBefore, firstAfter float64
		recovery                string
	}{
		{result: `{"outcome":"inconclusive","managed_statistics":{"verification_scope":"uncommitted_candidate_inputs_and_concurrent_load","measurement":"managed_sampled_inputs_and_normal_traffic_windows","variant":"column_target","applied":true,"retained":false,"restored":true,"outcome":"inconclusive","sampled_outcome":"improved_tested_inputs","reason":"The temporary trial was rolled back.","column_target":{"Confirmed":true,"Restored":true,"AnalyzeDuration":20000000},"before":[{"inputs":[987654321],"raw_sql":"PRIVATE_QUERY_TEXT","estimated_rows":20000,"sample":{"read_calls":1,"read_mean_ms":20,"blocks_per_read":500,"returned_rows":100,"plan":"Seq Scan"}}],"after":[{"estimated_rows":100,"sample":{"read_calls":1,"read_mean_ms":1,"blocks_per_read":20,"returned_rows":100,"plan":"Index Only Scan"}}],"before_traffic":{"read_calls":20,"write_calls":20,"read_mean_ms":2,"write_mean_ms":4},"after_traffic":{"read_calls":25,"write_calls":30,"read_mean_ms":1.8,"write_mean_ms":3.5}}}`, outcome: "inconclusive", action: "restored", restored: true, metrics: 5, firstLabel: "Trial input 1 · Measured latency", firstBefore: 20, firstAfter: 1},
		{result: `{"outcome":"regressed","managed_statistics":{"verification_scope":"uncommitted_candidate_inputs_and_concurrent_load","measurement":"managed_sampled_inputs_and_normal_traffic_windows","variant":"joint","applied":true,"restored":true,"outcome":"regressed","before":[{"sample":{"read_calls":1,"read_mean_ms":20,"returned_rows":100}},{"sample":{"read_calls":1,"read_mean_ms":25,"returned_rows":300}}],"after":[{"sample":{"read_calls":1,"read_mean_ms":30,"returned_rows":100}},{"sample":{"read_calls":1,"read_mean_ms":2,"returned_rows":300}}]}}`, outcome: "regressed", action: "removed", restored: true, metrics: 2, firstLabel: "Trial input 1 · Measured latency", firstBefore: 20, firstAfter: 30},
		{result: `{"outcome":"repair_interrupted","managed_maintenance":{"operation":"statistics","measurement":"interrupted_statistics_catalog_observation","recovery_status":"trial_statistics_setting","reason":"Catalog observed. Performance remains unverified.","action":{"Confirmed":true},"before":{"sample":{"read_calls":1,"read_mean_ms":20}},"after":{"sample":{"read_calls":1,"read_mean_ms":1}}}}`, outcome: "repair_interrupted", action: "interrupted", recovery: "trial_statistics_setting"},
		{result: `{"outcome":"improved_tested_inputs","managed_statistics":{"verification_scope":"uncommitted_candidate_inputs_and_concurrent_load","measurement":"managed_sampled_inputs_and_normal_traffic_windows","variant":"joint","applied":true,"restored":true,"outcome":"improved_tested_inputs"}}`, outcome: "inconclusive", action: "removed", restored: true},
		{result: `{"outcome":"inconclusive","managed_statistics":{"verification_scope":"uncommitted_candidate_inputs_and_concurrent_load","measurement":"managed_sampled_inputs_and_normal_traffic_windows","variant":"column_target","applied":true,"restored":true,"column_target":{"Confirmed":true,"Restored":true},"before":[{"sample":{"read_calls":1,"read_mean_ms":20,"returned_rows":100}}],"after":[{"sample":{"read_calls":1,"returned_rows":100}}]}}`, outcome: "inconclusive", action: "restored", restored: true},
		{result: `{"outcome":"improved_tested_inputs","managed_statistics":{"verification_scope":"uncommitted_candidate_inputs_and_concurrent_load","measurement":"managed_sampled_inputs_and_normal_traffic_windows","variant":"joint","applied":true,"retained":true,"restored":false}}`, outcome: "inconclusive", action: "interrupted"},
	}
	for i, c := range cases {
		if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,finished_at,result) VALUES($1,1,2,$2,'{}',statement_timestamp(),$3::jsonb)`, project, i+1, c.result); err != nil {
			t.Fatal(err)
		}
	}
	activity, err := ReadProjectActivity(ctx, state, project, time.Now().Add(-time.Hour))
	if err != nil || len(activity.Jobs) != len(cases) {
		t.Fatalf("read trials: %+v %v", activity, err)
	}
	for _, job := range activity.Jobs {
		c := cases[job.Query-1]
		if job.Outcome != c.outcome || job.Repair == nil || job.Repair.Kind != "statistics" || job.Repair.Action != c.action {
			t.Errorf("query %d lost scoped outcome: %+v", job.Query, job)
			continue
		}
		raw, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		var summary struct {
			Repair struct {
				Restored bool `json:"restored"`
			}
		}
		if err := json.Unmarshal(raw, &summary); err != nil {
			t.Fatal(err)
		}
		if summary.Repair.Restored != c.restored || job.Repair.RecoveryStatus != c.recovery || len(job.Repair.Metrics) != c.metrics {
			t.Errorf("query %d wrong recovery/metrics: %s", job.Query, raw)
		}
		if c.metrics > 0 {
			metric := job.Repair.Metrics[0]
			if metric.Label != c.firstLabel || metric.Before != c.firstBefore || metric.After != c.firstAfter {
				t.Errorf("query %d misleading sample: %+v", job.Query, metric)
			}
		}
		if strings.Contains(string(raw), "987654321") || strings.Contains(string(raw), "PRIVATE_QUERY_TEXT") || strings.Contains(string(raw), "\"inputs\"") {
			t.Errorf("query %d leaked raw inputs: %s", job.Query, raw)
		}
		for _, metric := range job.Repair.Metrics {
			if strings.Contains(metric.Label, "Protected") {
				t.Errorf("uncommitted trial mislabeled protected-plan verification: %+v", metric)
			}
		}
		if c.recovery != "" && job.Reason != "Catalog observed. Performance remains unverified." {
			t.Errorf("catalog-only reason omitted: %+v", job)
		}
	}
}

func TestProjectActivityDistinguishesCommittedStatisticsBenefitAndConfigurationRecovery(t *testing.T) {
	ctx, state, _ := projectActivityTestState(t)
	const project = "committed-statistics-activity"
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,1)`, project); err != nil {
		t.Fatal(err)
	}
	sample := func(ms float64, estimate int64) ManagedIndexMeasurement {
		return ManagedIndexMeasurement{EstimatedRows: estimate, Inputs: []int32{987654321}, Sample: TrialSample{Reads: 1, ReadMS: ms, PlanReturned: 100, Blocks: 100, Plan: "Index Only Scan"}}
	}
	report := ManagedStatisticsReport{ManagedStatisticsTrialReport: ManagedStatisticsTrialReport{Measurement: "managed_committed_statistics_trial", VerificationScope: "committed_candidate_inputs_and_normal_traffic", Variant: "column_target", Applied: true, Retained: true, Deadline: time.Now(), Before: []ManagedIndexMeasurement{sample(20, 20000)}, After: []ManagedIndexMeasurement{sample(1, 100)}, BeforeTraffic: &ManagedMaintenanceTraffic{ReadCalls: 10, WriteCalls: 10, ReadMeanMS: 1, WriteMeanMS: 1}, AfterTraffic: &ManagedMaintenanceTraffic{ReadCalls: 20, WriteCalls: 20, ReadMeanMS: 1, WriteMeanMS: 1}, ColumnTarget: &ProjectStatisticsResult{Confirmed: true}, Outcome: "improved_tested_inputs"}}
	cases := []struct {
		result                DispatchResult
		outcome, action       string
		restored, regenerated bool
		metrics               int
	}{
		{result: DispatchResult{Outcome: "improved_tested_inputs", ManagedStatistics: &report}, outcome: "improved_tested_inputs", action: "recorded", metrics: 5},
	}
	restored := report
	restored.Retained = false
	restored.Restored = true
	restored.StatisticsRegenerated = true
	restored.Outcome = "regressed"
	restored.ColumnTarget = &ProjectStatisticsResult{Confirmed: true, Restored: true}
	cases = append(cases, struct {
		result                DispatchResult
		outcome, action       string
		restored, regenerated bool
		metrics               int
	}{DispatchResult{Outcome: "regressed", ManagedStatistics: &restored}, "regressed", "restored", true, true, 5})
	changed := report
	changed.Retained = false
	changed.OwnerChanged = true
	changed.Outcome = "inconclusive"
	cases = append(cases, struct {
		result                DispatchResult
		outcome, action       string
		restored, regenerated bool
		metrics               int
	}{DispatchResult{Outcome: "inconclusive", ManagedStatistics: &changed}, "inconclusive", "interrupted", false, false, 0})
	for _, status := range []string{"statistics_configuration_restored", "statistics_object_removed"} {
		action := "restored"
		if status == "statistics_object_removed" {
			action = "removed"
		}
		cases = append(cases, struct {
			result                DispatchResult
			outcome, action       string
			restored, regenerated bool
			metrics               int
		}{DispatchResult{Outcome: "repair_interrupted", ManagedMaintenance: &ManagedMaintenanceReport{Operation: "statistics", Measurement: "interrupted_committed_statistics_recovery", RecoveryStatus: status, Reason: "Configuration restored and statistics regenerated. Performance remains unverified."}}, "repair_interrupted", action, true, true, 0})
	}
	for i, c := range cases {
		raw, err := json.Marshal(c.result)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,finished_at,result) VALUES($1,1,2,$2,'{}',statement_timestamp(),$3::jsonb)`, project, i+1, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	activity, err := ReadProjectActivity(ctx, state, project, time.Now().Add(-time.Hour))
	if err != nil || len(activity.Jobs) != len(cases) {
		t.Fatalf("read committed statistics: %+v %v", activity, err)
	}
	for _, job := range activity.Jobs {
		c := cases[job.Query-1]
		if job.Outcome != c.outcome || job.Repair == nil || job.Repair.Kind != "statistics" || job.Repair.Action != c.action || job.Repair.Restored != c.restored || len(job.Repair.Metrics) != c.metrics {
			t.Errorf("wrong committed/restored action: %+v", job)
			continue
		}
		raw, _ := json.Marshal(job)
		var fields struct {
			Repair struct {
				Regenerated bool `json:"statistics_regenerated"`
			}
		}
		_ = json.Unmarshal(raw, &fields)
		if fields.Repair.Regenerated != c.regenerated {
			t.Errorf("missing regenerated-sample boundary: %s", raw)
		}
		if strings.Contains(string(raw), "987654321") {
			t.Errorf("inputs leaked: %s", raw)
		}
		if c.metrics > 0 && job.Repair.Metrics[0].Label != "Committed trial input 1 · Measured latency" {
			t.Errorf("missing committed scope: %+v", job.Repair.Metrics[0])
		}
		if c.regenerated && !strings.Contains(job.Reason, "regenerated") {
			t.Errorf("restoration guarantee hidden: %+v", job)
		}
	}
}

func TestProjectActivityShowsManagedCronDatabaseScopeWithoutApplicationClaims(t *testing.T) {
	ctx, state, _ := projectActivityTestState(t)
	const project = "managed-cron-activity"
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,1)`, project); err != nil {
		t.Fatal(err)
	}
	before, after := managedCronPolicyExample(false), managedCronPolicyExample(true)
	report := ManagedCronReport{Window: before.Window, JobID: before.JobID, Before: before.Periods, After: after.Periods, Original: before.Original, Alternate: before.Alternate, Outcome: "improved_database_observed", Applied: true, CommitAt: time.Now(), ApplicationRequestVisibility: "unobserved", Reason: "Comparable completed database work improved."}
	raw, _ := json.Marshal(DispatchResult{Outcome: report.Outcome, ManagedCron: &report})
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,finished_at,result) VALUES($1,1,2,3,'{}',clock_timestamp(),$2::jsonb)`, project, string(raw)); err != nil {
		t.Fatal(err)
	}
	activity, err := ReadProjectActivity(ctx, state, project, time.Now().Add(-time.Hour))
	if err != nil || len(activity.Jobs) != 1 {
		t.Fatalf("read: %+v %v", activity, err)
	}
	job := activity.Jobs[0]
	if job.Outcome != "improved_database_observed" || job.Repair == nil || job.Repair.Kind != "cron" || len(job.Repair.Metrics) != 3 || !strings.Contains(job.Reason, "errors remain unobserved") || job.PlanNode != "" {
		t.Fatalf("scope overstated: %+v", job)
	}
}

func TestProjectActivityShowsManagedMemoryAndRestorationWithoutInventedMeasurements(t *testing.T) {
	ctx, state, _ := projectActivityTestState(t)
	const project = "managed-memory-activity"
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,1)`, project); err != nil {
		t.Fatal(err)
	}
	report := ManagedMemoryReport{FunctionOID: 77, Measurement: "matched_read_only_function_experiment", Before: ManagedMemorySample{Calls: 10, SortRows: 100000, MeanMS: 40, OuterTempBytes: 16 << 20, InnerTempBytes: 16 << 20, Result: 987654321}, After: &ManagedMemorySample{Calls: 10, SortRows: 100000, MeanMS: 10, Result: 987654321}, BeforeTraffic: &ManagedMaintenanceTraffic{ReadCalls: 10, WriteCalls: 10, ReadMeanMS: 1, WriteMeanMS: 1}, AfterTraffic: &ManagedMaintenanceTraffic{ReadCalls: 10, WriteCalls: 10, ReadMeanMS: 1, WriteMeanMS: 1}, Outcome: "improved_tested_inputs", Reason: "Matched result improved."}
	cases := []struct {
		report          ManagedMemoryReport
		outcome, action string
		metrics         int
	}{
		{report: report, outcome: "improved_tested_inputs", action: "recorded", metrics: 5},
	}
	restored := report
	restored.Restored = true
	restored.Outcome = "regressed"
	cases = append(cases, struct {
		report          ManagedMemoryReport
		outcome, action string
		metrics         int
	}{restored, "regressed", "restored", 5})
	missing := report
	missing.After = nil
	missing.BeforeTraffic = nil
	missing.AfterTraffic = nil
	cases = append(cases, struct {
		report          ManagedMemoryReport
		outcome, action string
		metrics         int
	}{missing, "inconclusive", "recorded", 0})
	for _, restore := range []bool{false, true} {
		action := "interrupted"
		if restore {
			action = "restored"
		}
		r := ManagedMemoryReport{FunctionOID: 77, Measurement: "interrupted_function_catalog_observation", Outcome: "repair_interrupted", Reason: "No benefit is claimed.", Restored: restore, RecoveryStatus: "catalog_checked"}
		cases = append(cases, struct {
			report          ManagedMemoryReport
			outcome, action string
			metrics         int
		}{r, "repair_interrupted", action, 0})
	}
	for i, c := range cases {
		raw, _ := json.Marshal(DispatchResult{Outcome: c.report.Outcome, ManagedMemory: &c.report})
		if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,finished_at,result) VALUES($1,1,2,$2,'{}',clock_timestamp(),$3::jsonb)`, project, i+1, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	activity, err := ReadProjectActivity(ctx, state, project, time.Now().Add(-time.Hour))
	if err != nil || len(activity.Jobs) != len(cases) {
		t.Fatalf("read: %+v %v", activity, err)
	}
	for _, job := range activity.Jobs {
		c := cases[job.Query-1]
		if job.Outcome != c.outcome || job.Repair == nil || job.Repair.Kind != "memory" || job.Repair.Action != c.action || len(job.Repair.Metrics) != c.metrics || job.Repair.Restored != c.report.Restored {
			t.Errorf("wrong memory result: %+v", job)
		}
		raw, _ := json.Marshal(job)
		if strings.Contains(string(raw), "987654321") {
			t.Errorf("result value leaked: %s", raw)
		}
	}
}

func TestProjectActivitySeparatesCleanupSettingFromActualAutomaticCleanup(t *testing.T) {
	ctx, state, _ := projectActivityTestState(t)
	const project = "managed-cleanup-activity"
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,1)`, project); err != nil {
		t.Fatal(err)
	}
	before := ManagedIndexMeasurement{Inputs: []int32{987654321}, Sample: TrialSample{Reads: 1, PlanReturned: 1, ReadMS: 20, Blocks: 100}}
	after := before
	after.Sample.ReadMS = 1
	after.Sample.Blocks = 2
	now := time.Now().UTC()
	target := ProjectIndexTarget{DatabaseOID: 1, TableOID: 777, Schema: "customer", Relation: "orders"}
	report := ManagedCleanupReport{Measurement: "same_sampled_input_and_normal_database_traffic", Before: before, After: &after, Outcome: "improved_tested_inputs", Trigger: CleanupTriggerReport{SettingChanged: true, AutomaticCleaned: true, Before: ProjectCleanupSettingsObservation{Target: target, At: now, Boot: now, DeadTuples: 100}, After: ProjectCleanupSettingsObservation{Target: target, At: now.Add(time.Minute), Boot: now, DeadTuples: 20}}, BeforeTraffic: &ManagedMaintenanceTraffic{ReadCalls: 10, WriteCalls: 10, ReadMeanMS: 1, WriteMeanMS: 1}, AfterTraffic: &ManagedMaintenanceTraffic{ReadCalls: 10, WriteCalls: 10, ReadMeanMS: 1, WriteMeanMS: 1}}
	cases := []struct {
		report          ManagedCleanupReport
		outcome, action string
		metrics         int
	}{
		{report, "improved_tested_inputs", "recorded", 5},
	}
	waiting := report
	waiting.Outcome = "awaiting_verification"
	waiting.Trigger.AutomaticCleaned = false
	waiting.After = nil
	waiting.AfterTraffic = nil
	cases = append(cases, struct {
		report          ManagedCleanupReport
		outcome, action string
		metrics         int
	}{waiting, "awaiting_verification", "recorded", 0})
	interrupted := report
	interrupted.Outcome = "repair_interrupted"
	interrupted.Trigger.Restored = true
	cases = append(cases, struct {
		report          ManagedCleanupReport
		outcome, action string
		metrics         int
	}{interrupted, "repair_interrupted", "interrupted", 0})
	restored := report
	restored.Outcome = "regressed"
	restored.Trigger.Restored = true
	cases = append(cases, struct {
		report          ManagedCleanupReport
		outcome, action string
		metrics         int
	}{restored, "regressed", "restored", 5})
	for i, c := range cases {
		raw, _ := json.Marshal(map[string]any{"outcome": c.report.Outcome, "managed_cleanup": c.report})
		if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,evidence,finished_at,result) VALUES($1,1,2,$2,'{}',clock_timestamp(),$3::jsonb)`, project, i+1, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	activity, err := ReadProjectActivity(ctx, state, project, time.Now().Add(-time.Hour))
	if err != nil || len(activity.Jobs) != len(cases) {
		t.Fatalf("read: %+v %v", activity, err)
	}
	for _, job := range activity.Jobs {
		c := cases[job.Query-1]
		if job.Outcome != c.outcome || job.Repair == nil || job.Repair.Kind != "vacuum" || job.Repair.Action != c.action || len(job.Repair.Metrics) != c.metrics {
			t.Errorf("wrong cleanup result: %+v", job)
		}
		raw, _ := json.Marshal(job)
		if strings.Contains(string(raw), "987654321") {
			t.Errorf("sample value leaked: %s", raw)
		}
	}
}

func TestActivityPagesKeepHourlyTotalsAndScope(t *testing.T) {
	ctx, state, _ := projectActivityTestState(t)
	const project = "example/retained-history"
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.projects(project_id,monitor_epoch) VALUES($1,1)`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, `INSERT INTO pooltune_worker.investigations(project_id,database_oid,role_oid,query_id,created_at,finished_at,evidence)
		SELECT $1,1,1,n,now()-n*interval '1 second',now(),'{}'::jsonb FROM generate_series(1,215)n`, project); err != nil {
		t.Fatal(err)
	}
	first, err := ReadProjectActivity(ctx, state, project, time.Now().Add(-24*time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, n := range first.HourlyJobs {
		total += n
	}
	if total != 215 || len(first.Jobs) != 41 {
		t.Fatalf("total=%d rows=%d", total, len(first.Jobs))
	}
	next, err := ReadProjectActivity(ctx, state, project, time.Now().Add(-24*time.Hour), first.Jobs[39].ID)
	if err != nil || len(next.Jobs) != 41 || next.Jobs[0].ID != first.Jobs[40].ID {
		t.Fatalf("unexpected history continuation: %v", err)
	}
}
