// Package scheduler owns bounded admission for one target and ingress path.
// Callers are trusted ingress adapters. This package does no authentication or I/O.
package scheduler

import (
	"container/list"
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"time"

	"pooltune/internal/domain"
	"pooltune/internal/policy"
)

var (
	ErrInvalid       = errors.New("invalid scheduler input")
	ErrExpired       = errors.New("queue deadline expired")
	ErrBlocked       = errors.New("workload blocked")
	ErrQueueFull     = errors.New("queue full")
	ErrCardinality   = errors.New("cardinality limit")
	ErrRateLimited   = errors.New("tenant arrival rate limit")
	ErrStale         = errors.New("stale classification")
	ErrCohortStopped = errors.New("cohort admissions stopped")
)

// Config contains explicit operator ceilings, not calibrated production defaults.
// RatePerSecond/RateBurst apply to accepted arrivals per trusted fairness key.
type Config struct {
	MaxActive, MaxQueued, MaxTenants, MaxWorkloads int
	MaxTenantQueued, MaxTenantActive, RateBurst    int
	MaxQueuedBytes                                 int64
	RatePerSecond                                  float64
}

// Snapshot installs cohort policies and capacity together. Maps are copied.
type Snapshot struct {
	Limits   domain.LimitSnapshot
	Policies map[string]*policy.CompiledPolicy
}
type Outcome uint8

const (
	Finished Outcome = iota
	Unknown
)

// Permit is an immutable public binding. Only the original pointer can release.
// Unknown outcomes remain active until Reconcile confirms backend completion.
type Permit struct {
	limitRevision  domain.RevisionID
	owner          *Scheduler
	self           *Permit
	classification domain.Classification
	started        time.Time
	state          uint8
	activeNode     *list.Element
}

func (p *Permit) Classification() domain.Classification {
	if p == nil {
		return domain.Classification{}
	}
	return p.classification
}
func (p *Permit) StartedAt() time.Time {
	if p == nil {
		return time.Time{}
	}
	return p.started
}

type WorkloadStats struct {
	Active, Uncertain, Queued        int
	OldestActiveAge, OldestQueuedAge time.Duration
}
type CohortStats struct {
	Active, Uncertain, Queued, Budget, Reserve int
	Stopped                                    bool
}
type Stats struct {
	Effective, CriticalReserve                    int
	Active, Uncertain, Queued, Tenants, Workloads int
	QueuedBytes                                   int64
	RateRejected                                  uint64
	Cohorts                                       map[string]CohortStats
	ByWorkload                                    map[string]WorkloadStats
}
type tenantState struct {
	key              string
	active, queued   int
	tokens           float64
	updated, expires time.Time
	heapIndex        int
}
type workloadState struct {
	active, uncertain, queued int
	activeList, queueList     list.List
}
type cohort struct {
	rollbackBusy                               bool
	name                                       string
	policy                                     *policy.CompiledPolicy
	sequence                                   []int
	position                                   int
	budget, reserve, active, uncertain, queued int
	classes                                    [3]classQueue
	classActive                                [3]int
	entries                                    list.List
	stopped                                    bool
}
type Scheduler struct {
	configurationGeneration   uint64
	nextOrder                 uint64
	mu                        sync.Mutex
	cfg                       Config
	limits                    domain.LimitSnapshot
	cohorts                   map[string]*cohort
	order                     []string
	cohortPosition            int
	tenants                   map[string]*tenantState
	workloads                 map[string]*workloadState
	deadlines                 deadlineHeap
	idleTenants               tenantHeap
	active, uncertain, queued int
	queuedBytes               int64
	rateRejected              uint64
	timer                     *time.Timer
	continuing                bool
}

func New(cfg Config, snap Snapshot) (*Scheduler, error) {
	if cfg.MaxActive <= 0 || cfg.MaxQueued <= 0 || cfg.MaxQueued > 100000 || cfg.MaxTenants <= 0 || cfg.MaxTenants > 4096 || cfg.MaxWorkloads <= 0 || cfg.MaxWorkloads > 4096 || cfg.MaxTenantQueued <= 0 || cfg.MaxTenantActive <= 0 || cfg.MaxQueuedBytes <= 0 || cfg.RateBurst <= 0 || cfg.RateBurst > 1000000 || cfg.RatePerSecond <= 0 || math.IsNaN(cfg.RatePerSecond) || math.IsInf(cfg.RatePerSecond, 0) {
		return nil, ErrInvalid
	}
	s := &Scheduler{cfg: cfg, cohorts: make(map[string]*cohort), tenants: make(map[string]*tenantState), workloads: make(map[string]*workloadState)}
	if err := s.Install(snap); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Scheduler) Acquire(ctx context.Context, c domain.Classification) (*Permit, error) {
	if ctx == nil {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(c.QueueDeadline) {
		c.QueueDeadline = deadline
	}
	s.mu.Lock()
	now := time.Now()
	s.dispatch(now)
	err := s.validateClassification(c, true)
	if err == nil && !c.QueueDeadline.After(now) {
		err = ErrExpired
	}
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	tenant := s.tenants[c.FairnessKey]
	work := s.workloads[c.Workload]
	if s.queued >= s.cfg.MaxQueued || c.BufferedBytes > s.cfg.MaxQueuedBytes-s.queuedBytes || (tenant != nil && tenant.queued >= s.cfg.MaxTenantQueued) || (work != nil && work.queued >= c.QueueLimit) {
		s.mu.Unlock()
		return nil, ErrQueueFull
	}
	if (tenant == nil && len(s.tenants) >= s.cfg.MaxTenants) || (work == nil && len(s.workloads) >= s.cfg.MaxWorkloads) {
		s.mu.Unlock()
		return nil, ErrCardinality
	}
	if tenant == nil {
		tenant = &tenantState{key: c.FairnessKey, tokens: float64(s.cfg.RateBurst), updated: now, heapIndex: -1}
		s.tenants[c.FairnessKey] = tenant
	}
	s.refill(tenant, now)
	if tenant.tokens < 1 {
		s.rateRejected++
		s.idle(tenant, now)
		s.armTimer(now)
		s.mu.Unlock()
		return nil, ErrRateLimited
	}
	s.unidle(tenant)
	tenant.tokens--
	if work == nil {
		work = &workloadState{}
		s.workloads[c.Workload] = work
	}
	s.nextOrder++
	e := &entry{classification: c, ctx: ctx, enqueued: now, order: s.nextOrder, done: make(chan struct{}), heapIndex: -1, queueIndex: -1}
	s.enqueue(e)
	s.dispatch(now)
	s.mu.Unlock()
	select {
	case <-e.done:
	case <-ctx.Done():
		s.mu.Lock()
		if !e.finished {
			s.finish(e, nil, ctx.Err())
			s.dispatch(time.Now())
		}
		s.mu.Unlock()
		<-e.done
	}
	return e.permit, e.err
}

func (s *Scheduler) validateClassification(c domain.Classification, fresh bool) error {
	if c.Blocked {
		return ErrBlocked
	}
	if classIndex(c.Class) < 0 || !identifier(c.Workload, 128) || !identifier(c.FairnessKey, 544) || !identifier(c.RuleID, 128) && c.RuleID != "" || !identifier(string(c.Revision), 128) || !identifier(c.Cohort, 128) || c.QueueDeadline.IsZero() || c.ConcurrencyCap <= 0 || c.QueueLimit <= 0 || c.BufferedBytes < 0 {
		return ErrInvalid
	}
	co := s.cohorts[c.Cohort]
	if co == nil {
		return ErrInvalid
	}
	if fresh && co.stopped {
		return ErrCohortStopped
	}
	if fresh && c.Revision != co.policy.Revision() {
		return ErrStale
	}
	return nil
}
func identifier(v string, max int) bool {
	return len(v) > 0 && len(v) <= max && !strings.ContainsAny(v, "\x00\r\n")
}
func classIndex(c domain.Class) int {
	switch c {
	case domain.ClassCritical:
		return 0
	case domain.ClassNormal:
		return 1
	case domain.ClassBackground:
		return 2
	}
	return -1
}

func (s *Scheduler) Release(p *Permit, outcome Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p == nil || p.owner != s || p.self != p || p.state != 0 {
		return
	}
	if outcome != Finished {
		p.state = 1
		s.uncertain++
		s.cohorts[p.classification.Cohort].uncertain++
		s.workloads[p.classification.Workload].uncertain++
		return
	}
	s.free(p)
	s.dispatch(time.Now())
}

// Reconcile must be called only after positive evidence of backend completion.
// Cancellation or frontend disconnection is not such evidence.
func (s *Scheduler) Reconcile(p *Permit) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p == nil || p.owner != s || p.self != p || p.state != 1 {
		return false
	}
	s.uncertain--
	s.cohorts[p.classification.Cohort].uncertain--
	s.workloads[p.classification.Workload].uncertain--
	s.free(p)
	s.dispatch(time.Now())
	return true
}
func (s *Scheduler) free(p *Permit) {
	c := p.classification
	co := s.cohorts[c.Cohort]
	t := s.tenants[c.FairnessKey]
	w := s.workloads[c.Workload]
	p.state = 2
	s.active--
	co.active--
	co.classActive[classIndex(c.Class)]--
	t.active--
	w.active--
	w.activeList.Remove(p.activeNode)
	s.idle(t, time.Now())
	s.removeWorkload(c.Workload, w)
}
func (s *Scheduler) removeWorkload(key string, w *workloadState) {
	if w.active == 0 && w.queued == 0 {
		delete(s.workloads, key)
	}
}

// Stats copies bounded cohort/workload indexes. It never walks requests.
func (s *Scheduler) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	st := Stats{Effective: s.limits.Effective, CriticalReserve: s.limits.CriticalReserve, Active: s.active, Uncertain: s.uncertain, Queued: s.queued, QueuedBytes: s.queuedBytes, Tenants: len(s.tenants), Workloads: len(s.workloads), RateRejected: s.rateRejected, Cohorts: make(map[string]CohortStats, len(s.cohorts)), ByWorkload: make(map[string]WorkloadStats, len(s.workloads))}
	for key, c := range s.cohorts {
		st.Cohorts[key] = CohortStats{Active: c.active, Uncertain: c.uncertain, Queued: c.queued, Budget: c.budget, Reserve: c.reserve, Stopped: c.stopped}
	}
	for key, w := range s.workloads {
		ws := WorkloadStats{Active: w.active, Uncertain: w.uncertain, Queued: w.queued}
		if n := w.activeList.Front(); n != nil {
			ws.OldestActiveAge = now.Sub(n.Value.(*Permit).started)
		}
		if n := w.queueList.Front(); n != nil {
			ws.OldestQueuedAge = now.Sub(n.Value.(*entry).enqueued)
		}
		st.ByWorkload[key] = ws
	}
	return st
}

func (p *Permit) LimitRevision() domain.RevisionID {
	if p == nil {
		return ""
	}
	return p.limitRevision
}
