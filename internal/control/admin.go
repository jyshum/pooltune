package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"pooltune/internal/admin"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/store"
	"time"
)

type ownerCommand struct{ Key, Hash, Action, Revision, ResultRevision, Status string }

func (s *Service) ownerRevision(v StateView) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%t", v.Active.Revision, len(s.persisted.Commands), s.persisted.Frozen)))
	return "owner-" + hex.EncodeToString(hash[:12])
}
func (s *Service) saveOwner() error {
	raw, err := json.Marshal(s.persisted)
	if err != nil || s.files.Save("service.json", raw) != nil {
		return ErrStorage
	}
	return nil
}
func (s *Service) Read(ctx context.Context, resource string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch resource {
	case "revisions":
		events, err := s.coordinator.ledger.Events(s.coordinator.target)
		if err != nil {
			return nil, err
		}
		items := []map[string]string{}
		for _, event := range events {
			items = append(items, map[string]string{"id": event.Revision, "actor": event.Actor, "status": string(event.Kind), "at": event.RecordedAt.Format(time.RFC3339), "reason": "durable activation"})
		}
		rejected, err := s.coordinator.ledger.Rejections(s.coordinator.target)
		if err != nil {
			return nil, err
		}
		for _, r := range rejected {
			items = append(items, map[string]string{"id": r.Revision, "actor": "traffic", "status": "rejected", "at": r.RecordedAt, "reason": "resume declined before activation"})
		}
		return map[string]any{"items": items}, nil
	case "runs":
		if s.runner == nil {
			return map[string]any{"items": []any{}}, nil
		}
		items, err := s.runner.History()
		return map[string]any{"items": items}, err
	case "state":
	default:
		return nil, admin.ErrDenied
	}
	state, err := s.view(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.client.View(ctx)
	if err != nil {
		return nil, err
	}
	if len(v.Active.Limits) != 1 {
		return nil, ErrInvalid
	}
	l := v.Active.Limits[0]
	groups := []map[string]any{}
	classes := map[string]domain.Class{}
	for _, rule := range state.Policy.Rules {
		classes[rule.WorkloadID] = rule.Class
	}
	for _, w := range state.Registry.Workloads {
		class := w.DefaultClass
		if c, ok := classes[w.ID]; ok {
			class = c
		}
		if w.LockedClass != "" {
			class = w.LockedClass
		}
		groups = append(groups, map[string]any{"workload_id": w.ID, "label": w.ID, "class": class, "locked": w.LockedClass != "", "source": "owner_configuration", "allowed_classes": w.AllowedClasses})
	}
	// A missing measurement stays null, never a fabricated zero-error/latency value.
	var p95, rate any
	if state.Evidence.Complete {
		var maximum float64
		var arrivals, failed uint64
		for _, w := range state.Evidence.Workloads {
			maximum = max(maximum, w.TotalP95MS)
			arrivals += w.Arrivals
			failed += w.Failed
		}
		p95 = maximum
		if arrivals > 0 {
			rate = float64(failed) / float64(arrivals)
		}
	}
	queued := 0
	for _, co := range v.Cohorts {
		queued += co.Queued
	}
	out := map[string]any{"revision": s.ownerRevision(v), "target": s.cfg.Target, "provider": "local", "path": s.cfg.Path, "ready": v.Ready && l.Effective > 0, "frozen": s.persisted.Frozen, "setup": map[string]any{"verified": v.Ready, "observed": state.Evidence.Complete, "approved": true, "groups": groups, "matched_percent": nil, "ai_available": s.runner != nil}, "limits": map[string]any{"effective": l.Effective, "physical": nil, "stable": l.CohortBudgets["stable"], "candidate": l.CohortBudgets["candidate"]}, "metrics": map[string]any{"p95_ms": p95, "error_rate": rate, "queued": queued}, "safety": map[string]any{"restricted": v.SafetyCeiling != nil, "stopped": v.SafetyCeiling != nil && l.Effective == 0, "resume_ready": v.ResumeCapacity > 0 && !state.Frozen && !state.ExperimentActive, "start_capacity": v.ResumeCapacity}, "gates": []string{"Local development profile. External fencing, correlated restore and managed-provider compatibility remain unverified.", "Initial boundaries come from the approved server profile. Online boundary/lock editing is not enabled.", "Latency is the largest observed workload p95 bucket bound, not a pooled percentile. Missing measurements stay unavailable."}}
	if state.Evidence.Complete {
		out["setup"].(map[string]any)["matched_percent"] = 100
	}
	if s.manager != nil {
		if ex, reason := s.manager.View(); ex != nil {
			fraction := 0.
			if ex.Stage >= 0 && ex.Stage < len(ex.Fractions) {
				fraction = ex.Fractions[ex.Stage]
			}
			out["experiment"] = map[string]any{"phase": ex.Phase, "fraction": fraction, "reason": reason}
		}
	}
	return out, nil
}
func (s *Service) Execute(ctx context.Context, action string, c admin.Command) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(struct {
		Action  string
		Command admin.Command
	}{action, c})
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	for i, old := range s.persisted.Commands {
		if old.Key != c.IdempotencyKey {
			continue
		}
		if old.Hash != hash {
			return nil, admin.ErrConflict
		}
		if old.Status == "rejected" {
			return nil, admin.ErrConflict
		}
		if old.Status != "confirmed" {
			ack, err := s.coordinator.Reconcile(ctx, domain.RevisionID(old.Revision))
			if err == nil && ack.State == "rejected" {
				s.persisted.Commands[i].Status = "rejected"
				if err = s.saveOwner(); err != nil {
					return nil, err
				}
				return nil, admin.ErrConflict
			}
			if err != nil || ack.State != "active" {
				return nil, ErrRecovery
			}
			v, err := s.client.View(ctx)
			if err != nil {
				return nil, err
			}
			s.persisted.Commands[i].Status = "confirmed"
			s.persisted.Commands[i].ResultRevision = s.ownerRevision(v)
			if err = s.saveOwner(); err != nil {
				return nil, err
			}
			old = s.persisted.Commands[i]
		}
		return map[string]string{"revision": old.ResultRevision, "status": old.Status}, nil
	}
	v, err := s.client.View(ctx)
	if err != nil {
		return nil, err
	}
	if c.ExpectedRevision != s.ownerRevision(v) {
		return nil, admin.ErrConflict
	}
	if c.IdempotencyKey == "" || len(c.IdempotencyKey) > 128 || len(s.persisted.Commands) >= 256 {
		return nil, admin.ErrDenied
	}
	for _, record := range s.persisted.Commands {
		if record.Status != "confirmed" && record.Status != "rejected" {
			return nil, ErrRecovery
		}
	}
	switch action {
	case "freeze", "resume", "revert", "safety-resume":
	case "setup/verify", "setup/observe":
		state, err := s.view(ctx)
		if err != nil || state.Frozen || action == "setup/observe" && !state.Evidence.Complete {
			return nil, ErrRecovery
		}
		return map[string]string{"revision": s.ownerRevision(v), "status": "observed"}, nil
	default:
		return nil, admin.ErrDenied
	}
	if action == "safety-resume" {
		state, err := s.view(ctx)
		if err != nil || state.Frozen || state.ExperimentActive || v.ResumeCapacity <= 0 {
			return nil, ErrRecovery
		}
		pending, err := s.coordinator.ledger.Pending(s.coordinator.target)
		if err != nil || len(pending) > 0 {
			return nil, ErrRecovery
		}
	}
	if action == "resume" && !v.Ready {
		return nil, ErrRecovery
	}
	resumeCapacity := v.ResumeCapacity
	// Persist a command marker before any effect; interruption leaves it uncertain.
	keyHash := sha256.Sum256([]byte(s.cfg.Target + "/" + c.IdempotencyKey))
	revision := "owner-" + action + "-" + hex.EncodeToString(keyHash[:12])
	if action == "safety-resume" {
		revision = "safety-resume-" + hex.EncodeToString(keyHash[:12])
	}
	var restored *policy.Policy
	if action == "revert" {
		prior, err := s.coordinator.confirmedPolicy(c.Revision)
		if err != nil {
			return nil, err
		}
		state, err := s.view(ctx)
		if err != nil {
			return nil, err
		}
		prior.ExpectedPredecessor = state.Policy.Revision
		prior.Revision = domain.RevisionID(revision + "-policy")
		prior.GuardrailVersion = state.Guardrails.Version
		if _, err = policy.Compile(prior, state.Guardrails, state.Registry); err != nil {
			return nil, err
		}
		restored = &prior
	}
	index := len(s.persisted.Commands)
	s.persisted.Commands = append(s.persisted.Commands, ownerCommand{Key: c.IdempotencyKey, Hash: hash, Action: action, Revision: revision, Status: "pending"})
	if err = s.saveOwner(); err != nil {
		s.persisted.Frozen = true
		s.driver.frozen.Store(true)
		return nil, err
	}
	if action == "freeze" || action == "resume" {
		if err = s.setFrozen(ctx, action == "freeze"); err != nil {
			return nil, err
		}
	}
	if action == "revert" {
		if err = s.setFrozen(ctx, true); err != nil {
			return nil, err
		}
	}
	v, err = s.client.View(ctx)
	if err != nil {
		return nil, err
	}
	if !v.Ready || v.AuditGap || v.Active.CandidateFraction != 0 {
		return nil, ErrRecovery
	}
	env := v.Active
	env.Limits = append([]domain.LimitSnapshot(nil), env.Limits...)
	if action == "safety-resume" {
		env.Limits[0].Effective = resumeCapacity
		env.Limits[0].CohortBudgets = map[string]int{"stable": resumeCapacity}
		env.Limits[0].CapacityRevision = revision
		var policies map[string]policy.Policy
		if decode(env.PolicyJSON, &policies) != nil {
			return nil, ErrInvalid
		}
		env.Limits[0].CriticalReserve = max(0, min(int(float64(resumeCapacity)*policies["stable"].ReserveFraction), resumeCapacity-1))
	}
	if action == "revert" {
		prior := *restored
		state, err := s.view(ctx)
		if err != nil {
			return nil, err
		}
		if _, err = policy.Compile(prior, state.Guardrails, state.Registry); err != nil {
			return nil, err
		}
		env.PolicyJSON, _ = json.Marshal(map[string]policy.Policy{"stable": prior})
		cap := env.Limits[0].Effective
		env.Limits[0].CriticalReserve = max(0, min(int(float64(cap)*prior.ReserveFraction), cap-1))
	}
	env.ExpectedPredecessor = env.Revision
	env.Revision = domain.RevisionID(revision)
	env.Limits[0].Revision = env.Revision
	env.Checksum = Checksum(env)
	if _, err = s.coordinator.Apply(ctx, env); err != nil {
		if errors.Is(err, ErrRejected) {
			s.persisted.Commands[index].Status = "rejected"
			if err = s.saveOwner(); err != nil {
				return nil, err
			}
			return nil, admin.ErrConflict
		}
		return nil, err
	}
	s.evidence.Complete = false
	v, err = s.client.View(ctx)
	if err != nil {
		return nil, err
	}
	s.persisted.Commands[index].Status = "confirmed"
	s.persisted.Commands[index].ResultRevision = s.ownerRevision(v)
	if err = s.saveOwner(); err != nil {
		return nil, err
	}
	return map[string]string{"revision": s.persisted.Commands[index].ResultRevision, "status": "confirmed"}, nil
}
func (c *Coordinator) confirmedPolicy(revision string) (policy.Policy, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !revisionPattern.MatchString(revision) {
		return policy.Policy{}, ErrInvalid
	}
	events, err := c.ledger.Events(c.target)
	if err != nil {
		return policy.Policy{}, err
	}
	confirmed := false
	for _, event := range events {
		if event.Revision == revision && event.Kind == store.KindConfirmed {
			confirmed = true
		}
	}
	if !confirmed {
		return policy.Policy{}, ErrRecovery
	}
	raw, err := c.payloads.Load("intent-" + eventID(c.target, domain.RevisionID(revision), store.KindIntent) + ".json")
	if err != nil {
		return policy.Policy{}, err
	}
	var env domain.ActivationEnvelope
	if decode(raw, &env) != nil || env.Checksum != Checksum(env) {
		return policy.Policy{}, ErrInvalid
	}
	var policies map[string]policy.Policy
	if decode(env.PolicyJSON, &policies) != nil || policies["stable"].Revision == "" {
		return policy.Policy{}, ErrInvalid
	}
	return policies["stable"], nil
}

// Resolve an interrupted safety resume from durable status, never by replaying
// the command or by assuming a transport failure means rejection.
func (s *Service) reconcileSafetyResume(ctx context.Context) (bool, error) {
	for i, c := range s.persisted.Commands {
		if c.Action != "safety-resume" || c.Status == "confirmed" || c.Status == "rejected" {
			continue
		}
		ack, err := s.coordinator.Reconcile(ctx, domain.RevisionID(c.Revision))
		if err != nil {
			return true, err
		}
		switch ack.State {
		case "rejected":
			s.persisted.Commands[i].Status = "rejected"
		case "active":
			v, err := s.client.View(ctx)
			if err != nil {
				return true, err
			}
			s.persisted.Commands[i].Status = "confirmed"
			s.persisted.Commands[i].ResultRevision = s.ownerRevision(v)
		default:
			return true, ErrRecovery
		}
		return true, s.saveOwner()
	}
	return false, nil
}
