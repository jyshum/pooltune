//go:build integration

package hostedquery

import (
	"context"
	"fmt"
	"testing"
	"time"

	"pooltune/internal/testpg"
)

func TestOwnedProjectMemoryBindsFunctionBodyAndVerifiesSpill(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t, "-c", "compute_query_id=on")
	const project = "supabase/osdjqwpzgyzwfaxyufze/1112223334445556"
	const schema = "pt_hosted_fn_1112223334445556"
	cfg, db := ownedTestWriter(t, ctx, cfg, schema)
	run := func(sql string) {
		t.Helper()
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	run(`COMMENT ON SCHEMA ` + schema + ` IS 'Pooltune hosted memory fixture ` + project + `';
		CREATE TABLE ` + schema + `.candidate(id integer PRIMARY KEY, lookup integer NOT NULL, padding text NOT NULL);
		INSERT INTO ` + schema + `.candidate SELECT n,n,repeat(md5(n::text),4) FROM generate_series(1,100000) n;
		CREATE TABLE ` + schema + `.healthy(id integer PRIMARY KEY, padding text NOT NULL);
		INSERT INTO ` + schema + `.healthy SELECT n,'a' FROM generate_series(1,20) n;
		ANALYZE ` + schema + `.candidate;`)
	var schemaOID uint32
	if err := db.QueryRow(ctx, `SELECT to_regnamespace($1)::oid`, schema).Scan(&schemaOID); err != nil {
		t.Fatal(err)
	}
	fn := qualified(schema, memoryFunction)
	definition := func(body string, replace bool) string {
		verb := "CREATE FUNCTION "
		if replace {
			verb = "CREATE OR REPLACE FUNCTION "
		}
		return verb + fn + `() RETURNS bigint LANGUAGE plpgsql VOLATILE PARALLEL UNSAFE SECURITY INVOKER
			SET search_path=pg_catalog SET work_mem='4MB' SET max_parallel_workers_per_gather=0 SET jit=off AS $memory$` + body + `$memory$`
	}
	body := OwnedMemoryFunctionBody(schema, schemaOID)
	run(definition(body, false) + "; REVOKE ALL ON FUNCTION " + fn + "() FROM PUBLIC")
	req := OwnedMemoryRequest{Workload: OwnedRepairWorkload{Project: project, RunID: "owned-memory-integration", Decision: Investigate,
		Target: ProjectIndexTarget{Schema: schema, Relation: "candidate"}}, Function: ProjectFunctionMemoryTarget{
		Project: project, Schema: schema, Function: memoryFunction}}
	if err := db.QueryRow(ctx, `SELECT (SELECT oid FROM pg_database WHERE datname=current_database()), current_user::regrole::oid,
		to_regclass($1)::oid,to_regclass($2)::oid,to_regprocedure($3)::oid`,
		schema+".candidate", schema+".healthy", fn+"()").Scan(&req.Workload.Target.DatabaseOID,
		&req.Workload.RoleOID, &req.Workload.Target.TableOID, &req.Workload.HealthyOID, &req.Function.FunctionOID); err != nil {
		t.Fatal(err)
	}
	req.Function.DatabaseOID = req.Workload.Target.DatabaseOID
	plan, err := scanTrialPlan(db.QueryRow(ctx, ownedMeasuredSQL+OwnedMemoryQuery(schema)))
	if err != nil {
		t.Fatal(err)
	}
	req.Workload.QueryID = plan.QueryID
	var phases []string
	allow := func(context.Context, ProjectFunctionMemoryIntent) error { return nil }
	guards := ProjectFunctionMemoryGuards{CheckCapacity: allow, CheckPermission: allow, RecordIntent: func(_ context.Context, action ProjectFunctionMemoryIntent) error {
		if action.Target != req.Function || action.RunID != req.Workload.RunID {
			return fmt.Errorf("unexpected journal: %+v", action)
		}
		phases = append(phases, action.Phase)
		return nil
	}}
	model := func(_ context.Context, e IndexEvidence) (IndexProposal, error) {
		return IndexProposal{RunID: e.RunID, TableOID: e.TableOID, Column: e.Column, Outcome: "propose", Repair: "memory", Reason: "Measured spill is attributable"}, nil
	}
	run(definition("BEGIN RETURN 5000050000; END", true))
	if _, err := RunOwnedProjectMemory(ctx, cfg, req, model, guards); err == nil || len(phases) != 0 {
		t.Fatal("modified function was used for an action", err)
	}
	run(definition(body, true))
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := RunOwnedProjectMemory(runCtx, cfg, req, model, guards)
	if err != nil || len(phases) == 0 || phases[0] != "apply" || result.MemoryMS <= 0 || result.Before.TempBytes < memoryMinSpill ||
		result.After.TempBytes > result.Before.TempBytes/2 || result.After.InternalTempBytes > result.Before.InternalTempBytes/2 ||
		result.Outcome != "improved_tested_inputs" && result.Outcome != "inconclusive" {
		t.Fatalf("unverified owned memory %+v phases=%v err=%v", result, phases, err)
	}
	if (result.Outcome == "inconclusive") != result.Restored {
		t.Fatalf("unexpected restoration: %+v", result)
	}
	t.Logf("function spill %.0f -> %.0f bytes; sampled %.3f -> %.3f ms; verdict %s",
		result.Before.TempBytes, result.After.TempBytes, result.Before.ReadMS, result.After.ReadMS, result.Outcome)
}
