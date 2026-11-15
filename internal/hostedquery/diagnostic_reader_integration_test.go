//go:build integration

package hostedquery

import (
	"github.com/jackc/pgx/v5"
	"pooltune/internal/testpg"
	"testing"
	"time"
)

// Removing the journal/identity check would expose a different fixture's evidence.
func TestFixtureDiagnosticReaderBindsOwnedObjectsAndReturnsFreshEvidence(t *testing.T) {
	ctx, cfg, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	db, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	if _, err = db.Exec(ctx, "CREATE EXTENSION pg_stat_statements; CREATE SCHEMA IF NOT EXISTS diagnostic_test"); err != nil {
		t.Fatal(err)
	}
	f, err := CreateConsumerFixture(ctx, db, db, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer RemoveConsumerFixture(ctx, db, db, "local", f.ID)
	expected, err := expectedFixture(ctx, db, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := IndexEvidence{RunID: "worker-1-1", TableOID: expected.Candidate, Column: 2, At: time.Now().UTC(), Decision: Investigate, Label: "lookup"}
	reader := FixtureDiagnosticEvidence(db, cfg, "local", f.ID)
	for _, tool := range []string{"plan", "indexes", "analyze"} {
		out, ok, err := reader(ctx, tool, base)
		if err != nil || !ok || out.TableOID != base.TableOID || out.Column != 2 || out.RunID != base.RunID || time.Since(out.At) > time.Second {
			t.Fatalf("%s: %+v %v %v", tool, out, ok, err)
		}
		if tool == "plan" && (out.Plan.Node != "Seq Scan" || out.Plan.ReturnedRows != 1 || out.Plan.ExaminedRows != 100000) {
			t.Fatalf("wrong plan: %+v", out.Plan)
		}
		if tool == "indexes" && out.Existing {
			t.Fatal("unexpected index")
		}
		if tool == "analyze" && (out.Analyze == nil || out.Analyze.EstimatedRows != 1 || out.Analyze.ActualRows != 1) {
			t.Fatalf("wrong stats: %+v", out.Analyze)
		}
	}
	if _, ok, err := reader(ctx, "cron", base); err != nil || ok {
		t.Fatal("unqualified capability should be unavailable")
	}
	if _, ok, err := reader(ctx, "SELECT 1", base); err == nil || ok {
		t.Fatal("arbitrary tool accepted")
	}
	base.TableOID++
	if _, ok, err := reader(ctx, "plan", base); err == nil || ok {
		t.Fatal("foreign table accepted")
	}
	base.TableOID--
	base.Column = 1
	if _, ok, err := reader(ctx, "plan", base); err == nil || ok {
		t.Fatal("foreign column accepted")
	}
	base.Column = 2
	if _, err = db.Exec(ctx, "UPDATE "+journalTable+" SET status='recovered' WHERE id=$1", f.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := reader(ctx, "plan", base); err == nil || ok {
		t.Fatal("retired enrollment accepted")
	}
}
