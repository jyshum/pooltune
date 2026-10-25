// Package controller computes capacity proposals. It performs no I/O or
// activation. Callers persist elective intent and apply safety bounds locally.
package controller

import (
	"math"
	"pooltune/internal/domain"
	"pooltune/internal/telemetry"
	"time"
)

// Proposal constants are testable draft settings, not calibrated defaults.
const (
	Utilization     = .8
	DrainSeconds    = 2.
	GrowthFraction  = .25
	BackoffFraction = .8
	QuietFraction   = .9
)
const (
	EvidenceWindow = 5 * time.Second
	Cooldown       = 5 * time.Second
	QuietWindow    = 30 * time.Second
	MinSamples     = 20
)

type Baseline struct {
	Version               string
	Approved              bool
	Samples               uint64
	MeanSeconds, Variance float64
	From, To              time.Time
	Capacity              int
	MaxOccupancy          time.Duration
}

func (b Baseline) valid(now time.Time) bool {
	return b.Approved && b.Version != "" && b.Samples >= 100 && finite(b.MeanSeconds) && b.MeanSeconds > 0 && finite(b.Variance) && b.Variance >= 0 && !b.From.IsZero() && b.To.After(b.From) && !b.To.After(now) && b.Capacity > 0 && b.MaxOccupancy > 0
}

type Guardrails struct {
	Approved                            bool
	Version                             string
	Floor, Ceiling, Start, HTTPEnvelope int
	CPU, Memory                         Thresholds
	MaxAge                              time.Duration
	ErrorLimit, WarmRatio               float64
	Baselines                           map[string]Baseline
}

func (g Guardrails) valid() bool {
	return g.Approved && g.Version != "" && g.Floor > 0 && g.Floor <= g.Ceiling && g.Ceiling <= 1000000 && g.Start > 0 && g.Start <= g.Ceiling && g.HTTPEnvelope >= 0 && g.HTTPEnvelope <= 1000000 && g.CPU.valid() && g.Memory.valid() && g.MaxAge > 0 && finite(g.ErrorLimit) && g.ErrorLimit > 0 && g.ErrorLimit <= 1 && finite(g.WarmRatio) && g.WarmRatio >= 0 && g.WarmRatio <= 1 && len(g.Baselines) <= telemetry.MaxWorkloads
}

type Target struct{ Admission, Physical int }
type State struct {
	Identity                                                                                                                  telemetry.Identity
	Commanded, Effective, PhysicalDesired, LastSafe, LastReady                                                                int
	Pending                                                                                                                   *Target
	StartupVerified, SafetyStopped, Resume, Frozen, StoreHealthy, ProviderUncertain, ProviderPending, Canary, EvaluationClear bool
	LastChange, LastBackoff, LastQuietCut, QuietSince, HealthySince, LastTick                                                 time.Time
}
type PoolState struct {
	Version string
	Desired int
	Pending bool
}

type MetricsSnapshot struct {
	Pool                     *PoolState
	LastQueuedAt             time.Time
	AuthoritySHA256          string
	Local                    telemetry.MetricsSnapshot
	CPU, Memory, Ready, Idle Observation
	Physical                 PhysicalBudget
}
type CapacityDecision struct {
	Identity                                           telemetry.Identity
	At                                                 time.Time
	Planned, Effective, Safe                           int
	PhysicalDesired, WarmBudget                        *int
	PhysicalSafe, ObservedIdle                         *int
	Demand, BaseLatency, CurrentLatency, Congestion    *float64
	SafetyStopped, AbortCanary, CancelPending, Changed bool
	HealthySince                                       time.Time
	Reason, Err                                        string
	Quality                                            []string
}

func number(x float64) *float64 { return &x }
func integer(x int) *int        { return &x }
func elapsed(now, at time.Time, d time.Duration) bool {
	return !at.After(now) && (at.IsZero() || now.Sub(at) >= d)
}

func NextCapacity(now time.Time, s State, m MetricsSnapshot, g Guardrails) CapacityDecision {
	d := CapacityDecision{Identity: s.Identity, At: now, Planned: s.Commanded, Reason: "hold", HealthySince: s.HealthySince}
	if !g.valid() || !s.Identity.Valid() || s.Identity.Guardrail != g.Version || now.IsZero() {
		d.Err = "invalid configuration or identity"
		d.Reason = "invalid"
		return d
	}
	for _, v := range []int{s.Commanded, s.Effective, s.PhysicalDesired, s.LastSafe, s.LastReady} {
		if v < 0 || v > 1000000000 {
			d.Err = "invalid controller state"
			d.Reason = "invalid"
			return d
		}
	}
	if s.Pending != nil {
		if s.Pending.Admission < 0 || s.Pending.Admission > g.Ceiling || s.Pending.Physical < 0 || s.Pending.Physical > 1000000000 {
			d.Err = "invalid pending target"
			return d
		}
		d.Planned = s.Pending.Admission
	}
	for _, at := range []time.Time{s.LastChange, s.LastBackoff, s.LastQuietCut, s.QuietSince, s.HealthySince, s.LastTick} {
		if at.After(now) {
			d.Err = "future state timestamp"
			return d
		}
	}
	cpu := m.CPU.fresh(now, g.MaxAge) && m.CPU.Value <= 1
	mem := m.Memory.fresh(now, g.MaxAge) && m.Memory.Value <= 1
	budget := m.Physical.fresh(now, g.MaxAge) && m.Physical.Owner == s.Identity.Target && m.Physical.Version == g.Version
	ready := m.Ready.fresh(now, g.MaxAge) && m.Ready.Value == math.Trunc(m.Ready.Value) && m.Ready.Value <= 1000000000
	physicalSafe := s.LastSafe
	if budget {
		physicalSafe = m.Physical.safe()
	}
	d.Safe = min(g.Ceiling, physicalSafe)
	if s.Identity.Path == domain.PathREST {
		d.Safe = min(g.Ceiling, g.HTTPEnvelope)
	} else {
		d.PhysicalSafe = integer(physicalSafe)
		if budget && m.Idle.fresh(now, g.MaxAge) && m.Idle.Value == math.Trunc(m.Idle.Value) && m.Idle.Value <= float64(m.Physical.ObservedPG) {
			d.ObservedIdle = integer(int(m.Idle.Value))
		}
	}
	fresh := cpu && mem && budget && ready
	if !fresh {
		d.Quality = append(d.Quality, "missing_stale_or_invalid_external_evidence")
	}
	growResources := fresh && m.CPU.Value < g.CPU.Grow && m.Memory.Value < g.Memory.Grow
	hard := (cpu && m.CPU.Value >= g.CPU.Stop) || (mem && m.Memory.Value >= g.Memory.Stop) || (budget && m.Physical.breached())
	local := localValid(now, s, m.Local)
	if !local {
		d.Quality = append(d.Quality, "invalid_local_window_identity_or_coverage")
	}
	demandOK, latencyOK := local, local
	var work, backlog, baseSum, seconds, n, terminals, failures, queued float64
	overdue := false
	if local {
		for _, w := range m.Local.Groups {
			b, ok := g.Baselines[w.Workload]
			ok = ok && b.valid(now)
			arrivals := math.Max(float64(w.Arrivals1), float64(w.Arrivals5)/5)
			if arrivals > 0 || w.Queued > 0 {
				if !ok {
					demandOK = false
				} else {
					work += arrivals * b.MeanSeconds
					backlog += float64(w.Queued) * b.MeanSeconds
				}
			}
			if w.Successes > 0 {
				if !ok {
					latencyOK = false
				} else {
					baseSum += float64(w.Successes) * b.MeanSeconds
				}
				n += float64(w.Successes)
				seconds += w.ServiceSeconds
			}
			if ok && w.OldestActive >= b.MaxOccupancy {
				overdue = true
			}
			terminals += float64(w.Terminals)
			failures += float64(w.OverloadFailures)
			queued += float64(w.Queued)
		}
	}
	if demandOK {
		x := math.Ceil((work + backlog/DrainSeconds) / Utilization)
		if finite(x) && x >= 0 && x <= 1e9 {
			d.Demand = number(x)
		} else {
			demandOK = false
		}
	}
	ratio := 0.
	if s.Commanded > 0 {
		ratio = m.Local.MeanInFlight / float64(s.Commanded)
	}
	if latencyOK && n >= MinSamples && seconds > 0 && finite(seconds) && finite(baseSum) && s.Commanded > 0 && ratio >= Utilization {
		r := seconds / n
		b := baseSum / n
		d.CurrentLatency = number(r)
		d.BaseLatency = number(b)
		d.Congestion = number(math.Ceil(float64(s.Commanded) * math.Max(0, 1-b/r)))
	}
	soft := overdue || (local && terminals >= MinSamples && failures/terminals >= g.ErrorLimit) || (cpu && m.CPU.Value >= g.CPU.Backoff) || (mem && m.Memory.Value >= g.Memory.Backoff)
	if d.Congestion != nil && *d.Congestion > math.Max(2, .2*float64(s.Commanded)) {
		soft = true
	}
	stopped := s.SafetyStopped || hard || s.Commanded == 0
	if s.Frozen {
		d.Planned = s.Commanded
		d.CancelPending = s.Pending != nil
		d.AbortCanary = s.Canary
	}
	if stopped {
		d.SafetyStopped = true
		d.Reason = "safety_stop"
		d.AbortCanary = s.Canary
		if hard || !growResources || !local || overdue || soft {
			d.HealthySince = time.Time{}
		} else if d.HealthySince.IsZero() || s.LastTick.IsZero() || now.Sub(s.LastTick) > 2*time.Second {
			d.HealthySince = now
		}
		if !hard && s.Resume && !s.Frozen && s.StoreHealthy && !s.ProviderUncertain && !s.ProviderPending && s.Pending == nil && growResources && local && !soft && !d.HealthySince.IsZero() && now.Sub(d.HealthySince) >= EvidenceWindow {
			d.Planned = min(g.Start, d.Safe, int(m.Ready.Value))
			if d.Planned > 0 {
				d.SafetyStopped = false
				d.Reason = "operator_resume"
				d.HealthySince = time.Time{}
			}
		}
	} else {
		switch {
		case s.Frozen:
			d.Planned = s.Commanded
			d.CancelPending = s.Pending != nil
			d.Reason = "frozen"
		case !s.StoreHealthy || s.ProviderUncertain:
			d.Reason = "reconcile"
		case soft && elapsed(now, s.LastBackoff, Cooldown):
			d.Planned = min(d.Safe, max(1, int(math.Floor(BackoffFraction*float64(s.Commanded)))))
			d.AbortCanary = s.Canary
			d.Reason = "backoff"
		case s.Canary || s.Pending != nil || s.ProviderPending || !s.EvaluationClear || !elapsed(now, s.LastChange, Cooldown):
			d.Reason = "measurement_or_operation_hold"
		case growResources && local && demandOK && d.Congestion != nil && queued > 0 && ratio >= Utilization && *d.Congestion < math.Max(1, .1*float64(s.Commanded)) && *d.Demand > float64(s.Commanded):
			d.Planned = min(d.Safe, int(*d.Demand), s.Commanded+max(1, int(math.Ceil(GrowthFraction*float64(s.Commanded)))))
			d.Reason = "demand_growth"
		case demandOK && queued == 0 && !s.QuietSince.IsZero() && elapsed(now, s.QuietSince, QuietWindow) && ratio <= .5 && *d.Demand < float64(s.Commanded) && elapsed(now, s.LastQuietCut, QuietWindow):
			d.Planned = min(d.Safe, max(g.Floor, int(*d.Demand), int(math.Ceil(QuietFraction*float64(s.Commanded)))))
			d.Reason = "quiet_reduction"
		}
	}
	// Always enforce safety, even with held commanded settings. Decreases drain.
	if !d.SafetyStopped {
		if ready {
			d.Effective = max(0, min(d.Planned, d.Safe, int(m.Ready.Value)))
		} else if s.StartupVerified {
			d.Effective = max(0, min(d.Planned, d.Safe, s.Effective, s.LastReady))
		}
		if s.Frozen || !s.StoreHealthy || s.ProviderUncertain {
			d.Effective = min(d.Effective, s.Effective)
		}
	}
	if s.Identity.Path == domain.PathPostgres && !s.Frozen && s.StoreHealthy && !s.ProviderUncertain && !s.ProviderPending && !d.SafetyStopped && !s.Canary && fresh {
		if s.Pending != nil && d.Reason != "backoff" {
			d.PhysicalDesired = integer(s.Pending.Physical)
		} else {
			p := min(physicalSafe, d.Planned+int(math.Ceil(g.WarmRatio*float64(d.Planned))))
			if p > s.PhysicalDesired && !growResources {
				p = s.PhysicalDesired
			}
			// Physical decreases have a separate drain/quiet gate.
			if p < s.PhysicalDesired && (s.QuietSince.IsZero() || !elapsed(now, s.QuietSince, QuietWindow)) {
				p = s.PhysicalDesired
			}
			d.PhysicalDesired = integer(p)
		}
		d.WarmBudget = integer(max(0, *d.PhysicalDesired-d.Planned))
	}
	previous := Target{Admission: s.Commanded, Physical: s.PhysicalDesired}
	if s.Pending != nil && !d.CancelPending {
		previous = *s.Pending
	}
	d.Changed = d.Planned != previous.Admission || (d.PhysicalDesired != nil && *d.PhysicalDesired != previous.Physical)
	return d
}

func localValid(now time.Time, s State, m telemetry.MetricsSnapshot) bool {
	if m.Identity != s.Identity || !m.Complete || m.Omitted != 0 || len(m.Groups) > telemetry.MaxWorkloads+1 || m.End.After(now) || now.Sub(m.End) > 2*time.Second || m.End.Sub(m.Start) != EvidenceWindow || m.At.Before(m.End) || m.At.After(now) || m.RuntimeAt.IsZero() || m.RuntimeAt.After(now) || now.Sub(m.RuntimeAt) > 2*time.Second || !finite(m.MeanInFlight) || m.MeanInFlight < 0 || m.MeanInFlight > 1000000 || (!s.LastChange.IsZero() && s.LastChange.After(m.Start)) {
		return false
	}
	seen := make(map[string]bool, len(m.Groups))
	for _, w := range m.Groups {
		if w.Workload == "" || len(w.Workload) > 128 || seen[w.Workload] || !finite(w.ServiceSeconds) || w.ServiceSeconds < 0 || w.Active < 0 || w.Queued < 0 || w.Uncertain < 0 || w.Uncertain > w.Active || w.OldestActive < 0 || w.OldestQueued < 0 || w.Successes > w.Terminals || w.OverloadFailures > w.Terminals || w.Rejected > w.Terminals || w.TimedOut > w.OverloadFailures || w.Arrivals1 > w.Arrivals5 {
			return false
		}
		for _, n := range []uint64{w.Arrivals1, w.Arrivals5, w.Successes, w.Terminals, w.OverloadFailures, w.Rejected, w.TimedOut, w.UncertainOutcomes} {
			if n > telemetry.MaxCounter {
				return false
			}
		}
		seen[w.Workload] = true
		if w.OverloadFailures > w.Terminals-w.Successes || w.Rejected > w.Terminals-w.Successes-w.OverloadFailures {
			return false
		}
	}
	return true
}

// ValidWindow exposes the same coverage/identity checks to continuous quiet tracking.
func ValidWindow(now time.Time, id telemetry.Identity, changed time.Time, m telemetry.MetricsSnapshot) bool {
	return localValid(now, State{Identity: id, LastChange: changed}, m)
}
