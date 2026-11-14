package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"pooltune/internal/domain"
	"pooltune/internal/gateway"
	"pooltune/internal/policy"
)

func (r *Runner) execute(ctx context.Context, rec *record, m mapping, operation string, call gateway.ToolCall) (json.RawMessage, bool, error) {
	if len(call.Input) > 64<<10 || safeTool(call.Name) == "disallowed" {
		return nil, false, ErrInvalid
	}
	if operation == "discovery" {
		if call.Name != "ProposeDiscovery" {
			return nil, false, ErrDenied
		}
		suggestions, e := discovery(call.Input, m)
		if e != nil {
			return nil, false, e
		}
		rec.Discovery = suggestions
		rec.Outcome = "discovery_review_required"
		return nil, true, nil
	}
	if call.Name == "ProposeDiscovery" {
		return nil, false, ErrDenied
	}
	// A human edit, freeze or changed experiment invalidates this entire run.
	state, e := r.authority.View(ctx)
	if e != nil || !r.validState(state) || state.Policy.Revision != m.state.Policy.Revision || state.Guardrails.Version != m.state.Guardrails.Version {
		return nil, false, ErrStale
	}
	encode := func(v any) (json.RawMessage, bool, error) { b, e := json.Marshal(v); return b, false, e }
	switch call.Name {
	case "ReadState":
		var req struct{}
		if decode(call.Input, &req) != nil {
			return nil, false, ErrInvalid
		}
		return encode(struct {
			PolicyRevision, GuardrailVersion, SnapshotID string
			Frozen, ExperimentActive                     bool
		}{m.preview.Context.PolicyRevision, m.preview.Context.GuardrailVersion, m.preview.Context.SnapshotID, state.Frozen, state.ExperimentActive})
	case "ReadWorkloadSummary":
		var req struct {
			Workloads []string `json:"workload_ids"`
		}
		if decode(call.Input, &req) != nil || len(req.Workloads) == 0 || len(req.Workloads) > 32 {
			return nil, false, ErrInvalid
		}
		result := []gateway.Workload{}
		seen := map[string]bool{}
		for _, id := range req.Workloads {
			if _, ok := m.local[id]; !ok || seen[id] {
				return nil, false, ErrInvalid
			}
			seen[id] = true
			for _, w := range m.preview.Context.Workloads {
				if w.ID == id {
					result = append(result, w)
				}
			}
		}
		return encode(result)
	case "ReadHistory":
		var req struct {
			Limit int `json:"limit"`
		}
		if decode(call.Input, &req) != nil || req.Limit < 1 || req.Limit > 10 {
			return nil, false, ErrInvalid
		}
		history := []History{}
		for i := len(state.History) - 1; i >= 0 && len(history) < req.Limit; i-- {
			item := state.History[i]
			switch item.Outcome {
			case "stable", "reverted", "inconclusive", "unsafe", "canary", "observing":
			default:
				item.Outcome = "unknown"
			}
			item.Revision = opaque("p", item.Revision)
			history = append(history, item)
		}
		return encode(history)
	case "SubmitCandidate":
		if rec.Candidate != nil {
			return nil, false, ErrDenied
		}
		var req struct {
			ExpectedRevision string        `json:"expected_revision"`
			GuardrailVersion string        `json:"guardrail_version"`
			EvidenceIDs      []string      `json:"evidence_ids"`
			Policy           policy.Policy `json:"policy"`
		}
		if decode(call.Input, &req) != nil || req.ExpectedRevision != m.preview.Context.PolicyRevision || req.GuardrailVersion != m.preview.Context.GuardrailVersion || len(req.EvidenceIDs) != 1 || req.EvidenceIDs[0] != m.preview.Context.SnapshotID {
			return nil, false, ErrStale
		}
		// Recheck the original evidence window as well as fresh control state.
		if !r.validState(m.state) {
			return nil, false, ErrStale
		}
		p := req.Policy
		p.Revision = domain.RevisionID("candidate-" + rec.RunID[4:])
		p.ExpectedPredecessor = state.Policy.Revision
		p.GuardrailVersion = state.Guardrails.Version
		p.Description = ""
		for i := range p.Workloads {
			local, ok := m.local[p.Workloads[i].WorkloadID]
			if !ok {
				return nil, false, ErrInvalid
			}
			p.Workloads[i].WorkloadID = local
		}
		for i := range p.Rules {
			local, ok := m.local[p.Rules[i].WorkloadID]
			if !ok {
				return nil, false, ErrInvalid
			}
			p.Rules[i].ID = "agent-" + m.opaque[local]
			p.Rules[i].WorkloadID = local
			p.Rules[i].Description = ""
		}
		if changedFamilies(state.Policy, p) != 1 {
			return nil, false, ErrInvalid
		}
		if _, e = policy.Compile(p, state.Guardrails, state.Registry); e != nil {
			return nil, false, ErrInvalid
		}
		c := Candidate{ID: string(p.Revision), ExpectedRevision: state.Policy.Revision, GuardrailVersion: state.Guardrails.Version, EvidenceID: m.state.Evidence.ID, Policy: p}
		rec.Candidate = &c
		rec.CandidateID = c.ID
		// Inactive candidate is durable before acknowledgement, never half-applied.
		if e = r.save(); e != nil {
			return nil, false, e
		}
		return encode(map[string]string{"candidate_id": c.ID, "status": "validated_inactive"})
	case "StartCanary":
		var req struct {
			CandidateID string `json:"candidate_id"`
			Key         string `json:"idempotency_key"`
		}
		if decode(call.Input, &req) != nil || rec.Candidate == nil || req.CandidateID != rec.Candidate.ID || len(req.Key) < 1 || len(req.Key) > 128 {
			return nil, false, ErrInvalid
		}
		if !r.validState(m.state) {
			return nil, false, ErrStale
		}
		c := *rec.Candidate
		if _, e = policy.Compile(c.Policy, state.Guardrails, state.Registry); e != nil {
			return nil, false, ErrStale
		}
		// Namespace a caller key with this local run; never trust arbitrary text in a
		// persisted trace or let another run reuse the mutation identity.
		rec.CanaryKey = opaque("canary-", rec.RunID+"/"+req.Key)
		rec.Outcome = "canary_pending"
		if e = r.save(); e != nil {
			return nil, false, e
		}
		experiment, e := r.authority.StartCanary(ctx, c, rec.CanaryKey)
		if errors.Is(e, ErrCanaryRejected) {
			rec.Outcome = "canary_rejected"
			return nil, true, e
		}
		if e != nil {
			rec.Outcome = "canary_uncertain"
			return nil, true, ErrUnknown
		}
		if experiment == "" || len(experiment) > 128 {
			rec.Outcome = "canary_uncertain"
			return nil, true, ErrUnknown
		}
		rec.ExperimentID = experiment
		rec.Outcome = "canary_requested"
		// Canary acceptance terminates inference. The deterministic evaluator owns
		// all subsequent decisions, including a crash or model outage after this.
		return nil, true, nil
	}
	return nil, false, ErrInvalid
}
func changedFamilies(a, b policy.Policy) int {
	n := 0
	if a.ReserveFraction != b.ReserveFraction {
		n++
	}
	if a.ClassWeights != b.ClassWeights {
		n++
	}
	rules := func(p policy.Policy) map[string]domain.Class {
		m := map[string]domain.Class{}
		for _, r := range p.Rules {
			m[r.WorkloadID] = r.Class
		}
		return m
	}
	settings := func(p policy.Policy) map[string]policy.WorkloadSettings {
		m := map[string]policy.WorkloadSettings{}
		for _, w := range p.Workloads {
			m[w.WorkloadID] = w
		}
		return m
	}
	if !reflect.DeepEqual(rules(a), rules(b)) {
		n++
	}
	if !reflect.DeepEqual(settings(a), settings(b)) {
		n++
	}
	return n
}

// Pending returns local records needing operator/coordinator reconciliation.
// It is read-only and never replays a paid call or an uncertain mutation.
func (r *Runner) Pending() []RunResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := []RunResult{}
	for _, rec := range r.state.Records {
		switch rec.Outcome {
		case "started", "canary_pending", "canary_uncertain":
			out := rec.RunResult
			out.Discovery = nil
			result = append(result, out)
		}
	}
	return result
}
