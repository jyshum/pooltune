//go:build integration

package hostedquery

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestEstimatedEvidenceDispatch(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, `
		INSERT INTO pooltune_worker.projects (project_id) VALUES ('estimate');
		INSERT INTO pooltune_worker.investigations (project_id,database_oid,role_oid,query_id,evidence)
		VALUES ('estimate',1,2,3,'{}'),('estimate',1,2,4,'{}');
	`); err != nil {
		t.Fatal(err)
	}
	result, err := DispatchEstimatedInvestigation(ctx, state, "estimate", func(_ context.Context, job InvestigationJob) (*EstimatedLookupEvidence, error) {
		if job.Query != 3 {
			t.Fatalf("wrong job: %+v", job)
		}
		return &EstimatedLookupEvidence{TableOID: 12, ColumnNumber: 1, Estimated: EstimatedPlan{Node: "Seq Scan", OutputRows: 1}}, nil
	})
	if err != nil || result == nil || result.Outcome != "needs_measured_evidence" || result.Estimate == nil || result.Evidence != nil || result.Proposal != nil {
		t.Fatalf("estimate result: %+v, %v", result, err)
	}
	result, err = DispatchEstimatedInvestigation(ctx, state, "estimate", func(_ context.Context, job InvestigationJob) (*EstimatedLookupEvidence, error) { return nil, nil })
	if err != nil || result == nil || result.Outcome != "insufficient_evidence" {
		t.Fatalf("missing result: %+v, %v", result, err)
	}
	var saved bool
	err = state.QueryRow(ctx, `SELECT result ? 'estimated_evidence' AND NOT (result ? 'evidence') FROM pooltune_worker.investigations WHERE query_id=3 AND finished_at IS NOT NULL`).Scan(&saved)
	if err != nil || !saved {
		t.Fatalf("estimate was not persisted distinctly: %v %v", saved, err)
	}
}

func TestEnrolledDispatchReusesDetectorMeasurements(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	policy := SamplingPolicy{Interval: time.Second, Window: 2 * time.Second}
	for _, tc := range []struct {
		name        string
		age         time.Duration
		existing    bool
		legacy      bool
		missingMean bool
		planNode    string
		wantReason  string
	}{
		{name: "fresh", wantReason: "missing_measured_plan"},
		{name: "existing index", existing: true, wantReason: "matching_index_exists"},
		{name: "stale", age: 11 * time.Minute, wantReason: "missing_fresh_latency"},
		{name: "legacy", legacy: true, wantReason: "missing_fresh_latency"},
		{name: "missing mean", missingMean: true, wantReason: "missing_fresh_latency"},
		{name: "another plan", planNode: "Index Scan", wantReason: "no_estimated_sequential_scan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := now.Add(-tc.age - 10*time.Second)
			enrollment := Enrollment{Project: tc.name, Queries: []QueryObjective{
				{Database: 1, Role: 2, Query: 3, MeanMS: 1},
				{Database: 1, Role: 2, Query: 4, MeanMS: 1},
			}}
			var calls int64
			var executionMS float64
			for i := 0; i <= 10; i++ {
				if i > 0 {
					window := (i + 1) / 2
					batch := int64(4 * window)
					calls += batch
					executionMS += float64(batch) * float64(7+window)
				}
				poll, err := recordPoll(ctx, state, enrollment, policy, start.Add(time.Duration(i)*time.Second),
					func(context.Context) (map[queryKey]Counter, error) {
						slow := Counter{Database: 1, Role: 2, Query: 3, Boot: start, Reset: start, Entry: start,
							Calls: calls, ExecutionMS: executionMS}
						healthy := slow
						healthy.Query, healthy.ExecutionMS = 4, float64(calls)*0.25
						return map[queryKey]Counter{slow.key(): slow, healthy.key(): healthy}, nil
					})
				if err != nil || (i == 10 && (len(poll.Jobs) != 1 || poll.Jobs[0].Query != 3)) {
					t.Fatalf("poll %d: %+v, %v", i, poll, err)
				}
			}
			if tc.legacy {
				if _, err := state.Exec(ctx, "UPDATE pooltune_worker.investigations SET evidence=evidence-'recent_window_calls' WHERE project_id=$1", tc.name); err != nil {
					t.Fatal(err)
				}
			}
			if tc.missingMean {
				if _, err := state.Exec(ctx, "UPDATE pooltune_worker.investigations SET evidence=jsonb_set(evidence,'{recent_window_mean_ms,0}','null') WHERE project_id=$1", tc.name); err != nil {
					t.Fatal(err)
				}
			}
			node := tc.planNode
			if node == "" {
				node = "Seq Scan"
			}
			result, err := DispatchEstimatedInvestigation(ctx, state, tc.name, func(_ context.Context, job InvestigationJob) (*EstimatedLookupEvidence, error) {
				if job.Query != 3 || len(job.Generation) != 3 {
					t.Fatalf("wrong job: %+v", job)
				}
				return &EstimatedLookupEvidence{
					ObservedAt: now, TableOID: 12, ColumnNumber: 1, CoveringIndex: tc.existing,
					Estimated: EstimatedPlan{Node: node, OutputRows: 1},
				}, nil
			})
			if err != nil || result == nil || result.Outcome != "needs_measured_evidence" || result.Estimate == nil || result.Proposal != nil || result.Evidence != nil {
				t.Fatalf("result: %+v, %v", result, err)
			}
			var diagnosis string
			var observed []byte
			err = state.QueryRow(ctx, "SELECT coalesce(result->>'diagnostic_reason',''),result->'observed_latency' FROM pooltune_worker.investigations WHERE id=$1 AND finished_at IS NOT NULL", result.JobID).Scan(&diagnosis, &observed)
			if err != nil || diagnosis != tc.wantReason {
				t.Fatalf("saved reason: %q, %v", diagnosis, err)
			}
			if tc.age > 0 || tc.legacy || tc.missingMean {
				if observed != nil {
					t.Fatalf("incomplete or stale observations were reused: %s", observed)
				}
				return
			}
			var latency struct {
				Objective float64   `json:"objective_ms"`
				Means     []float64 `json:"recent_window_mean_ms"`
				Calls     []int64   `json:"recent_window_calls"`
				Through   time.Time `json:"observed_through"`
			}
			if err := json.Unmarshal(observed, &latency); err != nil {
				t.Fatal(err)
			}
			if latency.Objective != 1 || !latency.Through.Equal(now) ||
				!slices.Equal(latency.Means, []float64{8, 9, 10, 11, 12}) || !slices.Equal(latency.Calls, []int64{8, 16, 24, 32, 40}) {
				t.Fatalf("wrong saved detector measurements: %+v", latency)
			}
		})
	}
}
