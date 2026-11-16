//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"
)

func TestManagedMemoryNativeOwnerFence(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t, "-c", "compute_query_id=on")
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	owner, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close(ctx)
	if _, err = db.Exec(ctx, `CREATE SCHEMA billing; CREATE TABLE billing.orders(id integer,payload text); INSERT INTO billing.orders VALUES(1,'a'); CREATE FUNCTION billing.order_total() RETURNS bigint LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER SET work_mem='4MB' SET search_path=pg_catalog SET max_parallel_workers_per_gather=0 SET jit=off AS 'BEGIN RETURN (SELECT sum(id) FROM (SELECT id FROM ONLY billing.orders ORDER BY payload,id OFFSET 0) AS sorted); END'`); err != nil {
		t.Fatal(err)
	}
	target := ProjectFunctionMemoryTarget{Project: "supabase/abcdefghijklmnopqrst/0123456789abcdef", Schema: "billing", Function: "order_total"}
	if err = db.QueryRow(ctx, `SELECT oid,'billing.order_total()'::regprocedure::oid FROM pg_database WHERE datname=current_database()`).Scan(&target.DatabaseOID, &target.FunctionOID); err != nil {
		t.Fatal(err)
	}
	fn, err := ReadManagedMemoryFunction(ctx, db, ManagedMemoryEnrollment{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = owner.Exec(ctx, `SET lock_timeout='50ms'`); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	ownerEdit := func(context.Context) error {
		attempts++
		_, editErr := owner.Exec(ctx, `CREATE OR REPLACE FUNCTION billing.order_total() RETURNS bigint LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER SET work_mem='4MB' SET search_path=pg_catalog SET max_parallel_workers_per_gather=0 SET jit=off AS 'BEGIN RETURN 2; END'`)
		var pgerr *pgconn.PgError
		if !errors.As(editErr, &pgerr) || pgerr.Code != "55P03" {
			t.Fatalf("native function lock did not fence owner replacement: %v", editErr)
		}
		return nil
	}
	h := supabaseproject.MemoryHeadroom{ProjectRef: "abcdefghijklmnopqrst", ObservedAt: time.Now(), AvailableBytes: 768 << 20, TotalBytes: 1 << 30}
	change, err := ApplyManagedMemory(ctx, db, fn, "managed-memory-owner-fence", h, ManagedMemoryGuards{CheckPermission: ownerEdit, RecordIntent: func(context.Context, ManagedMemoryIntent) error { return nil }, RecordApplied: func(context.Context, ManagedMemoryChange) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err = RestoreManagedMemory(ctx, db, change, ownerEdit); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("owner edit attempts=%d", attempts)
	}
	var definition string
	if err = db.QueryRow(ctx, `SELECT pg_get_functiondef($1)`, target.FunctionOID).Scan(&definition); err != nil || definition != fn.OriginalDefinition {
		t.Fatalf("restore changed baseline: %v", err)
	}
}

// QueryTracer supplies a deterministic concurrent owner write between the
// preflight catalog read and the native rename's tuple-lock acquisition.
type memoryFenceRaceTracer struct {
	edit  func()
	fired bool
}

func (t *memoryFenceRaceTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if !t.fired && strings.HasPrefix(d.SQL, "ALTER FUNCTION ") && strings.Contains(d.SQL, " RENAME TO ") {
		t.fired = true
		t.edit()
	}
	return ctx
}
func (*memoryFenceRaceTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestManagedMemoryFencePreservesEditsDuringAcquisition(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t)
	owner, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close(ctx)
	if _, err = owner.Exec(ctx, `CREATE SCHEMA billing;CREATE TABLE billing.orders(id integer,payload text);INSERT INTO billing.orders VALUES(1,'a');CREATE FUNCTION billing.order_total() RETURNS bigint LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER SET work_mem='4MB' SET search_path=pg_catalog SET max_parallel_workers_per_gather=0 SET jit=off AS 'BEGIN RETURN (SELECT sum(id) FROM (SELECT id FROM ONLY billing.orders ORDER BY payload,id OFFSET 0) AS sorted); END'`); err != nil {
		t.Fatal(err)
	}
	target := ProjectFunctionMemoryTarget{Project: "supabase/abcdefghijklmnopqrst/0123456789abcdef", Schema: "billing", Function: "order_total"}
	if err = owner.QueryRow(ctx, `SELECT oid,'billing.order_total()'::regprocedure::oid FROM pg_database WHERE datname=current_database()`).Scan(&target.DatabaseOID, &target.FunctionOID); err != nil {
		t.Fatal(err)
	}
	baseline, err := ReadManagedMemoryFunction(ctx, owner, ManagedMemoryEnrollment{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	guards := ManagedMemoryGuards{CheckPermission: func(context.Context) error { return nil }, RecordIntent: func(context.Context, ManagedMemoryIntent) error { return nil }, RecordApplied: func(context.Context, ManagedMemoryChange) error { return nil }}
	catalog := func() string {
		t.Helper()
		var out string
		if err := owner.QueryRow(ctx, `SELECT to_jsonb(p)::text||p.xmin::text FROM pg_proc p WHERE oid=$1`, target.FunctionOID).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, restore := range []bool{false, true} {
		for _, edit := range []string{`ALTER FUNCTION billing.order_total() COST 123`, `ALTER FUNCTION billing.order_total() SET work_mem='6MB'`, `REVOKE EXECUTE ON FUNCTION billing.order_total() FROM PUBLIC`, `CREATE OR REPLACE FUNCTION billing.order_total() RETURNS bigint LANGUAGE plpgsql AS 'BEGIN RETURN 2; END'`} {
			if _, err = owner.Exec(ctx, baseline.OriginalDefinition+`; GRANT EXECUTE ON FUNCTION billing.order_total() TO PUBLIC`); err != nil {
				t.Fatal(err)
			}
			fn, err := ReadManagedMemoryFunction(ctx, owner, ManagedMemoryEnrollment{Target: target})
			if err != nil {
				t.Fatal(err)
			}
			h := supabaseproject.MemoryHeadroom{ProjectRef: "abcdefghijklmnopqrst", ObservedAt: time.Now(), AvailableBytes: 768 << 20, TotalBytes: 1 << 30}
			var change ManagedMemoryChange
			if restore {
				change, err = ApplyManagedMemory(ctx, owner, fn, "managed-memory-race", h, guards)
				if err != nil {
					t.Fatal(err)
				}
			}
			var edited string
			tracer := &memoryFenceRaceTracer{edit: func() {
				if _, err := owner.Exec(ctx, edit); err != nil {
					t.Fatal(err)
				}
				edited = catalog()
			}}
			raceCfg := cfg.Copy()
			raceCfg.Tracer = tracer
			worker, err := pgx.ConnectConfig(ctx, raceCfg)
			if err != nil {
				t.Fatal(err)
			}
			if restore {
				err = RestoreManagedMemory(ctx, worker, change, guards.CheckPermission)
			} else {
				_, err = ApplyManagedMemory(ctx, worker, fn, "managed-memory-race", h, guards)
			}
			worker.Close(ctx)
			if !tracer.fired || !errors.Is(err, errManagedMemoryOwnerChanged) {
				t.Fatalf("owner race was not refused restore=%v edit=%s err=%v", restore, edit, err)
			}
			if catalog() != edited {
				t.Fatalf("owner edit overwritten restore=%v edit=%s", restore, edit)
			}
			if restore && !errors.Is(verifyManagedMemoryInstalled(ctx, owner, change), errManagedMemoryOwnerChanged) {
				t.Fatal("changed function passed final verification")
			}
		}
	}
}
