package control

import (
	"context"
	"database/sql"
	"path/filepath"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"pooltune/internal/telemetry"
	"strings"
	"testing"
	"time"
)

func TestCapacityGrowthUsesRevisionAndVerifiedEvidence(t *testing.T) {
	for _, path := range []domain.PathKind{domain.PathPostgres, domain.PathREST} {
		t.Run(string(path), func(t *testing.T) { testCapacityGrowthEvidence(t, path) })
	}
}
func testCapacityGrowthEvidence(t *testing.T, path domain.PathKind) {
	now := time.Unix(1000, 0)
	env := envelope("p1", "", 1)
	env.Limits[0].Path = path
	env.Limits[0].Effective = 20
	env.Limits[0].CohortBudgets["stable"] = 20
	env.Limits[0].CapacityRevision = "c1"
	env.Checksum = Checksum(env)
	v := StateView{Ready: true, Drained: true, Epoch: 1, Active: env, ActiveAt: now.Add(-time.Minute)}
	g := controller.Guardrails{Approved: true, Version: "g1", Floor: 1, Ceiling: 40, HTTPEnvelope: 40, Start: 1, CPU: controller.Thresholds{Grow: .6, Backoff: .8, Stop: .95}, Memory: controller.Thresholds{Grow: .6, Backoff: .8, Stop: .95}, MaxAge: 2 * time.Second, ErrorLimit: .1, Baselines: map[string]controller.Baseline{"web": {Version: "b1", Approved: true, Samples: 100, MeanSeconds: .02, From: now.Add(-time.Hour), To: now.Add(-time.Minute), Capacity: 20, MaxOccupancy: time.Second}}}
	m := controller.MetricsSnapshot{AuthoritySHA256: CapacityChecksum(g), Local: telemetry.MetricsSnapshot{Identity: telemetry.Identity{Target: "db", Path: env.Limits[0].Path, Capacity: "c1", Policy: "p1", Guardrail: "g1", Epoch: 1}, At: now, Start: now.Add(-5 * time.Second), End: now, RuntimeAt: now, Complete: true, MeanInFlight: 20, Groups: []telemetry.Group{{Workload: "web", Arrivals1: 1000, Arrivals5: 5000, Queued: 40, Successes: 100, Terminals: 100, ServiceSeconds: 2}}}, CPU: controller.Observation{Value: .1, At: now, Source: "local-host", Valid: true}, Memory: controller.Observation{Value: .1, At: now, Source: "local-host", Valid: true}, Ready: controller.Observation{Value: 32, At: now, Source: "verified-admission-envelope", Valid: true}, Physical: controller.PhysicalBudget{Valid: true, At: now, Source: "db", Owner: "db", Version: "g1", Max: 100, Reserved: 10, RESTEnvelope: 4, RESTVerified: true, PGAllocation: 40, User: 40, Provider: 40, Memory: 40}}
	out, ok := capacityProposal(now, v, m, g, false)
	if !ok || out.Limits[0].Effective != 25 || out.ExpectedPredecessor != "p1" || out.Checksum != Checksum(out) || string(out.PolicyJSON) != string(env.PolicyJSON) {
		t.Fatalf("growth: %+v %v", out, ok)
	}
	for _, name := range []string{"frozen", "missing-ready", "missing-physical", "wrong-authority", "old-epoch", "canary", "cooldown", "missing-coverage", "new-uncertainty"} {
		t.Run(name, func(t *testing.T) {
			vv, mm := v, m
			frozen := false
			switch name {
			case "frozen":
				frozen = true
			case "missing-ready":
				mm.Ready.Valid = false
			case "missing-physical":
				mm.Physical.RESTVerified = false
			case "wrong-authority":
				mm.AuthoritySHA256 = "wrong"
			case "old-epoch":
				mm.Local.Identity.Epoch = 2
			case "canary":
				vv.Active.CandidateFraction = .1
			case "cooldown":
				vv.ActiveAt = now
			case "missing-coverage":
				mm.Local.Omitted = 1
			case "new-uncertainty":
				mm.Local.Groups = append([]telemetry.Group(nil), m.Local.Groups...)
				mm.Local.Groups[0].Active = 1
				mm.Local.Groups[0].Uncertain = 1
			}
			if _, ok := capacityProposal(now, vv, mm, g, frozen); ok {
				t.Fatal("unsafe growth")
			}
		})
	}
}

// Controlled service evidence here exercises intent safety, not calibration.
type capacityMeasuredClient struct {
	measuredEngineClient
	metrics controller.MetricsSnapshot
}

func (c capacityMeasuredClient) CapacityMetrics(context.Context) (controller.MetricsSnapshot, error) {
	return c.metrics, nil
}
func configureGrowthEvidence(s *Service, e *Engine) {
	now := time.Now()
	e.state.ActiveAt = now.Add(-time.Minute)
	v := e.View()
	l := v.Active.Limits[0]
	g := controller.Guardrails{Approved: true, Version: "g1", Floor: 1, Ceiling: 20, Start: 1, CPU: controller.Thresholds{Grow: .6, Backoff: .8, Stop: .95}, Memory: controller.Thresholds{Grow: .6, Backoff: .8, Stop: .95}, MaxAge: 2 * time.Second, ErrorLimit: .1, Baselines: map[string]controller.Baseline{"w1": {Version: "component", Approved: true, Samples: 100, MeanSeconds: .02, From: now.Add(-time.Hour), To: now.Add(-time.Minute), Capacity: 1, MaxOccupancy: time.Second}}}
	s.cfg.Capacity = &CapacityProfile{Guardrails: g, EvidenceSHA256: strings.Repeat("a", 64)}
	end := now.Truncate(time.Second)
	m := controller.MetricsSnapshot{AuthoritySHA256: CapacityChecksum(g), Local: telemetry.MetricsSnapshot{Identity: telemetry.Identity{Target: l.Target, Path: l.Path, Capacity: l.CapacityRevision, Policy: string(v.Active.Revision), Guardrail: l.GuardrailVersion, Epoch: v.Epoch}, At: now, Start: end.Add(-5 * time.Second), End: end, RuntimeAt: now, Complete: true, MeanInFlight: float64(l.Effective), Groups: []telemetry.Group{{Workload: "w1", Arrivals1: 1000, Arrivals5: 5000, Queued: 40, Successes: 100, Terminals: 100, ServiceSeconds: 2}}}, CPU: controller.Observation{Value: .1, At: now, Source: "local-host", Valid: true}, Memory: controller.Observation{Value: .1, At: now, Source: "local-host", Valid: true}, Ready: controller.Observation{Value: 20, At: now, Source: "verified", Valid: true}, Physical: controller.PhysicalBudget{Valid: true, At: now, Source: "db", Owner: "db", Version: "g1", Max: 100, Reserved: 10, PGAllocation: 20, User: 20, Provider: 20, Memory: 20}}
	s.client = capacityMeasuredClient{measuredEngineClient{engineClient{e}}, m}
}
func TestContinuousGrowthRespectsSafetyCeilingWithoutPendingIntent(t *testing.T) {
	s, e, _, _, db, coord := serviceFixture(t, nil, false)
	if err := e.Constrain(3); err != nil {
		t.Fatal(err)
	}
	configureGrowthEvidence(s, e)
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal("healthy tick attempted forbidden growth", err)
	}
	pending, err := db.Pending(coord.LedgerTarget())
	if err != nil || len(pending) != 0 {
		t.Fatal("forbidden growth poisoned history", err)
	}
	if e.View().Active.Limits[0].Effective != 3 {
		t.Fatal("safety ceiling lifted")
	}
}
func TestContinuousGrowthReconcilesLostACKWithoutReplay(t *testing.T) {
	s, e, _, _, db, coord := serviceFixture(t, nil, false)
	env := e.View().Active
	env.ExpectedPredecessor = env.Revision
	env.Revision = "lower-start"
	env.Limits[0].Revision = env.Revision
	env.Limits[0].Effective = 3
	env.Limits[0].CohortBudgets = map[string]int{"stable": 3}
	env.Checksum = Checksum(env)
	if _, err := coord.Apply(t.Context(), env); err != nil {
		t.Fatal(err)
	}
	configureGrowthEvidence(s, e)
	lost := &lostACK{engine: e}
	coord.client = lost
	if err := s.Tick(t.Context()); err == nil {
		t.Fatal("lost acknowledgement not exposed")
	}
	if e.View().Active.Limits[0].Effective != 4 {
		t.Fatal("growth did not install before ACK loss")
	}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	pending, err := db.Pending(coord.LedgerTarget())
	if err != nil || len(pending) != 0 {
		t.Fatal("growth acknowledgement stayed uncertain", err)
	}
	if lost.calls != 1 {
		t.Fatal("activation replayed")
	}
	active, err := db.ActiveRevision(coord.LedgerTarget())
	if err != nil || active.ID != string(e.View().Active.Revision) {
		t.Fatal("confirmed intent missing")
	}
}

type countedActivationClient struct {
	engineClient
	calls int
}

func (c *countedActivationClient) Activate(ctx context.Context, e domain.ActivationEnvelope) (ActivationAck, error) {
	c.calls++
	return c.engineClient.Activate(ctx, e)
}
func TestContinuousGrowthReconcilesConfirmationWriteFailure(t *testing.T) {
	s, e, _, _, db, coord := serviceFixture(t, nil, false)
	env := e.View().Active
	env.ExpectedPredecessor = env.Revision
	env.Revision = "lower-start"
	env.Limits[0].Revision = env.Revision
	env.Limits[0].Effective = 3
	env.Limits[0].CohortBudgets = map[string]int{"stable": 3}
	env.Checksum = Checksum(env)
	if _, err := coord.Apply(t.Context(), env); err != nil {
		t.Fatal(err)
	}
	configureGrowthEvidence(s, e)
	client := &countedActivationClient{engineClient: engineClient{e}}
	coord.client = client
	fault, err := sql.Open("sqlite", filepath.Join(filepath.Dir(s.cfg.Directory), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fault.Close()
	if _, err = fault.Exec("CREATE TRIGGER reject_growth_confirmation BEFORE INSERT ON events WHEN NEW.kind='confirmed' BEGIN SELECT RAISE(ABORT,'injected confirmation failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(t.Context()); err == nil {
		t.Fatal("confirmation failure not exposed")
	}
	if e.View().Active.Limits[0].Effective != 4 {
		t.Fatal("growth not installed")
	}
	if _, err = fault.Exec("DROP TRIGGER reject_growth_confirmation"); err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	pending, err := db.Pending(coord.LedgerTarget())
	if err != nil || len(pending) != 0 {
		t.Fatal("confirmation failure not reconciled", err)
	}
	if client.calls != 1 {
		t.Fatal("activation replayed after confirmation failure")
	}
}

func TestQuietCapacityReductionRequiresContinuousWindow(t *testing.T) {
	for _, path := range []domain.PathKind{domain.PathPostgres, domain.PathREST} {
		t.Run(string(path), func(t *testing.T) {
			svc, e, _, _, _, _ := serviceFixture(t, nil, false)
			configureGrowthEvidence(svc, e)
			client := svc.client.(capacityMeasuredClient)
			m := client.metrics
			v := e.View()
			now := time.Now()
			v.Active.Limits[0].Path = path
			m.Local.Identity.Path = path
			m.Local.MeanInFlight = 0
			m.Local.Groups = nil
			m.Physical.RESTVerified = true
			g := svc.cfg.Capacity.Guardrails
			g.HTTPEnvelope = 20
			m.AuthoritySHA256 = CapacityChecksum(g)
			for _, duration := range []time.Duration{0, 29 * time.Second, 31 * time.Second} {
				env, changed := capacityProposal(now, v, m, g, false, now.Add(-duration))
				if changed != (duration > 30*time.Second) {
					t.Fatalf("quiet %v: changed=%v", duration, changed)
				}
				if changed && (env.Limits[0].Effective != 18 || string(env.PolicyJSON) != string(v.Active.PolicyJSON)) {
					t.Fatal("quiet reduction changed policy or skipped bounded cut")
				}
			}
		})
	}
}

func TestQuietCreditResetsOnGapsAndQueueEvents(t *testing.T) {
	s, e, _, _, _, _ := serviceFixture(t, nil, false)
	configureGrowthEvidence(s, e)
	m := s.client.(capacityMeasuredClient).metrics
	v := e.View()
	m.Local.Groups = nil
	var q quietTracker
	now := time.Now()
	observe := func(at time.Time) time.Time {
		m.Local.At = at
		m.Local.RuntimeAt = at
		m.Local.End = at.Truncate(time.Second)
		m.Local.Start = m.Local.End.Add(-5 * time.Second)
		return q.observe(at, v, m)
	}
	first := observe(now)
	if first.IsZero() {
		t.Fatal("quiet interval not started")
	}
	if observe(now.Add(time.Second)) != first {
		t.Fatal("continuous quiet reset")
	}
	gap := now.Add(4 * time.Second)
	if observe(gap) != gap {
		t.Fatal("gap retained quiet credit")
	}
	m.LastQueuedAt = gap.Add(500 * time.Millisecond)
	if observe(gap.Add(time.Second)) != m.LastQueuedAt {
		t.Fatal("missed queue excursion retained quiet credit")
	}
	m.Local.Omitted = 1
	if !observe(gap.Add(2 * time.Second)).IsZero() {
		t.Fatal("incomplete coverage retained quiet credit")
	}
}
