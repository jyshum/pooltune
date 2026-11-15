//go:build integration

package graphrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"pooltune/internal/testpg"
	"testing"
	"time"
)

func TestDiagnosticGraphReadsFreshPostgresEvidence(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	db, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	_, err = db.Exec(ctx, `CREATE TABLE diagnostic_lookup(id integer PRIMARY KEY, lookup integer) WITH (autovacuum_enabled=false);
 INSERT INTO diagnostic_lookup SELECT n,43 FROM generate_series(1,10000)n;
 CREATE INDEX diagnostic_lookup_idx ON diagnostic_lookup(lookup);
 ANALYZE diagnostic_lookup;
 UPDATE diagnostic_lookup SET lookup=id WHERE id<>43;`)
	if err != nil {
		t.Fatal(err)
	}
	var oid uint32
	var raw []byte
	if err = db.QueryRow(ctx, `SELECT 'diagnostic_lookup'::regclass::oid`).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) SELECT id,lookup FROM diagnostic_lookup WHERE lookup=43`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []struct {
		Plan struct {
			Node      string `json:"Node Type"`
			Estimated int64  `json:"Plan Rows"`
			Actual    int64  `json:"Actual Rows"`
			Removed   int64  `json:"Rows Removed by Filter"`
			Blocks    int64  `json:"Shared Hit Blocks"`
		}
	}
	if err = json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatal("missing plan")
	}
	p := plans[0].Plan
	input := diagnosticFixture()
	input.TableOID = oid
	planData, _ := json.Marshal(map[string]any{"node": p.Node, "examined_rows": p.Actual + p.Removed, "returned_rows": p.Actual, "shared_blocks": p.Blocks})
	input.Initial[0].Data = planData
	for i := range input.Initial {
		input.Initial[i].DiagnosticIdentity = input.DiagnosticIdentity
		input.Initial[i].At = time.Now().UTC()
	}
	reads := 0
	reader := func(ctx context.Context, tool string) (DiagnosticObservation, error) {
		if tool != "analyze" {
			return DiagnosticObservation{input.DiagnosticIdentity, tool, time.Now().UTC(), "unavailable", nil}, nil
		}
		reads++
		var fraction float64
		// Fixed query and exact registered relation. Model supplies neither SQL nor identifiers.
		err := db.QueryRow(ctx, `SELECT most_common_freqs[1]::float8 FROM pg_catalog.pg_stats WHERE schemaname='public' AND tablename='diagnostic_lookup' AND attname='lookup'`).Scan(&fraction)
		data, _ := json.Marshal(map[string]any{"estimated_rows": p.Estimated, "actual_rows": p.Actual, "sampled_lookup_fraction": fraction})
		return DiagnosticObservation{input.DiagnosticIdentity, tool, time.Now().UTC(), "available", data}, err
	}
	path, _ := filepath.Abs("testdata/diagnostic-helper.ts")
	command := func(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, "node", path) }
	if os.Getenv("POOLTUNE_LIVE_DIAGNOSTIC") == "1" {
		path, _ = filepath.Abs("../../investigator/diagnostic-run.ts")
		command = func(ctx context.Context) *exec.Cmd {
			c := exec.CommandContext(ctx, "node", path)
			c.Env = append(os.Environ(), "POOLTUNE_DIAGNOSTIC_REPLAY=1", "LANGSMITH_TRACING=false")
			return c
		}
	}
	out, err := runDiagnostic(ctx, input, reader, command)
	if err != nil {
		t.Fatal(err)
	}
	if reads != 1 || (out.ModelCalls < 2 || out.ModelCalls > 3) || out.Proposal.Outcome != "propose" || out.Proposal.Repair != "analyze" {
		t.Fatalf("wrong outcome: %+v, reads=%d", out, reads)
	}
	t.Logf("real PostgreSQL plan=%s estimated=%d actual=%d; one requested stats read; graph %.3f ms", p.Node, p.Estimated, p.Actual, out.TotalMS)
	if os.Getenv("POOLTUNE_LIVE_DIAGNOSTIC") == "1" {
		b, _ := json.Marshal(out)
		t.Logf("LIVE_DIAGNOSTIC_RESULT %s", b)
	}
}

func TestDiagnosticJournalReusesReceiptsAfterReconnect(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	input := diagnosticFixture()
	calls := 0
	source := func(_ context.Context, tool string) (DiagnosticObservation, error) {
		calls++
		return DiagnosticObservation{input.DiagnosticIdentity, tool, time.Now().UTC(), "available", json.RawMessage(`{"estimated_rows":100000,"actual_rows":1,"sampled_lookup_fraction":1}`)}, nil
	}
	journal, err := openDiagnosticJournal(ctx, config, input)
	if err != nil {
		t.Fatal(err)
	}
	original, err := journal.reader(source)(ctx, "analyze")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openDiagnosticJournal(ctx, config, input); err == nil {
		t.Fatal("concurrent investigation admitted")
	}
	journal.close()
	journal, err = openDiagnosticJournal(ctx, config, input)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := journal.reader(source)(ctx, "analyze")
	journal.close()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !restored.At.Equal(original.At) || restored.DiagnosticIdentity != original.DiagnosticIdentity {
		t.Fatal("receipt was remeasured or rebound")
	}
	input.TableOID++
	if journal, err = openDiagnosticJournal(ctx, config, input); err == nil {
		journal.close()
		t.Fatal("changed identity admitted on same run")
	}
}

func TestDiagnosticPublicProcessRestart(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	input := diagnosticFixture()
	calls := 0
	stateURL := url.URL{Scheme: "postgres", User: url.UserPassword(config.User, config.Password), Host: fmt.Sprintf("%s:%d", config.Host, config.Port), Path: "/" + config.Database}
	stateURL.RawQuery = "sslmode=disable"
	path, _ := filepath.Abs("testdata/diagnostic-helper.ts")
	source := func(_ context.Context, tool string) (DiagnosticObservation, error) {
		calls++
		return DiagnosticObservation{input.DiagnosticIdentity, tool, time.Now().UTC(), "available", json.RawMessage(`{"estimated_rows":100000,"actual_rows":1,"sampled_lookup_fraction":1}`)}, nil
	}
	attempt := func(stop bool) (DiagnosticResult, error) {
		journal, err := openDiagnosticJournal(ctx, config, input)
		if err != nil {
			t.Fatal(err)
		}
		defer journal.close()
		return runDiagnostic(ctx, input, journal.reader(source), func(ctx context.Context) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "node", path)
			cmd.Env = append(os.Environ(), "DIAGNOSTIC_TEST_STATE_DSN="+stateURL.String())
			if stop {
				cmd.Env = append(cmd.Env, "DIAGNOSTIC_TEST_EXIT=1")
			}
			return cmd
		}, diagnosticBudget{journal.attempts, journal.reserveModel})
	}
	interrupted, err := attempt(true)
	if err == nil || interrupted.TotalMS <= 0 || interrupted.ModelAttempts != 2 {
		t.Fatal("expected timed interrupted process")
	}
	resumed, err := attempt(false)
	if err != nil {
		t.Fatal(err)
	}
	completed, againErr := attempt(false)
	if againErr != nil || completed.ModelAttempts != 3 || completed.ModelCalls != 2 {
		t.Fatalf("completed checkpoint lost attempt count: result=%+v err=%v", completed, againErr)
	}
	if calls != 1 || resumed.Reads != 1 || resumed.ModelCalls != 2 || resumed.ModelAttempts != 3 || resumed.Proposal.Repair != "analyze" || resumed.Proposal.Outcome != "propose" {
		t.Fatalf("restart repeated evidence or lost result: calls=%d result=%+v", calls, resumed)
	}
}

func TestDiagnosticJournalModelBudgetSurvivesReconnect(t *testing.T) {
	ctx, config, _ := testpg.Start(t)
	input := diagnosticFixture()
	for want := 1; want <= 4; want++ {
		journal, err := openDiagnosticJournal(ctx, config, input)
		if err != nil {
			t.Fatal(err)
		}
		n, err := journal.reserveModel(ctx)
		journal.close()
		if want <= 3 {
			if err != nil || n != want {
				t.Fatalf("reservation %d: n=%d err=%v", want, n, err)
			}
		} else if err == nil || n != 3 {
			t.Fatal("fourth model attempt admitted")
		}
	}
}
