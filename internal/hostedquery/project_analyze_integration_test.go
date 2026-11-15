//go:build integration

package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestRunProjectAnalyzeRefreshesOnlyTheAuthorizedColumn(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "compute_query_id=on")
	admin := openMonitorConn(t, ctx, config)
	const schema, relation = "Invoices", "Unpaid"
	const setup = `
		CREATE SCHEMA "Invoices";
		CREATE TABLE "Invoices"."Unpaid" (id int PRIMARY KEY, note text, "Due Code" int)
			WITH (autovacuum_enabled=false);
		CREATE INDEX unpaid_due_code ON "Invoices"."Unpaid" ("Due Code");
		CREATE TABLE "Invoices".guard (id int PRIMARY KEY, entered boolean);
		INSERT INTO "Invoices".guard VALUES(1,false);
		INSERT INTO "Invoices"."Unpaid" SELECT n,'old distribution',43 FROM generate_series(1,100000) n;
		ANALYZE "Invoices"."Unpaid" ("Due Code");
		TRUNCATE "Invoices"."Unpaid";
		INSERT INTO "Invoices"."Unpaid" SELECT n,'new distribution',n FROM generate_series(1,100000) n;
		CREATE TABLE "Invoices".intents(run_id text PRIMARY KEY, target oid, fields smallint[]);
		GRANT USAGE ON SCHEMA "Invoices" TO fixture_app;
		GRANT MAINTAIN ON "Invoices"."Unpaid" TO fixture_app;
		SET max_parallel_workers_per_gather=0;
		SET jit=off;`
	if _, err := admin.Exec(ctx, setup); err != nil {
		t.Fatal(err)
	}
	target := ProjectIndexTarget{Schema: schema, Relation: relation}
	var appRole uint32
	if err := admin.QueryRow(ctx, `SELECT oid,'"Invoices"."Unpaid"'::regclass::oid,current_user::regrole::oid
		FROM pg_database WHERE datname=current_database()`).Scan(&target.DatabaseOID, &target.TableOID, &appRole); err != nil {
		t.Fatal(err)
	}
	const query = `SELECT id FROM ONLY "Invoices"."Unpaid" WHERE "Due Code"=$1`
	plan, err := projectAnalyzeTestPlan(ctx, admin, query)
	if err != nil || plan.QueryID == 0 {
		t.Fatalf("no measured query identity: %+v %v", plan, err)
	}
	request := ProjectAnalyzeRequest{
		Target: target, RunID: "owned-project-analyze", DatabaseRoleOID: appRole, QueryID: plan.QueryID,
		Column: 3, Decision: Investigate,
	}
	read := func(ctx context.Context, req ProjectAnalyzeRequest) (ProjectAnalyzeObservation, error) {
		return readProjectAnalyzeTestSample(ctx, admin, req, target, query)
	}
	actionConfig := config.Copy()
	actionConfig.User = "fixture_app"
	count := func() int64 {
		t.Helper()
		var count int64
		if err := admin.QueryRow(ctx, `SELECT analyze_count FROM pg_stat_all_tables WHERE relid=$1`, target.TableOID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	initial := count()
	allow := true
	refuse := errors.New("owner withdrew authorization")
	guards := ProjectMaintenanceGuards{
		CheckPermission: func(_ context.Context, intent ProjectMaintenanceIntent) error {
			if !allow {
				return refuse
			}
			if intent.Target != target || intent.Operation != ProjectAnalyze || len(intent.Columns) != 1 || intent.Columns[0] != 3 {
				return errors.New("intent changed relation or column")
			}
			return nil
		},
		CheckCapacity: func(context.Context, ProjectMaintenanceIntent) error { return nil },
		RecordIntent: func(ctx context.Context, intent ProjectMaintenanceIntent) error {
			_, err := admin.Exec(ctx, `INSERT INTO "Invoices".intents VALUES($1,$2,$3)`, intent.RunID, intent.Target.TableOID, intent.Columns)
			return err
		},
	}
	model := func(_ context.Context, e IndexEvidence) (IndexProposal, error) {
		p := IndexProposal{RunID: e.RunID, TableOID: e.TableOID, Column: e.Column, Repair: "analyze", Outcome: "propose", Reason: "measured stale frequency"}
		return p, ValidateDiagnosticProposal(e, p, time.Now())
	}
	declined := request
	declined.QueryID++
	neverModel := func(context.Context, IndexEvidence) (IndexProposal, error) {
		t.Fatal("model admitted without the measured query identity")
		return IndexProposal{}, nil
	}
	neverRead := func(context.Context, ProjectAnalyzeRequest) (ProjectAnalyzeObservation, error) {
		t.Fatal("probe ran before detection admitted the query")
		return ProjectAnalyzeObservation{}, nil
	}
	for _, decision := range []Decision{NoChange, InsufficientEvidence} {
		skipped := request
		skipped.Decision = decision
		if report, err := RunProjectAnalyze(ctx, actionConfig, skipped, neverRead, neverModel, guards); err != nil || report.Outcome != string(decision) {
			t.Fatalf("detector decision %q reported as %q: %v", decision, report.Outcome, err)
		}
	}
	if report, err := RunProjectAnalyze(ctx, actionConfig, declined, read, neverModel, guards); err != nil || report.Outcome != "insufficient_evidence" {
		t.Fatalf("wrong query identity: %+v %v", report, err)
	}
	revoke := func(ctx context.Context, e IndexEvidence) (IndexProposal, error) {
		p, err := model(ctx, e)
		allow = false
		return p, err
	}
	if report, err := RunProjectAnalyze(ctx, actionConfig, request, read, revoke, guards); !errors.Is(err, refuse) || report.AnalyzeMS != 0 || count() != initial {
		t.Fatalf("revoked capability reached ANALYZE: %+v %v", report, err)
	}
	allow = true
	// A fresh baseline is valid for the model. Reusing that same observation
	// at the final repair gate cannot establish that statistics stayed stale.
	baseline, err := read(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	cached := func(context.Context, ProjectAnalyzeRequest) (ProjectAnalyzeObservation, error) { return baseline, nil }
	if report, err := RunProjectAnalyze(ctx, actionConfig, request, cached, model, guards); err != nil ||
		report.Outcome != "no_change" || report.AnalyzeMS != 0 || count() != initial {
		t.Fatalf("cached pre-action evidence reached ANALYZE: %+v %v", report, err)
	}
	report, err := RunProjectAnalyze(ctx, actionConfig, request, read, model, guards)
	if err != nil || (report.Outcome != "inconclusive" && report.Outcome != "improved_tested_inputs") ||
		report.AnalyzeMS <= 0 || report.Before.Plan != "Seq Scan" || report.After.Plan != "Index Scan" ||
		report.Before.EstimateError < 10 || report.After.EstimateError != 1 || count() != initial+1 {
		t.Fatalf("project ANALYZE did not refresh and measure the exact column: %+v %v", report, err)
	}
	var correctIntent, otherColumnAnalyzed bool
	if err := admin.QueryRow(ctx, `SELECT
		(SELECT count(*)=1 AND bool_and(target=$1 AND fields='{3}'::smallint[]) FROM "Invoices".intents),
		EXISTS (SELECT FROM pg_stats WHERE schemaname='Invoices' AND tablename='Unpaid' AND attname='note')`,
		target.TableOID).Scan(&correctIntent, &otherColumnAnalyzed); err != nil || !correctIntent || otherColumnAnalyzed {
		t.Fatalf("wrong durable or target-column effect: intent=%t unrelated statistics=%t %v", correctIntent, otherColumnAnalyzed, err)
	}
}

const projectAnalyzeTestExplain = "EXPLAIN (ANALYZE, BUFFERS, VERBOSE, FORMAT JSON, TIMING OFF) "

func projectAnalyzeTestPlan(ctx context.Context, conn *pgx.Conn, query string) (plan explain, err error) {
	err = pgx.BeginTxFunc(ctx, conn, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		plan, err = readLookupPlan(ctx, tx, projectAnalyzeTestExplain+query, int32(43))
		return err
	})
	return plan, err
}

func readProjectAnalyzeTestSample(ctx context.Context, conn *pgx.Conn, req ProjectAnalyzeRequest, target ProjectIndexTarget, query string) (obs ProjectAnalyzeObservation, err error) {
	obs.Request = req
	obs.Request.Target = target
	tx, err := conn.Begin(ctx)
	if err != nil {
		return obs, err
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx, `SELECT current_user::regrole::oid,
		(SELECT most_common_freqs[1]::float8 FROM pg_stats
		 WHERE schemaname='Invoices' AND tablename='Unpaid' AND attname='Due Code' AND most_common_vals::text='{43}')`).
		Scan(&obs.Request.DatabaseRoleOID, &obs.SampledValueFraction); err != nil {
		return obs, err
	}
	obs.CoveringIndex = true
	var candidateTiming, healthyTiming, writeTiming []float64
	for range 10 {
		plan, err := readLookupPlan(ctx, tx, projectAnalyzeTestExplain+query, int32(43))
		if err != nil || plan.Node.Relation != "Unpaid" || plan.Node.Schema != "Invoices" ||
			plan.Node.Rows != 1 || plan.Node.Loops != 1 || len(plan.Node.Plans) != 0 {
			return obs, fmt.Errorf("unexpected measured candidate plan: %+v %w", plan, err)
		}
		obs.Request.QueryID = plan.QueryID
		obs.Sample.Plan, obs.Sample.EstimatedRows = plan.Node.Type, plan.Node.EstRows
		obs.Sample.PlanExamined, obs.Sample.PlanReturned = plan.Node.Rows+plan.Node.Filtered, plan.Node.Rows
		obs.Sample.Blocks += float64(plan.Node.Read + plan.Node.Hit)
		candidateTiming = append(candidateTiming, plan.ExecutionMS)
		for _, guard := range []struct {
			query   string
			timings *[]float64
		}{
			{`SELECT id FROM "Invoices".guard WHERE id=1`, &healthyTiming},
			{`UPDATE "Invoices".guard SET entered=NOT entered WHERE id=1`, &writeTiming},
		} {
			plan, err := readLookupPlan(ctx, tx, projectAnalyzeTestExplain+guard.query)
			if err != nil {
				return obs, err
			}
			*guard.timings = append(*guard.timings, plan.ExecutionMS)
		}
	}
	obs.Sample.Reads, obs.Sample.HealthyReads, obs.Sample.Writes = len(candidateTiming), len(healthyTiming), len(writeTiming)
	obs.Sample.ReadMS, obs.Sample.ReadMAD = meanMAD(candidateTiming)
	obs.Sample.HealthyMS, obs.Sample.HealthyMAD = meanMAD(healthyTiming)
	obs.Sample.WriteMS, obs.Sample.WriteMAD = meanMAD(writeTiming)
	obs.Sample.Blocks /= float64(obs.Sample.Reads)
	obs.At = time.Now().UTC()
	return obs, nil
}
