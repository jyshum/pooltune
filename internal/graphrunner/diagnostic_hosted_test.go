//go:build hostedqualification

package graphrunner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/hostedquery"
	"pooltune/internal/labdb"
)

// TestHostedDiagnosticRequestedEvidence is a separately opted-in read-only
// Railway/Rookery qualification. The controller owns fixture creation/cleanup.
// It deliberately supplies only plan and index evidence, so the real model must
// request a missing observation before recommending a statistics refresh.
// It never invokes a repair executor and does not prove queued execution.
func TestHostedDiagnosticRequestedEvidence(t *testing.T) {
	fixture := os.Getenv("POOLTUNE_DIAGNOSTIC_FIXTURE")
	model := os.Getenv("POOLTUNE_ANTHROPIC_MODEL")
	if os.Getenv("POOLTUNE_STAGING_PRIVATE_STATE") != "1" || hostedquery.ValidateConsumerFixtureID(fixture) != nil || !strings.HasPrefix(model, "claude-") || os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Fatal("hosted qualification requires private staging, an exact owned fixture and real Claude configuration")
	}
	targetConfig, err := labdb.TargetConfig(os.Getenv("POOLTUNE_LAB_TARGET_DSN"), "rookery")
	if err != nil {
		t.Fatal("invalid hosted qualification target configuration")
	}
	stateConfig, err := labdb.StateConfig(os.Getenv("POOLTUNE_LAB_STATE_DSN"))
	if err != nil {
		t.Fatal("invalid hosted qualification state configuration")
	}
	if targetConfig.Host == stateConfig.Host && targetConfig.Port == stateConfig.Port && targetConfig.Database == stateConfig.Database {
		t.Fatal("hosted qualification requires distinct target and state databases")
	}
	targetConfig.RuntimeParams["default_transaction_read_only"] = "on"
	targetConfig.RuntimeParams["statement_timeout"] = "3s"
	stateConfig.RuntimeParams["default_transaction_read_only"] = "on"
	stateConfig.RuntimeParams["statement_timeout"] = "3s"
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	target, err := pgx.ConnectConfig(ctx, targetConfig)
	if err != nil {
		t.Fatal("hosted qualification target connection failed")
	}
	defer target.Close(context.Background())
	state, err := pgx.ConnectConfig(ctx, stateConfig)
	if err != nil {
		t.Fatal("hosted qualification state connection failed")
	}
	defer state.Close(context.Background())
	// The name is derived solely from a validated fixture ID, never model output.
	relation := pgx.Identifier{"pooltune_trial_" + fixture, "candidate"}.Sanitize()
	var oid uint32
	if err := target.QueryRow(ctx, "SELECT to_regclass($1)::oid", relation).Scan(&oid); err != nil || oid == 0 {
		t.Fatal("owned qualification relation was not found")
	}
	type tableSnapshot struct {
		OID                     uint32
		LastAnalyze, Statistics string
		AnalyzeCount            int64
	}
	snapshot := func() tableSnapshot {
		var value tableSnapshot
		err := target.QueryRow(ctx, `SELECT c.oid,coalesce(s.last_analyze::text,''),s.analyze_count,
   coalesce((SELECT md5(string_agg(p::text,'' ORDER BY p.staattnum,p.stainherit)) FROM pg_catalog.pg_statistic p WHERE p.starelid=c.oid),'')
   FROM pg_catalog.pg_class c JOIN pg_catalog.pg_stat_all_tables s ON s.relid=c.oid
   WHERE c.oid=to_regclass($1)`, relation).Scan(&value.OID, &value.LastAnalyze, &value.AnalyzeCount, &value.Statistics)
		if err != nil {
			t.Fatal("qualification observation of table state failed")
		}
		return value
	}
	before := snapshot()
	token := make([]byte, 8)
	if _, err := rand.Read(token); err != nil {
		t.Fatal("qualification run identity unavailable")
	}
	base := hostedquery.IndexEvidence{RunID: "hosted-read-" + hex.EncodeToString(token), At: time.Now().UTC(), Decision: hostedquery.Investigate, TableOID: oid, Column: 2, Label: "lookup"}
	reader := hostedquery.AnalyzeDiagnosticEvidence(state, targetConfig, "rookery", fixture)
	initialStart := time.Now()
	plan, ok, err := reader(ctx, "plan", base)
	if err != nil || !ok {
		t.Fatal("initial owned plan observation failed")
	}
	indexes, ok, err := reader(ctx, "indexes", base)
	if err != nil || !ok {
		t.Fatal("initial owned index observation failed")
	}
	initialMS := float64(time.Since(initialStart)) / float64(time.Millisecond)
	base.Plan = plan.Plan
	base.Existing = indexes.Existing
	// Keep the earlier observation time: gathering the catalog must not refresh
	// the age of the already measured plan.
	base.At = plan.At
	if !base.Existing || base.Plan.Node != "Seq Scan" || base.Plan.ReturnedRows != 1 || base.Plan.ExaminedRows < 10000 {
		t.Fatal("qualification fixture does not present the expected unresolved planner problem")
	}
	investigate, err := DiagnosticReplayProcess()
	if err != nil {
		t.Fatal("live diagnostic process is unavailable")
	}
	var observationMu sync.Mutex
	requested := map[string]int{}
	available := map[string]int{}
	requestedMS := 0.0
	measuredReader := func(ctx context.Context, tool string, e hostedquery.IndexEvidence) (hostedquery.IndexEvidence, bool, error) {
		start := time.Now()
		observationMu.Lock()
		requested[tool]++
		observationMu.Unlock()
		out, ok, err := reader(ctx, tool, e)
		observationMu.Lock()
		requestedMS += float64(time.Since(start)) / float64(time.Millisecond)
		if ok && err == nil {
			available[tool]++
		}
		observationMu.Unlock()
		return out, ok, err
	}
	start := time.Now()
	proposal, runErr := AdaptDiagnostic(investigate, measuredReader)(ctx, base)
	wallMS := float64(time.Since(start)) / float64(time.Millisecond)
	after := snapshot()
	unchanged := before == after
	// A timed-out process can return before its reader callback finishes. Copy
	// the observations under lock so reporting never races with late completion.
	observationMu.Lock()
	requestedSnapshot, availableSnapshot := maps.Clone(requested), maps.Clone(available)
	requestedMSSnapshot := requestedMS
	observationMu.Unlock()
	// Log only selected outcomes and timings, never connection strings, prompts,
	// model prose, raw catalog values or credentials.
	report := map[string]any{"check": "readonly_requested_evidence", "outcome": proposal.Outcome, "repair": proposal.Repair, "initial_evidence_ms": initialMS, "requested_reader_ms": requestedMSSnapshot, "investigation_wall_ms": wallMS, "requested_tools": requestedSnapshot, "available_tools": availableSnapshot, "table_and_statistics_unchanged": unchanged, "process_succeeded": runErr == nil}
	if d := proposal.Diagnostic; d != nil {
		report["model_calls"] = d.ModelCalls
		report["model_attempts"] = d.ModelAttempts
		report["reads"] = d.Reads
		report["model_ms"] = d.ModelMS
		report["tool_ms"] = d.ToolMS
		report["graph_total_ms"] = d.TotalMS
		report["stop_reason"] = d.StopReason
	}
	if m := proposal.ModelCall; m != nil {
		report["model"] = m.Model
		report["model_requests"] = m.Requests
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal("qualification report encoding failed")
	}
	t.Log(string(raw))
	if !unchanged {
		t.Fatal("table identity or statistics changed during the read-only check")
	}
	if runErr != nil {
		t.Fatal("live diagnostic investigation failed; see sanitized report")
	}
	if proposal.Outcome != "propose" || proposal.Repair != "analyze" || proposal.RunID != base.RunID || proposal.TableOID != base.TableOID || proposal.Column != 2 || len(proposal.Columns) != 0 {
		t.Fatal("real model did not propose the expected enrolled statistics refresh")
	}
	if proposal.Diagnostic == nil || proposal.Diagnostic.Reads < 1 || proposal.Diagnostic.ModelCalls < 2 || proposal.Diagnostic.ModelCalls > 3 || availableSnapshot["analyze"] < 1 {
		t.Fatal("real model did not obtain missing live evidence before deciding")
	}
	if proposal.ModelCall == nil || proposal.ModelCall.Model != model || proposal.ModelCall.Requests < 2 {
		t.Fatal("real model call evidence is missing")
	}
}
