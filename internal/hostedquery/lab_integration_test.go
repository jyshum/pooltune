//go:build integration

package hostedquery

import (
	"strings"
	"testing"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestLabFixture(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	target, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close(ctx)
	if _, err := target.Exec(ctx, "CREATE EXTENSION pg_stat_statements"); err != nil {
		t.Fatal(err)
	}
	report, err := RunLab(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if report.Healthy.Decision != NoChange || (report.Healthy.Plan.Node != "Index Scan" && report.Healthy.Plan.Node != "Index Only Scan") ||
		report.Candidate.Decision != Investigate || report.Candidate.Plan.Node != "Seq Scan" {
		t.Fatalf("unexpected measured comparison: %+v", report)
	}
	if report.Candidate.Plan.ExaminedRows != 100_000 ||
		report.Healthy.Plan.ReturnedRows != 1 || report.Candidate.Plan.ReturnedRows != 1 {
		t.Fatalf("unexpected row counts: %+v", report)
	}
	for _, result := range []CaseResult{report.Healthy, report.Candidate} {
		if len(result.Observations) != 5 {
			t.Fatalf("expected five measured batches: %+v", result)
		}
		for _, observed := range result.Observations {
			if observed.Calls != 4 {
				t.Fatalf("unexpected number of measured calls: %+v", observed)
			}
		}
	}
	assertSchema := func(want bool) {
		t.Helper()
		var exists bool
		err := target.QueryRow(ctx, "SELECT to_regnamespace('pooltune_m1') IS NOT NULL").Scan(&exists)
		if err != nil || exists != want {
			t.Fatalf("schema exists = %v, error = %v", exists, err)
		}
	}
	assertSchema(false)

	if _, err := target.Exec(ctx, "DROP EXTENSION pg_stat_statements"); err != nil {
		t.Fatal(err)
	}
	if _, err := RunLab(ctx, target); err == nil {
		t.Fatal("lab accepted missing telemetry")
	}
	assertSchema(false)

	if _, err := target.Exec(ctx, "CREATE SCHEMA pooltune_m1"); err != nil {
		t.Fatal(err)
	}
	if _, err := RunLab(ctx, target); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected existing-schema refusal, got %v", err)
	}
	assertSchema(true)
}
