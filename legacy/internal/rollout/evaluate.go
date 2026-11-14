// Package rollout evaluates fixed-profile experiments without model involvement.
package rollout

import (
	"encoding/hex"
	"math"
	"pooltune/internal/domain"
	"pooltune/internal/telemetry"
	"time"
)

type Phase string

const (
	Partitioning Phase = "partition_baseline"
	Measuring    Phase = "candidate_measurement"
	Observing    Phase = "full_observation"
	Accepted     Phase = "accepted"
	Reverted     Phase = "reverted"
)

type Cohort struct {
	Arrivals, Successes, Failed, Rejected, TimedOut, Pending, Uncertain uint64
	Queue, Total                                                        telemetry.Histogram
	OldestQueued, OldestActive                                          time.Duration
}
type Group struct {
	Workload          string
	Stable, Candidate Cohort
}

// Counts include every eligible arrival assigned to this measurement window,
// even if still pending. Histograms cover successes only; other outcomes have
// separate non-regression tests. Runtime collection must preserve these units.
type Window struct {
	Start, End time.Time
	Complete   bool
	Groups     []Group
}
type LiveWorkload struct {
	Workload                   string
	Active, Queued, Uncertain  int
	OldestActive, OldestQueued time.Duration
}
type MetricsSnapshot struct {
	Live                                                                                        []LiveWorkload
	ExperimentID, Target, StableRevision, CandidateRevision, GuardrailVersion, CapacityRevision string
	Path                                                                                        domain.PathKind
	Epoch                                                                                       uint64
	Now, At, ResourceAt                                                                         time.Time
	CPU, Memory                                                                                 float64
	Frozen, SafetyStop, CapacityChanged, ProviderPending, AuditGap                              bool
	Window
}
type Objective struct {
	Workload                                                    string
	MaxQueue, MaxTotal, MaxOldest                               time.Duration
	MaxFailureRate, MaxRejectRate, MaxTimeoutRate               float64
	MaxLatencyRegression, MaxRateRegression, MinSuccessFraction float64
	Benefit                                                     bool
	MinImprovement                                              float64
}
type EvaluationProfile struct {
	Cooldown                                                                   time.Duration
	AllowNeutral                                                               bool
	ID, EvidenceSHA256                                                         string
	ApprovedAt                                                                 time.Time
	Validated                                                                  bool
	MinimumWindow, MaximumStage, MaximumMetricAge                              time.Duration
	MinimumSamples, MinimumTailSamples                                         uint64
	Quantile, ExposureTolerance, MaximumDemandChange, MaximumControlRegression float64
	MaximumCPU, MaximumMemory                                                  float64
	MinimumPassingWindows                                                      int
	Objectives                                                                 []Objective
}
type Decision struct {
	Action            domain.Decision
	Reason, Verdict   string
	ConcurrentControl bool
	Passing           bool
}

func result(action domain.Decision, reason, verdict string, control bool) Decision {
	return Decision{Action: action, Reason: reason, Verdict: verdict, ConcurrentControl: control}
}
func finite(x float64) bool   { return !math.IsNaN(x) && !math.IsInf(x, 0) }
func fraction(x float64) bool { return finite(x) && x >= 0 && x < 1 }
func (p EvaluationProfile) valid() bool {
	hash, e := hex.DecodeString(p.EvidenceSHA256)
	if p.Cooldown <= 0 || p.Cooldown > 24*time.Hour || p.ID == "" || len(p.ID) > 128 || !p.Validated || e != nil || len(hash) != 32 || p.ApprovedAt.IsZero() || p.MinimumWindow <= 0 || p.MaximumStage <= p.MinimumWindow || p.MaximumStage > time.Hour || p.MaximumMetricAge <= 0 || p.MaximumMetricAge > p.MinimumWindow || p.MinimumSamples == 0 || p.MinimumSamples > telemetry.MaxCounter || p.MinimumTailSamples == 0 || p.Quantile < .5 || p.Quantile >= 1 || !finite(p.Quantile) || float64(p.MinimumSamples)*(1-p.Quantile)+1e-8 < float64(p.MinimumTailSamples) || !fraction(p.ExposureTolerance) || p.ExposureTolerance > .05 || !fraction(p.MaximumDemandChange) || !fraction(p.MaximumControlRegression) || p.MaximumCPU <= 0 || p.MaximumCPU > 1 || !finite(p.MaximumCPU) || p.MaximumMemory <= 0 || p.MaximumMemory > 1 || !finite(p.MaximumMemory) || p.MinimumPassingWindows < 1 || p.MinimumPassingWindows > 10 || len(p.Objectives) == 0 || len(p.Objectives) > 128 {
		return false
	}
	seen := map[string]bool{}
	benefit := false
	for _, o := range p.Objectives {
		if o.Workload == "" || len(o.Workload) > 128 || seen[o.Workload] || o.MaxQueue <= 0 || o.MaxTotal <= 0 || o.MaxOldest <= 0 || !fraction(o.MaxFailureRate) || !fraction(o.MaxRejectRate) || !fraction(o.MaxTimeoutRate) || !fraction(o.MaxLatencyRegression) || !fraction(o.MaxRateRegression) || !finite(o.MinSuccessFraction) || o.MinSuccessFraction <= 0 || o.MinSuccessFraction > 1 || !fraction(o.MinImprovement) || (o.Benefit && o.MinImprovement <= 0) {
			return false
		}
		seen[o.Workload] = true
		benefit = benefit || o.Benefit
	}
	return benefit || p.AllowNeutral
}
func percentile(h telemetry.Histogram, q float64) (time.Duration, bool) {
	total := uint64(0)
	for _, n := range h {
		if n > telemetry.MaxCounter || total > telemetry.MaxCounter-n {
			return 0, false
		}
		total += n
	}
	if total == 0 {
		return 0, false
	}
	rank := uint64(math.Ceil(float64(total) * q))
	sum := uint64(0)
	bounds := telemetry.LatencyBounds()
	for i, n := range h {
		sum += n
		if sum >= rank {
			if i >= len(bounds) {
				return 0, false
			}
			return bounds[i], true
		}
	}
	return 0, false
}
func validCohort(c Cohort) bool {
	values := []uint64{c.Successes, c.Failed, c.Rejected, c.TimedOut, c.Pending, c.Uncertain}
	sum := uint64(0)
	for _, n := range values {
		if n > telemetry.MaxCounter || sum > telemetry.MaxCounter-n {
			return false
		}
		sum += n
	}
	if sum != c.Arrivals || c.Arrivals > telemetry.MaxCounter || c.OldestQueued < 0 || c.OldestActive < 0 {
		return false
	}
	for _, h := range []telemetry.Histogram{c.Queue, c.Total} {
		n := uint64(0)
		for _, v := range h {
			if v > telemetry.MaxCounter || n > telemetry.MaxCounter-v {
				return false
			}
			n += v
		}
		if n != c.Successes {
			return false
		}
	}
	return true
}
func enough(c Cohort, p EvaluationProfile) bool {
	return c.Successes >= p.MinimumSamples && float64(c.Successes)*(1-p.Quantile)+1e-8 >= float64(p.MinimumTailSamples)
}
func rate(n, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total)
}
func absolute(c Cohort, o Objective, p EvaluationProfile) (bool, bool) {
	if c.Arrivals == 0 {
		return false, false
	}
	if c.Uncertain > 0 || rate(c.Failed, c.Arrivals) > o.MaxFailureRate || rate(c.Rejected, c.Arrivals) > o.MaxRejectRate || rate(c.TimedOut, c.Arrivals) > o.MaxTimeoutRate || rate(c.Successes, c.Arrivals) < o.MinSuccessFraction {
		return true, true
	}
	queue, ok := percentile(c.Queue, p.Quantile)
	total, ok2 := percentile(c.Total, p.Quantile)
	if !ok || !ok2 {
		return false, false
	}
	return queue > o.MaxQueue || total > o.MaxTotal, true
}
func regressed(current, baseline Cohort, o Objective, p EvaluationProfile) (bool, bool) {
	cq, ok1 := percentile(current.Queue, p.Quantile)
	ct, ok2 := percentile(current.Total, p.Quantile)
	bq, ok3 := percentile(baseline.Queue, p.Quantile)
	bt, ok4 := percentile(baseline.Total, p.Quantile)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return false, false
	}
	bad := float64(cq) > float64(bq)*(1+o.MaxLatencyRegression) || float64(ct) > float64(bt)*(1+o.MaxLatencyRegression)
	for _, pair := range [][2]uint64{{current.Failed, baseline.Failed}, {current.Rejected, baseline.Rejected}, {current.TimedOut, baseline.TimedOut}} {
		if rate(pair[0], current.Arrivals) > rate(pair[1], baseline.Arrivals)+o.MaxRateRegression {
			bad = true
		}
	}
	if rate(current.Successes, current.Arrivals)+o.MaxRateRegression < rate(baseline.Successes, baseline.Arrivals) {
		bad = true
	}
	return bad, true
}
func Evaluate(e Experiment, m MetricsSnapshot, p EvaluationProfile) Decision {
	control := e.Stage >= 0 && e.Stage < len(e.Fractions) && e.Fractions[e.Stage] < 1
	revert := func(reason, verdict string) Decision { return result(domain.DecisionRevert, reason, verdict, control) }
	wait := func(reason string) Decision {
		if !m.Now.Before(e.Deadline) {
			return revert(reason, "inconclusive")
		}
		return result(domain.DecisionWait, reason, "measuring", control)
	}
	if m.Now.IsZero() || !p.valid() || p.ID != e.ProfileID || p.ApprovedAt.After(e.CreatedAt) {
		return revert("invalid_fixed_profile", "inconclusive")
	}
	if e.Stage < 0 || e.Stage >= len(e.Fractions) || e.Phase == Accepted || e.Phase == Reverted {
		return revert("invalid_experiment", "inconclusive")
	}
	if !m.Now.Before(e.LeaseExpires) {
		return revert("lease_expired", "unsafe")
	}
	if m.Frozen || m.SafetyStop || m.CapacityChanged || m.ProviderPending || m.AuditGap {
		return revert("safety_or_capacity_change", "unsafe")
	}
	if finite(m.CPU) && m.CPU > p.MaximumCPU || finite(m.Memory) && m.Memory > p.MaximumMemory {
		return revert("database_pressure", "unsafe")
	}
	objectives := map[string]Objective{}
	for _, o := range p.Objectives {
		objectives[o.Workload] = o
	}
	for _, live := range m.Live {
		if live.Uncertain > 0 {
			return revert("starvation_or_uncertainty", "unsafe")
		}
		if o, ok := objectives[live.Workload]; ok {
			if live.OldestQueued > o.MaxOldest || live.OldestActive > o.MaxOldest {
				return revert("starvation_or_uncertainty", "unsafe")
			}
		} else if live.Active > 0 || live.Queued > 0 {
			return wait("unknown_live_workload")
		}
	}
	for _, g := range m.Groups {
		if o, ok := objectives[g.Workload]; ok {
			for _, c := range []Cohort{g.Stable, g.Candidate} {
				if c.OldestQueued > o.MaxOldest || c.OldestActive > o.MaxOldest || c.Uncertain > 0 {
					return revert("starvation_or_uncertainty", "unsafe")
				}
			}
		}
	}
	if m.ExperimentID != e.ID || m.Target != e.Target || m.Path != e.Path || m.Epoch != e.Epoch || m.StableRevision != e.StableRevision || m.CandidateRevision != e.CandidateRevision || m.GuardrailVersion != e.GuardrailVersion || m.CapacityRevision != e.CapacityRevision {
		return wait("mixed_revision_evidence")
	}
	if !finite(m.CPU) || !finite(m.Memory) || m.CPU < 0 || m.Memory < 0 || m.At.After(m.Now) || m.Now.Sub(m.At) > p.MaximumMetricAge || m.ResourceAt.After(m.Now) || m.Now.Sub(m.ResourceAt) > p.MaximumMetricAge || m.End.After(m.Now) || m.Now.Sub(m.End) > p.MaximumMetricAge {
		return wait("stale_metrics")
	}
	if !m.Complete || m.Start.Before(e.StageStarted) || !m.Start.Before(m.End) || m.End.Sub(m.Start) < p.MinimumWindow || m.Start.Before(e.LastWindowEnd) || len(m.Groups) != len(objectives) {
		return wait("incomplete_window")
	}
	seen := map[string]bool{}
	base := map[string]Group{}
	for _, g := range e.Baseline.Groups {
		base[g.Workload] = g
	}
	fraction := e.Fractions[e.Stage]
	benefit := true
	for _, g := range m.Groups {
		o, known := objectives[g.Workload]
		if !known || seen[g.Workload] || !validCohort(g.Stable) || !validCohort(g.Candidate) {
			return wait("invalid_workload_coverage")
		}
		seen[g.Workload] = true
		arrivals := g.Stable.Arrivals + g.Candidate.Arrivals
		if arrivals == 0 || math.Abs(rate(g.Candidate.Arrivals, arrivals)-fraction) > p.ExposureTolerance {
			return wait("skewed_exposure")
		}
		if !enough(g.Candidate, p) || (control && !enough(g.Stable, p)) || (!control && g.Stable.Arrivals != 0) {
			return wait("insufficient_samples")
		}
		cohorts := []Cohort{g.Candidate}
		if control {
			cohorts = append(cohorts, g.Stable)
		}
		for _, c := range cohorts {
			bad, known := absolute(c, o, p)
			if !known {
				return wait("unbounded_latency")
			}
			if bad {
				return revert("absolute_objective_regression", "regressed")
			}
		}
		if e.Phase == Partitioning {
			continue
		}
		b, ok := base[g.Workload]
		if !ok || !e.Baseline.Complete || !validCohort(b.Candidate) || !validCohort(b.Stable) || !enough(b.Candidate, p) || !enough(b.Stable, p) || !e.Baseline.End.After(e.Baseline.Start) || e.Baseline.End.After(e.StageStarted) {
			return wait("missing_matched_baseline")
		}
		currentDemand := float64(arrivals) / m.End.Sub(m.Start).Seconds()
		baselineDemand := float64(b.Candidate.Arrivals+b.Stable.Arrivals) / e.Baseline.End.Sub(e.Baseline.Start).Seconds()
		if baselineDemand <= 0 || math.Abs(currentDemand/baselineDemand-1) > p.MaximumDemandChange {
			return wait("changed_demand")
		}
		bad, known := regressed(g.Candidate, b.Candidate, o, p)
		if !known {
			return wait("missing_comparison")
		}
		if bad {
			return revert("candidate_regression", "regressed")
		}
		if control {
			controlObjective := o
			controlObjective.MaxLatencyRegression = p.MaximumControlRegression
			bad, known = regressed(g.Stable, b.Stable, controlObjective, p)
			if !known {
				return wait("missing_control")
			}
			if bad {
				return revert("shared_control_regression", "inconclusive")
			}
		}
		if o.Benefit {
			current, _ := percentile(g.Candidate.Total, p.Quantile)
			previous, _ := percentile(b.Candidate.Total, p.Quantile)
			if float64(current) > float64(previous)*(1-o.MinImprovement) {
				benefit = false
			}
		}
	}
	if !benefit && !p.AllowNeutral {
		return wait("benefit_not_demonstrated")
	}
	if !m.Now.Before(e.Deadline) {
		return revert("stage_deadline", "inconclusive")
	}
	if e.PassingWindows+1 < p.MinimumPassingWindows {
		d := wait("additional_independent_window")
		d.Passing = true
		return d
	}
	d := result(domain.DecisionAdvance, "profile_passed", "passed", control)
	d.Passing = true
	return d
}
