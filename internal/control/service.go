package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pooltune/internal/agent"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"pooltune/internal/durable"
	"pooltune/internal/gateway"
	"pooltune/internal/policy"
	"pooltune/internal/rollout"
	"pooltune/internal/store"
	"pooltune/internal/telemetry"
)

type MeasurementClient interface {
	RolloutClient
	BeginWindow(context.Context, WindowRequest) error
	ReadWindow(context.Context) (WindowSnapshot, error)
}

// ServiceConfig contains approved local authority, never credentials. A nil
// evaluation profile permits observation only. File-based launch additionally
// verifies the calibration artifact digest before constructing this service.
type ServiceConfig struct {
	Capacity                            *CapacityProfile
	Target, Directory, ResourceSource   string
	Path                                domain.PathKind
	Guardrails                          policy.Guardrails
	Registry                            policy.Registry
	Tick, Window, MaxEvidenceAge, Lease time.Duration
	Evaluation                          *rollout.EvaluationProfile
	Agent                               *agent.Config
	Trigger                             *agent.TriggerProfile
}
type serviceState struct {
	Target   string
	Path     domain.PathKind
	Frozen   bool
	Commands []ownerCommand
}
type Service struct {
	quiet            quietTracker
	syncedFallback   domain.RevisionID
	mu               sync.Mutex
	cfg              ServiceConfig
	client           MeasurementClient
	coordinator      *Coordinator
	driver           *RolloutDriver
	manager          *rollout.Manager
	runner           *agent.Runner
	gate             *agent.TriggerGate
	files            *durable.Files
	persisted        serviceState
	evidence         agent.Evidence
	evidenceRevision domain.RevisionID
	lastWindow       time.Time
	signal           agent.Signal
	ready            atomic.Bool
	running          atomic.Bool
}

func NewService(c ServiceConfig, db *store.Store, coord *Coordinator, client MeasurementClient, model agent.Gateway) (*Service, error) {
	if db == nil || coord == nil || client == nil || c.Target != coord.owner || c.Path != coord.path || c.ResourceSource == "" || c.Tick <= 0 || c.Tick > time.Second || c.Window < c.Tick || c.Window > time.Hour || c.MaxEvidenceAge < c.Tick || c.MaxEvidenceAge > time.Hour || len(c.Registry.Workloads) == 0 || len(c.Registry.Workloads) > 128 {
		return nil, ErrInvalid
	}
	if p := c.Capacity; p != nil {
		hash, err := hex.DecodeString(p.EvidenceSHA256)
		id := telemetry.Identity{Target: c.Target, Path: c.Path, Epoch: 1, Policy: "validation", Capacity: "validation", Guardrail: c.Guardrails.Version}
		if err != nil || len(hash) != 32 || (c.Path != domain.PathPostgres && c.Path != domain.PathREST) || (c.Path == domain.PathREST && p.Guardrails.HTTPEnvelope <= 0) || controller.NextCapacity(time.Now(), controller.State{Identity: id}, controller.MetricsSnapshot{}, p.Guardrails).Err != "" {
			return nil, ErrInvalid
		}
	}
	// Own all mutable maps/slices. Compiled policy is rebuilt from the observed revision.
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, ErrInvalid
	}
	var owned ServiceConfig
	if err = json.Unmarshal(raw, &owned); err != nil {
		return nil, ErrInvalid
	}
	c = owned
	for _, w := range c.Registry.Workloads {
		if w.Selector.Target != c.Target || w.Selector.Path != c.Path {
			return nil, ErrInvalid
		}
	}
	files, err := durable.Open(c.Directory)
	if err != nil {
		return nil, err
	}
	s := &Service{cfg: c, client: client, coordinator: coord, files: files, persisted: serviceState{Target: c.Target, Path: c.Path}}
	ok := false
	defer func() {
		if !ok {
			s.Close()
		}
	}()
	raw, err = files.Load("service.json")
	if err == nil {
		if decode(raw, &s.persisted) != nil || s.persisted.Target != c.Target || s.persisted.Path != c.Path {
			return nil, ErrStorage
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrStorage
	}
	d, err := NewRolloutDriver(c.Target, c.Path, coord, client, nil)
	if err != nil {
		return nil, err
	}
	s.driver = d
	d.frozen.Store(s.persisted.Frozen)
	if c.Evaluation != nil {
		if c.Window != c.Evaluation.MinimumWindow {
			return nil, ErrInvalid
		}
		if err = d.ConfigureMeasurements(c.Window, c.ResourceSource); err != nil {
			return nil, err
		}
		dir := filepath.Join(c.Directory, "rollout")
		if err = os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		s.manager, err = rollout.NewManager(rollout.ManagerConfig{Target: c.Target, Directory: dir, Profile: *c.Evaluation, Lease: c.Lease, Interval: c.Tick, Clock: time.Now}, db, d)
		if err != nil {
			return nil, err
		}
	}
	if model != nil {
		if c.Agent == nil || c.Trigger == nil || s.manager == nil || c.Trigger.MaximumGap < c.Window+c.Tick {
			return nil, ErrInvalid
		}
		ac := *c.Agent
		ac.Target = c.Target
		ac.Path = c.Path
		ac.Directory = filepath.Join(c.Directory, "agent")
		if err = os.MkdirAll(ac.Directory, 0700); err != nil {
			return nil, err
		}
		s.runner, err = agent.New(ac, s, model)
		if err != nil {
			return nil, err
		}
		s.gate, err = agent.NewTriggerGate(*c.Trigger)
		if err != nil {
			return nil, err
		}
	}
	ok = true
	return s, nil
}

// Close follows Run's return, so the agent worker has already stopped.
func (s *Service) Close() error {
	if s.runner != nil {
		s.runner.Close()
	}
	if s.manager != nil {
		s.manager.Close()
	}
	if s.files != nil {
		return s.files.Close()
	}
	return nil
}
func (s *Service) Ready() bool { return s.ready.Load() }
func (s *Service) view(ctx context.Context) (agent.State, error) {
	v, err := s.client.View(ctx)
	if err != nil {
		return agent.State{}, err
	}
	if v.AuthoritySHA256 != AuthorityChecksum(s.cfg.Guardrails, s.cfg.Registry) || v.Active.Checksum != Checksum(v.Active) || len(v.Active.Limits) != 1 || v.Active.Limits[0].Target != s.cfg.Target || v.Active.Limits[0].Path != s.cfg.Path || v.Epoch != v.Active.Epoch || v.Active.GuardrailVersion != s.cfg.Guardrails.Version {
		return agent.State{}, ErrStale
	}
	var policies map[string]policy.Policy
	if decode(v.Active.PolicyJSON, &policies) != nil {
		return agent.State{}, ErrInvalid
	}
	p, ok := policies["stable"]
	if !ok {
		return agent.State{}, ErrInvalid
	}
	bounds := s.cfg.Guardrails
	bounds.CurrentRevision = ""
	bounds.CurrentPolicy = nil
	approved := p
	approved.ExpectedPredecessor = ""
	compiled, err := policy.Compile(approved, bounds, s.cfg.Registry)
	if err != nil {
		return agent.State{}, ErrInvalid
	}
	bounds.CurrentRevision = p.Revision
	bounds.CurrentPolicy = compiled
	frozen := s.persisted.Frozen || v.PhysicalBlocked || !v.Ready || v.AuditGap || !v.Drained
	for _, command := range s.persisted.Commands {
		frozen = frozen || (command.Status != "confirmed" && command.Status != "rejected")
	}
	for _, co := range v.Cohorts {
		frozen = frozen || co.Uncertain > 0
	}
	evidence := s.evidence
	age := time.Since(evidence.At)
	if s.evidenceRevision != v.Active.Revision || age < 0 || age > s.cfg.MaxEvidenceAge || frozen {
		evidence.Complete = false
	}
	active := v.Active.CandidateFraction > 0 || s.manager != nil && s.manager.HoldElectiveChanges()
	return agent.State{Target: s.cfg.Target, Path: s.cfg.Path, Policy: p, Guardrails: bounds, Registry: s.cfg.Registry, Evidence: evidence, Frozen: frozen, ExperimentActive: active}, nil
}
func (s *Service) View(ctx context.Context) (agent.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.view(ctx)
	if err != nil {
		return state, err
	}
	// Callers cannot mutate the service's evidence or configured registry.
	raw, _ := json.Marshal(state)
	var owned agent.State
	if json.Unmarshal(raw, &owned) != nil {
		return agent.State{}, ErrInvalid
	}
	owned.Guardrails.CurrentPolicy = state.Guardrails.CurrentPolicy
	return owned, nil
}
func (s *Service) StartCanary(ctx context.Context, c agent.Candidate, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.manager == nil {
		return "", fmt.Errorf("%w: %w", agent.ErrCanaryRejected, agent.ErrDenied)
	}
	state, err := s.view(ctx)
	if err != nil || state.Frozen || state.ExperimentActive || !state.Evidence.Complete || c.ExpectedRevision != state.Policy.Revision || c.Policy.ExpectedPredecessor != state.Policy.Revision || c.GuardrailVersion != state.Guardrails.Version || c.EvidenceID != state.Evidence.ID {
		return "", fmt.Errorf("%w: %w", agent.ErrCanaryRejected, agent.ErrStale)
	}
	hash := sha256.Sum256([]byte(s.cfg.Target + "/" + key))
	id := "agent-" + hex.EncodeToString(hash[:16])
	err = s.manager.Start(ctx, rollout.StartRequest{ID: id, Key: key, Path: s.cfg.Path, CandidateCreatedAt: time.Now(), Stable: state.Policy, Candidate: c.Policy, Guardrails: state.Guardrails, Registry: state.Registry, Fraction: .1})
	s.evidence.Complete = false
	if errors.Is(err, rollout.ErrIneligible) {
		return "", fmt.Errorf("%w: %w", agent.ErrCanaryRejected, err)
	}
	return id, err
}
func (s *Service) SetFrozen(ctx context.Context, frozen bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setFrozen(ctx, frozen)
}
func (s *Service) setFrozen(ctx context.Context, frozen bool) error {
	s.quiet = quietTracker{}
	next := s.persisted
	next.Frozen = frozen
	raw, _ := json.Marshal(next)
	err := s.files.Save("service.json", raw)
	if err != nil && !frozen {
		return ErrStorage
	}
	// A storage failure must still stop elective work and abort a canary.
	s.persisted = next
	s.driver.frozen.Store(frozen)
	s.evidence.Complete = false
	if s.manager != nil {
		if tickErr := s.manager.Tick(ctx); tickErr != nil {
			return tickErr
		}
	}
	if err != nil {
		s.ready.Store(false)
		return ErrStorage
	}
	return nil
}
func (s *Service) RunAgent(ctx context.Context, trigger agent.Trigger) (agent.RunResult, error) {
	if s.runner == nil {
		return agent.RunResult{}, agent.ErrDenied
	}
	return s.runner.Run(ctx, trigger, agent.Approval{})
}

// Tick performs no inference. Manager safety checks run before collecting the
// next stable-policy observation; a slow or failed gateway never delays them.
func (s *Service) Tick(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready.Store(false)
	if pending, err := s.reconcileSafetyResume(ctx); err != nil || pending {
		s.evidence.Complete = false
		return err
	}
	if pending, err := s.reconcileCapacityIntent(ctx); err != nil || pending {
		s.evidence.Complete = false
		return err
	}
	if s.manager != nil {
		if err := s.manager.Tick(ctx); err != nil {
			s.evidence.Complete = false
			return err
		}
	}
	v, err := s.client.View(ctx)
	if err != nil {
		s.evidence.Complete = false
		return err
	}
	if !v.Ready {
		s.evidence.Complete = false
		return ErrRecovery
	}
	// Reconcile observed safety fallback without replaying an uncertain command.
	if (strings.HasPrefix(string(v.Active.Revision), "fallback-") || strings.HasPrefix(string(v.Active.Revision), "recovery-")) && (s.syncedFallback != v.Active.Revision || v.AuditGap) {
		if _, err = s.coordinator.SyncFallback(ctx); err != nil {
			s.evidence.Complete = false
			return err
		}
		s.syncedFallback = v.Active.Revision
	}
	if v.AuditGap && !strings.HasPrefix(string(v.Active.Revision), "fallback-") && !strings.HasPrefix(string(v.Active.Revision), "recovery-") {
		s.evidence.Complete = false
		return ErrRecovery
	}
	if _, err = s.view(ctx); err != nil {
		s.evidence.Complete = false
		return err
	}
	s.ready.Store(true)
	if v.Active.CandidateFraction > 0 || s.manager != nil && s.manager.HoldElectiveChanges() {
		s.evidence.Complete = false
		return nil
	}
	if changed, err := s.capacityTick(ctx, v); err != nil || changed {
		return err
	}
	sample, err := s.client.ReadWindow(ctx)
	if err != nil || sample.Revision != v.Active.Revision {
		s.evidence.Complete = false
		return s.client.BeginWindow(ctx, WindowRequest{Revision: v.Active.Revision, Duration: s.cfg.Window})
	}
	if sample.Window.End.IsZero() || !sample.Window.End.After(s.lastWindow) {
		return nil
	}
	state, err := s.view(ctx)
	if err != nil {
		return err
	}
	evidence, signal := stableEvidence(state, sample, s.cfg, time.Now())
	s.evidence = evidence
	s.signal = signal
	s.evidenceRevision = v.Active.Revision
	s.lastWindow = sample.Window.End
	if err = s.client.BeginWindow(ctx, WindowRequest{Revision: v.Active.Revision, Duration: s.cfg.Window, PreviousEnd: sample.Window.End}); err != nil {
		s.evidence.Complete = false
		return err
	}
	return nil
}
func (s *Service) Run(ctx context.Context) error {
	if !s.running.CompareAndSwap(false, true) {
		return ErrInvalid
	}
	defer s.running.Store(false)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan agent.Trigger)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case trigger := <-jobs:
				s.RunAgent(ctx, trigger)
			}
		}
	}()
	defer func() { cancel(); <-done; s.ready.Store(false) }()
	ticker := time.NewTicker(s.cfg.Tick)
	defer ticker.Stop()
	for {
		err := s.Tick(ctx)
		if s.gate != nil {
			s.mu.Lock()
			signal := s.signal
			if err != nil {
				signal = agent.Signal{At: time.Now()}
			}
			s.mu.Unlock()
			if trigger, ok := s.gate.Observe(signal); ok {
				select {
				case jobs <- trigger:
				default:
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func histogramP95(h telemetry.Histogram) (float64, bool) {
	var total uint64
	for _, n := range h {
		if n > telemetry.MaxCounter || total > telemetry.MaxCounter-n {
			return 0, false
		}
		total += n
	}
	if total == 0 {
		return 0, false
	}
	rank := uint64(math.Ceil(.95 * float64(total)))
	var sum uint64
	bounds := telemetry.LatencyBounds()
	for i, n := range h {
		sum += n
		if sum >= rank {
			if i >= len(bounds) {
				return 0, false
			}
			return float64(bounds[i]) / float64(time.Millisecond), true
		}
	}
	return 0, false
}
func stableEvidence(state agent.State, sample WindowSnapshot, c ServiceConfig, now time.Time) (agent.Evidence, agent.Signal) {
	w := sample.Window
	out := agent.Evidence{ID: fmt.Sprintf("%s/%d", sample.Revision, w.End.UnixNano()), At: w.End}
	signal := agent.Signal{At: w.End}
	r := sample.Resources
	if !w.Complete || w.End.After(now) || now.Sub(w.End) > c.MaxEvidenceAge || w.End.Sub(w.Start) < c.Window || r.Source != c.ResourceSource || r.At.IsZero() || r.At.After(now) || now.Sub(r.At) > c.MaxEvidenceAge || math.IsNaN(r.CPU) || math.IsNaN(r.Memory) || r.CPU < 0 || r.CPU > 1 || r.Memory < 0 || r.Memory > 1 || len(w.Groups) != len(state.Registry.Workloads) {
		return out, signal
	}
	if c.Evaluation != nil && (r.CPU > c.Evaluation.MaximumCPU || r.Memory > c.Evaluation.MaximumMemory) {
		return out, signal
	}
	for _, live := range sample.Live {
		if live.Uncertain > 0 {
			return out, signal
		}
	}
	known := map[string]domain.Class{}
	for _, item := range state.Registry.Workloads {
		known[item.ID] = item.DefaultClass
	}
	for _, rule := range state.Policy.Rules {
		known[rule.WorkloadID] = rule.Class
	}
	objectives := map[string]rollout.Objective{}
	if c.Evaluation != nil {
		for _, o := range c.Evaluation.Objectives {
			objectives[o.Workload] = o
		}
	}
	seen := map[string]bool{}
	for _, group := range w.Groups {
		class, ok := known[group.Workload]
		a := group.Stable
		if !ok || seen[group.Workload] || group.Candidate.Arrivals != 0 || a.Pending != 0 || a.Uncertain != 0 || a.Arrivals > telemetry.MaxCounter {
			return out, signal
		}
		seen[group.Workload] = true
		var sum uint64
		for _, n := range []uint64{a.Successes, a.Failed, a.Rejected, a.TimedOut} {
			if n > telemetry.MaxCounter || sum > telemetry.MaxCounter-n {
				return out, signal
			}
			sum += n
		}
		if sum != a.Arrivals || a.Arrivals == 0 {
			return out, signal
		}
		for _, h := range []telemetry.Histogram{a.Queue, a.Total} {
			var count uint64
			for _, n := range h {
				if n > telemetry.MaxCounter || count > telemetry.MaxCounter-n {
					return out, signal
				}
				count += n
			}
			if count != a.Successes {
				return out, signal
			}
		}
		q, qok := histogramP95(a.Queue)
		total, tok := histogramP95(a.Total)
		if !qok || !tok {
			return out, signal
		}
		if out.Samples > telemetry.MaxCounter-a.Arrivals {
			return out, signal
		}
		out.Samples += a.Arrivals
		out.Workloads = append(out.Workloads, gateway.Workload{ID: group.Workload, Class: string(class), Arrivals: a.Arrivals, Completed: a.Successes, Rejected: a.Rejected, Failed: a.Failed + a.TimedOut, QueueP95MS: q, TotalP95MS: total})
		if o, ok := objectives[group.Workload]; ok {
			signal.SLOFailure = signal.SLOFailure || q > float64(o.MaxQueue)/float64(time.Millisecond) || total > float64(o.MaxTotal)/float64(time.Millisecond)
			signal.QueueExpiry = signal.QueueExpiry || a.TimedOut > 0
			signal.Starvation = signal.Starvation || a.OldestQueued > o.MaxOldest || a.OldestActive > o.MaxOldest
		}
	}
	out.Complete = true
	signal.Complete = true
	signal.Samples = out.Samples
	return out, signal
}
