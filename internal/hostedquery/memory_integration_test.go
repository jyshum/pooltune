//go:build integration

package hostedquery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

func memoryTestFixture(t *testing.T) (context.Context, *pgx.ConnConfig, *pgx.Conn, *pgx.Conn, ConsumerFixture, fixtureObjects, MemoryIdentity) {
	t.Helper()
	ctx, cfg, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	target, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { target.Close(ctx) })
	for _, sql := range []string{"CREATE EXTENSION pg_stat_statements", "CREATE DATABASE memory_state"} {
		if _, err := target.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	sc := cfg.Copy()
	sc.Database = "memory_state"
	state, err := pgx.ConnectConfig(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close(ctx) })
	f, err := CreateMemoryFixture(ctx, target, state, "local")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := RemoveConsumerFixture(ctx, target, state, "local", f.ID); err != nil {
			t.Error(err)
		}
	})
	objects, err := expectedFixture(ctx, state, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	fn, err := expectedMemoryFunction(ctx, state, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, cfg, target, state, f, objects, fn
}
func TestMemoryMeasuresInternalSpillAndPreservesResults(t *testing.T) {
	ctx, _, target, _, f, objects, fn := memoryTestFixture(t)
	b, err := sampleMemory(ctx, target, f.ID, objects, fn, memoryOriginal)
	if err != nil {
		t.Fatal(err)
	}
	if b.TempBytes < 8388608 || b.InternalTempBytes < 8388608 || b.SortRows != 100000 || !b.ResultsCorrect {
		t.Fatalf("missing spill: %+v", b)
	}
	applied, err := changeMemorySetting(ctx, target, f.ID, objects, fn, memoryOriginal, memoryCandidate)
	if err != nil || !applied {
		t.Fatalf("apply %v %v", applied, err)
	}
	a, err := sampleMemory(ctx, target, f.ID, objects, fn, memoryCandidate)
	if err != nil {
		t.Fatal(err)
	}
	if a.TempBytes != 0 || a.InternalTempBytes != 0 || !a.ResultsCorrect {
		t.Fatalf("spill remains: %+v", a)
	}
	o, r := verifyMemory(b, a)
	t.Logf("before %.3f ms / %.0f temp bytes; after %.3f ms / %.0f temp bytes; %s: %s", b.ReadMS, b.TempBytes, a.ReadMS, a.TempBytes, o, r)
}
func TestMemoryAdmissionCapsCallsAndInternalDiagnostic(t *testing.T) {
	ctx, cfg, target, _, f, objects, fn := memoryTestFixture(t)
	for range 3 {
		c, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close(ctx)
		tx, err := c.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		var n int64
		if err := tx.QueryRow(ctx, memoryQuery(f.Schema)).Scan(&n); err != nil || n != 5000050000 {
			t.Fatalf("admitted %d %v", n, err)
		}
	}
	var n int64
	if err := target.QueryRow(ctx, memoryQuery(f.Schema)).Scan(&n); err == nil || !strings.Contains(err.Error(), "memory concurrency limit") {
		t.Fatalf("fourth call admitted: %v", err)
	}
	if _, err := sampleMemory(ctx, target, f.ID, objects, fn, memoryOriginal); err == nil {
		t.Fatal("diagnostic bypassed admission")
	}
}
func TestMemoryRejectsIdentityAndSettingDriftAndPreservesForeignObjects(t *testing.T) {
	ctx, _, target, state, f, objects, fn := memoryTestFixture(t)
	for _, mode := range []string{"setting", "body", "owner", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			name := qualified(f.Schema, memoryFunction) + "()"
			switch mode {
			case "setting":
				target.Exec(ctx, "ALTER FUNCTION "+name+" SET work_mem='16MB'")
			case "body":
				target.Exec(ctx, "CREATE OR REPLACE FUNCTION "+name+" RETURNS bigint LANGUAGE plpgsql AS 'BEGIN RETURN 0; END'")
			case "owner":
				target.Exec(ctx, "CREATE ROLE memory_other")
				target.Exec(ctx, "ALTER FUNCTION "+name+" OWNER TO memory_other")
			case "foreign":
				target.Exec(ctx, "CREATE FUNCTION "+qualified(f.Schema, "foreign_fn")+"() RETURNS int LANGUAGE sql AS 'SELECT 1'")
			}
			if mode != "foreign" {
				if applied, err := changeMemorySetting(ctx, target, f.ID, objects, fn, memoryOriginal, memoryCandidate); err == nil || applied {
					t.Fatalf("drift applied %v %v", applied, err)
				}
			}
			if err := RemoveConsumerFixture(ctx, target, state, "local", f.ID); err == nil {
				t.Fatal("changed or foreign function removed")
			}
			switch mode {
			case "setting":
				target.Exec(ctx, "ALTER FUNCTION "+name+" SET work_mem='4MB'")
			case "body":
				target.Exec(ctx, fn.Definition)
			case "owner":
				target.Exec(ctx, "ALTER FUNCTION "+name+" OWNER TO "+pgx.Identifier{target.Config().User}.Sanitize())
			case "foreign":
				target.Exec(ctx, "DROP FUNCTION "+qualified(f.Schema, "foreign_fn")+"()")
			}
		})
	}
}
func TestQueuedMemoryRejectsUnsafeOutcomesAndRestoresPendingChange(t *testing.T) {
	for _, mode := range []string{"apply", "healthy", "wrong repair", "wrong identity", "decline", "provider failure", "changed setting", "changed body", "verification failure", "independent candidate"} {
		t.Run(mode, func(t *testing.T) { exerciseQueuedMemory(t, mode) })
	}
}
func exerciseQueuedMemory(t *testing.T, mode string) {
	ctx, cfg, target, state, f, objects, fn := memoryTestFixture(t)
	if mode == "healthy" {
		if _, err := changeMemorySetting(ctx, target, f.ID, objects, fn, memoryOriginal, memoryCandidate); err != nil {
			t.Fatal(err)
		}
	}
	if err := PrepareWorkerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(ctx, target, state, f.Enrollment, SamplingPolicy{Interval: time.Second, Window: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	if err := target.QueryRow(ctx, memoryQuery(f.Schema)).Scan(&sum); err != nil || sum != 5000050000 {
		t.Fatal(err)
	}
	counters, err := worker.collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key := f.Enrollment.Queries[1].key()
	c := counters[key]
	if c.Calls != 1 {
		t.Fatalf("missing call: %+v", c)
	}
	start := time.Now().Add(-5 * time.Second)
	for tick := range 6 {
		c.Calls++
		c.ExecutionMS += 100
		if _, err := recordPoll(ctx, state, f.Enrollment, worker.policy, start.Add(time.Duration(tick)*time.Second), func(context.Context) (map[queryKey]Counter, error) { return map[queryKey]Counter{key: c}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	called := false
	dispatchConfig := cfg.Copy()
	trace := &interruptMemoryTrace{needle: "SET LOCAL search_path=pg_catalog; SET LOCAL statement_timeout='750ms'; SET LOCAL lock_timeout='250ms'; SET LOCAL transaction_timeout='10s'; SET LOCAL max_parallel_workers_per_gather=0; SET LOCAL jit=off; SET LOCAL work_mem='32MB'"}
	if mode == "verification failure" {
		dispatchConfig.Tracer = trace
	}
	binding := ConsumerFixture{ID: f.ID, Enrollment: Enrollment{Project: f.Enrollment.Project}}
	result, err := DispatchFixtureMemory(ctx, state, dispatchConfig, "local", binding, func(_ context.Context, e IndexEvidence) (IndexProposal, error) {
		called = true
		if !memoryCause(e) {
			t.Fatalf("weak cause %+v", e)
		}
		p := IndexProposal{Outcome: "propose", Repair: "memory", RunID: e.RunID, TableOID: e.TableOID, Column: e.Column, Reason: "attributed internal spill"}
		switch mode {
		case "wrong repair":
			p.Repair = "vacuum"
		case "wrong identity":
			p.TableOID++
		case "decline":
			p.Outcome = "no_change"
			p.Repair = ""
		case "provider failure":
			return IndexProposal{}, context.DeadlineExceeded
		case "independent candidate":
			if _, err := target.Exec(ctx, "ALTER FUNCTION "+qualified(f.Schema, memoryFunction)+"() SET work_mem='32MB'"); err != nil {
				t.Fatal(err)
			}
		case "changed setting":
			target.Exec(ctx, "ALTER FUNCTION "+qualified(f.Schema, memoryFunction)+"() SET work_mem='16MB'")
		case "changed body":
			target.Exec(ctx, "CREATE OR REPLACE FUNCTION "+qualified(f.Schema, memoryFunction)+"() RETURNS bigint LANGUAGE plpgsql AS 'BEGIN RETURN 0; END'")
		}
		return p, nil
	})
	if mode == "changed body" {
		defer target.Exec(ctx, fn.Definition)
	}
	if mode == "changed setting" {
		defer target.Exec(ctx, "ALTER FUNCTION "+qualified(f.Schema, memoryFunction)+"() SET work_mem='4MB'")
	}
	if mode == "healthy" && called {
		t.Fatal("healthy called model")
	}
	if mode != "healthy" && !called {
		t.Fatalf("missing model %v", err)
	}
	if mode == "independent candidate" {
		if err == nil {
			t.Fatal("changed setting was not detected")
		}
		if _, e := state.Exec(ctx, "UPDATE pooltune_worker.investigations SET lease_until=now()-interval '1 second' WHERE project_id=$1", f.Enrollment.Project); e != nil {
			t.Fatal(e)
		}
		if _, e := DispatchFixtureMemory(ctx, state, cfg, "local", binding, func(context.Context, IndexEvidence) (IndexProposal, error) {
			t.Fatal("retry called model")
			return IndexProposal{}, nil
		}); e != nil {
			t.Fatal(e)
		}
		current, e := readMemoryFunction(ctx, target, f.Schema)
		if e != nil || current.WorkMem != "32MB" {
			t.Fatalf("independent candidate restored: %s %v", current.WorkMem, e)
		}
		return
	}
	if mode == "verification failure" {
		if err == nil || !trace.seen {
			t.Fatalf("verification interruption missing: %v", err)
		}
		current, e := readMemoryFunction(ctx, target, f.Schema)
		if e != nil || current.WorkMem != "4MB" {
			t.Fatalf("failed verification left setting %s: %v", current.WorkMem, e)
		}
		return
	}
	if mode == "wrong repair" || mode == "wrong identity" || mode == "provider failure" || mode == "changed setting" || mode == "changed body" {
		if err == nil || result != nil {
			t.Fatalf("unsafe outcome %+v %v", result, err)
		}
		return
	}
	if err != nil || result == nil || result.Memory == nil {
		t.Fatalf("dispatch %+v %v", result, err)
	}
	if mode != "apply" {
		if result.Outcome != "no_change" {
			t.Fatal(result.Outcome)
		}
		return
	}
	r := result.Memory
	if r.After.TempBytes != 0 || !r.After.ResultsCorrect {
		t.Fatalf("missing repair %+v", r)
	}
	for _, status := range []string{"memory_verified", "memory_pending", "independent"} {
		if status != "memory_verified" {
			target.Exec(ctx, "ALTER FUNCTION "+qualified(f.Schema, memoryFunction)+"() SET work_mem='32MB'")
		}
		if status == "independent" {
			target.Exec(ctx, "ALTER FUNCTION "+qualified(f.Schema, memoryFunction)+"() SET work_mem='16MB'")
		}
		writeStatus := status
		if status == "independent" {
			writeStatus = "memory_pending"
		}
		if err := writeMemoryStatus(ctx, state, writeStatus, r); err != nil {
			t.Fatal(err)
		}
		if _, err := state.Exec(ctx, "UPDATE pooltune_worker.investigations SET attempts=1,finished_at=NULL,lease_until=now()-interval '1 second',result=NULL WHERE id=$1", result.JobID); err != nil {
			t.Fatal(err)
		}
		retry, err := DispatchFixtureMemory(ctx, state, cfg, "local", f, func(context.Context, IndexEvidence) (IndexProposal, error) {
			t.Fatal("recovery called model")
			return IndexProposal{}, nil
		})
		if err != nil || retry == nil {
			t.Fatalf("retry %+v %v", retry, err)
		}
		if status != "memory_verified" {
			if retry.Outcome != "repair_interrupted" {
				t.Fatalf("not interrupted %+v", retry)
			}
			current, err := readMemoryFunction(ctx, target, f.Schema)
			if err != nil {
				t.Fatal(err)
			}
			want := "4MB"
			if status == "independent" {
				want = "16MB"
			}
			if current.WorkMem != want {
				t.Fatalf("restore %s want %s", current.WorkMem, want)
			}
		}
	}
	target.Exec(ctx, "ALTER FUNCTION "+qualified(f.Schema, memoryFunction)+"() SET work_mem='4MB'")
}
func TestMemoryPreparingRecoveryPreservesReadyAndDrift(t *testing.T) {
	ctx, _, target, state, f, _, fn := memoryTestFixture(t)
	if _, err := state.Exec(ctx, "UPDATE "+journalTable+" SET status='memory_preparing' WHERE id=$1", f.ID); err != nil {
		t.Fatal(err)
	}
	next, err := CreateMemoryFixture(ctx, target, state, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer RemoveConsumerFixture(ctx, target, state, "local", next.ID)
	var gone bool
	if err := target.QueryRow(ctx, "SELECT to_regnamespace($1) IS NULL", f.Schema).Scan(&gone); err != nil || !gone {
		t.Fatalf("preparing not recovered %v %v", gone, err)
	}
	if err := recoverMemoryPreparations(ctx, target, state, "local"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, "UPDATE "+journalTable+" SET status='memory_preparing' WHERE id=$1", next.ID); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := state.QueryRow(ctx, "SELECT memory_function FROM "+journalTable+" WHERE id=$1", next.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &fn)
	target.Exec(ctx, "ALTER FUNCTION "+qualified(next.Schema, memoryFunction)+"() SET work_mem='16MB'")
	if err := recoverMemoryPreparations(ctx, target, state, "local"); err == nil {
		t.Fatal("recovery accepted drift")
	}
	target.Exec(ctx, "ALTER FUNCTION "+qualified(next.Schema, memoryFunction)+"() SET work_mem='4MB'")
}

type interruptMemoryTrace struct {
	needle string
	seen   bool
}

func (t *interruptMemoryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, t.needle) {
		t.seen = true
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		return ctx
	}
	return ctx
}
func (*interruptMemoryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestMemoryFailedPreparationRemovesCommittedFunction(t *testing.T) {
	ctx, cfg, target, state, _, _, _ := memoryTestFixture(t)
	trace := &interruptMemoryTrace{needle: "EXPLAIN (FORMAT JSON, VERBOSE) SELECT "}
	fc := cfg.Copy()
	fc.Tracer = trace
	failing, err := pgx.ConnectConfig(ctx, fc)
	if err != nil {
		t.Fatal(err)
	}
	defer failing.Close(ctx)
	failed, err := CreateMemoryFixture(ctx, failing, state, "local")
	if err == nil || !trace.seen || failed.ID == "" {
		t.Fatalf("missing setup interruption %+v %v", failed, err)
	}
	var gone bool
	if err := target.QueryRow(ctx, "SELECT to_regnamespace($1) IS NULL", failed.Schema).Scan(&gone); err != nil || !gone {
		t.Fatalf("failed preparation leaked function %v %v", gone, err)
	}
}

func TestMemoryReplacementOIDAndChangedOtherSettingsArePreserved(t *testing.T) {
	ctx, _, target, state, f, objects, fn := memoryTestFixture(t)
	name := qualified(f.Schema, memoryFunction) + "()"
	if _, err := target.Exec(ctx, "ALTER FUNCTION "+name+" SET jit=on"); err != nil {
		t.Fatal(err)
	}
	if _, err := sampleMemory(ctx, target, f.ID, objects, fn, memoryOriginal); err == nil {
		t.Fatal("changed non-memory setting admitted")
	}
	if restored, err := restoreMemory(ctx, target, f.ID, objects, &MemoryReport{Function: fn, OriginalWorkMem: memoryOriginal, AppliedWorkMem: memoryCandidate}); err != nil || restored {
		t.Fatalf("independent setting overwritten: %v %v", restored, err)
	}
	if _, err := target.Exec(ctx, "ALTER FUNCTION "+name+" SET jit=off"); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(ctx, "DROP FUNCTION "+name); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(ctx, fn.Definition); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(ctx, "REVOKE ALL ON FUNCTION "+name+" FROM PUBLIC"); err != nil {
		t.Fatal(err)
	}
	if _, err := sampleMemory(ctx, target, f.ID, objects, fn, memoryOriginal); err == nil {
		t.Fatal("replacement function admitted")
	}
	if err := RemoveConsumerFixture(ctx, target, state, "local", f.ID); err == nil {
		t.Fatal("replacement function deleted")
	}
	var exists bool
	if err := target.QueryRow(ctx, "SELECT to_regprocedure($1) IS NOT NULL", name).Scan(&exists); err != nil || !exists {
		t.Fatal("replacement not preserved")
	}
	// Dispose the replacement explicitly inside this disposable test database.
	if _, err := target.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{f.Schema}.Sanitize()+" CASCADE"); err != nil {
		t.Fatal(err)
	}
}
