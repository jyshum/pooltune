//go:build integration

package hostedquery

import (
	"testing"

	"pooltune/internal/testpg"

	"github.com/jackc/pgx/v5"
)

func TestWorkerTrafficFromAnotherSession(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	traffic, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer traffic.Close(ctx)
	if _, err := traffic.Exec(ctx, "CREATE EXTENSION pg_stat_statements"); err != nil {
		t.Fatal(err)
	}
	if _, err := traffic.Exec(ctx, "CREATE DATABASE worker_state"); err != nil {
		t.Fatal(err)
	}
	target, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close(ctx)
	stateConfig := config.Copy()
	stateConfig.Database = "worker_state"
	state, err := pgx.ConnectConfig(ctx, stateConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	report, err := RunWorkerTraffic(ctx, traffic, target, state, "local")
	if err != nil {
		t.Fatal(err)
	}
	if report.Healthy.Decision != NoChange || report.Candidate.Decision != Investigate ||
		report.Healthy.Calls == 0 || report.Candidate.Calls == 0 ||
		report.Jobs != 1 || report.DetectionMS <= 0 {
		t.Fatalf("unexpected traffic report: %+v", report)
	}
	for _, caseReport := range []TrafficCaseReport{report.Healthy, report.Candidate} {
		if caseReport.ClientP95MS <= 0 || caseReport.DatabaseMeanMS <= 0 {
			t.Fatalf("no measured latency for %+v", caseReport)
		}
	}
	var exists bool
	if err := target.QueryRow(ctx, "SELECT to_regnamespace($1) IS NOT NULL", report.Schema).Scan(&exists); err != nil || exists {
		t.Fatalf("traffic fixture remains: exists=%v err=%v", exists, err)
	}
	var count int
	err = state.QueryRow(ctx, "SELECT count(*) FROM pooltune_worker.investigations WHERE project_id=$1", report.Project).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("expected 1 persisted job, got %d, err=%v", count, err)
	}
}
