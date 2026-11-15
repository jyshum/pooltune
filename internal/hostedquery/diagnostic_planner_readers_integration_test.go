//go:build integration

package hostedquery

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
)

// Reusing the original snapshot would miss an ANALYZE performed between requests.
// Calling the mutation-bearing baseline sampler fails on the read-only connection.
func TestAnalyzeDiagnosticReaderMeasuresCurrentEvidenceWithoutWrites(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t, "-c", "compute_query_id=on")
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	f, err := CreateAnalyzeFixture(ctx, db, db, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := RemoveConsumerFixture(ctx, db, db, "local", f.ID); err != nil {
			t.Error(err)
		}
	}()
	expected, err := expectedFixture(ctx, db, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := IndexEvidence{RunID: "reader-analyze", At: time.Now().UTC(), Decision: Investigate, TableOID: expected.Candidate, Column: 2, Label: "lookup"}
	readConfig := cfg.Copy()
	readConfig.RuntimeParams["default_transaction_read_only"] = "on"
	read := AnalyzeDiagnosticEvidence(db, readConfig, "local", f.ID)
	checkPlannerReaderDoesNotBlockWrites(t, ctx, cfg, read, base, f.Schema)
	for _, tool := range []string{"plan", "indexes", "analyze"} {
		out, ok, err := read(ctx, tool, base)
		if err != nil || !ok {
			t.Fatalf("%s: %v %v", tool, ok, err)
		}
		checkPlannerIdentity(t, base, out)
		if !out.Existing {
			t.Fatal("missing existing index")
		}
		if tool == "plan" && (out.Plan.Node != "Seq Scan" || out.Plan.ReturnedRows != 1 || out.Plan.ExaminedRows != 100000) {
			t.Fatalf("wrong measured plan: %+v", out.Plan)
		}
		if tool == "analyze" && (out.Analyze == nil || out.Analyze.EstimatedRows < 10000 || out.Analyze.ActualRows != 1 || out.Analyze.SampledValueFraction == nil || *out.Analyze.SampledValueFraction != 1) {
			t.Fatalf("missing stale statistics evidence: %+v", out.Analyze)
		}
	}
	if _, err := db.Exec(ctx, "ANALYZE "+qualified(f.Schema, "candidate")+" (lookup)"); err != nil {
		t.Fatal(err)
	}
	out, ok, err := read(ctx, "analyze", base)
	if err != nil || !ok || out.Analyze.EstimatedRows != 1 || out.Analyze.SampledValueFraction != nil {
		t.Fatalf("did not collect changed evidence: %+v %v %v", out.Analyze, ok, err)
	}
	checkPlannerRefusals(t, ctx, db, read, base, f.ID)
}

// Dropping the second enrolled predicate or trusting the caller's evidence would
// hide the independent estimate error; removing identity checks exposes another fixture.
func TestStatisticsDiagnosticReaderMeasuresBothPredicatesAndRejectsDrift(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t, "-c", "compute_query_id=on")
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	f, err := CreateStatisticsFixture(ctx, db, db, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := RemoveConsumerFixture(ctx, db, db, "local", f.ID); err != nil {
			t.Error(err)
		}
	}()
	expected, err := expectedFixture(ctx, db, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := IndexEvidence{RunID: "reader-statistics", At: time.Now().UTC(), Decision: Investigate, TableOID: expected.Candidate, Column: 2, Label: "lookup"}
	readConfig := cfg.Copy()
	readConfig.RuntimeParams["default_transaction_read_only"] = "on"
	read := StatisticsDiagnosticEvidence(db, readConfig, "local", f.ID)
	checkPlannerReaderDoesNotBlockWrites(t, ctx, cfg, read, base, f.Schema)
	for _, tool := range []string{"plan", "indexes", "analyze", "statistics"} {
		out, ok, err := read(ctx, tool, base)
		if err != nil || !ok {
			t.Fatalf("%s: %v %v", tool, ok, err)
		}
		checkPlannerIdentity(t, base, out)
		if !out.Existing {
			t.Fatal("existing composite index not detected")
		}
		if tool == "plan" && (out.Plan.ReturnedRows != 100 || out.Plan.ExaminedRows != 100000) {
			t.Fatalf("wrong plan: %+v", out.Plan)
		}
		if tool == "analyze" && (out.Analyze == nil || out.Analyze.ActualRows != 100 || out.Analyze.SampledValueFraction != nil) {
			t.Fatalf("wrong single-case evidence: %+v", out.Analyze)
		}
		if tool == "statistics" {
			if out.Statistics == nil || !out.Statistics.Correlated || len(out.Statistics.Cases) != 2 {
				t.Fatalf("incomplete correlation evidence: %+v", out.Statistics)
			}
			for i, want := range []int64{100, 300} {
				c := out.Statistics.Cases[i]
				if c.ActualRows != want || c.EstimatedRows < 10*want {
					t.Fatalf("case %d: %+v", i, c)
				}
			}
		}
	}
	if _, err := db.Exec(ctx, "COMMENT ON TABLE "+qualified(f.Schema, "candidate")+" IS 'changed'"); err != nil {
		t.Fatal(err)
	}
	out, ok, err := read(ctx, "statistics", base)
	if err != nil || !ok || out.Statistics == nil || out.Statistics.Correlated {
		t.Fatalf("trusted stale correlation marker: %+v %v %v", out.Statistics, ok, err)
	}
	if _, err := db.Exec(ctx, "COMMENT ON TABLE "+qualified(f.Schema, "candidate")+" IS 'pooltune fresh correlated fixture'"); err != nil {
		t.Fatal(err)
	}
	checkPlannerRefusals(t, ctx, db, read, base, f.ID)
}

func checkPlannerIdentity(t *testing.T, base, out IndexEvidence) {
	t.Helper()
	if out.RunID != base.RunID || out.TableOID != base.TableOID || out.Column != base.Column || out.At.Before(base.At) || time.Since(out.At) > time.Second {
		t.Fatalf("changed or old identity: %+v", out)
	}
}
func checkPlannerRefusals(t *testing.T, ctx context.Context, db *pgx.Conn, read DiagnosticEvidenceReader, base IndexEvidence, fixture string) {
	t.Helper()
	for _, tool := range []string{"memory", "vacuum", "cron"} {
		if _, ok, err := read(ctx, tool, base); ok || err != nil {
			t.Fatalf("unsupported %s: %v %v", tool, ok, err)
		}
	}
	if _, ok, err := read(ctx, "SELECT 1", base); ok || err == nil {
		t.Fatal("arbitrary tool allowed")
	}
	for _, change := range []func(*IndexEvidence){func(e *IndexEvidence) { e.TableOID++ }, func(e *IndexEvidence) { e.Columns = []int16{2, 3} }, func(e *IndexEvidence) { e.Column = 1 }, func(e *IndexEvidence) { e.At = time.Now().Add(-time.Minute) }, func(e *IndexEvidence) { e.At = time.Now().Add(time.Minute) }, func(e *IndexEvidence) { e.Decision = NoChange }} {
		bad := base
		change(&bad)
		if _, ok, err := read(ctx, "plan", bad); ok || err == nil {
			t.Fatalf("unsafe input accepted: %+v", bad)
		}
	}
	var originalStatus string
	if err := db.QueryRow(ctx, "SELECT status FROM "+journalTable+" WHERE id=$1", fixture).Scan(&originalStatus); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(ctx, "UPDATE "+journalTable+" SET status=$2 WHERE id=$1", fixture, originalStatus); err != nil {
			t.Error(err)
		}
	}()
	if _, err := db.Exec(ctx, "UPDATE "+journalTable+" SET status='recovered' WHERE id=$1", fixture); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := read(ctx, "plan", base); ok || err == nil {
		t.Fatal("retired fixture accepted")
	}
}

// An inspection should coexist with ordinary writes, rather than take repair locks.
func checkPlannerReaderDoesNotBlockWrites(t *testing.T, ctx context.Context, cfg *pgx.ConnConfig, read DiagnosticEvidenceReader, base IndexEvidence, schema string) {
	t.Helper()
	writer, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close(ctx)
	tx, err := writer.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, table := range []string{"candidate", "healthy"} {
		if _, err := tx.Exec(ctx, "UPDATE "+qualified(schema, table)+" SET padding=padding WHERE id=1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := read(ctx, "indexes", base); err != nil || !ok {
		t.Fatalf("reader blocked ordinary write: %v %v", ok, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	ddl, err := writer.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer ddl.Rollback(ctx)
	if _, err := ddl.Exec(ctx, "LOCK TABLE ONLY "+qualified(schema, "healthy")+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := read(ctx, "indexes", base); ok || err == nil {
		t.Fatal("reader ignored concurrent DDL lock")
	}
	// Candidate was locked before setup failed on healthy. The failed reader
	// must close/rollback its partial transaction rather than leak that lock.
	if _, err := ddl.Exec(ctx, "LOCK TABLE ONLY "+qualified(schema, "candidate")+" IN ACCESS EXCLUSIVE MODE NOWAIT"); err != nil {
		t.Fatalf("partial setup leaked a table lock: %v", err)
	}
}

// Serial client/server exchanges used to exhaust the reader's 2 s budget before
// measuring the query. Delay actual PostgreSQL calls to represent cross-region
// connection/round-trip costs; the database and every guard still execute.
func TestPlannerDiagnosticReadersFitBoundedCrossRegionRoundTrips(t *testing.T) {
	for _, statistics := range []bool{false, true} {
		name := "analyze"
		if statistics {
			name = "statistics"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cfg, _ := testpg.Start(t, "-c", "compute_query_id=on")
			db, err := pgx.ConnectConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close(ctx)
			create := CreateAnalyzeFixture
			factory := AnalyzeDiagnosticEvidence
			tools := []string{"indexes", "plan", "analyze"}
			if statistics {
				create = CreateStatisticsFixture
				factory = StatisticsDiagnosticEvidence
				tools = append(tools, "statistics")
			}
			f, err := create(ctx, db, db, "local")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := RemoveConsumerFixture(ctx, db, db, "local", f.ID); err != nil {
					t.Error(err)
				}
			}()
			objects, err := expectedFixture(ctx, db, f.ID)
			if err != nil {
				t.Fatal(err)
			}
			delayed := cfg.Copy()
			delayed.Tracer = plannerRoundTripDelay{}
			delayed.RuntimeParams["default_transaction_read_only"] = "on"
			read := factory(db, delayed, "local", f.ID)
			for _, tool := range tools {
				base := IndexEvidence{RunID: "roundtrip-bound", At: time.Now().UTC(), Decision: Investigate, TableOID: objects.Candidate, Column: 2, Label: "lookup"}
				start := time.Now()
				out, ok, err := read(ctx, tool, base)
				t.Logf("%s: %s", tool, time.Since(start))
				if err != nil || !ok {
					t.Fatalf("bounded cross-region %s read: %v %v", tool, ok, err)
				}
				if out.TableOID != objects.Candidate || !out.Existing {
					t.Fatal("missing validated catalog")
				}
				if tool == "analyze" && out.Analyze == nil {
					t.Fatal("missing measured estimate")
				}
				if tool == "statistics" && (out.Statistics == nil || len(out.Statistics.Cases) != 2 || !out.Statistics.Correlated) {
					t.Fatal("missing measured joint evidence")
				}
			}
		})
	}
}

type plannerRoundTripDelay struct{}

func plannerDelay(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
func (plannerRoundTripDelay) TraceConnectStart(ctx context.Context, _ pgx.TraceConnectStartData) context.Context {
	plannerDelay(ctx, 800*time.Millisecond)
	return ctx
}
func (plannerRoundTripDelay) TraceConnectEnd(context.Context, pgx.TraceConnectEndData) {}
func (plannerRoundTripDelay) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	plannerDelay(ctx, 135*time.Millisecond)
	return ctx
}
func (plannerRoundTripDelay) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func (plannerRoundTripDelay) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	plannerDelay(ctx, 135*time.Millisecond)
	return ctx
}
func (plannerRoundTripDelay) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}
func (plannerRoundTripDelay) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData)     {}
