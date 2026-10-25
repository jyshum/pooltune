package rollout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"pooltune/internal/domain"
	"pooltune/internal/durable"
	"pooltune/internal/policy"
	"sync"
	"time"
)

var ErrUncertain = errors.New("rollout acknowledgement uncertain")
var ErrStorage = errors.New("rollout history unavailable")

type Journal interface {
	RolloutCandidate(string, string) (json.RawMessage, error)
	LatestRollout(string) (int64, json.RawMessage, error)
	AppendRollout(string, string, int64, json.RawMessage) (int64, error)
}
type Runtime struct {
	ActivationRevision, PolicyRevision, GuardrailVersion, CapacityRevision string
	Epoch                                                                  uint64
	Effective                                                              int
	Healthy, Frozen, ProviderPending, CapacityPending, AuditGap            bool
}
type Command struct {
	ExperimentID, Revision, ExpectedRevision, GuardrailVersion, CapacityRevision string
	Path                                                                         domain.PathKind
	Epoch                                                                        uint64
	Phase                                                                        Phase
	Stage, Effective, StableBudget, CandidateBudget                              int
	Fraction                                                                     float64
	StablePolicy, CandidatePolicy                                                policy.Policy
	Expires                                                                      time.Time
}
type Ack struct {
	Revision string
	Active   bool
	At       time.Time
}

// Driver applies through the existing revision coordinator. Active means the
// requested budgets have drained into place, not merely that an API accepted it.
// Abort uses cached last-known-good state and a new revision, even on ledger
// failure; it must not affect an unrelated experiment or a newer human policy.
type Driver interface {
	Inspect(context.Context, domain.PathKind) (Runtime, error)
	Apply(context.Context, Command) (Ack, error)
	Status(context.Context, string) (Ack, error)
	Abort(context.Context, string, string) (Ack, error)
	Metrics(context.Context, Experiment) (MetricsSnapshot, error)
}
type ManagerConfig struct {
	Target, Directory string
	Profile           EvaluationProfile
	Lease, Interval   time.Duration
	Clock             func() time.Time
}
type StartRequest struct {
	ID, Key            string
	Path               domain.PathKind
	CandidateCreatedAt time.Time
	Stable, Candidate  policy.Policy
	Guardrails         policy.Guardrails
	Registry           policy.Registry
	Fraction           float64
}
type managerState struct {
	Experiment                         *Experiment
	Request                            *StartRequest
	Profile                            EvaluationProfile
	Pending                            *Command
	LastTick                           time.Time
	Kind, Reason, Key, AppliedRevision string
}
type Manager struct {
	mu            sync.Mutex
	cfg           ManagerConfig
	driver        Driver
	journal       Journal
	files         *durable.Files
	state         managerState
	sequence      int64
	storageFailed bool
}

func NewManager(c ManagerConfig, j Journal, d Driver) (*Manager, error) {
	if c.Target == "" || len(c.Target) > 128 || !c.Profile.valid() || c.Clock == nil || c.Interval <= 0 || c.Interval > time.Minute || c.Interval > c.Profile.MinimumWindow || c.Lease <= c.Profile.MaximumStage+c.Interval || c.Lease > time.Hour || j == nil || d == nil {
		return nil, ErrIneligible
	}
	profileJSON, _ := json.Marshal(c.Profile)
	var ownedProfile EvaluationProfile
	if json.Unmarshal(profileJSON, &ownedProfile) != nil {
		return nil, ErrIneligible
	}
	c.Profile = ownedProfile
	files, e := durable.Open(c.Directory)
	if e != nil {
		return nil, e
	}
	m := &Manager{cfg: c, journal: j, driver: d, files: files}
	seq, raw, e := j.LatestRollout(c.Target)
	if e != nil {
		files.Close()
		return nil, e
	}
	m.sequence = seq
	if seq > 0 {
		if json.Unmarshal(raw, &m.state) != nil || m.state.Experiment == nil || m.state.Experiment.Target != c.Target {
			files.Close()
			return nil, ErrStorage
		}
		// A restarted controller never resumes an unaccepted experiment. Tick executes
		// the same safety fallback before any elective work, irrespective of model state.
		if m.state.Experiment.Phase != Accepted && m.state.Experiment.Phase != Reverted {
			m.state.Kind = "restart_abort"
			if m.state.Pending != nil && m.state.Pending.Phase == Accepted {
				m.state.Kind = "restart_acceptance"
			}
		}
	}
	return m, nil
}
func (m *Manager) Close() error { return m.files.Close() }
func (m *Manager) persist(kind, reason string) error {
	m.state.Kind = kind
	m.state.Reason = reason
	raw, e := json.Marshal(m.state)
	if e != nil {
		m.storageFailed = true
		return ErrStorage
	}
	hash := sha256.Sum256(append([]byte(fmt.Sprint(m.sequence)), raw...))
	seq, e := m.journal.AppendRollout(m.cfg.Target, hex.EncodeToString(hash[:]), m.sequence, raw)
	if e != nil {
		m.storageFailed = true
		return ErrStorage
	}
	m.sequence = seq
	return nil
}
func (m *Manager) HoldElectiveChanges() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.storageFailed || m.state.Experiment != nil && m.state.Experiment.Phase != Accepted && m.state.Experiment.Phase != Reverted
}
func (m *Manager) View() (*Experiment, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Experiment == nil {
		return nil, ""
	}
	raw, _ := json.Marshal(m.state.Experiment)
	var out Experiment
	json.Unmarshal(raw, &out)
	return &out, m.state.Reason
}
func (m *Manager) Start(ctx context.Context, r StartRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.storageFailed {
		return ErrStorage
	}
	if m.state.Experiment != nil {
		if m.state.Key == r.Key && m.state.Experiment.ID == r.ID {
			old, _ := json.Marshal(m.state.Request)
			next, _ := json.Marshal(r)
			if string(old) != string(next) {
				return ErrIneligible
			}
			return nil
		}
		if m.state.Experiment.Phase != Accepted && m.state.Experiment.Phase != Reverted {
			return ErrIneligible
		}
	}
	prior, err := m.journal.RolloutCandidate(m.cfg.Target, string(r.Candidate.Revision))
	if err != nil {
		return ErrStorage
	}
	if len(prior) > 0 {
		return ErrIneligible
	}
	now := m.cfg.Clock()
	if m.state.Experiment != nil && !m.state.Experiment.CompletedAt.IsZero() && now.Sub(m.state.Experiment.CompletedAt) < m.cfg.Profile.Cooldown {
		return ErrIneligible
	}
	if r.ID == "" || len(r.ID) > 128 || r.Key == "" || len(r.Key) > 128 || (r.Path != domain.PathREST && r.Path != domain.PathPostgres) || r.CandidateCreatedAt.IsZero() || r.CandidateCreatedAt.After(now) || m.cfg.Profile.ApprovedAt.After(r.CandidateCreatedAt) || (r.Fraction != .05 && r.Fraction != .1) || r.Stable.Revision != r.Guardrails.CurrentRevision || r.Candidate.ExpectedPredecessor != r.Stable.Revision {
		return ErrIneligible
	}
	scope := map[string]bool{}
	for _, w := range r.Registry.Workloads {
		if w.Selector.Target != m.cfg.Target || w.Selector.Path != r.Path {
			return ErrIneligible
		}
		scope[w.ID] = true
	}
	if len(scope) != len(m.cfg.Profile.Objectives) {
		return ErrIneligible
	}
	for _, objective := range m.cfg.Profile.Objectives {
		if !scope[objective.Workload] {
			return ErrIneligible
		}
	}
	if _, e := policy.Compile(r.Candidate, r.Guardrails, r.Registry); e != nil {
		return ErrIneligible
	}
	runtime, e := m.driver.Inspect(ctx, r.Path)
	if e != nil || !runtime.Healthy || runtime.Frozen || runtime.ProviderPending || runtime.CapacityPending || runtime.AuditGap || runtime.ActivationRevision == "" || runtime.PolicyRevision != string(r.Stable.Revision) || runtime.GuardrailVersion != r.Guardrails.Version || runtime.Epoch == 0 {
		return ErrIneligible
	}
	if _, _, e = Budgets(runtime.Effective, r.Fraction, r.Stable, r.Candidate); e != nil {
		return e
	}
	raw, _ := json.Marshal(r)
	var owned StartRequest
	if json.Unmarshal(raw, &owned) != nil {
		return ErrIneligible
	}
	owned.Guardrails.CurrentPolicy = r.Guardrails.CurrentPolicy
	pRaw, _ := json.Marshal(m.cfg.Profile)
	var p EvaluationProfile
	json.Unmarshal(pRaw, &p)
	ex := &Experiment{ID: r.ID, Target: m.cfg.Target, Path: r.Path, StableRevision: string(r.Stable.Revision), CandidateRevision: string(r.Candidate.Revision), GuardrailVersion: r.Guardrails.Version, CapacityRevision: runtime.CapacityRevision, ProfileID: p.ID, Epoch: runtime.Epoch, CreatedAt: r.CandidateCreatedAt, Phase: Partitioning, Fractions: []float64{r.Fraction, .25, .5, 1}, LeaseExpires: now.Add(m.cfg.Lease)}
	m.state = managerState{Experiment: ex, Request: &owned, Profile: p, Key: r.Key, AppliedRevision: runtime.ActivationRevision}
	return m.transition(ctx, Partitioning, 0, runtime.Effective)
}
func (m *Manager) transition(ctx context.Context, phase Phase, stage, capacity int) error {
	e := m.state.Experiment
	r := m.state.Request
	fraction := e.Fractions[stage]
	c, s, err := Budgets(capacity, fraction, r.Stable, r.Candidate)
	if err != nil {
		return m.abort(ctx, "ineligible_stage")
	}
	revision := fmt.Sprintf("rollout-%s-%d", opaque(e.ID), m.sequence+1)
	command := Command{ExperimentID: e.ID, Revision: revision, ExpectedRevision: m.state.AppliedRevision, GuardrailVersion: e.GuardrailVersion, CapacityRevision: e.CapacityRevision, Path: e.Path, Epoch: e.Epoch, Phase: phase, Stage: stage, Effective: capacity, StableBudget: s, CandidateBudget: c, Fraction: fraction, StablePolicy: r.Stable, CandidatePolicy: r.Candidate, Expires: m.cfg.Clock().Add(m.cfg.Lease)}
	if phase == Partitioning {
		command.CandidatePolicy = r.Stable
	}
	m.state.Pending = &command
	if err = m.persist("activation_intent", ""); err != nil {
		return m.abort(ctx, "history_failure")
	}
	ack, err := m.driver.Apply(ctx, command)
	if err != nil {
		return ErrUncertain
	}
	return m.confirm(ack)
}
func opaque(value string) string { s := sha256.Sum256([]byte(value)); return hex.EncodeToString(s[:8]) }
func (m *Manager) confirm(a Ack) error {
	cmd := m.state.Pending
	if cmd == nil || a.Revision != cmd.Revision {
		return ErrUncertain
	}
	if !a.Active {
		return nil
	}
	now := m.cfg.Clock()
	if a.At.After(now) || a.At.IsZero() || !a.At.Before(cmd.Expires) {
		return ErrUncertain
	}
	e := m.state.Experiment
	e.Phase = cmd.Phase
	if cmd.Phase == Accepted {
		e.CompletedAt = now
	}
	e.Stage = cmd.Stage
	e.StageStarted = a.At
	e.Deadline = a.At.Add(m.state.Profile.MaximumStage)
	e.LeaseExpires = cmd.Expires
	e.LastWindowEnd = time.Time{}
	e.PassingWindows = 0
	m.state.AppliedRevision = cmd.Revision
	m.state.Pending = nil
	m.state.LastTick = time.Time{}
	return m.persist("activation_confirmed", "")
}
func (m *Manager) abort(ctx context.Context, reason string) error {
	// Record intent when possible, but ledger failure cannot disable safety.
	historyErr := m.persist("rollback_intent", reason)
	ack, e := m.driver.Abort(ctx, m.state.Experiment.ID, reason)
	if e != nil || !ack.Active || ack.Revision == "" {
		return ErrUncertain
	}
	m.state.Experiment.Phase = Reverted
	m.state.Experiment.CompletedAt = m.cfg.Clock()
	m.state.Pending = nil
	m.state.AppliedRevision = ack.Revision
	if historyErr != nil {
		return historyErr
	}
	return m.persist("rollback_confirmed", reason)
}
func (m *Manager) Tick(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.state.Experiment
	if e == nil || e.Phase == Accepted || e.Phase == Reverted {
		return nil
	}
	now := m.cfg.Clock()
	if m.state.Kind == "restart_acceptance" || (m.state.Pending != nil && m.state.Pending.Phase == Accepted && m.state.Kind != "rollback_intent" && !m.storageFailed) {
		ack, err := m.driver.Status(ctx, m.state.Pending.Revision)
		if err != nil {
			return m.abort(ctx, "acceptance_reconciliation_failed")
		}
		return m.confirm(ack)
	}
	if m.state.Kind == "restart_abort" || m.state.Kind == "rollback_intent" || m.storageFailed {
		return m.abort(ctx, "recovery_or_history_failure")
	}
	if !now.Before(e.LeaseExpires) {
		return m.abort(ctx, "lease_expired")
	}
	runtime, err := m.driver.Inspect(ctx, e.Path)
	if err != nil {
		return m.abort(ctx, "runtime_unavailable")
	}
	expectedActivation := runtime.ActivationRevision == m.state.AppliedRevision
	if m.state.Pending != nil {
		expectedActivation = expectedActivation || runtime.ActivationRevision == m.state.Pending.Revision || runtime.ActivationRevision == m.state.Pending.Revision+".drain"
	}
	if !expectedActivation {
		return m.abort(ctx, "activation_changed")
	}
	if !runtime.Healthy || runtime.Frozen || runtime.ProviderPending || runtime.CapacityPending || runtime.AuditGap || runtime.PolicyRevision != e.StableRevision || runtime.Epoch != e.Epoch || runtime.GuardrailVersion != e.GuardrailVersion || runtime.CapacityRevision != e.CapacityRevision {
		return m.abort(ctx, "safety_or_capacity_change")
	}
	if m.state.Pending != nil {
		ack, err := m.driver.Status(ctx, m.state.Pending.Revision)
		if err != nil {
			return ErrUncertain
		}
		return m.confirm(ack)
	}
	metrics, err := m.driver.Metrics(ctx, *e)
	if err != nil {
		if !now.Before(e.Deadline) {
			return m.abort(ctx, "missing_metrics")
		}
		return nil
	}
	now = m.cfg.Clock()
	metrics.Now = now
	decision := Evaluate(*e, metrics, m.state.Profile)
	if decision.Action == domain.DecisionRevert {
		return m.abort(ctx, decision.Reason)
	}
	if metrics.End.IsZero() || metrics.Start.IsZero() || metrics.End.After(now) {
		return nil
	}
	if !m.state.LastTick.IsZero() && now.Sub(m.state.LastTick) < m.cfg.Interval {
		return nil
	}
	m.state.LastTick = now
	if metrics.End.After(e.LastWindowEnd) && !metrics.Start.Before(e.LastWindowEnd) {
		e.LastWindowEnd = metrics.End
		if decision.Passing {
			e.PassingWindows++
		} else {
			e.PassingWindows = 0
		}
	}
	if decision.Action != domain.DecisionAdvance {
		return m.persist("evaluation", decision.Reason)
	}
	if e.Phase == Partitioning {
		raw, _ := json.Marshal(metrics.Window)
		json.Unmarshal(raw, &e.Baseline)
		return m.transition(ctx, Measuring, 0, runtime.Effective)
	}
	if e.Stage+1 < len(e.Fractions) {
		nextPhase := Measuring
		if e.Fractions[e.Stage+1] == 1 {
			nextPhase = Observing
		}
		return m.transition(ctx, nextPhase, e.Stage+1, runtime.Effective)
	}
	// Acceptance is itself a revision transition, not just a successful verdict.
	// Driver receives Accepted only after full-traffic observation passes, and must
	// preserve all old occupancy while making the candidate the new stable policy.
	return m.transition(ctx, Accepted, e.Stage, runtime.Effective)
}
