//go:build integration

package hostedquery

import (
	"context"
	"testing"
	"time"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestQueuedIndexRepairsTheEnrolledFixtureOnce(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	target, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close(ctx)
	if _, err := target.Exec(ctx, "CREATE EXTENSION pg_stat_statements"); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(ctx, "CREATE DATABASE queue_state"); err != nil {
		t.Fatal(err)
	}
	stateConfig := config.Copy()
	stateConfig.Database = "queue_state"
	state, err := pgx.ConnectConfig(ctx, stateConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	fixture, err := CreateConsumerFixture(ctx, target, state, "local")
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
	var row int32
	if err := target.QueryRow(ctx, "SELECT id FROM "+qualified(fixture.Schema, "candidate")+" WHERE lookup=$1", int32(43)).Scan(&row); err != nil {
		t.Fatal(err)
	}
	samples, err := worker.collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	query := fixture.Enrollment.Queries[1].key()
	counter := samples[query]
	if counter.Calls != 1 {
		t.Fatalf("query counters: %+v", counter)
	}
	start := time.Now().Add(-5 * time.Second)
	var id int64
	for tick := range 6 {
		counter.Calls++
		counter.ExecutionMS += 8
		counter.Blocks += 1000
		result, err := recordPoll(ctx, state, fixture.Enrollment, worker.policy, start.Add(time.Duration(tick)*time.Second),
			func(context.Context) (map[queryKey]Counter, error) { return map[queryKey]Counter{query: counter}, nil })
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Jobs) == 1 {
			id = result.Jobs[0].ID
		}
	}
	if id == 0 {
		t.Fatal("detector queued no job")
	}
	result, err := DispatchFixtureRepair(ctx, state, config, "local", fixture, ScriptedInvestigator)
	if err != nil || result == nil || result.JobID != id || result.Repair == nil || !result.Repair.IndexValid ||
		result.Repair.Before.Plan != "Seq Scan" || result.Repair.After.Plan != "Index Scan" || result.Repair.After.Writes != 40 ||
		result.Proposal == nil || result.Proposal.TableOID != result.Repair.Evidence.TableOID || result.ThreadID != result.Repair.Proposal.RunID {
		t.Fatalf("queued repair = %+v, %v", result, err)
	}
	// A separate trial's recovery must not consume a queue-owned verified action.
	if err := RecoverIndexTrials(ctx, target, state, "local"); err != nil {
		t.Fatal(err)
	}
	index, err := inspectTrialIndex(ctx, target, fixture.Schema)
	if err != nil || !index.Valid {
		t.Fatalf("standalone recovery touched a queued index: %+v, %v", index, err)
	}
	// Simulate loss of the consumer after it wrote the verified repair but before
	// it could finish the queue row. The next claimant must reuse that report.
	if _, err := state.Exec(ctx, `UPDATE pooltune_worker.investigations
		SET finished_at=NULL, lease_until=now()-interval '1 second', result=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	replayed, err := DispatchFixtureRepair(ctx, state, config, "local", fixture, func(context.Context, IndexEvidence) (IndexProposal, error) {
		t.Fatal("retry invoked the investigator")
		return IndexProposal{}, nil
	})
	if err != nil || replayed == nil || replayed.Attempt != 2 || replayed.Repair == nil ||
		replayed.Repair.Outcome != result.Repair.Outcome || replayed.ThreadID != result.ThreadID || replayed.ObservedLatency == nil {
		t.Fatalf("retry did not restore the verified report: %+v, %v", replayed, err)
	}

	var terminal bool
	if err := state.QueryRow(ctx, "SELECT finished_at IS NOT NULL AND result->'repair'->>'index_valid'='true' FROM pooltune_worker.investigations WHERE id=$1", id).Scan(&terminal); err != nil || !terminal {
		t.Fatalf("terminal result = %v, %v", terminal, err)
	}
	if second, err := DispatchFixtureRepair(ctx, state, config, "local", fixture, ScriptedInvestigator); err != nil || second != nil {
		t.Fatalf("duplicate dispatch: %+v, %v", second, err)
	}

	// A crash after PostgreSQL built the index leaves the exact queued fixture
	// pending. A fresh recovery must not consume a different fixture.
	other, err := CreateConsumerFixture(ctx, target, state, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := RemoveConsumerFixture(ctx, target, state, "local", other.ID); err != nil {
			t.Error(err)
		}
	}()
	if err := writeTrialStatus(ctx, state, fixture.ID, "queued_building", nil, result.Repair); err != nil {
		t.Fatal(err)
	}
	observed, err := recoverQueuedFixture(ctx, target, state, fixture.ID)
	if err != nil || observed != "valid" {
		t.Fatalf("queued reconciliation: %q, %v", observed, err)
	}
	assertTrialRemoved(t, ctx, target, fixture.ID)
	if _, err := loadFixture(ctx, target, other.ID); err != nil {
		t.Fatalf("queued recovery affected another fixture: %v", err)
	}

}
