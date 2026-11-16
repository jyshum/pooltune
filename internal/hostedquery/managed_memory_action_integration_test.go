//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
	"pooltune/internal/testpg"
)

func TestManagedMemoryActionExpiryAndOwnerChange(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t, "-c", "compute_query_id=on")
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
	exec(`CREATE SCHEMA billing;CREATE TABLE billing.orders(id integer,payload text);INSERT INTO billing.orders SELECT i,repeat(md5(i::text),4) FROM generate_series(1,100000)i;CREATE FUNCTION billing.order_total() RETURNS bigint LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER SET work_mem='4MB' SET search_path=pg_catalog SET max_parallel_workers_per_gather=0 SET jit=off AS 'BEGIN RETURN (SELECT sum(id) FROM (SELECT id FROM ONLY billing.orders ORDER BY payload,id OFFSET 0) AS sorted); END';GRANT USAGE ON SCHEMA billing TO fixture_app;GRANT SELECT ON billing.orders TO fixture_app`)
	target := ProjectFunctionMemoryTarget{Project: "supabase/abcdefghijklmnopqrst/0123456789abcdef", Schema: "billing", Function: "order_total"}
	if err := db.QueryRow(ctx, `SELECT oid,'billing.order_total()'::regprocedure::oid FROM pg_database WHERE datname=current_database()`).Scan(&target.DatabaseOID, &target.FunctionOID); err != nil {
		t.Fatal(err)
	}
	fn, err := ReadManagedMemoryFunction(ctx, db, ManagedMemoryEnrollment{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	appCfg := cfg.Copy()
	appCfg.User = "fixture_app"
	app, err := pgx.ConnectConfig(ctx, appCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(ctx)

	var appRole uint32
	if err := app.QueryRow(ctx, `SELECT current_user::regrole::oid`).Scan(&appRole); err != nil {
		t.Fatal(err)
	}
	plan, err := scanTrialPlan(app.QueryRow(ctx, `EXPLAIN (ANALYZE,BUFFERS,VERBOSE,FORMAT JSON,TIMING OFF) SELECT billing.order_total()`))
	if err != nil || plan.QueryID == 0 {
		t.Fatalf("missing actual app invocation: %+v %v", plan, err)
	}
	before, err := measureManagedMemory(ctx, db, fn, appRole, plan.QueryID, nil)
	if err != nil || before.OuterTempBytes < memoryMinSpill || before.InnerTempBytes < memoryMinSpill {
		t.Fatalf("ordinary function spill: %+v %v", before, err)
	}

	// Refuse function/session-sensitive or RLS evidence without running it.
	exec(`ALTER TABLE billing.orders ENABLE ROW LEVEL SECURITY`)
	if _, err := ReadManagedMemoryFunction(ctx, db, ManagedMemoryEnrollment{Target: target}); err == nil {
		t.Fatal("RLS function relation admitted")
	}
	exec(`ALTER TABLE billing.orders DISABLE ROW LEVEL SECURITY`)
	malicious := strings.Replace(fn.OriginalDefinition, "BEGIN RETURN", "BEGIN PERFORM pg_catalog.pg_sleep(1000); RETURN", 1)
	exec(malicious)
	if _, err := ReadManagedMemoryFunction(ctx, db, ManagedMemoryEnrollment{Target: target}); err == nil {
		t.Fatal("unsupported function body admitted")
	}
	exec(fn.OriginalDefinition)
	fn, err = ReadManagedMemoryFunction(ctx, db, ManagedMemoryEnrollment{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	h := supabaseproject.MemoryHeadroom{ProjectRef: "abcdefghijklmnopqrst", ObservedAt: time.Now(), AvailableBytes: 768 << 20, TotalBytes: 1 << 30}
	recorded, applied := false, false
	guards := ManagedMemoryGuards{CheckPermission: func(context.Context) error { return nil }, RecordIntent: func(context.Context, ManagedMemoryIntent) error { recorded = true; return nil }, RecordApplied: func(context.Context, ManagedMemoryChange) error { applied = true; return nil }}
	bad := fn
	bad.Query = "SELECT 1"
	if _, err := ApplyManagedMemory(ctx, db, bad, "invalid", h, guards); err == nil {
		t.Fatal("caller query substitution admitted")
	}
	change, err := ApplyManagedMemory(ctx, db, fn, "managed-memory-1-1", h, guards)
	if err != nil {
		t.Fatal(err)
	}
	if !recorded || !applied {
		t.Fatal("missing durable intent or exact applied generation")
	}

	after, err := measureManagedMemory(ctx, db, fn, appRole, plan.QueryID, &change)
	if err != nil || after.Result != before.Result || after.OuterTempBytes > before.OuterTempBytes*.5 || after.InnerTempBytes > before.InnerTempBytes*.5 {
		t.Fatalf("matched function experiment: before%+v after%+v %v", before, after, err)
	}
	var workMem, body string
	if err := db.QueryRow(ctx, `SELECT p.prosrc,(SELECT substring(v,10) FROM unnest(p.proconfig)v WHERE v LIKE 'work_mem=%') FROM pg_proc p WHERE oid=$1`, target.FunctionOID).Scan(&body, &workMem); err != nil || workMem != "4MB" {
		t.Fatalf("baseline setting changed: %s %v", workMem, err)
	}
	var sum int64
	if err := app.QueryRow(ctx, `SELECT billing.order_total()`).Scan(&sum); err != nil || sum != 5000050000 {
		t.Fatalf("original application behavior: %d %v", sum, err)
	}
	var slots int
	if err := db.QueryRow(ctx, "SELECT slots FROM "+qualified(change.Intent.PolicySchema, "allowance")+"($1::oid)", target.FunctionOID).Scan(&slots); err != nil || slots != 3 {
		t.Fatalf("allowance missing: %d %v", slots, err)
	}
	exec("UPDATE " + qualified(change.Intent.PolicySchema, "allowances") + " SET valid_until=clock_timestamp()-interval '1 second'")
	if err := db.QueryRow(ctx, "SELECT slots FROM "+qualified(change.Intent.PolicySchema, "allowance")+"($1::oid)", target.FunctionOID).Scan(&slots); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired allowance stayed active: %v", err)
	}
	if err := app.QueryRow(ctx, `SELECT billing.order_total()`).Scan(&sum); err != nil || sum != 5000050000 {
		t.Fatalf("expiry changed result: %d %v", sum, err)
	}
	if err := RefreshManagedMemoryAllowance(ctx, db, change, h); err != nil {
		t.Fatal(err)
	}
	if err := RestoreManagedMemory(ctx, db, change, guards.CheckPermission); err != nil {
		t.Fatal(err)
	}
	var restored string
	if err := db.QueryRow(ctx, `SELECT pg_get_functiondef($1)`, target.FunctionOID).Scan(&restored); err != nil || restored != fn.OriginalDefinition {
		t.Fatalf("original definition not restored: %v", err)
	}
	fn, err = ReadManagedMemoryFunction(ctx, db, ManagedMemoryEnrollment{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	h.ObservedAt = time.Now()
	change, err = ApplyManagedMemory(ctx, db, fn, "managed-memory-2-1", h, guards)
	if err != nil {
		t.Fatal(err)
	}
	unconfirmed, err := reconcileManagedMemory(ctx, db, managedMemoryJournal{ManagedMemoryIntent: change.Intent}, guards.CheckPermission)
	if err != nil || unconfirmed.RecoveryStatus != "applied_generation_unconfirmed" || unconfirmed.Restored {
		t.Fatalf("lost acknowledgement invented ownership: %+v %v", unconfirmed, err)
	}
	recovered, err := reconcileManagedMemory(ctx, db, managedMemoryJournal{ManagedMemoryIntent: change.Intent, Applied: &change}, guards.CheckPermission)
	if err != nil || !recovered.Restored || recovered.RecoveryStatus != "original_definition_restored" {
		t.Fatalf("restart did not restore exact known change: %+v %v", recovered, err)
	}
	fn, err = ReadManagedMemoryFunction(ctx, db, ManagedMemoryEnrollment{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	h.ObservedAt = time.Now()
	change, err = ApplyManagedMemory(ctx, db, fn, "managed-memory-3-1", h, guards)
	if err != nil {
		t.Fatal(err)
	}
	exec(fmt.Sprintf("ALTER FUNCTION billing.order_total() COST %d", 110))
	if err := RestoreManagedMemory(ctx, db, change, guards.CheckPermission); err == nil {
		t.Fatal("owner edit overwritten")
	}
	if err := RefreshManagedMemoryAllowance(ctx, db, change, h); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("owner edit renewed allowance: %v", err)
	}
}
