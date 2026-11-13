//go:build integration

package hostedquery

import (
	"encoding/json"
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
	if (report.Healthy.Plan.Node != "Index Scan" && report.Healthy.Plan.Node != "Index Only Scan") ||
		report.Candidate.Plan.Node != "Seq Scan" {
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
		breaches := 0
		for _, observed := range result.Observations {
			if observed.Calls != 4 {
				t.Fatalf("unexpected number of measured calls: %+v", observed)
			}
			if observed.MeanMS > report.ObjectiveMS {
				breaches++
			}
		}
		want := NoChange
		if breaches >= 3 {
			want = Investigate
		}
		if result.Decision != want {
			t.Fatalf("got %q for %d measured breaches, want %q", result.Decision, breaches, want)
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

	if _, err := target.Exec(ctx, "SET pg_stat_statements.track = 'none'"); err != nil {
		t.Fatal(err)
	}
	if _, err := RunLab(ctx, target); err == nil || !strings.Contains(err.Error(), "read start counters") {
		t.Fatalf("expected missing telemetry after fixture creation, got %v", err)
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

func TestReportSurvivesStateReconnect(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := PrepareState(ctx, state); err != nil {
		t.Fatal(err)
	}
	report := Report{ServerVersion: "17", Healthy: CaseResult{Decision: NoChange}}
	id, err := RecordReport(ctx, state, report)
	if err != nil || id < 1 {
		t.Fatalf("record ID = %d, error = %v", id, err)
	}
	var count int
	if err := state.QueryRow(ctx, "SELECT count(*) FROM pooltune_lab_state.runs").Scan(&count); err != nil || count != 1 {
		t.Fatalf("expected only the real report, found %d rows, error = %v", count, err)
	}
	if err := state.Close(ctx); err != nil {
		t.Fatal(err)
	}
	state, err = pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	var payload []byte
	var restored Report
	if err := state.QueryRow(ctx, "SELECT report FROM pooltune_lab_state.runs WHERE id = $1", id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &restored); err != nil || restored.Healthy.Decision != NoChange {
		t.Fatalf("restored report = %+v, error = %v", restored, err)
	}
}

func TestPrepareStateRejectsIncompatibleTable(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	if _, err := state.Exec(ctx, "CREATE SCHEMA pooltune_lab_state"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(ctx, "CREATE TABLE pooltune_lab_state.runs (id bigint PRIMARY KEY, report jsonb NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if err := PrepareState(ctx, state); err == nil {
		t.Fatal("preflight accepted a state table that cannot record a report")
	}
	var existing bool
	err = state.QueryRow(ctx, "SELECT to_regclass('pooltune_lab_state.runs') IS NOT NULL").Scan(&existing)
	if err != nil || !existing {
		t.Fatalf("preflight must preserve the existing table: exists = %v, error = %v", existing, err)
	}
}

func TestLabDefersUnknownActivity(t *testing.T) {
	ctx, config, _ := testpg.Start(t, "-c", "shared_preload_libraries=pg_stat_statements")
	target, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close(ctx)
	other, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close(ctx)
	for _, statement := range []string{
		"CREATE EXTENSION pg_stat_statements",
		"CREATE ROLE restricted_fixture",
		"GRANT CREATE ON DATABASE postgres TO restricted_fixture",
		"SET ROLE restricted_fixture",
	} {
		if _, err := target.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RunLab(ctx, target); err == nil || !strings.Contains(err.Error(), "fixture deferred") {
		t.Fatalf("expected unknown activity to defer the fixture, got %v", err)
	}
}

func TestPrepareStateRejectsMissingWriteGrant(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	state, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close(ctx)
	if err := PrepareState(ctx, state); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE ROLE state_reader",
		"GRANT CREATE ON DATABASE postgres TO state_reader",
		"GRANT USAGE, CREATE ON SCHEMA pooltune_lab_state TO state_reader",
		"GRANT SELECT ON pooltune_lab_state.runs TO state_reader",
		"SET ROLE state_reader",
	} {
		if _, err := state.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := PrepareState(ctx, state); err == nil {
		t.Fatal("preflight accepted a role that cannot append reports")
	}
}
