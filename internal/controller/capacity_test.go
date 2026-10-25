package controller

import (
	"math"
	"pooltune/internal/domain"
	"pooltune/internal/telemetry"
	"testing"
	"time"
)

func fixture() (time.Time, State, MetricsSnapshot, Guardrails) {
	now := time.Unix(1000, 0)
	id := telemetry.Identity{Target: "db", Path: domain.PathPostgres, Capacity: "c1", Policy: "p1", Guardrail: "g1", Epoch: 1}
	s := State{Identity: id, Commanded: 20, Effective: 20, PhysicalDesired: 25, LastSafe: 32, LastReady: 32, StartupVerified: true, StoreHealthy: true, EvaluationClear: true, LastChange: now.Add(-time.Minute), LastBackoff: now.Add(-time.Minute), LastQuietCut: now.Add(-time.Minute)}
	g := Guardrails{Approved: true, Version: "g1", Floor: 2, Ceiling: 40, Start: 4, HTTPEnvelope: 30, CPU: Thresholds{.6, .8, .95}, Memory: Thresholds{.6, .8, .95}, MaxAge: 10 * time.Second, ErrorLimit: .1, WarmRatio: .25, Baselines: map[string]Baseline{"web": {Version: "b1", Approved: true, Samples: 100, MeanSeconds: .02, Variance: 0, From: now.Add(-time.Hour), To: now.Add(-time.Minute), Capacity: 20, MaxOccupancy: time.Second}}}
	local := telemetry.MetricsSnapshot{Identity: id, At: now, Start: now.Add(-5 * time.Second), End: now, Complete: true, MeanInFlight: 20, RuntimeAt: now, Groups: []telemetry.Group{{Workload: "web", Arrivals1: 1000, Arrivals5: 5000, Queued: 40, Successes: 100, ServiceSeconds: 2, Terminals: 100}}}
	m := MetricsSnapshot{Local: local, CPU: Observation{.3, now, "cpu", true}, Memory: Observation{.3, now, "mem", true}, Ready: Observation{32, now, "verified", true}, Physical: PhysicalBudget{Valid: true, At: now, Source: "db", Owner: "db", Version: "g1", Max: 100, Reserved: 10, Other: 30, PGAllocation: 40, User: 40, Provider: 50, Memory: 32, ObservedPG: 20, RESTVerified: true}}
	return now, s, m, g
}

// Literal expectations reproduce the spec and catch incorrect demand, mix,
// utilization, readiness and rounding rather than recomputing the algorithm.
func TestWorkedExamples(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		edit                      func(*State, *MetricsSnapshot, *Guardrails)
		plan, effective, physical int
		demand, congestion        float64
	}{
		{"burst", func(*State, *MetricsSnapshot, *Guardrails) {}, 25, 25, 32, 26, 0},
		{"not_ready", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.Ready.Value = 20 }, 25, 20, 32, 26, 0},
		{"ordinary", func(s *State, m *MetricsSnapshot, g *Guardrails) {
			m.Local.Groups[0].Arrivals1 = 800
			m.Local.Groups[0].Arrivals5 = 4000
			m.Local.Groups[0].Queued = 0
		}, 20, 20, 25, 20, 0},
		{"slowdown", func(s *State, m *MetricsSnapshot, g *Guardrails) {
			b := g.Baselines["web"]
			b.MeanSeconds = .01
			g.Baselines["web"] = b
		}, 16, 16, 25, 13, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now, s, m, g := fixture()
			tc.edit(&s, &m, &g)
			d := NextCapacity(now, s, m, g)
			if d.Err != "" || d.Planned != tc.plan || d.Effective != tc.effective || d.PhysicalDesired == nil || *d.PhysicalDesired != tc.physical || d.Demand == nil || *d.Demand != tc.demand || d.Congestion == nil || *d.Congestion != tc.congestion {
				t.Fatalf("decision: %+v", d)
			}
		})
	}
}

func TestPrecedenceAndMissingEvidence(t *testing.T) {
	for _, tc := range []struct {
		name            string
		edit            func(*State, *MetricsSnapshot, *Guardrails)
		plan, effective int
	}{
		{"freeze", func(s *State, m *MetricsSnapshot, g *Guardrails) { s.Frozen = true; m.CPU.Value = .85 }, 20, 20},
		{"store_down", func(s *State, m *MetricsSnapshot, g *Guardrails) { s.StoreHealthy = false }, 20, 20},
		{"provider_uncertain", func(s *State, m *MetricsSnapshot, g *Guardrails) { s.ProviderUncertain = true }, 20, 20},
		{"pending_no_compound", func(s *State, m *MetricsSnapshot, g *Guardrails) { s.Pending = &Target{Admission: 25, Physical: 32} }, 25, 25},
		{"canary_hold", func(s *State, m *MetricsSnapshot, g *Guardrails) { s.Canary = true }, 20, 20},
		{"canary_backoff", func(s *State, m *MetricsSnapshot, g *Guardrails) { s.Canary = true; m.CPU.Value = .85 }, 16, 16},
		{"stale_cpu", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.CPU.At = m.CPU.At.Add(-time.Minute) }, 20, 20},
		{"missing_cpu", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.CPU.Valid = false }, 20, 20},
		{"missing_baseline", func(s *State, m *MetricsSnapshot, g *Guardrails) { delete(g.Baselines, "web") }, 20, 20},
		{"partial_window", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.Local.Complete = false }, 20, 20},
		{"lost_coverage", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.Local.Omitted = 1 }, 20, 20},
		{"epoch_reset", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.Local.Identity.Epoch = 2 }, 20, 20},
		{"policy_changed", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.Local.Identity.Policy = "p2" }, 20, 20},
		{"unverified_startup", func(s *State, m *MetricsSnapshot, g *Guardrails) { s.StartupVerified = false; m.Ready.Valid = false }, 20, 0},
		{"stale_ready", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.Ready.At = m.Ready.At.Add(-time.Minute) }, 20, 20},
		{"tighter_frozen", func(s *State, m *MetricsSnapshot, g *Guardrails) { s.Frozen = true; m.Ready.Value = 7 }, 20, 7},
		{"resource_stop_frozen", func(s *State, m *MetricsSnapshot, g *Guardrails) { s.Frozen = true; m.CPU.Value = .99 }, 20, 0},
		{"stuck", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.Local.Groups[0].OldestActive = time.Second }, 16, 16},
		{"overload_errors", func(s *State, m *MetricsSnapshot, g *Guardrails) {
			m.Local.Groups[0].OverloadFailures = 10
			m.Local.Groups[0].Successes = 90
			m.Local.Groups[0].ServiceSeconds = 1.8
		}, 16, 16},
		{"evaluation_hold", func(s *State, m *MetricsSnapshot, g *Guardrails) { s.EvaluationClear = false }, 20, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now, s, m, g := fixture()
			tc.edit(&s, &m, &g)
			d := NextCapacity(now, s, m, g)
			if d.Planned != tc.plan || d.Effective != tc.effective {
				t.Fatalf("got %+v", d)
			}
			if s.Canary && tc.plan < 20 && !d.AbortCanary {
				t.Fatal("safety cut did not abort canary")
			}
		})
	}
}

func TestQuietAndRecovery(t *testing.T) {
	now, s, m, g := fixture()
	m.Local.MeanInFlight = 2
	m.Local.Groups[0].Queued = 0
	m.Local.Groups[0].Arrivals1 = 10
	m.Local.Groups[0].Arrivals5 = 50
	s.QuietSince = now.Add(-31 * time.Second)
	d := NextCapacity(now, s, m, g)
	if d.Planned != 18 {
		t.Fatalf("quiet: %+v", d)
	}
	s.SafetyStopped = true
	s.Commanded = 0
	s.Resume = true
	d = NextCapacity(now, s, m, g)
	if d.Effective != 0 || !d.SafetyStopped || d.HealthySince.IsZero() {
		t.Fatalf("premature recovery %+v", d)
	}
	s.HealthySince = now.Add(-5 * time.Second)
	s.LastTick = now.Add(-time.Second)
	d = NextCapacity(now, s, m, g)
	if d.SafetyStopped || d.Effective != 4 || d.Planned != 4 {
		t.Fatalf("resume %+v", d)
	}
	m.CPU.Valid = false
	d = NextCapacity(now, s, m, g)
	if !d.SafetyStopped || d.Effective != 0 || !d.HealthySince.IsZero() {
		t.Fatal("stale cleared stop")
	}
}

func TestInvalidInputsCannotGrow(t *testing.T) {
	for _, v := range []float64{-1, math.NaN(), math.Inf(1)} {
		now, s, m, g := fixture()
		m.CPU.Value = v
		d := NextCapacity(now, s, m, g)
		if d.Planned > 20 || d.PhysicalDesired != nil {
			t.Fatalf("invalid resource grew: %+v", d)
		}
	}
	now, s, m, g := fixture()
	g.CPU.Grow = math.NaN()
	d := NextCapacity(now, s, m, g)
	if d.Err == "" || d.Effective != 0 {
		t.Fatal("invalid configuration authorized capacity")
	}
	now, s, m, g = fixture()
	m.Local.Groups[0].ServiceSeconds = math.Inf(1)
	d = NextCapacity(now, s, m, g)
	if d.Planned > 20 {
		t.Fatal("invalid latency grew")
	}
}

func TestFixedBudgetsAndREST(t *testing.T) {
	now, s, m, g := fixture()
	m.Physical.RESTEnvelope = 15
	m.Physical.PGAllocation = 30
	d := NextCapacity(now, s, m, g)
	if d.Safe != 30 {
		t.Fatalf("fixed allocation: %+v", d)
	}
	m.Physical.Max = 50
	d = NextCapacity(now, s, m, g)
	if d.Safe != 0 || d.Effective != 0 {
		t.Fatalf("REST reserve not accounted: %+v", d)
	}
	now, s, m, g = fixture()
	s.Identity.Path = domain.PathREST
	m.Local.Identity = s.Identity
	m.Ready.Value = 30
	d = NextCapacity(now, s, m, g)
	if d.PhysicalDesired != nil || d.WarmBudget != nil || d.Planned != 25 {
		t.Fatalf("REST physical mutation: %+v", d)
	}
	m.CPU.Value = .99
	d = NextCapacity(now, s, m, g)
	if d.Effective != 0 {
		t.Fatal("shared pressure failed to stop REST")
	}
}

func TestPendingIntentAndSafetyHolds(t *testing.T) {
	now, s, m, g := fixture()
	s.Pending = &Target{Admission: 25, Physical: 32}
	d := NextCapacity(now, s, m, g)
	if d.Changed {
		t.Fatal("same pending target would create duplicate revision")
	}
	s.StoreHealthy = false
	d = NextCapacity(now, s, m, g)
	if d.Effective > 20 {
		t.Fatal("storage outage activated pending increase")
	}
	s.StoreHealthy = true
	s.ProviderUncertain = true
	d = NextCapacity(now, s, m, g)
	if d.Effective > 20 {
		t.Fatal("uncertain provider activated increase")
	}
	s.Frozen = true
	m.CPU.Value = .99
	d = NextCapacity(now, s, m, g)
	if !d.CancelPending || d.Planned != 20 {
		t.Fatal("frozen hard stop retained elective pending activation")
	}
}

func TestRecoveryRequiresContinuousEvidence(t *testing.T) {
	now, s, m, g := fixture()
	s.SafetyStopped = true
	s.Resume = true
	s.HealthySince = now.Add(-time.Hour)
	d := NextCapacity(now, s, m, g)
	if !d.SafetyStopped {
		t.Fatal("unobserved hour counted as healthy recovery")
	}
}

func TestNoWarmExpansionDuringPressure(t *testing.T) {
	now, s, m, g := fixture()
	s.PhysicalDesired = 20
	m.CPU.Value = .85
	s.LastBackoff = now
	d := NextCapacity(now, s, m, g)
	if d.PhysicalDesired != nil && *d.PhysicalDesired > 20 {
		t.Fatal("warm expansion during overload cooldown")
	}
}

func TestMalformedAndArithmeticEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*State, *MetricsSnapshot, *Guardrails)
	}{
		{"future_cpu", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.CPU.At = m.CPU.At.Add(time.Minute) }},
		{"future_budget", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.Physical.At = m.Physical.At.Add(time.Minute) }},
		{"budget_wrong_owner", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.Physical.Owner = "other" }},
		{"negative_budget", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.Physical.Other = -1 }},
		{"huge_budget", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.Physical.Max = math.MaxInt }},
		{"changed_setting", func(s *State, m *MetricsSnapshot, g *Guardrails) { s.LastChange = m.Local.End.Add(-time.Second) }},
		{"future_window", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.Local.End = m.Local.End.Add(time.Second) }},
		{"oversized_count", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.Local.Groups[0].Arrivals5 = math.MaxUint64 }},
		{"overflow_demand", func(s *State, m *MetricsSnapshot, g *Guardrails) {
			b := g.Baselines["web"]
			b.MeanSeconds = math.MaxFloat64
			g.Baselines["web"] = b
		}},
		{"missing_provenance", func(s *State, m *MetricsSnapshot, g *Guardrails) { m.Ready.Source = "" }},
		{"bad_baseline_version", func(s *State, m *MetricsSnapshot, g *Guardrails) {
			b := g.Baselines["web"]
			b.Version = ""
			g.Baselines["web"] = b
		}},
		{"duplicate_workload", func(s *State, m *MetricsSnapshot, g *Guardrails) {
			m.Local.Groups = append(m.Local.Groups, m.Local.Groups[0])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now, s, m, g := fixture()
			tc.edit(&s, &m, &g)
			d := NextCapacity(now, s, m, g)
			if d.Planned > 20 || d.Effective > 20 {
				t.Fatalf("invalid evidence grew %+v", d)
			}
		})
	}
}

func TestMatchedMixAndBoundedGrowth(t *testing.T) {
	now, s, m, g := fixture()
	b := g.Baselines["web"]
	b.MeanSeconds = .1
	g.Baselines["slow"] = b
	m.Local.Groups[0].Successes = 50
	m.Local.Groups[0].ServiceSeconds = 1
	m.Local.Groups = append(m.Local.Groups, telemetry.Group{Workload: "slow", Successes: 50, Terminals: 50, ServiceSeconds: 5})
	d := NextCapacity(now, s, m, g)
	if d.BaseLatency == nil || math.Abs(*d.BaseLatency-.06) > 1e-12 || *d.Congestion != 0 || d.Planned != 25 {
		t.Fatalf("mix bias %+v", d)
	}
	s.Commanded = 3
	s.Effective = 3
	s.LastSafe = 3
	m.Physical.Memory = 3
	m.Ready.Value = 3
	m.Local.MeanInFlight = 3
	d = NextCapacity(now, s, m, g)
	if d.Planned != 3 || d.Effective != 3 || d.Safe != 3 {
		t.Fatal("rounding exceeded ceiling")
	}
}

func TestFreezeAbortsCanaryAndReportsDistinctCapacity(t *testing.T) {
	now, s, m, g := fixture()
	s.Canary = true
	s.Frozen = true
	m.Idle = Observation{7, now, "pool_readback", true}
	d := NextCapacity(now, s, m, g)
	if !d.AbortCanary {
		t.Fatal("freeze left experimental policy running")
	}
	if d.PhysicalSafe == nil || *d.PhysicalSafe != 32 || d.ObservedIdle == nil || *d.ObservedIdle != 7 {
		t.Fatal("physical/idle observations lost")
	}
	m.Idle.Value = 100
	d = NextCapacity(now, s, m, g)
	if d.ObservedIdle != nil {
		t.Fatal("idle exceeded observed total")
	}
}

func TestInconsistentTerminalCoverageDisablesGrowth(t *testing.T) {
	now, s, m, g := fixture()
	m.Local.Groups[0].OverloadFailures = 1 // already 100 successes / 100 total
	d := NextCapacity(now, s, m, g)
	if d.Planned > 20 || d.Demand != nil {
		t.Fatal("overlapping outcomes accepted as complete evidence")
	}
}
