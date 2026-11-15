//go:build integration

package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

func TestProjectFunctionMemoryAppliesAndRestoresOrdinaryOverload(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	exec := func(sql string) {
		t.Helper()
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	inner := `SELECT sum(id) FROM (SELECT id FROM "Dispatch".shipments WHERE id >= 1 ORDER BY payload OFFSET 0) ordered`
	exec(`CREATE SCHEMA "Dispatch";
		CREATE TABLE "Dispatch".shipments AS SELECT i AS id, repeat(md5(i::text),5) AS payload FROM generate_series(1,65000) i;
		CREATE TABLE memory_journal (phase text, baseline text, candidate text);
		CREATE FUNCTION "Dispatch"."Rank Orders"(threshold int) RETURNS bigint LANGUAGE sql VOLATILE PARALLEL UNSAFE
		SET search_path=pg_catalog SET work_mem='1MB' SET max_parallel_workers_per_gather=0 SET jit=off AS $$` + inner + `$$;
		CREATE FUNCTION "Dispatch"."Rank Orders"(threshold bigint) RETURNS bigint LANGUAGE sql AS $$ SELECT 7::bigint $$;
		ANALYZE "Dispatch".shipments`)
	target := ProjectFunctionMemoryTarget{Project: "owned-local", Schema: "Dispatch", Function: "Rank Orders"}
	if err := db.QueryRow(ctx, `SELECT d.oid, '"Dispatch"."Rank Orders"(integer)'::regprocedure::oid FROM pg_database d WHERE datname=current_database()`).Scan(&target.DatabaseOID, &target.FunctionOID); err != nil {
		t.Fatal(err)
	}
	baseline, err := ReadProjectFunctionMemory(ctx, db, target)
	if err != nil || baseline.WorkMem != "1MB" {
		t.Fatalf("baseline %+v %v", baseline, err)
	}
	probe := func(sql string) (float64, int) {
		t.Helper()
		var raw []byte
		if err := db.QueryRow(ctx, `EXPLAIN (ANALYZE,BUFFERS,TIMING OFF, FORMAT JSON) `+sql).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		type node struct {
			Kind     string            `json:"Node Type"`
			Blocks   float64           `json:"Temp Written Blocks"`
			Children []json.RawMessage `json:"Plans"`
		}
		var root []struct {
			Plan json.RawMessage `json:"Plan"`
		}
		if err := json.Unmarshal(raw, &root); err != nil || len(root) != 1 {
			t.Fatal("invalid measured plan")
		}
		var visit func(json.RawMessage) (float64, int)
		visit = func(raw json.RawMessage) (float64, int) {
			var n node
			if err := json.Unmarshal(raw, &n); err != nil {
				t.Fatal(err)
			}
			sorts := 0
			if n.Kind == "Sort" {
				sorts++
			}
			for _, c := range n.Children {
				_, m := visit(c)
				sorts += m
			}
			return n.Blocks * 8192, sorts
		}
		return visit(root[0].Plan)
	}
	exec("SET work_mem='1MB'; SET max_parallel_workers_per_gather=0; SET jit=off")
	outer := `SELECT "Dispatch"."Rank Orders"(1::int)`
	external, _ := probe(outer)
	internal, sorts := probe(inner)
	if external < memoryMinSpill || internal < memoryMinSpill || sorts != 1 {
		t.Fatalf("spill outer %.0f inner %.0f sorts %d", external, internal, sorts)
	}
	evidence := ProjectFunctionMemoryEvidence{Baseline: baseline, RunID: "ordinary-memory", At: time.Now(), Spill: MemoryEvidence{
		FunctionOID: target.FunctionOID, TempBytes: external, InternalTempBytes: internal, SortNodes: sorts, MaxConcurrentCalls: 2,
		CandidateBytes: 24 << 20, AggregateBytes: 48 << 20, CurrentWorkMem: "1MB", CandidateWorkMem: "24MB", IdentityVerified: true}}
	denied := errors.New("owner denied")
	guards := ProjectFunctionMemoryGuards{
		CheckPermission: func(_ context.Context, i ProjectFunctionMemoryIntent) error {
			if i.Target != target || i.Signature != `"Dispatch"."Rank Orders"(pg_catalog.int4)` {
				return denied
			}
			return nil
		},
		CheckCapacity: func(_ context.Context, i ProjectFunctionMemoryIntent) error {
			if i.AggregateBytes != 48<<20 || i.CandidateBytes != 24<<20 || i.MaxConcurrentCalls != 2 {
				return denied
			}
			return nil
		},
		RecordIntent: func(ctx context.Context, i ProjectFunctionMemoryIntent) error {
			_, e := db.Exec(ctx, `INSERT INTO memory_journal VALUES ($1,$2,$3)`, i.Phase, i.PreviousWorkMem, i.AppliedWorkMem)
			return e
		},
	}
	bad := guards
	bad.RecordIntent = func(context.Context, ProjectFunctionMemoryIntent) error { return denied }
	if _, err := ApplyProjectFunctionMemory(ctx, cfg, target, evidence, bad); !errors.Is(err, denied) {
		t.Fatalf("journal refusal %v", err)
	}
	current, err := ReadProjectFunctionMemory(ctx, db, target)
	if err != nil || current != baseline {
		t.Fatalf("failed journal changed function: %+v %v", current, err)
	}
	for _, mutate := range []func(*ProjectFunctionMemoryEvidence){
		func(e *ProjectFunctionMemoryEvidence) { e.Spill.InternalTempBytes = 0 }, func(e *ProjectFunctionMemoryEvidence) { e.Spill.AggregateBytes-- }, func(e *ProjectFunctionMemoryEvidence) { e.At = time.Now().Add(-time.Minute) }, func(e *ProjectFunctionMemoryEvidence) { e.Spill.FunctionOID++ },
	} {
		unusable := evidence
		mutate(&unusable)
		if _, err := ApplyProjectFunctionMemory(ctx, cfg, target, unusable, guards); err == nil {
			t.Fatal("unsafe evidence admitted")
		}
	}
	change, err := ApplyProjectFunctionMemory(ctx, cfg, target, evidence, guards)
	if err != nil {
		t.Fatal(err)
	}
	current, err = ReadProjectFunctionMemory(ctx, db, target)
	if err != nil || current.Fingerprint != baseline.Fingerprint || current.WorkMemBytes != 24<<20 || current.Generation == baseline.Generation {
		t.Fatalf("wrong change %+v %v", current, err)
	}
	if bytes, _ := probe(outer); bytes != 0 {
		t.Fatalf("function still spilled %.0f bytes", bytes)
	}
	var value, protected int64
	if err := db.QueryRow(ctx, outer+`, "Dispatch"."Rank Orders"(1::bigint)`).Scan(&value, &protected); err != nil || value != 2112532500 || protected != 7 {
		t.Fatalf("results %d %d %v", value, protected, err)
	}
	if err := RestoreProjectFunctionMemory(ctx, cfg, change, guards); err != nil {
		t.Fatal(err)
	}
	current, err = ReadProjectFunctionMemory(ctx, db, target)
	if err != nil || current.WorkMem != baseline.WorkMem || current.Fingerprint != baseline.Fingerprint {
		t.Fatalf("restore %+v %v", current, err)
	}
	if bytes, _ := probe(outer); bytes < memoryMinSpill {
		t.Fatalf("baseline spill not restored %.0f", bytes)
	}
	var applies, restores int
	if err := db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE phase='apply'),count(*) FILTER(WHERE phase='restore') FROM memory_journal`).Scan(&applies, &restores); err != nil || applies != 1 || restores != 1 {
		t.Fatalf("durable journal %d %d %v", applies, restores, err)
	}
	if err := RestoreProjectFunctionMemory(ctx, cfg, change, guards); err == nil {
		t.Fatal("stale restore applied")
	}
	evidence.Baseline, evidence.At = current, time.Now()
	change, err = ApplyProjectFunctionMemory(ctx, cfg, target, evidence, guards)
	if err != nil {
		t.Fatal(err)
	}
	exec(`ALTER FUNCTION "Dispatch"."Rank Orders"(int) SET work_mem='24576kB'`)
	if err := RestoreProjectFunctionMemory(ctx, cfg, change, guards); err == nil {
		t.Fatal("restored an independently rewritten setting")
	}
	current, err = ReadProjectFunctionMemory(ctx, db, target)
	if err != nil || current.WorkMemBytes != 24<<20 || current.Generation == change.After.Generation {
		t.Fatalf("independent function change was lost: %+v %v", current, err)
	}
	other := target
	other.Schema = "public"
	if _, err := ReadProjectFunctionMemory(ctx, db, other); err == nil {
		t.Fatal("mismatched schema accepted")
	}
	exec(`ALTER FUNCTION "Dispatch"."Rank Orders"(int) OWNER TO fixture_app`)
	if _, err := ReadProjectFunctionMemory(ctx, db, target); err == nil {
		t.Fatal("direct connection under the former owner was accepted")
	}
}
