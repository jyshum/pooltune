// Package agent runs a bounded background tool loop. It never handles traffic.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"pooltune/internal/domain"
	"pooltune/internal/durable"
	"pooltune/internal/gateway"
	"pooltune/internal/policy"
)

var (
	// ErrCanaryRejected is returned only when the authority proves no canary
	// mutation was dispatched. Transport errors must never use this marker.
	ErrCanaryRejected = errors.New("canary rejected before dispatch")
	ErrInvalid        = errors.New("invalid agent input")
	ErrStale          = errors.New("agent evidence or policy is stale")
	ErrDenied         = errors.New("agent run denied")
	ErrStorage        = errors.New("agent history unavailable")
	ErrExhausted      = errors.New("agent step limit reached")
	ErrUnknown        = errors.New("canary request outcome uncertain")
)

type Config struct {
	Directory, Target                     string
	Path                                  domain.PathKind
	MaxDuration, Cooldown, MaxEvidenceAge time.Duration
	MinimumSamples                        uint64
	MaxRunsPerHour                        int
}
type Trigger struct{ Kind string }
type Approval struct{ SemanticPreviewHash string }
type Evidence struct {
	ID        string
	At        time.Time
	Complete  bool
	Samples   uint64
	Workloads []gateway.Workload
}
type History struct{ Revision, Outcome string }
type State struct {
	Target                   string
	Path                     domain.PathKind
	Policy                   policy.Policy
	Guardrails               policy.Guardrails
	Registry                 policy.Registry
	Evidence                 Evidence
	Frozen, ExperimentActive bool
	Descriptions             map[string]string
	History                  []History
}
type Candidate struct {
	ID                           string
	ExpectedRevision             domain.RevisionID
	GuardrailVersion, EvidenceID string
	Policy                       policy.Policy
}

// View must return a consistent owned snapshot. StartCanary must atomically
// recheck the predecessor/guardrails, safety and one-experiment constraint. It
// must durably deduplicate key before any activation; the runner never retries it.
type Authority interface {
	View(context.Context) (State, error)
	StartCanary(context.Context, Candidate, string) (string, error)
}
type Gateway interface {
	Start(context.Context) (string, error)
	Step(context.Context, string, int, gateway.ModelRequest) (gateway.ModelResponse, error)
}
type Trace struct {
	Step                      int
	Tool, Outcome             string
	InputTokens, OutputTokens int
}
type RunResult struct {
	RunID, Outcome, CandidateID, ExperimentID string
	Steps                                     int
	Discovery                                 []Suggestion
}
type record struct {
	RunResult
	Started                                               time.Time
	Trigger, PolicyRevision, GuardrailVersion, EvidenceID string
	Traces                                                []Trace
	Candidate                                             *Candidate
	CanaryKey                                             string
}
type journal struct {
	Target  string
	Path    domain.PathKind
	Records []record
}
type Runner struct {
	mu        sync.Mutex
	config    Config
	authority Authority
	gateway   Gateway
	files     *durable.Files
	state     journal
}

func New(c Config, a Authority, g Gateway) (*Runner, error) {
	if c.Target == "" || (c.Path != domain.PathPostgres && c.Path != domain.PathREST) || c.MaxDuration <= 0 || c.MaxDuration > 5*time.Minute || c.Cooldown <= 0 || c.Cooldown > 24*time.Hour || c.MaxEvidenceAge <= 0 || c.MaxEvidenceAge > time.Hour || c.MinimumSamples == 0 || c.MaxRunsPerHour < 1 || c.MaxRunsPerHour > 100 || a == nil || g == nil {
		return nil, ErrInvalid
	}
	files, e := durable.Open(c.Directory)
	if e != nil {
		return nil, e
	}
	r := &Runner{config: c, authority: a, gateway: g, files: files, state: journal{Target: c.Target, Path: c.Path}}
	raw, e := files.Load("agent.json")
	if e == nil {
		if decode(raw, &r.state) != nil || r.state.Target != c.Target || r.state.Path != c.Path || len(r.state.Records) > 256 {
			files.Close()
			return nil, ErrStorage
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		files.Close()
		return nil, ErrStorage
	}
	return r, nil
}
func (r *Runner) Close() error { return r.files.Close() }
func (r *Runner) save() error {
	b, e := json.Marshal(r.state)
	if e != nil || r.files.Save("agent.json", b) != nil {
		return ErrStorage
	}
	return nil
}
func (r *Runner) validState(s State) bool {
	age := time.Since(s.Evidence.At)
	return s.Target == r.config.Target && s.Path == r.config.Path && !s.Frozen && !s.ExperimentActive && s.Evidence.Complete && s.Evidence.Samples >= r.config.MinimumSamples && age >= 0 && age <= r.config.MaxEvidenceAge && s.Policy.Revision == s.Guardrails.CurrentRevision && s.Policy.GuardrailVersion == s.Guardrails.Version && s.Guardrails.CurrentPolicy != nil && s.Guardrails.CurrentPolicy.Revision() == s.Policy.Revision
}
func (r *Runner) Run(ctx context.Context, trigger Trigger, approval Approval) (out RunResult, err error) {
	if !r.mu.TryLock() {
		return out, ErrDenied
	}
	defer r.mu.Unlock()
	switch trigger.Kind {
	case "slo", "starvation", "mix", "queue_expiry", "operation", "manual", "discovery":
	default:
		return out, ErrInvalid
	}
	if len(r.state.Records) >= 256 {
		return out, ErrStorage
	}
	now := time.Now()
	recent := 0
	for _, record := range r.state.Records {
		if record.Outcome == "canary_pending" || record.Outcome == "canary_uncertain" {
			return out, ErrUnknown
		}
		if record.Started.After(now.Add(-time.Hour)) {
			recent++
		}
		if now.Sub(record.Started) < r.config.Cooldown {
			return out, ErrDenied
		}
	}
	if recent >= r.config.MaxRunsPerHour {
		return out, ErrDenied
	}
	ctx, cancel := context.WithTimeout(ctx, r.config.MaxDuration)
	defer cancel()
	state, e := r.authority.View(ctx)
	if e != nil || !r.validState(state) {
		return out, ErrStale
	}
	mapped, e := prepare(state, approval.SemanticPreviewHash != "")
	if e != nil {
		return out, e
	}
	if approval.SemanticPreviewHash != "" && mapped.preview.Hash != approval.SemanticPreviewHash {
		return out, ErrDenied
	}
	idBytes := make([]byte, 16)
	if _, e = rand.Read(idBytes); e != nil {
		return out, e
	}
	id := "run-" + hex.EncodeToString(idBytes)
	index := len(r.state.Records)
	r.state.Records = append(r.state.Records, record{RunResult: RunResult{RunID: id, Outcome: "started"}, Started: now, Trigger: trigger.Kind, PolicyRevision: string(state.Policy.Revision), GuardrailVersion: state.Guardrails.Version, EvidenceID: state.Evidence.ID})
	if e = r.save(); e != nil {
		return out, e
	}
	rec := &r.state.Records[index]
	defer func() {
		if err != nil && rec.Outcome != "canary_uncertain" && rec.Outcome != "canary_pending" && rec.Outcome != "canary_requested" && rec.Outcome != "canary_rejected" {
			rec.Outcome = "stopped"
		}
		out = rec.RunResult
		if e := r.save(); e != nil {
			err = e
		}
	}()
	run, e := r.gateway.Start(ctx)
	if e != nil {
		return out, ErrDenied
	}
	op := "priority"
	if trigger.Kind == "discovery" {
		op = "discovery"
	}
	input := gateway.ModelRequest{Operation: op, Reason: trigger.Kind, Context: mapped.preview.Context}
	for step := 0; step < 8; step++ {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		response, e := r.gateway.Step(ctx, run, step, input)
		rec.Steps = step + 1
		if e != nil {
			rec.Traces = append(rec.Traces, Trace{Step: step, Outcome: "gateway_denied"})
			return out, ErrDenied
		}
		if response.Done == (response.Tool != nil) {
			return out, ErrInvalid
		}
		if response.Done {
			rec.Outcome = "no_action"
			if rec.Candidate != nil {
				rec.Outcome = "candidate_only"
			}
			return rec.RunResult, nil
		}
		result, terminal, e := r.execute(ctx, rec, mapped, op, *response.Tool)
		trace := Trace{Step: step, Tool: safeTool(response.Tool.Name), Outcome: "accepted", InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens}
		if e != nil {
			trace.Outcome = "rejected"
		}
		rec.Traces = append(rec.Traces, trace)
		if saveErr := r.save(); saveErr != nil {
			return out, saveErr
		}
		if e != nil {
			return out, e
		}
		if terminal {
			return rec.RunResult, nil
		}
		input.History = append(input.History, gateway.Exchange{Tool: response.Tool.Name, Result: result})
	}
	return out, ErrExhausted
}
func safeTool(name string) string {
	switch name {
	case "ReadState", "ReadWorkloadSummary", "ReadHistory", "SubmitCandidate", "StartCanary", "ProposeDiscovery":
		return name
	}
	return "disallowed"
}
