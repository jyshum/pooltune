//go:build hostedqualification

package graphrunner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	trace := &hostedProbeTrace{}
	targetConfig.Tracer = &hostedProbeConnectionTrace{trace: trace, role: "target"}
	stateConfig.Tracer = &hostedProbeConnectionTrace{trace: trace, role: "state"}
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
		logHostedProbeFailure(t, "initial_plan", initialStart, err, trace)
	}
	indexStart := time.Now()
	indexes, ok, err := reader(ctx, "indexes", base)
	if err != nil || !ok {
		logHostedProbeFailure(t, "initial_indexes", indexStart, err, trace)
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
	report := map[string]any{"check": "readonly_requested_evidence", "outcome": proposal.Outcome, "repair": proposal.Repair, "initial_evidence_ms": initialMS, "requested_reader_ms": requestedMSSnapshot, "investigation_wall_ms": wallMS, "requested_tools": requestedSnapshot, "available_tools": availableSnapshot, "table_and_statistics_unchanged": unchanged, "process_succeeded": runErr == nil, "error_category": hostedProbeErrorCategory(runErr), "database_phases": trace.snapshot()}
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

// Probe-only tracing records fixed phase labels, elapsed time and safe error
// categories. It deliberately discards SQL, arguments, DSNs and error prose.
type hostedProbeEvent struct {
	Role      string  `json:"role"`
	Phase     string  `json:"phase"`
	ElapsedMS float64 `json:"elapsed_ms"`
	Error     string  `json:"error_category"`
}
type hostedProbeTrace struct {
	mu     sync.Mutex
	events []hostedProbeEvent
}

func (p *hostedProbeTrace) snapshot() []hostedProbeEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]hostedProbeEvent(nil), p.events...)
}

type hostedProbeConnectionTrace struct {
	trace *hostedProbeTrace
	role  string
}
type hostedProbeTraceKey struct{}
type hostedProbeSpan struct {
	start time.Time
	phase string
}

func (p *hostedProbeConnectionTrace) start(ctx context.Context, phase string) context.Context {
	return context.WithValue(ctx, hostedProbeTraceKey{}, hostedProbeSpan{time.Now(), phase})
}
func (p *hostedProbeConnectionTrace) end(ctx context.Context, err error) {
	span, ok := ctx.Value(hostedProbeTraceKey{}).(hostedProbeSpan)
	if !ok {
		return
	}
	event := hostedProbeEvent{p.role, span.phase, float64(time.Since(span.start)) / float64(time.Millisecond), hostedProbeErrorCategory(err)}
	p.trace.mu.Lock()
	p.trace.events = append(p.trace.events, event)
	p.trace.mu.Unlock()
}
func (p *hostedProbeConnectionTrace) TraceConnectStart(ctx context.Context, _ pgx.TraceConnectStartData) context.Context {
	return p.start(ctx, "connect")
}
func (p *hostedProbeConnectionTrace) TraceConnectEnd(ctx context.Context, data pgx.TraceConnectEndData) {
	p.end(ctx, data.Err)
}
func (p *hostedProbeConnectionTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	sql := strings.ToUpper(strings.TrimSpace(data.SQL))
	phase := "catalog_read"
	switch {
	case strings.HasPrefix(sql, "BEGIN"):
		phase = "begin"
	case strings.HasPrefix(sql, "COMMIT"):
		phase = "commit"
	case strings.HasPrefix(sql, "ROLLBACK"):
		phase = "rollback"
	case strings.HasPrefix(sql, "SET "):
		phase = "settings"
	case strings.HasPrefix(sql, "LOCK TABLE"):
		phase = "table_lock"
	case strings.HasPrefix(sql, "EXPLAIN"):
		phase = "measured_plan"
	case strings.Contains(sql, "MOST_COMMON_FREQS"):
		phase = "distribution_statistics"
	case strings.Contains(sql, "JOIN PG_CLASS CP"):
		phase = "fixture_ownership"
	case strings.Contains(sql, "COUNT(*)=6"):
		phase = "fixture_shape"
	case strings.Contains(sql, "INDNKEYATTS=1"):
		phase = "single_index_identity"
	case strings.Contains(sql, "INDNKEYATTS=2"):
		phase = "joint_index_identity"
	case strings.Contains(sql, "LAST_ANALYZE >"):
		phase = "current_correlation"
	}
	return p.start(ctx, phase)
}
func (p *hostedProbeConnectionTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	p.end(ctx, data.Err)
}
func (p *hostedProbeConnectionTrace) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return p.start(ctx, "measurement_batch")
}
func (p *hostedProbeConnectionTrace) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {
}
func (p *hostedProbeConnectionTrace) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceBatchEndData) {
	p.end(ctx, data.Err)
}
func hostedProbeErrorCategory(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		switch pgerr.Code {
		case "57014":
			return "statement_canceled"
		case "55P03":
			return "lock_unavailable"
		case "42501":
			return "permission_denied"
		case "25006":
			return "read_only_violation"
		default:
			return "postgres_error"
		}
	}
	if pgconn.Timeout(err) {
		return "network_timeout"
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "missing_catalog_row"
	}
	return "validation_or_transport_error"
}
func logHostedProbeFailure(t *testing.T, stage string, start time.Time, err error, trace *hostedProbeTrace) {
	t.Helper()
	raw, marshalErr := json.Marshal(map[string]any{"check": "readonly_requested_evidence", "failed_stage": stage, "stage_elapsed_ms": float64(time.Since(start)) / float64(time.Millisecond), "error_category": hostedProbeErrorCategory(err), "database_phases": trace.snapshot()})
	if marshalErr == nil {
		t.Log(string(raw))
	}
	t.Fatal("hosted requested-evidence probe failed; see sanitized phase report")
}
