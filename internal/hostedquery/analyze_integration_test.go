//go:build integration

package hostedquery

import (
	"context"
	"fmt"
	"testing"
	"time"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestQueuedAnalyzeRefreshesOnlyTheEnrolledColumn(t *testing.T) {
	t.Run("qualifying stale statistics", func(t *testing.T) { exerciseQueuedAnalyze(t, false) })
	t.Run("already refreshed", func(t *testing.T) { exerciseQueuedAnalyze(t, true) })
}

func exerciseQueuedAnalyze(t *testing.T, refreshWhileInvestigating bool) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	target, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close(ctx)
	for _, sql := range []string{"CREATE EXTENSION pg_stat_statements", "CREATE DATABASE analyze_state"} {
		if _, err := target.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	stateConfig := config.Copy()
	stateConfig.Database = "analyze_state"
	state, err := pgx.ConnectConfig(ctx, stateConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	fixture, err := CreateAnalyzeFixture(ctx, target, state, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := RemoveConsumerFixture(ctx, target, state, "local", fixture.ID); err != nil {
			t.Error(err)
		}
	}()
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(ctx, target, state, fixture.Enrollment, SamplingPolicy{Interval: time.Second, Window: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var found int32
	if err := target.QueryRow(ctx, "SELECT id FROM ONLY "+qualified(fixture.Schema, "candidate")+" WHERE lookup=$1", int32(43)).Scan(&found); err != nil || found != 43 {
		t.Fatalf("seed traffic: %d %v", found, err)
	}
	samples, err := worker.collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	query := fixture.Enrollment.Queries[1].key()
	counter := samples[query]
	if counter.Calls != 1 {
		t.Fatalf("missing ordinary query counters: %+v", counter)
	}
	when := time.Now().Add(-5 * time.Second)
	for tick := range 6 {
		counter.Calls++
		counter.ExecutionMS += 10
		if _, err := recordPoll(ctx, state, fixture.Enrollment, worker.policy, when.Add(time.Duration(tick)*time.Second),
			func(context.Context) (map[queryKey]Counter, error) { return map[queryKey]Counter{query: counter}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	result, err := DispatchFixtureAnalyze(ctx, state, config, "local", fixture, func(_ context.Context, e IndexEvidence) (IndexProposal, error) {
		if e.Analyze == nil || e.Analyze.EstimatedRows < 10 || e.Analyze.ActualRows != 1 || !e.Existing {
			return IndexProposal{}, fmt.Errorf("wrong or unmeasured analyze evidence: %+v", e)
		}
		if refreshWhileInvestigating {
			if _, err := target.Exec(ctx, "ANALYZE "+qualified(fixture.Schema, "candidate")+" (lookup)"); err != nil {
				return IndexProposal{}, err
			}
		}
		return IndexProposal{Outcome: "propose", Repair: "analyze", RunID: e.RunID, TableOID: e.TableOID, Column: e.Column, Reason: "stale lookup statistics"}, nil
	})
	if err != nil || result == nil || result.Analyze == nil {
		t.Fatalf("queued analyze: %+v, %v", result, err)
	}
	r := result.Analyze
	if refreshWhileInvestigating {
		if result.Outcome != "no_change" || r.AnalyzeMS != 0 || r.After.Reads != 0 || r.Reason != "the plan changed before the statistics refresh" {
			t.Fatalf("already refreshed fixture was analyzed again: %+v", r)
		}
		return
	}
	if r.Before.Plan != "Seq Scan" || r.After.Plan != "Index Scan" || r.After.EstimatedRows != 1 ||
		r.After.PlanReturned != 1 || r.Before.Reads != 10 || r.After.Writes != 10 {
		t.Fatalf("unexpected repaired workload: %+v", r)
	}
	if again, err := DispatchFixtureAnalyze(ctx, state, config, "local", fixture, ScriptedInvestigator); err != nil || again != nil {
		t.Fatalf("duplicate dispatch: %+v, %v", again, err)
	}

	// Simulate losing the first process after preparing the operation but before
	// saving its outcome. A retry must leave the database untouched.
	var analyzeBefore int64
	if err := target.QueryRow(ctx, "SELECT analyze_count FROM pg_stat_all_tables WHERE relid=$1", r.Evidence.TableOID).Scan(&analyzeBefore); err != nil {
		t.Fatal(err)
	}
	if err := writeAnalyzeStatus(ctx, state, "analyze_pending", r); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, `UPDATE pooltune_worker.investigations
		SET finished_at=NULL,lease_until=now()-interval '1 second',result=NULL WHERE id=$1`, result.JobID); err != nil {
		t.Fatal(err)
	}
	retry, err := DispatchFixtureAnalyze(ctx, state, config, "local", fixture, func(context.Context, IndexEvidence) (IndexProposal, error) {
		t.Fatal("retry invoked the investigator for a pending action")
		return IndexProposal{}, nil
	})
	if err != nil || retry == nil || retry.Outcome != "repair_interrupted" || retry.Attempt != 2 {
		t.Fatalf("unexpected retry: %+v, %v", retry, err)
	}
	var analyzeAfter int64
	if err := target.QueryRow(ctx, "SELECT analyze_count FROM pg_stat_all_tables WHERE relid=$1", r.Evidence.TableOID).Scan(&analyzeAfter); err != nil || analyzeAfter != analyzeBefore {
		t.Fatalf("retry changed ANALYZE count: %d -> %d, %v", analyzeBefore, analyzeAfter, err)
	}

}
