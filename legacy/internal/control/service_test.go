package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"pooltune/internal/admin"
	"strings"
	"testing"
	"time"

	"pooltune/internal/agent"
	"pooltune/internal/domain"
	"pooltune/internal/gateway"
	"pooltune/internal/measurement"
	"pooltune/internal/pgwire"
	"pooltune/internal/policy"
	"pooltune/internal/rollout"
	"pooltune/internal/store"
)

// These are controlled component measurements, not calibration or provider evidence.
func serviceFixture(t *testing.T, model agent.Gateway, automate bool) (*Service, *Engine, *measurement.Collector, ServiceConfig, *store.Store, *Coordinator) {
	t.Helper()
	e, _ := fixture(t, t.TempDir(), 1, approved)
	t.Cleanup(func() { e.Close() })
	e.cfg.CohortKey = make([]byte, 32)
	reg := policy.Registry{Fallback: policy.Fallback{QueueWait: time.Second, QueueLimit: 10, ConcurrencyCap: 20}, Workloads: []policy.Workload{{ID: "w1", Selector: policy.Selector{Target: "db", Path: domain.PathPostgres, Principal: "app"}, DefaultClass: domain.ClassNormal, AllowedClasses: []domain.Class{domain.ClassNormal}, MaxQueueWait: time.Second, MaxQueueLimit: 10, MaxConcurrency: 20}}}
	e.cfg.Registry = reg
	collector, err := measurement.New([]string{"w1"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	e.cfg.Measurements = collector
	e.cfg.Resources = func() domain.Resources {
		return domain.Resources{Source: "local-host", At: time.Now(), CPU: .1, Memory: .2}
	}
	v := envelope("p1", "", 1)
	var initial map[string]policy.Policy
	json.Unmarshal(v.PolicyJSON, &initial)
	p := initial["stable"]
	p.Workloads = []policy.WorkloadSettings{{WorkloadID: "w1", QueueWait: time.Second, QueueLimit: 10, ConcurrencyCap: 20}}
	initial["stable"] = p
	v.PolicyJSON, _ = json.Marshal(initial)
	v.Checksum = Checksum(v)
	if _, err = e.Activate(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	if err = e.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	v.Limits[0].Effective = 20
	v.Limits[0].CohortBudgets = map[string]int{"stable": 20}
	v.Limits[0].CapacityRevision = "capacity1"
	v.ExpectedPredecessor = v.Revision
	v.Revision = "capacity1"
	v.Limits[0].Revision = v.Revision
	v.Checksum = Checksum(v)
	root := t.TempDir()
	os.Chmod(root, 0700)
	db, err := store.OpenStore(filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	coordDir := filepath.Join(root, "intents")
	os.Mkdir(coordDir, 0700)
	coord, err := NewCoordinator("db", domain.PathPostgres, coordDir, db, engineClient{e})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { coord.Close() })
	if _, err = coord.Apply(t.Context(), e.View().Active); err != nil {
		t.Fatal(err)
	}
	if _, err = coord.Apply(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "service")
	os.Mkdir(dir, 0700)
	c := ServiceConfig{Target: "db", Path: domain.PathPostgres, Directory: dir, Guardrails: e.cfg.Guardrails, Registry: reg, Tick: 5 * time.Millisecond, Window: 20 * time.Millisecond, MaxEvidenceAge: time.Second, ResourceSource: "local-host"}
	if automate {
		c.Evaluation = &rollout.EvaluationProfile{ID: "fixture-only", Validated: true, EvidenceSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ApprovedAt: time.Now().Add(-time.Hour), Cooldown: time.Second, AllowNeutral: true, MinimumWindow: 20 * time.Millisecond, MaximumStage: time.Second, MaximumMetricAge: 20 * time.Millisecond, MinimumSamples: 20, MinimumTailSamples: 1, Quantile: .95, ExposureTolerance: .05, MaximumDemandChange: .2, MaximumControlRegression: .2, MaximumCPU: .9, MaximumMemory: .9, MinimumPassingWindows: 1, Objectives: []rollout.Objective{{Workload: "w1", MaxQueue: time.Second, MaxTotal: time.Second, MaxOldest: time.Second, MaxFailureRate: .1, MaxRejectRate: .1, MaxTimeoutRate: .1, MaxLatencyRegression: .2, MaxRateRegression: .1, MinSuccessFraction: .9}}}
		c.Lease = 2 * time.Second
		c.Agent = &agent.Config{MaxDuration: time.Second, Cooldown: time.Second, MaxEvidenceAge: time.Second, MinimumSamples: 1, MaxRunsPerHour: 10}
		c.Trigger = &agent.TriggerProfile{Sustain: 20 * time.Millisecond, MaximumGap: time.Second, Cooldown: time.Second, MinimumSamples: 1}
	}
	s, err := NewService(c, db, coord, measuredEngineClient{engineClient{e}}, model)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, e, collector, c, db, coord
}

type measuredEngineClient struct{ engineClient }

func (c measuredEngineClient) BeginWindow(_ context.Context, r WindowRequest) error {
	return c.e.beginWindow(r)
}
func (c measuredEngineClient) ReadWindow(context.Context) (WindowSnapshot, error) {
	return c.e.readWindow()
}
func sampleService(t *testing.T, s *Service, e *Engine, c *measurement.Collector) {
	t.Helper()
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	v := e.View().Active
	c.Record(pgwire.Observation{Kind: pgwire.Arrived, ID: 1, ArrivedAt: now, Classification: domain.Classification{Workload: "w1", Cohort: "stable", ActivationRevision: v.Revision}})
	c.Record(pgwire.Observation{Kind: pgwire.Admitted, ID: 1, ArrivedAt: now, AcquiredAt: now, Classification: domain.Classification{Workload: "w1", Cohort: "stable", ActivationRevision: v.Revision}})
	c.Record(pgwire.Observation{Kind: pgwire.Completed, ID: 1, ArrivedAt: now, AcquiredAt: now, FinishedAt: now.Add(time.Millisecond), Classification: domain.Classification{Workload: "w1", Cohort: "stable", ActivationRevision: v.Revision}})
	time.Sleep(25 * time.Millisecond)
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func TestServiceMeasuredAuthorityRejectsUnknownAndStaleEvidence(t *testing.T) {
	s, e, c, _, _, _ := serviceFixture(t, nil, false)
	sampleService(t, s, e, c)
	v, err := s.View(t.Context())
	if err != nil || !v.Evidence.Complete || v.Evidence.Samples != 1 || len(v.Evidence.Workloads) != 1 || v.Evidence.Workloads[0].Completed != 1 {
		t.Fatalf("measured state: %+v %v", v.Evidence, err)
	}
	if _, err = s.StartCanary(t.Context(), agent.Candidate{}, "key"); err == nil {
		t.Fatal("automation without calibrated profile")
	}
	// A live policy edit invalidates the old observation immediately, before a tick.
	next := e.View().Active
	next.ExpectedPredecessor = next.Revision
	next.Revision = "owner-edit"
	next.Limits[0].Revision = next.Revision
	next.Checksum = Checksum(next)
	if _, err = e.Activate(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	v, err = s.View(t.Context())
	if err != nil || v.Evidence.Complete {
		t.Fatal("old activation evidence remained eligible")
	}
}

type blockedServiceGateway struct{ started chan struct{} }

func (g *blockedServiceGateway) Start(ctx context.Context) (string, error) {
	close(g.started)
	<-ctx.Done()
	return "", ctx.Err()
}
func (g *blockedServiceGateway) Step(context.Context, string, int, gateway.ModelRequest) (gateway.ModelResponse, error) {
	panic("unreachable")
}
func TestServiceModelWaitDoesNotBlockFreezeOrTicks(t *testing.T) {
	g := &blockedServiceGateway{started: make(chan struct{})}
	s, e, c, _, _, _ := serviceFixture(t, g, true)
	sampleService(t, s, e, c)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.RunAgent(ctx, agent.Trigger{Kind: "manual"}); done <- err }()
	select {
	case <-g.started:
	case <-time.After(time.Second):
		t.Fatal("agent did not start")
	}
	frozen := make(chan error, 1)
	go func() { frozen <- s.SetFrozen(t.Context(), true) }()
	select {
	case err := <-frozen:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("model blocked safety control")
	}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	v, err := s.View(t.Context())
	if err != nil || !v.Frozen {
		t.Fatal("freeze not visible")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not cancel")
	}
}
func TestServiceStartsRealManagerAndFreezeRestoresStable(t *testing.T) {
	s, e, c, cfg, db, coord := serviceFixture(t, nil, true)
	sampleService(t, s, e, c)
	v, err := s.View(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p := v.Policy
	p.Revision = "candidate2"
	p.ExpectedPredecessor = v.Policy.Revision
	p.ClassWeights.Normal++
	candidate := agent.Candidate{ID: "c2", ExpectedRevision: v.Policy.Revision, GuardrailVersion: v.Guardrails.Version, EvidenceID: v.Evidence.ID, Policy: p}
	if _, err = s.StartCanary(t.Context(), candidate, "request2"); err != nil {
		t.Fatal(err)
	}
	active := e.View().Active
	if active.CandidateFraction != .1 || active.Limits[0].CohortBudgets["candidate"] != 2 {
		t.Fatal("real partition was not installed")
	}
	if err = s.SetFrozen(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if e.View().Active.CandidateFraction != 0 || e.View().Active.Revision == active.Revision {
		t.Fatal("freeze did not restore stable as new revision")
	}
	s.Close()
	restarted, err := NewService(cfg, db, coord, measuredEngineClient{engineClient{e}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	v, err = restarted.View(t.Context())
	if err != nil || !v.Frozen || v.Evidence.Complete {
		t.Fatal("restart lost freeze or reused old evidence")
	}
	var policies map[string]policy.Policy
	json.Unmarshal(e.View().Active.PolicyJSON, &policies)
	if policies["stable"].Revision != "p1" {
		t.Fatal("stable changed")
	}
}

func TestServiceRebuildsApprovedPolicyAfterOwnerRevision(t *testing.T) {
	s, e, _, _, _, _ := serviceFixture(t, nil, false)
	v := e.View().Active
	var policies map[string]policy.Policy
	json.Unmarshal(v.PolicyJSON, &policies)
	p := policies["stable"]
	p.ExpectedPredecessor = p.Revision
	p.Revision = "owner-policy"
	p.ClassWeights.Normal++
	v.PolicyJSON, _ = json.Marshal(map[string]policy.Policy{"stable": p})
	v.ExpectedPredecessor = v.Revision
	v.Revision = "owner-activation"
	v.Limits[0].Revision = v.Revision
	v.Checksum = Checksum(v)
	if _, err := e.Activate(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	state, err := s.View(t.Context())
	if err != nil || state.Policy.Revision != "owner-policy" || state.Guardrails.CurrentPolicy.Revision() != "owner-policy" {
		t.Fatalf("approved current policy unavailable: %v", err)
	}
}
func TestAdoptInitialObservesWithoutActivationAndRejectsMissingHistory(t *testing.T) {
	e, _, _ := readyEngine(t)
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	coord, err := NewCoordinator("db", domain.PathPostgres, dir, db, engineClient{e})
	if err != nil {
		t.Fatal(err)
	}
	defer coord.Close()
	before := e.View()
	if err = coord.AdoptInitial(t.Context()); err != nil {
		t.Fatal(err)
	}
	active, err := db.ActiveRevision(coord.LedgerTarget())
	if err != nil || active.ID != "p1" || e.View().ActiveAt != before.ActiveAt {
		t.Fatal("bootstrap observation changed traffic or lost journal")
	}
	if err = coord.AdoptInitial(t.Context()); err != nil {
		t.Fatal("repeat adoption failed")
	}
	if _, err = e.Activate(t.Context(), envelope("p2", "p1", 1)); err != nil {
		t.Fatal(err)
	}
	if err = coord.AdoptInitial(t.Context()); err == nil {
		t.Fatal("unrecorded successor accepted")
	}
}

func TestOwnerBackendFreezeUsesExpectedRevisionAndDurableHistory(t *testing.T) {
	s, e, c, cfg, db, coord := serviceFixture(t, nil, false)
	sampleService(t, s, e, c)
	before, err := s.Read(t.Context(), "state")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(before)
	var dto struct {
		Revision string `json:"revision"`
	}
	json.Unmarshal(raw, &dto)
	command := admin.Command{ExpectedRevision: dto.Revision, IdempotencyKey: "freeze-once"}
	result, err := s.Execute(t.Context(), "freeze", command)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Execute(t.Context(), "resume", admin.Command{ExpectedRevision: dto.Revision, IdempotencyKey: "stale-resume"}); err != admin.ErrConflict {
		t.Fatal("stale owner write accepted", err)
	}
	again, err := s.Execute(t.Context(), "freeze", command)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(result)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Fatal("retry changed result")
	}
	history, err := s.Read(t.Context(), "revisions")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(history)
	if !strings.Contains(string(raw), "owner-freeze") || !strings.Contains(string(raw), "confirmed") {
		t.Fatal("owner action missing from real ledger")
	}
	s.Close()
	restarted, err := NewService(cfg, db, coord, measuredEngineClient{engineClient{e}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	again, err = restarted.Execute(t.Context(), "freeze", command)
	if err != nil {
		t.Fatal("durable retry lost", err)
	}
	v, err := restarted.View(t.Context())
	if err != nil || !v.Frozen {
		t.Fatal("freeze lost")
	}
}
func TestOwnerRestoreCreatesNewPolicyAndPreservesHistory(t *testing.T) {
	s, e, c, _, _, _ := serviceFixture(t, nil, false)
	sampleService(t, s, e, c)
	initial := e.View().Active.Revision
	readRevision := func() string {
		v, err := s.Read(t.Context(), "state")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(v)
		var dto struct {
			Revision string `json:"revision"`
		}
		json.Unmarshal(raw, &dto)
		return dto.Revision
	}
	if _, err := s.Execute(t.Context(), "freeze", admin.Command{ExpectedRevision: readRevision(), IdempotencyKey: "freeze"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(t.Context(), "revert", admin.Command{ExpectedRevision: readRevision(), IdempotencyKey: "restore", Revision: string(initial)}); err != nil {
		t.Fatal(err)
	}
	v := e.View()
	var p map[string]policy.Policy
	json.Unmarshal(v.Active.PolicyJSON, &p)
	if v.Active.Revision == initial || p["stable"].Revision == "p1" || p["stable"].ExpectedPredecessor != "p1" {
		t.Fatal("restore reused history instead of a new policy revision")
	}
}

func TestInvalidOwnerRestoreDoesNotPoisonFutureCommands(t *testing.T) {
	s, _, _, _, _, _ := serviceFixture(t, nil, false)
	v, err := s.Read(t.Context(), "state")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	var state struct {
		Revision string `json:"revision"`
	}
	json.Unmarshal(raw, &state)
	if _, err = s.Execute(t.Context(), "revert", admin.Command{ExpectedRevision: state.Revision, IdempotencyKey: "bad", Revision: "unknown"}); err == nil {
		t.Fatal("unknown history restored")
	}
	if _, err = s.Execute(t.Context(), "freeze", admin.Command{ExpectedRevision: state.Revision, IdempotencyKey: "valid"}); err != nil {
		t.Fatal("invalid request mutated owner state", err)
	}
}

func TestContinuousServiceAutomaticallyTriggersAgentFromMeasuredWindows(t *testing.T) {
	model := &triggeredGateway{called: make(chan struct{}, 1)}
	s, e, c, _, _, _ := serviceFixture(t, model, true)
	// Fixture SLO uses a 100 ns limit to exercise the trigger path, not calibration.
	s.cfg.Evaluation.Objectives[0].MaxTotal = 100 * time.Nanosecond
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(time.Second)
	var id uint64
	for {
		select {
		case <-model.called:
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("continuous measured trigger did not reach runner")
		}
		now := time.Now()
		v := e.View()
		id++
		o := pgwire.Observation{ID: id, ArrivedAt: now, AcquiredAt: now, FinishedAt: now.Add(time.Microsecond), Classification: domain.Classification{Workload: "w1", Cohort: "stable", ActivationRevision: v.Active.Revision}}
		o.Kind = pgwire.Arrived
		c.Record(o)
		o.Kind = pgwire.Admitted
		c.Record(o)
		o.Kind = pgwire.Completed
		c.Record(o)
		time.Sleep(2 * time.Millisecond)
	}
}

type triggeredGateway struct{ called chan struct{} }

func (g *triggeredGateway) Start(context.Context) (string, error) { return "run", nil }
func (g *triggeredGateway) Step(_ context.Context, _ string, _ int, in gateway.ModelRequest) (gateway.ModelResponse, error) {
	if in.Context.Workloads[0].Arrivals == 0 {
		return gateway.ModelResponse{}, agent.ErrInvalid
	}
	g.called <- struct{}{}
	return gateway.ModelResponse{Done: true}, nil
}
func TestServiceReconcilesSafetyAuditGapBeforeStartingNewWork(t *testing.T) {
	s, e, _, _, _, _ := serviceFixture(t, nil, false)
	persist := e.persist
	e.persist = func(snapshot) error { return ErrStorage }
	if err := e.Constrain(8); err != ErrStorage {
		t.Fatal(err)
	}
	e.persist = persist
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal("durable fallback could not reconcile", err)
	}
	if e.View().AuditGap {
		t.Fatal("audit gap not acknowledged after durable reconciliation")
	}
}

func TestServiceRejectsDifferentAuthorityWithSameVersionAndWorkloadIDs(t *testing.T) {
	for _, which := range []string{"selector", "bounds", "class"} {
		t.Run(which, func(t *testing.T) {
			s, e, c, _, _, _ := serviceFixture(t, nil, false)
			sampleService(t, s, e, c)
			switch which {
			case "selector":
				s.cfg.Registry.Workloads[0].Selector.Principal = "other"
			case "bounds":
				s.cfg.Guardrails.MaxClassWeight++
			case "class":
				s.cfg.Registry.Workloads[0].DefaultClass = domain.ClassCritical
				s.cfg.Registry.Workloads[0].AllowedClasses = append(s.cfg.Registry.Workloads[0].AllowedClasses, domain.ClassCritical)
			}
			if v, err := s.View(t.Context()); err == nil || v.Evidence.Complete {
				t.Fatal("mismatched traffic/control authority accepted")
			}
		})
	}
}
func TestServiceStaleWindowIsDefiniteCanaryRejection(t *testing.T) {
	s, e, c, _, _, _ := serviceFixture(t, nil, true)
	sampleService(t, s, e, c)
	state, err := s.View(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p := state.Policy
	p.Revision = "p2"
	p.ExpectedPredecessor = state.Policy.Revision
	p.ClassWeights.Normal++
	cnd := agent.Candidate{ID: "c2", ExpectedRevision: state.Policy.Revision, GuardrailVersion: state.Guardrails.Version, EvidenceID: state.Evidence.ID, Policy: p}
	sampleService(t, s, e, c)
	_, err = s.StartCanary(t.Context(), cnd, "stale-window")
	if !errors.Is(err, agent.ErrCanaryRejected) {
		t.Fatalf("known pre-dispatch rejection treated as uncertain: %v", err)
	}
	if e.View().Active.CandidateFraction != 0 {
		t.Fatal("stale proposal activated")
	}
}

func TestAdminReadinessReflectsZeroAdmissionSafetyStop(t *testing.T) {
	s, e, _, _, _, _ := serviceFixture(t, nil, false)
	if err := e.Constrain(0); err != nil {
		t.Fatal(err)
	}
	dto, err := s.Read(t.Context(), "state")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(dto)
	var state struct {
		Ready bool `json:"ready"`
	}
	json.Unmarshal(raw, &state)
	if state.Ready {
		t.Fatal("dashboard reported ready with zero admission")
	}
}
