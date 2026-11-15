//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

func TestOwnedProjectVacuumChecksLiveQueryAndConfirmsVisibility(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "compute_query_id=on")
	const project = "supabase/osdjqwpzgyzwfaxyufze/1112223334445556"
	const schema = "pt_hosted_vc_1112223334445556"
	config, db := ownedTestWriter(t, ctx, config, schema)
	exec := func(sql string) {
		t.Helper()
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`COMMENT ON SCHEMA ` + schema + ` IS 'Pooltune hosted vacuum fixture ` + project + `';
		CREATE TABLE ` + schema + `.candidate(id integer PRIMARY KEY, lookup integer NOT NULL, padding text NOT NULL) WITH (autovacuum_enabled=false);
		INSERT INTO ` + schema + `.candidate SELECT n,n,repeat('p',64) FROM generate_series(1,100000) n ORDER BY (n::bigint*7919)%100003;
		CREATE INDEX candidate_lookup ON ` + schema + `.candidate(lookup);
		ANALYZE ` + schema + `.candidate;
		CREATE TABLE ` + schema + `.healthy(id integer PRIMARY KEY, padding text NOT NULL);
		INSERT INTO ` + schema + `.healthy SELECT n,'a' FROM generate_series(1,20) n;`)
	req := OwnedVacuumRequest{Workload: OwnedRepairWorkload{Project: project, RunID: "owned-vacuum-integration", Decision: Investigate,
		Target: ProjectIndexTarget{Schema: schema, Relation: "candidate"}}, IndexName: "candidate_lookup"}
	if err := db.QueryRow(ctx, `SELECT (SELECT oid FROM pg_database WHERE datname=current_database()), current_user::regrole::oid,
		to_regclass($1)::oid,to_regclass($2)::oid,to_regclass($3)::oid`,
		schema+".candidate", schema+".healthy", schema+".candidate_lookup").Scan(&req.Workload.Target.DatabaseOID,
		&req.Workload.RoleOID, &req.Workload.Target.TableOID, &req.Workload.HealthyOID, &req.IndexOID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "SET max_parallel_workers_per_gather=0; SET random_page_cost=1"); err != nil {
		t.Fatal(err)
	}
	p, err := scanTrialPlan(db.QueryRow(ctx, ownedMeasuredSQL+vacuumQuery(schema), int32(1), int32(50000)))
	if err != nil {
		t.Fatal(err)
	}
	req.Workload.QueryID = p.QueryID
	journal := 0
	allowed := func(context.Context, ProjectMaintenanceIntent) error { return nil }
	guards := ProjectMaintenanceGuards{CheckPermission: allowed, CheckCapacity: allowed, RecordIntent: func(_ context.Context, intent ProjectMaintenanceIntent) error {
		if intent.Target != req.Workload.Target || intent.Operation != ProjectVacuum {
			return errors.New("mismatched journal")
		}
		journal++
		return nil
	}}
	model := func(_ context.Context, e IndexEvidence) (IndexProposal, error) {
		return IndexProposal{RunID: e.RunID, TableOID: e.TableOID, Column: e.Column, Outcome: "propose", Repair: "vacuum", Reason: "Visibility work is high"}, nil
	}
	stale := req
	stale.Workload.QueryID++
	if _, err := RunOwnedProjectVacuum(ctx, config, stale, model, guards); err == nil || journal != 0 {
		t.Fatal("unbound app query was allowed to record VACUUM", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, 14*time.Second)
	defer cancel()
	out, err := RunOwnedProjectVacuum(runCtx, config, req, model, guards)
	if err != nil || journal != 1 || out.VacuumMS <= 0 || out.Before.HeapFetches < 1000 || out.After.HeapFetches > out.Before.HeapFetches/2 ||
		out.Outcome != "improved_tested_inputs" && out.Outcome != "inconclusive" {
		t.Fatalf("unverified owned VACUUM: %+v, journals=%d, err=%v", out, journal, err)
	}
	t.Logf("exact VACUUM changed hit tuples %.0f -> %.0f; sampled %.3f -> %.3f ms; verdict %s",
		out.Before.HeapFetches, out.After.HeapFetches, out.Before.ReadMS, out.After.ReadMS, out.Outcome)
}

func ownedTestWriter(t *testing.T, ctx context.Context, adminConfig *pgx.ConnConfig, schema string) (*pgx.ConnConfig, *pgx.Conn) {
	t.Helper()
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	role := pgx.Identifier{schema}.Sanitize()
	const password = "local-owned-test-writer"
	if _, err := admin.Exec(ctx, "CREATE ROLE "+role+" LOGIN PASSWORD '"+password+"'; CREATE SCHEMA "+role+" AUTHORIZATION "+role); err != nil {
		t.Fatal(err)
	}
	writer := adminConfig.Copy()
	writer.User, writer.Password = schema, password
	db, err := pgx.ConnectConfig(ctx, writer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeVacuumConnection(db) })
	return writer, db
}
