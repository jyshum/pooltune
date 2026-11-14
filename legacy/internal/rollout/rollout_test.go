package rollout

import (
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/telemetry"
	"strings"
	"testing"
	"time"
)

func fixture() (Experiment, MetricsSnapshot, EvaluationProfile) {
	now := time.Now()
	obj := Objective{Workload: "interactive", MaxQueue: time.Second, MaxTotal: 2 * time.Second, MaxOldest: 3 * time.Second, MaxFailureRate: .05, MaxRejectRate: .05, MaxTimeoutRate: .05, MaxLatencyRegression: .1, MaxRateRegression: .01, MinSuccessFraction: .8, MinImprovement: .1, Benefit: true}
	p := EvaluationProfile{Cooldown: time.Minute, ID: "fixture-v1", EvidenceSHA256: strings.Repeat("a", 64), ApprovedAt: now.Add(-time.Hour), Validated: true, MinimumWindow: 10 * time.Second, MaximumStage: time.Minute, MaximumMetricAge: 2 * time.Second, MinimumSamples: 100, MinimumTailSamples: 5, Quantile: .95, ExposureTolerance: .03, MaximumDemandChange: .2, MaximumControlRegression: .1, MaximumCPU: .9, MaximumMemory: .9, Objectives: []Objective{obj}, MinimumPassingWindows: 2}
	base := Cohort{Arrivals: 1000, Successes: 1000}
	base.Queue[5] = 1000
	base.Total[6] = 1000
	stable := base
	stable.Arrivals = 9000
	stable.Successes = 9000
	stable.Queue[5] = 9000
	stable.Total[6] = 9000
	improved := base
	improved.Queue = telemetry.Histogram{}
	improved.Total = telemetry.Histogram{}
	improved.Queue[4] = 1000
	improved.Total[5] = 1000
	baseline := Window{Start: now.Add(-40 * time.Second), End: now.Add(-30 * time.Second), Complete: true, Groups: []Group{{Workload: "interactive", Stable: stable, Candidate: base}}}
	e := Experiment{ID: "e1", Target: "db", Path: domain.PathREST, StableRevision: "p1", CandidateRevision: "p2", GuardrailVersion: "g1", CapacityRevision: "c1", ProfileID: p.ID, Epoch: 1, CreatedAt: now.Add(-time.Minute), Phase: Measuring, Stage: 0, Fractions: []float64{.1, .25, .5, 1}, StageStarted: now.Add(-20 * time.Second), Deadline: now.Add(40 * time.Second), LeaseExpires: now.Add(30 * time.Second), Baseline: baseline, PassingWindows: 1}
	m := MetricsSnapshot{ExperimentID: e.ID, Target: e.Target, Path: e.Path, StableRevision: e.StableRevision, CandidateRevision: e.CandidateRevision, GuardrailVersion: e.GuardrailVersion, CapacityRevision: e.CapacityRevision, Epoch: 1, At: now, ResourceAt: now, Now: now, CPU: .5, Memory: .5, Window: Window{Start: now.Add(-10 * time.Second), End: now, Complete: true, Groups: []Group{{Workload: "interactive", Stable: stable, Candidate: improved}}}}
	return e, m, p
}
func TestBeneficialComparableWindowAdvances(t *testing.T) {
	e, m, p := fixture()
	d := Evaluate(e, m, p)
	if d.Action != domain.DecisionAdvance {
		t.Fatalf("%+v", d)
	}
}
func TestBadEvidenceCannotPromote(t *testing.T) {
	for _, kind := range []string{"skew", "missing", "delayed", "mixed", "samples", "unvalidated", "futureprofile"} {
		t.Run(kind, func(t *testing.T) {
			e, m, p := fixture()
			switch kind {
			case "skew":
				m.Groups[0].Candidate.Arrivals = 9000
				m.Groups[0].Candidate.Successes = 9000
				m.Groups[0].Candidate.Queue[4] = 9000
				m.Groups[0].Candidate.Total[5] = 9000
			case "missing":
				m.Complete = false
			case "delayed":
				m.At = m.Now.Add(-time.Minute)
			case "mixed":
				m.CandidateRevision = "different"
			case "samples":
				p.MinimumSamples = 2000
			case "unvalidated":
				p.Validated = false
			case "futureprofile":
				p.ApprovedAt = e.CreatedAt.Add(time.Second)
			}
			if d := Evaluate(e, m, p); d.Action == domain.DecisionAdvance {
				t.Fatalf("promoted %s", kind)
			}
		})
	}
}
func TestRejectingTrafficCannotFakeLatencyBenefit(t *testing.T) {
	e, m, p := fixture()
	g := &m.Groups[0].Candidate
	g.Successes = 500
	g.Rejected = 500
	g.Queue[4] = 500
	g.Total[5] = 500
	if d := Evaluate(e, m, p); d.Action != domain.DecisionRevert {
		t.Fatalf("%+v", d)
	}
}
func TestStarvationAndSafetyBypassSamples(t *testing.T) {
	for _, which := range []string{"oldest", "pressure", "freeze", "lease"} {
		t.Run(which, func(t *testing.T) {
			e, m, p := fixture()
			m.Complete = false
			switch which {
			case "oldest":
				m.Groups[0].Candidate.OldestQueued = 4 * time.Second
			case "pressure":
				m.CPU = .95
			case "freeze":
				m.Frozen = true
			case "lease":
				e.LeaseExpires = m.Now
			}
			if d := Evaluate(e, m, p); d.Action != domain.DecisionRevert {
				t.Fatalf("%+v", d)
			}
		})
	}
}
func TestTimeoutRestoresStableAndFullStageDisclosesNoControl(t *testing.T) {
	e, m, p := fixture()
	e.Deadline = m.Now
	m.Complete = false
	if d := Evaluate(e, m, p); d.Action != domain.DecisionRevert || d.Verdict != "inconclusive" {
		t.Fatalf("%+v", d)
	}
	e, m, p = fixture()
	e.Stage = 3
	e.Phase = Observing
	m.Groups[0].Stable = Cohort{}
	g := &m.Groups[0].Candidate
	g.Arrivals = 10000
	g.Successes = 10000
	g.Queue[4] = 10000
	g.Total[5] = 10000
	if d := Evaluate(e, m, p); d.Action != domain.DecisionAdvance || d.ConcurrentControl {
		t.Fatalf("%+v", d)
	}
}
func TestRoundedCanaryMustExpressChange(t *testing.T) {
	a := policy.Policy{ReserveFraction: .1, ClassWeights: policy.ClassWeights{Critical: 6, Normal: 3, Background: 1}}
	b := a
	b.ReserveFraction = .2
	if _, _, e := Budgets(20, .1, a, b); e == nil {
		t.Fatal("both reserve fractions round to zero")
	}
	if c, s, e := Budgets(100, .1, a, b); e != nil || c != 10 || s != 90 {
		t.Fatal("representative reserve change rejected")
	}
	if _, _, e := Budgets(10, .1, a, b); e == nil {
		t.Fatal("one-slot canary accepted")
	}
	b = a
	b.ClassWeights = policy.ClassWeights{Critical: 12, Normal: 6, Background: 2}
	if _, _, e := Budgets(100, .1, a, b); e == nil {
		t.Fatal("equivalent weights accepted")
	}
}

func TestOldWorkOutsideMeasurementWindowStillTriggersSafety(t *testing.T) {
	e, m, p := fixture()
	m.Live = []LiveWorkload{{Workload: "interactive", OldestActive: 10 * time.Second}}
	if d := Evaluate(e, m, p); d.Action != domain.DecisionRevert {
		t.Fatal("old transaction hidden outside sampled arrivals")
	}
}
