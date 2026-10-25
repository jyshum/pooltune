package rollout

import (
	"errors"
	"math"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"time"
)

var ErrIneligible = errors.New("rollout is not eligible")

type Experiment struct {
	CompletedAt                                                                                  time.Time
	ID, Target, StableRevision, CandidateRevision, GuardrailVersion, CapacityRevision, ProfileID string
	Path                                                                                         domain.PathKind
	Epoch                                                                                        uint64
	CreatedAt, StageStarted, Deadline, LeaseExpires, LastWindowEnd                               time.Time
	Phase                                                                                        Phase
	Stage, PassingWindows                                                                        int
	Fractions                                                                                    []float64
	Baseline                                                                                     Window
}

func Budgets(capacity int, fraction float64, stable, candidate policy.Policy) (int, int, error) {
	if capacity < 1 || capacity > 1000000 || !finite(fraction) || (fraction != .05 && fraction != .1 && fraction != .25 && fraction != .5 && fraction != 1) {
		return 0, 0, ErrIneligible
	}
	c := int(math.Floor(float64(capacity) * fraction))
	s := capacity - c
	if c < 2 || (fraction < 1 && s < 2) {
		return 0, 0, ErrIneligible
	}
	if !finite(stable.ReserveFraction) || !finite(candidate.ReserveFraction) || stable.ReserveFraction < 0 || candidate.ReserveFraction < 0 || stable.ReserveFraction >= 1 || candidate.ReserveFraction >= 1 {
		return 0, 0, ErrIneligible
	}
	changed := false
	if stable.ReserveFraction != candidate.ReserveFraction {
		if int(float64(c)*stable.ReserveFraction) == int(float64(c)*candidate.ReserveFraction) {
			return 0, 0, ErrIneligible
		}
		changed = true
	}
	a, b := stable.ClassWeights, candidate.ClassWeights
	if a != b {
		if a.Critical < 1 || a.Normal < 1 || a.Background < 1 || b.Critical < 1 || b.Normal < 1 || b.Background < 1 || a.Critical+a.Normal+a.Background > 256 || b.Critical+b.Normal+b.Background > 256 {
			return 0, 0, ErrIneligible
		}
		if a.Critical*b.Normal == b.Critical*a.Normal && a.Normal*b.Background == b.Normal*a.Background {
			return 0, 0, ErrIneligible
		}
		changed = true
	}
	before := map[string]policy.WorkloadSettings{}
	for _, w := range stable.Workloads {
		before[w.WorkloadID] = w
	}
	for _, w := range candidate.Workloads {
		old, ok := before[w.WorkloadID]
		if !ok {
			return 0, 0, ErrIneligible
		}
		if old != w {
			if old.ConcurrencyCap != w.ConcurrencyCap && min(old.ConcurrencyCap, c) == min(w.ConcurrencyCap, c) && old.QueueLimit == w.QueueLimit && old.QueueWait == w.QueueWait && old.Blocked == w.Blocked {
				return 0, 0, ErrIneligible
			}
			changed = true
		}
	}
	rules := map[string]domain.Class{}
	for _, r := range stable.Rules {
		rules[r.WorkloadID] = r.Class
	}
	for _, r := range candidate.Rules {
		if rules[r.WorkloadID] != r.Class {
			changed = true
		}
		delete(rules, r.WorkloadID)
	}
	if len(rules) > 0 {
		changed = true
	}
	if !changed {
		return 0, 0, ErrIneligible
	}
	return c, s, nil
}
