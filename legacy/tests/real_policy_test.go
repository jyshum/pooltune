//go:build model

package tests

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pooltune/internal/agent"
	"pooltune/internal/domain"
	"pooltune/internal/gateway"
	"pooltune/internal/policy"
	"pooltune/internal/secrets"
)

// This harness proves paid model -> real HTTP gateway -> local compiler only.
// It deliberately cannot activate database traffic. Rollout requires separate
// integration evidence, not this fixture authority.
type modelAuthority struct{ state agent.State }

func (a *modelAuthority) View(context.Context) (agent.State, error) { return a.state, nil }
func (a *modelAuthority) StartCanary(context.Context, agent.Candidate, string) (string, error) {
	return "", agent.ErrDenied
}
func TestRealPolicyRun(t *testing.T) {
	if os.Getenv("POOLTUNE_MODEL_TEST") != "1" {
		t.Skip("paid calls disabled; requires explicit model profile, private files and persistent $5 gateway ledger")
	}
	required := []string{"POOLTUNE_MODEL_PROFILE", "POOLTUNE_MODEL_KEY_FILE", "POOLTUNE_INSTALL_TOKEN_FILE", "POOLTUNE_GATEWAY_DB"}
	values := map[string]string{}
	for _, name := range required {
		values[name] = os.Getenv(name)
		if !filepath.IsAbs(values[name]) {
			t.Fatal("missing absolute integration configuration path")
		}
	}
	raw, e := os.ReadFile(values["POOLTUNE_MODEL_PROFILE"])
	if e != nil {
		t.Fatal("model profile unavailable")
	}
	var profile gateway.Profile
	if len(raw) > 16384 || json.Unmarshal(raw, &profile) != nil {
		t.Fatal("invalid reviewed model profile")
	}
	keyReader, e := secrets.Open(filepath.Dir(values["POOLTUNE_MODEL_KEY_FILE"]))
	if e != nil {
		t.Fatal("private model secret root unavailable")
	}
	defer keyReader.Close()
	tokenReader, e := secrets.Open(filepath.Dir(values["POOLTUNE_INSTALL_TOKEN_FILE"]))
	if e != nil {
		t.Fatal("installation token root unavailable")
	}
	defer tokenReader.Close()
	key := func() (string, error) {
		b, e := keyReader.Read(values["POOLTUNE_MODEL_KEY_FILE"])
		return strings.TrimSpace(string(b)), e
	}
	token := func() (string, error) {
		b, e := tokenReader.Read(values["POOLTUNE_INSTALL_TOKEN_FILE"])
		return strings.TrimSpace(string(b)), e
	}
	// Exact owner CLI limits. Never create a new accounting directory or overwrite
	// a ledger here; the configured persistent DB must already contain approval.
	if _, e = os.Stat(values["POOLTUNE_GATEWAY_DB"]); e != nil {
		t.Fatal("owner-approved persistent ledger required")
	}
	ledger, e := gateway.Open(values["POOLTUNE_GATEWAY_DB"], gateway.Limits{GlobalMicros: 5000000, MaxConcurrent: 4, MaxAccountConcurrent: 1, RequestsPerMinute: 30, MaxRunSteps: 8, RunTTL: 5 * time.Minute})
	if e != nil {
		t.Fatal("cannot open persistent $5 ledger")
	}
	defer ledger.Close()
	install, e := token()
	if e != nil {
		t.Fatal("installation token unavailable")
	}
	if _, e = ledger.Authorize(install); e != nil {
		t.Fatal("account not approved")
	}
	model, e := gateway.NewClaude(profile, key, "", false)
	if e != nil {
		t.Fatal("verified model profile required")
	}
	handler, e := gateway.NewHandler(&gateway.Service{Store: ledger, Model: observedModel{model: model, t: t}, Profile: profile}, true)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client, e := agent.NewClient(server.URL, token, profile.Timeout+time.Second, true)
	if e != nil {
		t.Fatal(e)
	}
	registry := policy.Registry{Fallback: policy.Fallback{QueueWait: time.Second, QueueLimit: 10, ConcurrencyCap: 4}, Workloads: []policy.Workload{{ID: "synthetic-interactive", Selector: policy.Selector{Target: "fixture", Path: domain.PathPostgres, Principal: "fixture-role"}, DefaultClass: domain.ClassCritical, AllowedClasses: []domain.Class{domain.ClassCritical}, MaxQueueWait: time.Second, MaxQueueLimit: 100, MaxConcurrency: 100}, {ID: "synthetic-background", Selector: policy.Selector{Target: "fixture", Path: domain.PathPostgres, Principal: "fixture-worker"}, DefaultClass: domain.ClassBackground, AllowedClasses: []domain.Class{domain.ClassBackground}, MaxQueueWait: time.Second, MaxQueueLimit: 100, MaxConcurrency: 100}}}
	stable := policy.Policy{SchemaVersion: "1", Revision: "p1", GuardrailVersion: "g1", ReserveFraction: .1, ClassWeights: policy.ClassWeights{Critical: 1, Normal: 1, Background: 8}, Workloads: []policy.WorkloadSettings{{WorkloadID: "synthetic-interactive", QueueWait: time.Second, QueueLimit: 50, ConcurrencyCap: 50}, {WorkloadID: "synthetic-background", QueueWait: time.Second, QueueLimit: 50, ConcurrencyCap: 50}}}
	bounds := policy.Guardrails{Version: "g1", MaxChangedEntries: 5, MaxReserveFraction: .5, MinClassWeight: 1, MaxClassWeight: 10}
	compiled, e := policy.Compile(stable, bounds, registry)
	if e != nil {
		t.Fatal(e)
	}
	bounds.CurrentRevision = "p1"
	bounds.CurrentPolicy = compiled
	a := &modelAuthority{state: agent.State{Target: "fixture", Path: domain.PathPostgres, Policy: stable, Guardrails: bounds, Registry: registry, Evidence: agent.Evidence{ID: "synthetic-slo", At: time.Now(), Complete: true, Samples: 2000, Workloads: []gateway.Workload{{ID: "synthetic-interactive", Class: "critical", Arrivals: 1000, Completed: 900, Rejected: 100, QueueP95MS: 400, TotalP95MS: 500}, {ID: "synthetic-background", Class: "background", Arrivals: 1000, Completed: 1000, QueueP95MS: 1, TotalP95MS: 10}}}}}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	runner, e := agent.New(agent.Config{Directory: dir, Target: "fixture", Path: domain.PathPostgres, MaxDuration: 2 * time.Minute, Cooldown: time.Minute, MaxEvidenceAge: 3 * time.Minute, MinimumSamples: 100, MaxRunsPerHour: 1}, a, client)
	if e != nil {
		t.Fatal(e)
	}
	defer runner.Close()
	result, runErr := runner.Run(t.Context(), agent.Trigger{Kind: "slo"}, agent.Approval{})
	if result.CandidateID == "" {
		t.Fatalf("real tool loop did not produce a validated candidate: outcome=%s steps=%d error=%v; stable policy unchanged", result.Outcome, result.Steps, runErr)
	}
	t.Log("Real configured-model candidate validated against synthetic approved context. No traffic activation.")
}

// Diagnostics contain only tool names and typed fixture policy fields. Never log
// HTTP headers, credentials or raw provider errors.
type observedModel struct {
	model gateway.Model
	t     *testing.T
}

func (m observedModel) Infer(ctx context.Context, in gateway.ModelRequest) (gateway.ModelResponse, error) {
	out, e := m.model.Infer(ctx, in)
	if out.Tool != nil {
		m.t.Logf("model tool=%s", out.Tool.Name)
		if out.Tool.Name == "SubmitCandidate" {
			var v struct {
				Expected  string        `json:"expected_revision"`
				Guardrail string        `json:"guardrail_version"`
				Policy    policy.Policy `json:"policy"`
			}
			if json.Unmarshal(out.Tool.Input, &v) == nil {
				m.t.Logf("candidate metadata matches=%v weights=%+v reserve=%g rule_count=%d settings=%d", v.Expected == in.Context.PolicyRevision && v.Guardrail == in.Context.GuardrailVersion, v.Policy.ClassWeights, v.Policy.ReserveFraction, len(v.Policy.Rules), len(v.Policy.Workloads))
			}
		}
	} else {
		m.t.Logf("model done=%v usage_known=%v error=%v", out.Done, out.Usage.Known, e)
	}
	return out, e
}
