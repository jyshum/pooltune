//go:build integration

package hostedquery

import (
	"context"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
	"strings"
	"testing"
	"time"
)

func compoundFixtureForTest(t *testing.T) (context.Context, *pgx.Conn, *pgx.Conn, ConsumerFixture, fixtureObjects) {
	t.Helper()
	ctx, cfg, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	target, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { target.Close(context.Background()) })
	if _, err = target.Exec(ctx, "CREATE EXTENSION pg_stat_statements"); err != nil {
		t.Fatal(err)
	}
	if _, err = target.Exec(ctx, "CREATE DATABASE compound_state"); err != nil {
		t.Fatal(err)
	}
	sc := cfg.Copy()
	sc.Database = "compound_state"
	state, err := pgx.ConnectConfig(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close(context.Background()) })
	f, err := CreateCompoundFixture(ctx, target, state, "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := RemoveConsumerFixture(context.Background(), target, state, "local", f.ID); err != nil {
			t.Error(err)
		}
	})
	expected, err := expectedFixture(ctx, state, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, target, state, f, expected
}
func TestCompoundRepairMeasuresBothKeysAndRecoversExactIndex(t *testing.T) {
	ctx, target, state, f, expected := compoundFixtureForTest(t)
	before, err := sampleCompoundTrial(ctx, target, f.ID, expected)
	if err != nil {
		t.Fatal(err)
	}
	if before.Plan != "Seq Scan" || before.PlanExamined != fixtureRows || before.Reads != 40 {
		t.Fatalf("before %+v", before)
	}
	// Mechanism qualification uses measured plans; the end-to-end latency gate
	// is exercised by the queued path and may honestly be inconclusive on fast hosts.
	e, err := compoundTrialEvidence(ctx, target, f.ID, before)
	if err != nil {
		t.Fatal(err)
	}
	e.Decision = Investigate
	p := IndexProposal{Outcome: "propose", RunID: f.ID, TableOID: e.TableOID, Column: 2, Columns: []int16{2, 3}, Reason: "Two enrolled equality predicates."}
	report := IndexTrialReport{RunID: f.ID, JobID: 1, Evidence: e, Proposal: p, Before: before}
	report, err = applyIndexTrial(ctx, target, state, report, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !report.IndexValid || (report.After.Plan != "Index Scan" && report.After.Plan != "Bitmap Heap Scan") || report.After.PlanExamined != 1 || report.After.Writes != 40 {
		t.Fatalf("after %+v", report)
	}
	index, err := inspectCompoundIndex(ctx, target, f.Schema)
	if err != nil || !index.Matches {
		t.Fatalf("index %+v %v", index, err)
	}
	if _, err = state.Exec(ctx, "UPDATE "+journalTable+" SET status='compound_building' WHERE id=$1", f.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := pgx.ConnectConfig(ctx, target.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close(ctx)
	observed, err := recoverQueuedCompound(ctx, fresh, state, f.ID)
	if err != nil || observed != "valid" {
		t.Fatalf("recovery %q %v", observed, err)
	}
}
func TestCompoundCatalogRefusesEquivalentIndexAndPreservesForeignDefinition(t *testing.T) {
	ctx, target, state, f, _ := compoundFixtureForTest(t)
	table := qualified(f.Schema, "candidate")
	if _, err := target.Exec(ctx, "CREATE INDEX "+pgx.Identifier{trialIndex}.Sanitize()+" ON "+table+" (padding,lookup)"); err != nil {
		t.Fatal(err)
	}
	e, err := collectCompoundCatalog(ctx, target, f.ID)
	if err != nil || !e.Existing {
		t.Fatalf("equivalent index %+v %v", e, err)
	}
	if _, err = cleanTrial(ctx, target, state, f.ID); err == nil {
		t.Fatal("cleanup dropped unapproved reverse-order index")
	}
	var exists bool
	if err = target.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", qualified(f.Schema, trialIndex)).Scan(&exists); err != nil || !exists {
		t.Fatal("foreign index lost")
	}
	if _, err = target.Exec(ctx, "DROP INDEX "+qualified(f.Schema, trialIndex)); err != nil {
		t.Fatal(err)
	}
}
func TestCompoundEvidenceReaderRevalidatesEnrolledIdentity(t *testing.T) {
	ctx, target, state, f, expected := compoundFixtureForTest(t)
	before, err := sampleCompoundTrial(ctx, target, f.ID, expected)
	if err != nil {
		t.Fatal(err)
	}
	e, err := compoundTrialEvidence(ctx, target, f.ID, before)
	if err != nil {
		t.Fatal(err)
	}
	e.Decision = Investigate
	read := CompoundDiagnosticEvidence(state, target.Config(), "local", f.ID)
	for _, tool := range []string{"plan", "indexes"} {
		o, ok, err := read(ctx, tool, e)
		if err != nil || !ok || len(o.Columns) != 2 {
			t.Fatalf("%s %+v %v %v", tool, o, ok, err)
		}
	}
	if _, ok, err := read(ctx, "memory", e); err != nil || ok {
		t.Fatal("unsupported tool available")
	}
	for _, change := range []func(*IndexEvidence){func(e *IndexEvidence) { e.Columns = []int16{3, 2} }, func(e *IndexEvidence) { e.Columns = []int16{2, 4} }, func(e *IndexEvidence) { e.TableOID++ }, func(e *IndexEvidence) { e.At = time.Now().Add(-time.Minute) }} {
		bad := e
		change(&bad)
		if _, ok, err := read(ctx, "plan", bad); err == nil || ok {
			t.Fatal("changed identity accepted")
		}
	}
}

func TestCompoundSingleKeysMatchManyRowsButConjunctionOne(t *testing.T) {
	ctx, target, _, f, _ := compoundFixtureForTest(t)
	for _, input := range inputIDs {
		var first, second, both int64
		if err := target.QueryRow(ctx, "SELECT count(*) FILTER(WHERE lookup=$1),count(*) FILTER(WHERE padding=$2),count(*) FILTER(WHERE lookup=$1 AND padding=$2) FROM "+qualified(f.Schema, "candidate"), indexReadArgs(input, true)...).Scan(&first, &second, &both); err != nil {
			t.Fatal(err)
		}
		if first < 24000 || second < 24000 || both != 1 {
			t.Fatalf("input %d: first=%d second=%d combined=%d", input, first, second, both)
		}
	}
}

func TestQueuedCompoundBindsOrderedKeysAndReusesCompletedReport(t *testing.T) {
	ctx, target, state, f, _ := compoundFixtureForTest(t)
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(ctx, target, state, f.Enrollment, SamplingPolicy{Interval: time.Second, Window: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var row int32
	if err := target.QueryRow(ctx, compoundQuery(f.Schema), indexReadArgs(inputIDs[0], true)...).Scan(&row); err != nil || row != inputIDs[0] {
		t.Fatal(err)
	}
	counters, err := worker.collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := f.Enrollment.Queries[1].key()
	counter := counters[key]
	if counter.Calls != 1 {
		t.Fatalf("counter %+v", counter)
	}
	start := time.Now().Add(-5 * time.Second)
	var job int64
	for tick := range 6 {
		counter.Calls++
		counter.ExecutionMS += 8
		counter.Blocks += 2600
		poll, err := recordPoll(ctx, state, f.Enrollment, worker.policy, start.Add(time.Duration(tick)*time.Second), func(context.Context) (map[queryKey]Counter, error) { return map[queryKey]Counter{key: counter}, nil })
		if err != nil {
			t.Fatal(err)
		}
		if len(poll.Jobs) == 1 {
			job = poll.Jobs[0].ID
		}
	}
	if job == 0 {
		t.Fatal("not queued")
	}
	result, err := DispatchFixtureCompound(ctx, state, target.Config(), "local", f, ScriptedInvestigator)
	if err != nil || result == nil || result.Repair == nil || !result.Repair.IndexValid || len(result.Proposal.Columns) != 2 {
		t.Fatalf("dispatch %+v %v", result, err)
	}
	if result.Repair.Before.PlanExamined != 100000 || result.Repair.After.PlanExamined != 1 || result.Repair.After.Writes != 40 {
		t.Fatal("incomplete measurement")
	}
	if _, err = state.Exec(ctx, "UPDATE pooltune_worker.investigations SET finished_at=NULL,lease_until=now()-interval '1 second',result=NULL WHERE id=$1", job); err != nil {
		t.Fatal(err)
	}
	replay, err := DispatchFixtureCompound(ctx, state, target.Config(), "local", f, func(context.Context, IndexEvidence) (IndexProposal, error) {
		t.Fatal("replayed model")
		return IndexProposal{}, nil
	})
	if err != nil || replay == nil || replay.Repair == nil || len(replay.Proposal.Columns) != 2 || replay.ThreadID != result.ThreadID {
		t.Fatalf("replay %+v %v", replay, err)
	}
}

func TestCompoundReaderAllowsOrdinaryConcurrentWrites(t *testing.T) {
	ctx, target, state, f, expected := compoundFixtureForTest(t)
	before, err := sampleCompoundTrial(ctx, target, f.ID, expected)
	if err != nil {
		t.Fatal(err)
	}
	e, err := compoundTrialEvidence(ctx, target, f.ID, before)
	if err != nil {
		t.Fatal(err)
	}
	e.Decision = Investigate
	tx, err := target.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "UPDATE "+qualified(f.Schema, "candidate")+" SET lookup=lookup WHERE id=99999"); err != nil {
		t.Fatal(err)
	}
	read := CompoundDiagnosticEvidence(state, target.Config(), "local", f.ID)
	if _, ok, err := read(ctx, "plan", e); err != nil || !ok {
		t.Fatalf("ordinary writer blocked inspection: ok=%v err=%v", ok, err)
	}
}

func TestCompoundPreparationRecoveryDiscoversOnlyInterruptedOwnedWork(t *testing.T) {
	for _, mode := range []string{"global recovery", "next setup"} {
		t.Run(mode, func(t *testing.T) {
			ctx, target, state, f, _ := compoundFixtureForTest(t)
			live, err := CreateCompoundFixture(ctx, target, state, "local")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := RemoveConsumerFixture(ctx, target, state, "local", live.ID); err != nil {
					t.Error(err)
				}
			}()
			if _, err := state.Exec(ctx, "UPDATE "+journalTable+" SET status='compound_preparing' WHERE id=$1", f.ID); err != nil {
				t.Fatal(err)
			}
			if mode == "global recovery" {
				if err := RecoverIndexTrials(ctx, target, state, "local"); err != nil {
					t.Fatal(err)
				}
			} else {
				next, err := CreateCompoundFixture(ctx, target, state, "local")
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := RemoveConsumerFixture(ctx, target, state, "local", next.ID); err != nil {
						t.Error(err)
					}
				}()
			}
			var gone, kept bool
			if err := target.QueryRow(ctx, "SELECT to_regnamespace($1) IS NULL,to_regnamespace($2) IS NOT NULL", f.Schema, live.Schema).Scan(&gone, &kept); err != nil {
				t.Fatal(err)
			}
			if !gone || !kept {
				t.Fatalf("interrupted removed=%v live preserved=%v", gone, kept)
			}
		})
	}
}

func TestCompoundRecoveryRejectsChangedDurableDefinition(t *testing.T) {
	ctx, target, state, f, _ := compoundFixtureForTest(t)
	if _, err := state.Exec(ctx, "UPDATE "+journalTable+" SET status='compound_building' WHERE id=$1", f.ID); err != nil {
		t.Fatal(err)
	}
	if err := buildCompoundIndex(ctx, target, f.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, "UPDATE "+journalTable+" SET index_definition=$2 WHERE id=$1", f.ID, trialDefinition); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := state.Exec(ctx, "UPDATE "+journalTable+" SET index_definition=$2 WHERE id=$1", f.ID, compoundDefinition); err != nil {
			t.Error(err)
		}
	}()
	if _, err := recoverQueuedCompound(ctx, target, state, f.ID); err == nil {
		t.Error("recovery accepted changed durable definition")
	}
	if _, err := cleanTrial(ctx, target, state, f.ID); err == nil {
		t.Error("direct cleanup accepted changed durable definition")
	}
	var preserved bool
	if err := target.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", qualified(f.Schema, trialIndex)).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("mismatched artifact preserved=%v err=%v", preserved, err)
	}
}

type cancelCompoundPreparation struct{ cancel context.CancelFunc }

func (t *cancelCompoundPreparation) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, " SET lookup=($1::int[])") {
		t.cancel()
	}
	return ctx
}
func (*cancelCompoundPreparation) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestCompoundFailedPreparationCleansCommittedBaseWithFreshConnection(t *testing.T) {
	ctx, target, state, live, _ := compoundFixtureForTest(t)
	trial, cancel := context.WithCancel(ctx)
	defer cancel()
	cfg := target.Config().Copy()
	cfg.Tracer = &cancelCompoundPreparation{cancel: cancel}
	failing, err := pgx.ConnectConfig(trial, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer failing.Close(context.Background())
	abandoned, err := CreateCompoundFixture(trial, failing, state, "local")
	if err == nil || abandoned.ID == "" {
		t.Fatalf("fault did not interrupt preparation: %+v %v", abandoned, err)
	}
	var gone, kept bool
	if err := target.QueryRow(ctx, "SELECT to_regnamespace($1) IS NULL,to_regnamespace($2) IS NOT NULL", abandoned.Schema, live.Schema).Scan(&gone, &kept); err != nil {
		t.Fatal(err)
	}
	if !gone || !kept {
		t.Fatalf("failed preparation removed=%v live preserved=%v", gone, kept)
	}
}
