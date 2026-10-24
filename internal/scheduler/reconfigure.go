package scheduler

import (
	"math"
	"sort"
	"time"

	"pooltune/internal/domain"
	"pooltune/internal/policy"
)

type preparedCohort struct {
	policy          *policy.CompiledPolicy
	sequence        []int
	budget, reserve int
}

func (s *Scheduler) prepare(snap Snapshot) (map[string]preparedCohort, error) {
	l := snap.Limits
	if !identifier(l.Target, 128) || (l.Path != domain.PathPostgres && l.Path != domain.PathREST) || !identifier(string(l.Revision), 128) || !identifier(l.GuardrailVersion, 128) || l.Epoch == 0 || l.Effective < 0 || l.Effective > s.cfg.MaxActive || l.CriticalReserve < 0 || l.CriticalReserve > l.Effective || len(l.CohortBudgets) == 0 || len(l.CohortBudgets) > 2 || len(snap.Policies) != len(l.CohortBudgets) {
		return nil, ErrInvalid
	}
	prepared := make(map[string]preparedCohort, len(l.CohortBudgets))
	total := 0
	reserved := 0
	for name, budget := range l.CohortBudgets {
		p := snap.Policies[name]
		if !identifier(name, 128) || budget < 0 || budget > l.Effective-total || p == nil || p.Revision() == "" {
			return nil, ErrInvalid
		}
		total += budget
		reserve := 0
		if budget > 0 {
			reserve = min(int(math.Floor(p.ReserveFraction()*float64(budget))), budget-1)
		}
		reserved += reserve
		weights := p.ClassWeights()
		sequence := interleaved([3]int{weights.Critical, weights.Normal, weights.Background})
		prepared[name] = preparedCohort{policy: p, sequence: sequence, budget: budget, reserve: reserve}
	}
	if l.CriticalReserve != reserved {
		return nil, ErrInvalid
	}
	return prepared, nil
}

// Smooth weighted round robin is precompiled into one bounded repeating cycle.
func interleaved(weights [3]int) []int {
	total := weights[0] + weights[1] + weights[2]
	sequence := make([]int, 0, total)
	var current [3]int
	for n := 0; n < total; n++ {
		best := 0
		for i, w := range weights {
			current[i] += w
			if current[i] > current[best] {
				best = i
			}
		}
		sequence = append(sequence, best)
		current[best] -= total
	}
	return sequence
}

// Install atomically changes policies and budgets. Epoch must equal the original
// owner's epoch, not increase per update. B09 owns predecessor activation checks.
func (s *Scheduler) Install(snap Snapshot) error {
	prepared, err := s.prepare(snap)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.installLocked(snap.Limits, prepared)
}
func (s *Scheduler) installLocked(l domain.LimitSnapshot, prepared map[string]preparedCohort) error {
	if s.limits.Target != "" && (l.Target != s.limits.Target || l.Path != s.limits.Path || l.GuardrailVersion != s.limits.GuardrailVersion || l.Epoch != s.limits.Epoch) {
		return ErrStale
	}
	for name, c := range s.cohorts {
		if _, ok := prepared[name]; !ok && (c.active > 0 || c.queued > 0) {
			return ErrInvalid
		}
	}
	next := make(map[string]*cohort, len(prepared))
	order := make([]string, 0, len(prepared))
	for name, p := range prepared {
		c := s.cohorts[name]
		if c == nil {
			c = &cohort{name: name}
		}
		c.policy = p.policy
		c.sequence = p.sequence
		c.position %= len(p.sequence)
		c.budget = p.budget
		c.reserve = p.reserve
		next[name] = c
		order = append(order, name)
	}
	sort.Strings(order)
	s.cohorts = next
	s.order = order
	s.cohortPosition = 0
	l.CohortBudgets = make(map[string]int, len(prepared))
	for name, p := range prepared {
		l.CohortBudgets[name] = p.budget
	}
	s.limits = l
	s.configurationGeneration++
	s.dispatch(time.Now())
	return nil
}

// InstallLimits preserves current policies, bindings, and rollback stops. Policy
// compilation stays outside the accounting lock. ErrStale on a concurrent install
// means the caller should refresh its control snapshot and retry.
func (s *Scheduler) InstallLimits(l domain.LimitSnapshot) error {
	s.mu.Lock()
	generation := s.configurationGeneration
	policies := make(map[string]*policy.CompiledPolicy, len(s.cohorts))
	for name, c := range s.cohorts {
		policies[name] = c.policy
	}
	s.mu.Unlock()
	prepared, err := s.prepare(Snapshot{Limits: l, Policies: policies})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.configurationGeneration {
		return ErrStale
	}
	return s.installLocked(l, prepared)
}
