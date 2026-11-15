//go:build integration

package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

const ownedStatisticsPGFixture = "pt_hosted_st_0123456789abcdef"
const ownedStatisticsPGProject = "supabase/" + OwnedIndexRookeryRef + "/0123456789abcdef"

func TestOwnedStatisticsRepairRunsExactAnalyzedLookup(t *testing.T) {
	ctx, admin, writer, request := setupOwnedStatisticsPG(t, "analyze")
	var actions int
	model := ownedStatisticsTestModel("analyze")
	guards := ownedStatisticsTestGuards(func(intent any) error {
		i, ok := intent.(ProjectMaintenanceIntent)
		if !ok || i.RunID != request.RunID || i.Target != request.Target ||
			i.Operation != ProjectAnalyze || len(i.Columns) != 1 || i.Columns[0] != 2 {
			t.Fatalf("unexpected maintenance intent: %+v", intent)
		}
		actions++
		return nil
	})
	wrong := request
	wrong.QueryID++
	neverModel := func(context.Context, IndexEvidence) (IndexProposal, error) {
		t.Fatal("unsupported workload reached the investigator")
		return IndexProposal{}, nil
	}
	rejected, err := RunOwnedStatisticsRepair(ctx, writer, wrong, neverModel, guards)
	if err != nil || rejected.Outcome != "insufficient_evidence" || actions != 0 {
		t.Fatalf("wrong identity passed: %+v %v", rejected, err)
	}
	result, err := RunOwnedStatisticsRepair(ctx, writer, request, model, guards)
	if err != nil || result.Analyze == nil {
		t.Fatalf("missing analyze result: %+v %v", result, err)
	}
	report := result.Analyze
	if actions != 1 || report.Before.Plan != "Seq Scan" || report.Before.EstimateError < 10 ||
		report.After.EstimateError != 1 || report.Before.Reads != 10 || report.After.Writes != 10 ||
		report.After.Plan == "Seq Scan" || report.AnalyzeMS <= 0 {
		t.Fatalf("unverified analyze action: saved=%d outcome=%s before=%+v after=%+v", actions, result.Outcome, report.Before, report.After)
	}
	t.Logf("ANALYZE %s: %.3f -> %.3f ms", result.Outcome, report.Before.ReadMS, report.After.ReadMS)
	assertOwnedStatisticsGuardPreserved(t, ctx, admin)
}

func TestOwnedStatisticsRepairMeasuresJointStatistics(t *testing.T) {
	ctx, admin, writer, request := setupOwnedStatisticsPG(t, "statistics")
	var actions int
	guards := ownedStatisticsTestGuards(func(intent any) error {
		i, ok := intent.(OwnedExtendedStatisticsIntent)
		if !ok || i.Target != request.Target || i.RunID != request.RunID || len(i.ColumnNumbers) != 2 ||
			i.ColumnNumbers[0] != 2 || i.ColumnNumbers[1] != 3 || i.RelationBytes <= 0 {
			t.Fatalf("unexpected joint statistics intent: %+v", intent)
		}
		actions++
		return nil
	})
	model := ownedStatisticsTestModel("statistics")
	denied := errors.New("capability revoked")
	revoked := guards
	revoked.CheckPermission = func(context.Context) error { return denied }
	if _, err := RunOwnedStatisticsRepair(ctx, writer, request, model, revoked); !errors.Is(err, denied) || actions != 0 {
		t.Fatalf("revocation reached action: saved=%d %v", actions, err)
	}
	result, err := RunOwnedStatisticsRepair(ctx, writer, request, model, guards)
	if err != nil || result.Statistics == nil {
		t.Fatalf("missing statistics result: %+v %v", result, err)
	}
	report := result.Statistics
	if actions != 1 || !report.After.Applied || len(report.Before.Samples) != 2 || len(report.After.Samples) != 2 ||
		report.Before.Samples[0].EstimateError < 10 || report.Before.Samples[1].EstimateError < 10 ||
		report.After.Samples[0].EstimateError > 3 || report.After.Samples[1].EstimateError > 3 ||
		report.After.Samples[0].ReadMS >= report.Before.Samples[0].ReadMS || report.After.Samples[1].ReadMS >= report.Before.Samples[1].ReadMS {
		t.Fatalf("unverified joint statistics action: saved=%d outcome=%s before=%+v after=%+v", actions, result.Outcome, report.Before, report.After)
	}
	var existing int
	if err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_statistic_ext WHERE stxrelid=$1", request.Target.TableOID).Scan(&existing); err != nil ||
		(existing == 1) != (result.Outcome == "improved_tested_inputs" && report.After.Retained) {
		t.Fatalf("retention disagrees with the measured outcome: %s %d %v", result.Outcome, existing, err)
	}
	t.Logf("Joint statistics %s: %.3f -> %.3f ms and %.3f -> %.3f ms", result.Outcome,
		report.Before.Samples[0].ReadMS, report.After.Samples[0].ReadMS,
		report.Before.Samples[1].ReadMS, report.After.Samples[1].ReadMS)
	assertOwnedStatisticsGuardPreserved(t, ctx, admin)
}

func TestOwnedStatisticsRepairRestoresColumnTargetWhenGainIsInconclusive(t *testing.T) {
	ctx, admin, writer, request := setupOwnedStatisticsPG(t, "column_target")
	// The Railway descriptor uses the same statistics capability with this
	// single-column, exact worker-authored bound.
	if err := json.Unmarshal([]byte(`{"NextTarget":2}`), &request); err != nil {
		t.Fatal(err)
	}
	var priorSample string
	stats := "SELECT most_common_vals::text FROM pg_stats WHERE schemaname=$1 AND tablename=$2 AND attname='lookup'"
	if err := admin.QueryRow(ctx, stats, request.Target.Schema, request.Target.Relation).Scan(&priorSample); err != nil || priorSample != "{42}" {
		t.Fatalf("expected the low target to capture only the leading value: %q, %v", priorSample, err)
	}
	var actions int
	guards := ownedStatisticsTestGuards(func(value any) error {
		intent, ok := value.(ProjectStatisticsIntent)
		if !ok || intent.Target != request.Target || intent.Column != 2 || intent.RunID != request.RunID ||
			intent.Prior != (ProjectStatisticsSetting{Value: 1}) || intent.NextTarget != 2 {
			t.Fatalf("statistics adjustment did not durably bind the exact prior and new settings: %+v", value)
		}
		actions++
		return nil
	})
	result, err := RunOwnedStatisticsRepair(ctx, writer, request, ownedStatisticsTestModel("statistics"), guards)
	if err != nil || result.Outcome != "inconclusive" || actions != 1 || result.Statistics == nil {
		t.Fatalf("expected one inconclusive and rolled-back column target trial, got %q, actions=%d: %v", result.Outcome, actions, err)
	}
	report := result.Statistics
	if len(report.Before.Samples) != 1 || len(report.After.Samples) != 1 || !report.After.Applied ||
		report.After.Retained || report.After.AnalyzeMS <= 0 ||
		report.Before.Samples[0].EstimateError < 10 || report.After.Samples[0].EstimateError > 3 {
		t.Fatalf("missing exact measured plan and analysis cost: before=%+v after=%+v", report.Before, report.After)
	}
	if prior := readProjectStatisticsSetting(t, ctx, admin, request.Target, 2); prior != (ProjectStatisticsSetting{Value: 1}) {
		t.Fatalf("inconclusive trial left a new statistics target: %+v", prior)
	}
	var restoredSample string
	if err := admin.QueryRow(ctx, stats, request.Target.Schema, request.Target.Relation).Scan(&restoredSample); err != nil || restoredSample != priorSample {
		t.Fatalf("rollback changed the original planner sample: %q, %v", restoredSample, err)
	}
	assertOwnedStatisticsGuardPreserved(t, ctx, admin)
}

func TestOwnedStatisticsRepairKeepsTheActionSessionActiveDuringPrewriteSampling(t *testing.T) {
	ctx, admin, writer, request := setupOwnedStatisticsPG(t, "column_target")
	request.NextTarget = 2
	writer.Tracer = statisticsSamplingLatency{}
	var actions int
	result, err := RunOwnedStatisticsRepair(ctx, writer, request, ownedStatisticsTestModel("statistics"),
		ownedStatisticsTestGuards(func(any) error { actions++; return nil }))
	if err != nil || result.Statistics == nil || result.Statistics.ColumnTarget == nil ||
		result.Outcome != "inconclusive" || actions != 1 || !result.Statistics.ColumnTarget.Restored ||
		!result.Statistics.After.Applied || result.Statistics.After.AnalyzeMS <= 0 {
		t.Fatalf("bounded catalog round trips stranded the partial trial: actions=%d, reason=%q, err=%v", actions, result.DiagnosticReason, err)
	}
	if setting := readProjectStatisticsSetting(t, ctx, admin, request.Target, 2); setting != (ProjectStatisticsSetting{Value: 1}) {
		t.Fatalf("prewrite sampling delay leaked a trial setting: %+v", setting)
	}
	assertOwnedStatisticsGuardPreserved(t, ctx, admin)
}

type statisticsSamplingLatency struct{}

func (statisticsSamplingLatency) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	// Model ordinary per-exchange latency around the sampler's PostgreSQL
	// reads. No individual exchange is close to a database timeout.
	if strings.Contains(data.SQL, "IN ACCESS SHARE MODE NOWAIT") ||
		strings.Contains(data.SQL, "most_common_freqs") ||
		strings.Contains(data.SQL, "JOIN pg_class g ON") ||
		strings.Contains(data.SQL, "SELECT ARRAY(SELECT attname") ||
		strings.HasPrefix(data.SQL, "EXPLAIN (VERBOSE, FORMAT JSON)") {
		timer := time.NewTimer(380 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
		}
	}
	return ctx
}

func (statisticsSamplingLatency) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func setupOwnedStatisticsPG(t *testing.T, operation string) (context.Context, *pgx.Conn, *pgx.ConnConfig, OwnedStatisticsRepairRequest) {
	t.Helper()
	ctx, config, _ := testpg.Start(t, "-c", "compute_query_id=on")
	admin := openMonitorConn(t, ctx, config)
	s := ownedStatisticsPGFixture
	const password = "owned-statistics-fixture-only"
	prepare := []string{
		"CREATE ROLE " + s + " LOGIN PASSWORD '" + password + "'",
		"CREATE SCHEMA " + s + " AUTHORIZATION " + s,
		"COMMENT ON SCHEMA " + s + " IS 'Pooltune hosted statistics fixture " + ownedStatisticsPGProject + "'",
		"SET ROLE " + s,
		"CREATE TABLE " + s + ".workload (row_id int PRIMARY KEY, lookup int NOT NULL, category int NOT NULL, payload text NOT NULL) WITH (autovacuum_enabled=false)",
		"CREATE TABLE " + s + ".guard (row_id int PRIMARY KEY, protected_value int NOT NULL)",
		"INSERT INTO " + s + ".guard VALUES (1,0),(2,0)",
	}
	request := OwnedStatisticsRepairRequest{Operation: operation, ProjectID: ownedStatisticsPGProject, RunID: "owned-statistics-test",
		Target: ProjectIndexTarget{Schema: s, Relation: "workload"}, Columns: []int16{2}, Inputs: [][]int32{{43}}}
	query := "SELECT lookup FROM ONLY " + s + ".workload WHERE lookup=$1"
	args := []any{int32(43)}
	switch operation {
	case "analyze":
		prepare = append(prepare,
			"CREATE INDEX workload_lookup ON "+s+".workload (lookup)",
			"INSERT INTO "+s+".workload SELECT n,43,1,repeat('a',56) FROM generate_series(1,160000) n",
			"ANALYZE "+s+".workload (lookup)",
			"TRUNCATE "+s+".workload",
			"INSERT INTO "+s+".workload SELECT n,n,1,repeat('a',56) FROM generate_series(1,160000) n")
	case "statistics":
		prepare = append(prepare,
			"CREATE INDEX workload_keys ON "+s+".workload (lookup,category)",
			"INSERT INTO "+s+".workload SELECT n, CASE WHEN n<=100 OR n BETWEEN 401 AND 80000 THEN 1 ELSE 2 END, "+
				"CASE WHEN n BETWEEN 101 AND 80000 THEN 1 ELSE 2 END,repeat('a',56) "+
				"FROM generate_series(1,160000) n ORDER BY (n * 7919::bigint) % 160001",
			"ANALYZE "+s+".workload (lookup,category)")
		request.Columns, request.Inputs = []int16{2, 3}, [][]int32{{1, 2}, {2, 1}}
		query += " AND category=$2"
		args = []any{int32(1), int32(2)}
	case "column_target":
		request.Operation = "statistics"
		prepare = append(prepare,
			"CREATE INDEX workload_lookup ON "+s+".workload (lookup)",
			"ALTER TABLE "+s+".workload ALTER COLUMN lookup SET STATISTICS 1",
			"INSERT INTO "+s+".workload SELECT n, CASE WHEN n<=6240 THEN 42 WHEN n<=10080 THEN 43 ELSE n END, "+
				"1, repeat('a',56) FROM generate_series(1,12000) n ORDER BY (n * 7919::bigint) % 12001",
			"ANALYZE "+s+".workload (lookup)")
	default:
		t.Fatalf("unknown test operation %s", operation)
	}
	for _, statement := range prepare {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := admin.QueryRow(ctx, "SELECT oid,to_regclass($1)::oid,to_regclass($2)::oid,current_user::regrole::oid "+
		"FROM pg_database WHERE datname=current_database()", s+".workload", s+".guard").
		Scan(&request.Target.DatabaseOID, &request.Target.TableOID, &request.GuardTableOID, &request.OwnerRoleOID); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, admin, func(tx pgx.Tx) error {
		plan, err := readLookupPlan(ctx, tx, "EXPLAIN (VERBOSE, FORMAT JSON) "+query, args...)
		request.QueryID = plan.QueryID
		return err
	}); err != nil || request.QueryID == 0 {
		t.Fatalf("failed to establish exact test query identity: %d %v", request.QueryID, err)
	}
	if _, err := admin.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatal(err)
	}
	writer := config.Copy()
	writer.User, writer.Password = s, password
	return ctx, admin, writer, request
}

func ownedStatisticsTestModel(operation string) IndexInvestigator {
	return func(_ context.Context, evidence IndexEvidence) (IndexProposal, error) {
		proposal := IndexProposal{RunID: evidence.RunID, TableOID: evidence.TableOID, Column: evidence.Column, Repair: operation,
			Outcome: "propose", Reason: "measured eligible evidence in owned statistics integration"}
		return proposal, ValidateDiagnosticProposal(evidence, proposal, time.Now())
	}
}

func ownedStatisticsTestGuards(record func(any) error) OwnedStatisticsRepairGuards {
	return OwnedStatisticsRepairGuards{
		CheckPermission: func(context.Context) error { return nil },
		CheckCapacity:   func(context.Context) error { return nil },
		RecordIntent:    func(_ context.Context, intent any) error { return record(intent) },
	}
}

func assertOwnedStatisticsGuardPreserved(t *testing.T, ctx context.Context, admin *pgx.Conn) {
	t.Helper()
	var protected int
	if err := admin.QueryRow(ctx, "SELECT protected_value FROM "+ownedStatisticsPGFixture+".guard WHERE row_id=1").Scan(&protected); err != nil || protected != 0 {
		t.Fatalf("measurement leaked a write onto the fixture guard: %d %v", protected, err)
	}
}
