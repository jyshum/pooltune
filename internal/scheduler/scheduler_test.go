package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"pooltune/internal/domain"
	"pooltune/internal/policy"
)

func compiled(t testing.TB, revision string, reserve float64, weights policy.ClassWeights) *policy.CompiledPolicy {
	t.Helper()
	p, err := policy.Compile(policy.Policy{SchemaVersion: "1", Revision: domain.RevisionID(revision), GuardrailVersion: "g1", ReserveFraction: reserve, ClassWeights: weights}, policy.Guardrails{Version: "g1", MaxReserveFraction: .9, MinClassWeight: 1, MaxClassWeight: 256}, policy.Registry{Fallback: policy.Fallback{QueueWait: time.Minute, QueueLimit: 1000, ConcurrencyCap: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func config() Config {
	return Config{MaxActive: 1000, MaxQueued: 1000, MaxQueuedBytes: 10000, MaxTenants: 1000, MaxWorkloads: 1000, MaxTenantQueued: 1000, MaxTenantActive: 1000, RatePerSecond: 1000000, RateBurst: 10000}
}
func snapshot(t testing.TB, budget int, reserve float64) Snapshot {
	return Snapshot{Limits: domain.LimitSnapshot{Target: "db", Path: domain.PathPostgres, Revision: "r1", Effective: budget, CriticalReserve: reserveFor(budget, reserve), CohortBudgets: map[string]int{"stable": budget}, GuardrailVersion: "g1", Epoch: 7}, Policies: map[string]*policy.CompiledPolicy{"stable": compiled(t, "r1", reserve, policy.ClassWeights{Critical: 6, Normal: 3, Background: 1})}}
}
func newScheduler(t testing.TB, cfg Config, snap Snapshot) *Scheduler {
	t.Helper()
	s, e := New(cfg, snap)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func classification(class domain.Class, tenant string) domain.Classification {
	return domain.Classification{Class: class, Cohort: "stable", Workload: "work", FairnessKey: tenant, Revision: "r1", QueueDeadline: time.Now().Add(time.Minute), ConcurrencyCap: 1000, QueueLimit: 1000}
}
func acquire(t testing.TB, s *Scheduler, c domain.Classification) *Permit {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p, e := s.Acquire(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	return p
}

type result struct {
	p   *Permit
	err error
}

func waiting(s *Scheduler, ctx context.Context, c domain.Classification) <-chan result {
	ch := make(chan result, 1)
	go func() { p, e := s.Acquire(ctx, c); ch <- result{p, e} }()
	return ch
}
func awaitQueued(t testing.TB, s *Scheduler, n int) {
	t.Helper()
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		if s.Stats().Queued == n {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("queued: got %d want %d", s.Stats().Queued, n)
}
func receive(t testing.TB, ch <-chan result) result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("result timed out")
		return result{}
	}
}
func noResult(t testing.TB, ch <-chan result) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("unexpected result: %+v", r)
	default:
	}
}

func TestCriticalReserveAndDraining(t *testing.T) {
	s := newScheduler(t, config(), snapshot(t, 5, .4))
	var held []*Permit
	for i := 0; i < 3; i++ {
		held = append(held, acquire(t, s, classification(domain.ClassBackground, "bg")))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := waiting(s, ctx, classification(domain.ClassNormal, "normal"))
	awaitQueued(t, s, 1)
	held = append(held, acquire(t, s, classification(domain.ClassCritical, "critical")))
	held = append(held, acquire(t, s, classification(domain.ClassCritical, "critical")))
	noResult(t, q)
	lim := snapshot(t, 2, .4).Limits
	if e := s.InstallLimits(lim); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		s.Release(held[i], Finished)
	}
	noResult(t, q)
	s.Release(held[3], Finished)
	r := receive(t, q)
	if r.err != nil {
		t.Fatal(r.err)
	}
	s.Release(held[4], Finished)
	s.Release(r.p, Finished)
	if got := s.Stats().Active; got != 0 {
		t.Fatalf("active=%d", got)
	}
}
func TestUnknownReservationAndOwnerOnce(t *testing.T) {
	s := newScheduler(t, config(), snapshot(t, 1, 0))
	other := newScheduler(t, config(), snapshot(t, 1, 0))
	p := acquire(t, s, classification(domain.ClassNormal, "a"))
	other.Release(p, Finished)
	s.Release(p, Unknown)
	s.Release(p, Finished)
	if st := s.Stats(); st.Active != 1 || st.Uncertain != 1 {
		t.Fatalf("reservation lost: %+v", st)
	}
	if other.Reconcile(p) {
		t.Fatal("foreign reconciliation")
	}
	if !s.Reconcile(p) || s.Reconcile(p) {
		t.Fatal("reconcile must win once")
	}
	if s.Stats().Active != 0 {
		t.Fatal("leaked reservation")
	}
}
func TestCancellationAdmissionRace(t *testing.T) {
	s := newScheduler(t, config(), snapshot(t, 1, 0))
	for i := 0; i < 100; i++ {
		p := acquire(t, s, classification(domain.ClassNormal, "a"))
		ctx, cancel := context.WithCancel(context.Background())
		ch := waiting(s, ctx, classification(domain.ClassNormal, "b"))
		awaitQueued(t, s, 1)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); cancel() }()
		go func() { defer wg.Done(); s.Release(p, Finished); s.Release(p, Finished) }()
		wg.Wait()
		r := receive(t, ch)
		if (r.p == nil) == (r.err == nil) {
			t.Fatalf("invalid result %+v", r)
		}
		if r.p != nil {
			s.Release(r.p, Finished)
		}
		if st := s.Stats(); st.Active != 0 || st.Queued != 0 {
			t.Fatalf("leak: %+v", st)
		}
	}
}
func TestTinyBudgetsAndExpiry(t *testing.T) {
	for _, b := range []int{0, 1} {
		t.Run(string(rune('0'+b)), func(t *testing.T) {
			s := newScheduler(t, config(), snapshot(t, b, .9))
			c := classification(domain.ClassBackground, "a")
			if b == 1 {
				p := acquire(t, s, c)
				s.Release(p, Finished)
				return
			}
			c.QueueDeadline = time.Now().Add(20 * time.Millisecond)
			p, e := s.Acquire(context.Background(), c)
			if p != nil || !errors.Is(e, ErrExpired) {
				t.Fatalf("expiry: %v %v", p, e)
			}
			if s.Stats().Queued != 0 {
				t.Fatal("expired queue retained")
			}
		})
	}
}
func TestCohortsNoBorrowAndTenantCap(t *testing.T) {
	snap := snapshot(t, 20, .2)
	snap.Limits.CohortBudgets = map[string]int{"stable": 18, "candidate": 2}
	snap.Limits.CriticalReserve = 3
	snap.Policies["candidate"] = compiled(t, "r2", .2, policy.ClassWeights{Critical: 1, Normal: 1, Background: 1})
	cfg := config()
	cfg.MaxTenantActive = 2
	s := newScheduler(t, cfg, snap)
	c := classification(domain.ClassNormal, "same")
	p := acquire(t, s, c)
	c.Cohort = "candidate"
	c.Revision = "r2"
	p2 := acquire(t, s, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Class = domain.ClassCritical
	ch := waiting(s, ctx, c)
	awaitQueued(t, s, 1)
	other := c
	other.FairnessKey = "other"
	p3 := acquire(t, s, other)
	noResult(t, ch)
	s.Release(p, Finished)
	noResult(t, ch)
	s.Release(p3, Finished)
	r := receive(t, ch)
	if r.err != nil {
		t.Fatal(r.err)
	}
	s.Release(p2, Finished)
	s.Release(r.p, Finished)
}

func TestQueueBoundsAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		change        func(*Config)
		first, second func() domain.Classification
		want          error
	}{
		{"count", func(c *Config) { c.MaxQueued = 1 }, func() domain.Classification { return classification(domain.ClassNormal, "a") }, func() domain.Classification { return classification(domain.ClassNormal, "b") }, ErrQueueFull},
		{"bytes", func(c *Config) { c.MaxQueuedBytes = 2 }, func() domain.Classification {
			c := classification(domain.ClassNormal, "a")
			c.BufferedBytes = 2
			return c
		}, func() domain.Classification {
			c := classification(domain.ClassNormal, "b")
			c.BufferedBytes = 1
			return c
		}, ErrQueueFull},
		{"tenants", func(c *Config) { c.MaxTenants = 1 }, func() domain.Classification { return classification(domain.ClassNormal, "a") }, func() domain.Classification { return classification(domain.ClassNormal, "b") }, ErrCardinality},
		{"workloads", func(c *Config) { c.MaxWorkloads = 1 }, func() domain.Classification { return classification(domain.ClassNormal, "a") }, func() domain.Classification {
			c := classification(domain.ClassNormal, "a")
			c.Workload = "other"
			return c
		}, ErrCardinality},
		{"tenant_queue", func(c *Config) { c.MaxTenantQueued = 1 }, func() domain.Classification { return classification(domain.ClassNormal, "a") }, func() domain.Classification { return classification(domain.ClassCritical, "a") }, ErrQueueFull},
		{"workload_queue", func(c *Config) {}, func() domain.Classification { c := classification(domain.ClassNormal, "a"); c.QueueLimit = 1; return c }, func() domain.Classification { c := classification(domain.ClassNormal, "b"); c.QueueLimit = 1; return c }, ErrQueueFull},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config()
			tc.change(&cfg)
			s := newScheduler(t, cfg, snapshot(t, 0, 0))
			ctx, cancel := context.WithCancel(context.Background())
			ch := waiting(s, ctx, tc.first())
			awaitQueued(t, s, 1)
			p, e := s.Acquire(context.Background(), tc.second())
			if p != nil || !errors.Is(e, tc.want) {
				t.Fatalf("got %v %v want %v", p, e, tc.want)
			}
			cancel()
			receive(t, ch)
			if s.Stats().QueuedBytes != 0 {
				t.Fatal("buffer accounting leak")
			}
		})
	}
	s := newScheduler(t, config(), snapshot(t, 1, 0))
	for _, mutate := range []func(*domain.Classification){func(c *domain.Classification) { c.Blocked = true }, func(c *domain.Classification) { c.Class = "bad" }, func(c *domain.Classification) { c.Revision = "stale" }, func(c *domain.Classification) { c.BufferedBytes = -1 }, func(c *domain.Classification) { c.FairnessKey = string(make([]byte, 600)) }, func(c *domain.Classification) { c.Cohort = "unknown" }} {
		c := classification(domain.ClassNormal, "a")
		mutate(&c)
		if p, e := s.Acquire(context.Background(), c); p != nil || e == nil {
			t.Fatalf("accepted invalid %+v", c)
		}
	}
}
func TestRateLimitAndBoundedRateState(t *testing.T) {
	cfg := config()
	cfg.RateBurst = 1
	cfg.RatePerSecond = 1
	cfg.MaxTenants = 1
	s := newScheduler(t, cfg, snapshot(t, 1, 0))
	p := acquire(t, s, classification(domain.ClassNormal, "a"))
	s.Release(p, Finished)
	if _, e := s.Acquire(context.Background(), classification(domain.ClassNormal, "a")); !errors.Is(e, ErrRateLimited) {
		t.Fatalf("rate error %v", e)
	}
	if _, e := s.Acquire(context.Background(), classification(domain.ClassNormal, "b")); !errors.Is(e, ErrCardinality) {
		t.Fatalf("rate state evicted too soon: %v", e)
	}
	if st := s.Stats(); st.Tenants > 1 || st.RateRejected != 1 {
		t.Fatalf("bad stats %+v", st)
	}
}
func TestSnapshotValidationCopyAndOldBinding(t *testing.T) {
	snap := snapshot(t, 1, 0)
	s := newScheduler(t, config(), snap)
	snap.Limits.CohortBudgets["stable"] = 999
	snap.Policies["stable"] = nil
	p := acquire(t, s, classification(domain.ClassNormal, "a"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := waiting(s, ctx, classification(domain.ClassNormal, "b"))
	awaitQueued(t, s, 1)
	for _, mutate := range []func(*domain.LimitSnapshot){func(l *domain.LimitSnapshot) { l.Target = "other" }, func(l *domain.LimitSnapshot) { l.Path = domain.PathREST }, func(l *domain.LimitSnapshot) { l.GuardrailVersion = "g2" }, func(l *domain.LimitSnapshot) { l.Epoch++ }, func(l *domain.LimitSnapshot) { l.Effective = -1 }, func(l *domain.LimitSnapshot) { l.CohortBudgets["stable"] = 2 }} {
		lim := snapshot(t, 1, 0).Limits
		mutate(&lim)
		if e := s.InstallLimits(lim); e == nil {
			t.Fatal("invalid limit accepted")
		}
	}
	next := snapshot(t, 1, 0)
	next.Limits.Revision = "r2"
	next.Policies["stable"] = compiled(t, "r2", 0, policy.ClassWeights{Critical: 1, Normal: 1, Background: 1})
	if e := s.Install(next); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Acquire(context.Background(), classification(domain.ClassNormal, "c")); !errors.Is(e, ErrStale) {
		t.Fatalf("stale accepted: %v", e)
	}
	s.Release(p, Finished)
	r := receive(t, ch)
	if r.err != nil || r.p.Classification().Revision != "r1" {
		t.Fatalf("old binding lost: %+v", r)
	}
	s.Release(r.p, Finished)
}
func TestTenantRotationAndClassWeights(t *testing.T) {
	snap := snapshot(t, 0, 0)
	s := newScheduler(t, config(), snap)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a1 := waiting(s, ctx, classification(domain.ClassNormal, "a"))
	awaitQueued(t, s, 1)
	a2 := waiting(s, ctx, classification(domain.ClassNormal, "a"))
	awaitQueued(t, s, 2)
	b := waiting(s, ctx, classification(domain.ClassNormal, "b"))
	awaitQueued(t, s, 3)
	lim := snap.Limits
	lim.Effective = 1
	lim.CohortBudgets = map[string]int{"stable": 1}
	if e := s.InstallLimits(lim); e != nil {
		t.Fatal(e)
	}
	r := receive(t, a1)
	s.Release(r.p, Finished)
	r = receive(t, b)
	noResult(t, a2)
	s.Release(r.p, Finished)
	r = receive(t, a2)
	s.Release(r.p, Finished)
	s = newScheduler(t, config(), snapshot(t, 0, 0))
	chs := make([]<-chan result, 0, 60)
	for i := 0; i < 20; i++ {
		for _, cl := range []domain.Class{domain.ClassCritical, domain.ClassNormal, domain.ClassBackground} {
			chs = append(chs, waiting(s, ctx, classification(cl, string(cl))))
			awaitQueued(t, s, len(chs))
		}
	}
	lim = snapshot(t, 1, 0).Limits
	if e := s.InstallLimits(lim); e != nil {
		t.Fatal(e)
	}
	counts := map[domain.Class]int{}
	for n := 0; n < 10; n++ {
		until := time.Now().Add(2 * time.Second)
		found := false
		for !found && time.Now().Before(until) {
			for i, ch := range chs {
				if ch == nil {
					continue
				}
				select {
				case r := <-ch:
					if r.err != nil {
						t.Fatal(r.err)
					}
					counts[r.p.Classification().Class]++
					chs[i] = nil
					if n < 9 {
						s.Release(r.p, Finished)
					} else {
						defer s.Release(r.p, Finished)
					}
					found = true
				default:
				}
				if found {
					break
				}
			}
			runtime.Gosched()
		}
		if !found {
			t.Fatal("weighted admission stalled")
		}
	}
	if counts[domain.ClassCritical] != 6 || counts[domain.ClassNormal] != 3 || counts[domain.ClassBackground] != 1 {
		t.Fatalf("weights %+v", counts)
	}
	cancel()
	for _, ch := range chs {
		if ch != nil {
			r := receive(t, ch)
			if r.p != nil {
				s.Release(r.p, Finished)
			}
		}
	}
}
func TestWorkloadCapAcrossCohorts(t *testing.T) {
	snap := snapshot(t, 4, 0)
	snap.Limits.CohortBudgets = map[string]int{"stable": 2, "candidate": 2}
	snap.Policies["candidate"] = compiled(t, "r2", 0, policy.ClassWeights{Critical: 1, Normal: 1, Background: 1})
	s := newScheduler(t, config(), snap)
	c := classification(domain.ClassNormal, "a")
	c.ConcurrencyCap = 1
	p := acquire(t, s, c)
	c.Cohort = "candidate"
	c.Revision = "r2"
	c.FairnessKey = "b"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := waiting(s, ctx, c)
	awaitQueued(t, s, 1)
	s.Release(p, Unknown)
	noResult(t, ch)
	s.Reconcile(p)
	r := receive(t, ch)
	if r.err != nil {
		t.Fatal(r.err)
	}
	s.Release(r.p, Finished)
}

func splitSnapshot(t testing.TB) Snapshot {
	snap := snapshot(t, 4, 0)
	snap.Limits.CohortBudgets = map[string]int{"stable": 2, "candidate": 2}
	snap.Policies["candidate"] = compiled(t, "r2", 0, policy.ClassWeights{Critical: 1, Normal: 1, Background: 1})
	return snap
}
func TestRollbackStopsCandidatePreservesBindingDeadlineAndFIFO(t *testing.T) {
	snap := splitSnapshot(t)
	s := newScheduler(t, config(), snap)
	stable := classification(domain.ClassNormal, "a")
	stable.Workload = "stable-held"
	p1 := acquire(t, s, stable)
	p2 := acquire(t, s, stable)
	candidate := classification(domain.ClassBackground, "a")
	candidate.Cohort = "candidate"
	candidate.Revision = "r2"
	candidate.Workload = "candidate-held"
	pc := acquire(t, s, candidate)
	if err := s.StopCohort("candidate"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(context.Background(), candidate); !errors.Is(err, ErrCohortStopped) {
		t.Fatalf("candidate accepted: %v", err)
	}
	// Recreate the fixture with queued candidate work before stopping it.
	s.Release(p1, Finished)
	s.Release(p2, Finished)
	s.Release(pc, Finished)
	s = newScheduler(t, config(), snap)
	p1 = acquire(t, s, stable)
	p2 = acquire(t, s, stable)
	pc = acquire(t, s, candidate)
	pc2 := acquire(t, s, candidate)
	candidate.Workload = "queued"
	candidate.QueueDeadline = time.Now().Add(30 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := waiting(s, ctx, candidate)
	awaitQueued(t, s, 1)
	newer := classification(domain.ClassNormal, "a")
	newer.Workload = "queued"
	new := waiting(s, ctx, newer)
	awaitQueued(t, s, 2)
	entered := make(chan struct{})
	resume := make(chan struct{})
	rolled := make(chan error, 1)
	go func() {
		_, err := s.RollbackBatch("candidate", "stable", func(c domain.Classification) (domain.Classification, error) {
			close(entered)
			<-resume
			c.Class = domain.ClassNormal
			c.Revision = "r1"
			c.QueueDeadline = c.QueueDeadline.Add(time.Hour)
			return c, nil
		})
		rolled <- err
	}()
	<-entered
	if !s.Stats().Cohorts["candidate"].Stopped {
		t.Fatal("callback ran before stop")
	}
	// Callback is outside lock: stats and release must remain available.
	s.Release(pc, Unknown)
	if pc.Classification().Revision != "r2" {
		t.Fatal("active candidate relabelled")
	}
	close(resume)
	if err := <-rolled; err != nil {
		t.Fatal(err)
	}
	s.Release(p1, Finished)
	r := receive(t, old)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if c := r.p.Classification(); c.Revision != "r1" || c.Cohort != "stable" || !c.QueueDeadline.Equal(candidate.QueueDeadline) {
		t.Fatalf("rollback binding/deadline %+v", c)
	}
	noResult(t, new)
	s.Release(r.p, Finished)
	r = receive(t, new)
	if r.err != nil {
		t.Fatal(r.err)
	}
	s.Release(r.p, Finished)
	s.Release(p2, Finished)
	s.Release(pc2, Finished)
	s.Reconcile(pc)
	if st := s.Stats(); st.Active != 0 || st.Queued != 0 {
		t.Fatalf("leak %+v", st)
	}
}
func TestRollbackBatchBoundAndCancellation(t *testing.T) {
	snap := splitSnapshot(t)
	snap.Limits.Effective = 0
	snap.Limits.CohortBudgets = map[string]int{"stable": 0, "candidate": 0}
	s := newScheduler(t, config(), snap)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := classification(domain.ClassNormal, "a")
	c.Cohort = "candidate"
	c.Revision = "r2"
	var chs []<-chan result
	for i := 0; i < 40; i++ {
		chs = append(chs, waiting(s, ctx, c))
		awaitQueued(t, s, i+1)
	}
	calls := 0
	remaining, err := s.RollbackBatch("candidate", "stable", func(c domain.Classification) (domain.Classification, error) {
		calls++
		c.Revision = "r1"
		return c, nil
	})
	if err != nil || remaining != 8 || calls != 32 {
		t.Fatalf("batch remaining=%d calls=%d err=%v", remaining, calls, err)
	}
	remaining, err = s.RollbackBatch("candidate", "stable", func(c domain.Classification) (domain.Classification, error) {
		cancel()
		c.Revision = "r1"
		return c, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range chs {
		r := receive(t, ch)
		if r.p != nil || !errors.Is(r.err, context.Canceled) {
			t.Fatalf("cancel race %+v", r)
		}
	}
	if s.Stats().Queued != 0 {
		t.Fatalf("rollback leak remaining=%d", remaining)
	}
}
func TestRollbackErrorRetainsOriginalAndRetiresOnlyAfterDrain(t *testing.T) {
	snap := splitSnapshot(t)
	s := newScheduler(t, config(), snap)
	c := classification(domain.ClassNormal, "a")
	c.Cohort = "candidate"
	c.Revision = "r2"
	p := acquire(t, s, c)
	p2 := acquire(t, s, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := waiting(s, ctx, c)
	awaitQueued(t, s, 1)
	boom := errors.New("reclassify failed")
	if _, err := s.RollbackBatch("candidate", "stable", func(c domain.Classification) (domain.Classification, error) { return c, boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if s.Stats().Cohorts["candidate"].Queued != 1 {
		t.Fatal("discarded candidate on error")
	}
	if err := s.Install(snapshot(t, 4, 0)); err == nil {
		t.Fatal("retired occupied cohort")
	}
	if _, err := s.RollbackBatch("candidate", "stable", func(c domain.Classification) (domain.Classification, error) { c.Revision = "r1"; return c, nil }); err != nil {
		t.Fatal(err)
	}
	r := receive(t, ch)
	if r.err != nil {
		t.Fatal(r.err)
	}
	s.Release(r.p, Finished)
	s.Release(p, Unknown)
	s.Release(p2, Finished)
	if err := s.Install(snapshot(t, 4, 0)); err == nil {
		t.Fatal("retired uncertain cohort")
	}
	s.Reconcile(p)
	if err := s.Install(snapshot(t, 4, 0)); err != nil {
		t.Fatal(err)
	}
}
func TestStatsOldestAgesAndCopiedPermit(t *testing.T) {
	s := newScheduler(t, config(), snapshot(t, 1, 0))
	p := acquire(t, s, classification(domain.ClassNormal, "a"))
	copied := *p
	s.Release(&copied, Finished)
	if s.Stats().Active != 1 {
		t.Fatal("copied permit released original occupancy")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := waiting(s, ctx, classification(domain.ClassNormal, "a"))
	awaitQueued(t, s, 1)
	s.Release(p, Unknown)
	st := s.Stats()
	w := st.ByWorkload["work"]
	if w.Active != 1 || w.Uncertain != 1 || w.Queued != 1 || w.OldestActiveAge <= 0 || w.OldestQueuedAge <= 0 {
		t.Fatalf("workload stats %+v", w)
	}
	delete(st.ByWorkload, "work")
	delete(st.Cohorts, "stable")
	if len(s.Stats().ByWorkload) != 1 {
		t.Fatal("stats leaked mutable state")
	}
	s.Reconcile(p)
	r := receive(t, ch)
	s.Release(r.p, Finished)
}
func TestExpiredHeadAdvancesAndLargeBatchContinues(t *testing.T) {
	s := newScheduler(t, config(), snapshot(t, 0, 0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := classification(domain.ClassNormal, "a")
	c.QueueDeadline = time.Now().Add(100 * time.Millisecond)
	expired := waiting(s, ctx, c)
	awaitQueued(t, s, 1)
	var chs []<-chan result
	for i := 0; i < 80; i++ {
		chs = append(chs, waiting(s, ctx, classification(domain.ClassNormal, "a")))
		awaitQueued(t, s, i+2)
	}
	r := receive(t, expired)
	if !errors.Is(r.err, ErrExpired) {
		t.Fatalf("expiry %v", r.err)
	}
	if err := s.InstallLimits(snapshot(t, 80, 0).Limits); err != nil {
		t.Fatal(err)
	}
	for _, ch := range chs {
		r := receive(t, ch)
		if r.err != nil {
			t.Fatal(r.err)
		}
		s.Release(r.p, Finished)
	}
	if s.Stats().Active != 0 || s.Stats().Queued != 0 {
		t.Fatal("batch leaked")
	}
}

func TestMaxLengthClassifierIdentityIsAdmissible(t *testing.T) {
	identity := strings.Repeat("x", 128)
	reg := policy.Registry{Fallback: policy.Fallback{QueueWait: time.Minute, QueueLimit: 10, ConcurrencyCap: 10}, Workloads: []policy.Workload{{ID: "long", Selector: policy.Selector{Target: identity, Path: domain.PathPostgres, Principal: identity}, IdentityDefault: true, DefaultClass: domain.ClassNormal, AllowedClasses: []domain.Class{domain.ClassNormal}, TenantTrust: true, MaxQueueWait: time.Minute, MaxQueueLimit: 10, MaxConcurrency: 10}}}
	p, err := policy.Compile(policy.Policy{SchemaVersion: "1", Revision: "r1", GuardrailVersion: "g1", ClassWeights: policy.ClassWeights{Critical: 1, Normal: 1, Background: 1}, Workloads: []policy.WorkloadSettings{{WorkloadID: "long", QueueWait: time.Minute, QueueLimit: 10, ConcurrencyCap: 10}}}, policy.Guardrails{Version: "g1", MaxReserveFraction: .5, MinClassWeight: 1, MaxClassWeight: 10}, reg)
	if err != nil {
		t.Fatal(err)
	}
	snap := snapshot(t, 1, 0)
	snap.Limits.Target = identity
	snap.Policies["stable"] = p
	s := newScheduler(t, config(), snap)
	c := policy.Classify(p, domain.RequestContext{Target: identity, Path: domain.PathPostgres, Principal: identity, Tenant: identity, TrustedTenant: true})
	c.Cohort = "stable"
	if len(c.FairnessKey) <= 128 {
		t.Fatal("fixture did not produce composite identity")
	}
	permit := acquire(t, s, c)
	s.Release(permit, Finished)
}
func TestRateStateReclaimsOnlyAfterRefill(t *testing.T) {
	cfg := config()
	cfg.RateBurst = 1
	cfg.RatePerSecond = 100
	cfg.MaxTenants = 1
	s := newScheduler(t, cfg, snapshot(t, 1, 0))
	p := acquire(t, s, classification(domain.ClassNormal, "a"))
	s.Release(p, Finished)
	until := time.Now().Add(2 * time.Second)
	for s.Stats().Tenants > 0 && time.Now().Before(until) {
		runtime.Gosched()
	}
	p = acquire(t, s, classification(domain.ClassNormal, "b"))
	s.Release(p, Finished)
}
func TestConcurrentRollbackBatchFenced(t *testing.T) {
	snap := splitSnapshot(t)
	snap.Limits.Effective = 0
	snap.Limits.CohortBudgets = map[string]int{"stable": 0, "candidate": 0}
	s := newScheduler(t, config(), snap)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := classification(domain.ClassNormal, "a")
	c.Cohort = "candidate"
	c.Revision = "r2"
	ch := waiting(s, ctx, c)
	awaitQueued(t, s, 1)
	entered := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.RollbackBatch("candidate", "stable", func(c domain.Classification) (domain.Classification, error) {
			close(entered)
			<-resume
			c.Revision = "r1"
			return c, nil
		})
	}()
	<-entered
	for i := 0; i < 100; i++ {
		if _, err := s.RollbackBatch("candidate", "stable", func(c domain.Classification) (domain.Classification, error) { return c, nil }); !errors.Is(err, ErrRollbackBusy) {
			t.Fatalf("concurrent batch %v", err)
		}
	}
	cancel()
	close(resume)
	<-done
	receive(t, ch)
}

func TestPermitBindsAdmissionLimitRevision(t *testing.T) {
	s := newScheduler(t, config(), snapshot(t, 0, 0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := waiting(s, ctx, classification(domain.ClassNormal, "a"))
	awaitQueued(t, s, 1)
	lim := snapshot(t, 1, 0).Limits
	lim.Revision = "capacity-2"
	if err := s.InstallLimits(lim); err != nil {
		t.Fatal(err)
	}
	r := receive(t, ch)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.p.LimitRevision() != "capacity-2" || r.p.Classification().Revision != "r1" {
		t.Fatal("lost distinct revisions")
	}
	lim.Revision = "capacity-3"
	s.InstallLimits(lim)
	if r.p.LimitRevision() != "capacity-2" {
		t.Fatal("rewrote admitted limit revision")
	}
	s.Release(r.p, Finished)
}

func TestExpiryAdvancesEligibleWorkBehindCappedHead(t *testing.T) {
	s := newScheduler(t, config(), snapshot(t, 2, 0))
	held := classification(domain.ClassNormal, "holder")
	held.ConcurrencyCap = 1
	p := acquire(t, s, held)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := classification(domain.ClassNormal, "waiting")
	c.ConcurrencyCap = 1
	c.QueueDeadline = time.Now().Add(100 * time.Millisecond)
	expired := waiting(s, ctx, c)
	awaitQueued(t, s, 1)
	c = classification(domain.ClassNormal, "waiting")
	c.Workload = "other"
	next := waiting(s, ctx, c)
	awaitQueued(t, s, 2)
	r := receive(t, expired)
	if !errors.Is(r.err, ErrExpired) {
		t.Fatal(r.err)
	}
	r = receive(t, next)
	if r.err != nil {
		t.Fatal(r.err)
	}
	s.Release(r.p, Finished)
	s.Release(p, Finished)
}
func TestControlAndByteArithmeticCannotOverflow(t *testing.T) {
	cfg := config()
	cfg.MaxActive = int(^uint(0) >> 1)
	cfg.MaxQueuedBytes = int64(^uint64(0) >> 1)
	snap := splitSnapshot(t)
	snap.Limits.Effective = cfg.MaxActive
	snap.Limits.CohortBudgets = map[string]int{"stable": cfg.MaxActive, "candidate": 1}
	if _, err := New(cfg, snap); err == nil {
		t.Fatal("overflowing cohort sum accepted")
	}
	snap.Limits.CohortBudgets = map[string]int{"stable": cfg.MaxActive, "candidate": cfg.MaxActive}
	if _, err := New(cfg, snap); err == nil {
		t.Fatal("two max budgets accepted")
	}
	s := newScheduler(t, cfg, snapshot(t, 0, 0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := classification(domain.ClassNormal, "a")
	c.BufferedBytes = cfg.MaxQueuedBytes
	ch := waiting(s, ctx, c)
	awaitQueued(t, s, 1)
	c.FairnessKey = "b"
	c.BufferedBytes = 1
	if _, err := s.Acquire(context.Background(), c); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("overflowing bytes accepted %v", err)
	}
	cancel()
	receive(t, ch)
}
func TestInvalidConfigurationAndSnapshots(t *testing.T) {
	for _, mutate := range []func(*Config){func(c *Config) { c.MaxQueued = 0 }, func(c *Config) { c.MaxQueuedBytes = 0 }, func(c *Config) { c.MaxActive = 0 }, func(c *Config) { c.MaxTenants = 0 }, func(c *Config) { c.MaxTenants = 4097 }, func(c *Config) { c.MaxWorkloads = 0 }, func(c *Config) { c.MaxTenantQueued = 0 }, func(c *Config) { c.MaxTenantActive = 0 }, func(c *Config) { c.RateBurst = 0 }, func(c *Config) { c.RatePerSecond = 0 }, func(c *Config) { c.RatePerSecond = math.NaN() }, func(c *Config) { c.RatePerSecond = math.Inf(1) }} {
		cfg := config()
		mutate(&cfg)
		if _, err := New(cfg, snapshot(t, 1, 0)); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	for _, mutate := range []func(*Snapshot){func(s *Snapshot) { s.Limits.Epoch = 0 }, func(s *Snapshot) { s.Limits.CohortBudgets["stable"] = -1 }, func(s *Snapshot) { s.Policies["stable"] = nil }, func(s *Snapshot) { s.Limits.Effective = 1001 }, func(s *Snapshot) { s.Limits.CriticalReserve = -1 }, func(s *Snapshot) { s.Limits.Path = "wrong" }, func(s *Snapshot) { s.Limits.Target = "" }, func(s *Snapshot) { s.Limits.CohortBudgets = nil }} {
		snap := snapshot(t, 1, 0)
		mutate(&snap)
		if _, err := New(config(), snap); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
}
func TestRollbackInvalidResultsAndBlockedPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(domain.Classification) (domain.Classification, error)
		want error
	}{
		{"identity", func(c domain.Classification) (domain.Classification, error) {
			c.Revision = "r1"
			c.FairnessKey = "forged"
			return c, nil
		}, ErrInvalid},
		{"stale", func(c domain.Classification) (domain.Classification, error) { return c, nil }, ErrStale},
		{"panic", func(c domain.Classification) (domain.Classification, error) { panic("bad mapping") }, ErrInvalid},
		{"blocked", func(c domain.Classification) (domain.Classification, error) {
			c.Revision = "r1"
			c.Blocked = true
			return c, nil
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := splitSnapshot(t)
			snap.Limits.Effective = 0
			snap.Limits.CohortBudgets = map[string]int{"stable": 0, "candidate": 0}
			s := newScheduler(t, config(), snap)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := classification(domain.ClassNormal, "a")
			c.Cohort = "candidate"
			c.Revision = "r2"
			ch := waiting(s, ctx, c)
			awaitQueued(t, s, 1)
			_, err := s.RollbackBatch("candidate", "stable", tc.fn)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error %v", err)
			}
			if tc.name == "blocked" {
				if r := receive(t, ch); !errors.Is(r.err, ErrBlocked) {
					t.Fatal(r.err)
				}
			} else {
				if s.Stats().Queued != 1 {
					t.Fatal("invalid callback lost waiter")
				}
				cancel()
				receive(t, ch)
			}
		})
	}
}

func TestConcurrentLimitsNeverOverridePolicyBinding(t *testing.T) {
	s := newScheduler(t, config(), snapshot(t, 1, 0))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			lim := snapshot(t, 1, 0).Limits
			err := s.InstallLimits(lim)
			if err != nil && !errors.Is(err, ErrStale) {
				t.Error(err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			snap := snapshot(t, 1, 0)
			snap.Limits.Revision = "r2"
			snap.Policies["stable"] = compiled(t, "r2", 0, policy.ClassWeights{Critical: 1, Normal: 1, Background: 1})
			if err := s.Install(snap); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
	c := classification(domain.ClassNormal, "a")
	c.Revision = "r2"
	p := acquire(t, s, c)
	s.Release(p, Finished)
}

func TestTokenBucketKeepsCreditWhenBatchTimePredatesCleanup(t *testing.T) {
	// A dispatch captures its batch time before cleanup can touch the same idle
	// tenant with a later time.Now. Reusing that batch time must not debit tokens.
	s := newScheduler(t, config(), snapshot(t, 0, 0))
	now := time.Now()
	tenant := &tenantState{tokens: 1, updated: now, heapIndex: -1}
	s.refill(tenant, now.Add(-time.Millisecond))
	if tenant.tokens != 1 || !tenant.updated.Equal(now) {
		t.Fatalf("batch time reduced tokens or rewound clock: %+v", tenant)
	}
}

func reserveFor(budget int, rho float64) int {
	if budget == 0 {
		return 0
	}
	return min(int(math.Floor(rho*float64(budget))), budget-1)
}

func TestSnapshotCriticalReserveMustMatchCohortSum(t *testing.T) {
	for _, budget := range []int{0, 1, 20} {
		snap := snapshot(t, budget, .2)
		if budget == 20 {
			snap.Limits.CohortBudgets = map[string]int{"stable": 18, "candidate": 2}
			snap.Policies["candidate"] = compiled(t, "r2", .6, policy.ClassWeights{Critical: 1, Normal: 1, Background: 1})
			snap.Limits.CriticalReserve = 4
		}
		s := newScheduler(t, config(), snap)
		correct := snap.Limits.CriticalReserve
		if st := s.Stats(); st.Effective != budget || st.CriticalReserve != correct {
			t.Fatalf("aggregate reservation stats %+v", st)
		}
		if budget == 20 {
			snap.Limits.CriticalReserve = 3
		} else {
			snap.Limits.CriticalReserve = 1
		}
		if err := s.Install(snap); err == nil {
			t.Fatalf("accepted inconsistent reserve at budget %d", budget)
		}
		snap.Limits.CriticalReserve = correct
		if err := s.Install(snap); err != nil {
			t.Fatal(err)
		}
	}
}

type enteredContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *enteredContext) Err() error { c.once.Do(func() { close(c.entered) }); return c.Context.Err() }

func TestDeadlineExpiringDuringMutexWaitCannotGrant(t *testing.T) {
	s := newScheduler(t, config(), snapshot(t, 1, 0))
	base, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	ctx := &enteredContext{Context: base, entered: make(chan struct{})}
	c := classification(domain.ClassNormal, "a")
	s.mu.Lock()
	ch := waiting(s, ctx, c)
	<-ctx.entered
	// The caller has entered Acquire while this test owns the lock. Advance on
	// the actual context deadline event, not an arbitrary sleep/readiness guess.
	<-base.Done()
	s.mu.Unlock()
	r := receive(t, ch)
	if r.p != nil || r.err == nil {
		t.Fatalf("expired mutex waiter admitted: %+v", r)
	}
	if st := s.Stats(); st.Active != 0 || st.Queued != 0 {
		t.Fatalf("expired waiter retained: %+v", st)
	}
}

func TestCancellationOnlyDispatchBatchContinuesToLiveWaiter(t *testing.T) {
	s := newScheduler(t, config(), snapshot(t, 0, 0))
	c := classification(domain.ClassNormal, "tenant")
	canceled, cancelCanceled := context.WithCancel(context.Background())
	cancelCanceled()
	// Insert the already-canceled heads without Acquire goroutines. Only dispatch
	// can remove them, so cancellation handlers cannot mask a missing wakeup.
	s.mu.Lock()
	s.tenants[c.FairnessKey] = &tenantState{key: c.FairnessKey, tokens: float64(s.cfg.RateBurst), updated: time.Now(), heapIndex: -1}
	s.workloads[c.Workload] = &workloadState{}
	var heads []*entry
	for i := 0; i < batchSize; i++ {
		s.nextOrder++
		e := &entry{classification: c, ctx: canceled, enqueued: time.Now(), order: s.nextOrder, done: make(chan struct{}), heapIndex: -1, queueIndex: -1}
		s.enqueue(e)
		heads = append(heads, e)
	}
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	live := waiting(s, ctx, c)
	awaitQueued(t, s, batchSize+1)
	if err := s.InstallLimits(snapshot(t, 1, 0).Limits); err != nil {
		t.Fatal(err)
	}
	for _, e := range heads {
		<-e.done
		if !errors.Is(e.err, context.Canceled) {
			t.Fatalf("head result %v", e.err)
		}
	}
	r := receive(t, live)
	if r.err != nil {
		t.Fatal(r.err)
	}
	s.Release(r.p, Finished)
	if st := s.Stats(); st.Active != 0 || st.Queued != 0 {
		t.Fatalf("batch leaked %+v", st)
	}
}

func TestRollbackBlockedErrorsRetainCandidate(t *testing.T) {
	wrapped := fmt.Errorf("mapping unavailable: %w", ErrBlocked)
	for _, tc := range []struct {
		name string
		fn   func(domain.Classification) (domain.Classification, error)
		want error
	}{
		{"direct_error", func(domain.Classification) (domain.Classification, error) { return domain.Classification{}, ErrBlocked }, ErrBlocked},
		{"wrapped_error", func(domain.Classification) (domain.Classification, error) { return domain.Classification{}, wrapped }, wrapped},
		{"blocked_wrong_identity", func(c domain.Classification) (domain.Classification, error) {
			c.Revision = "r1"
			c.Blocked = true
			c.FairnessKey = "forged"
			return c, nil
		}, ErrInvalid},
		{"blocked_wrong_workload", func(c domain.Classification) (domain.Classification, error) {
			c.Revision = "r1"
			c.Blocked = true
			c.Workload = "forged"
			return c, nil
		}, ErrInvalid},
		{"blocked_wrong_revision", func(c domain.Classification) (domain.Classification, error) { c.Blocked = true; return c, nil }, ErrStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := splitSnapshot(t)
			snap.Limits.Effective = 0
			snap.Limits.CohortBudgets = map[string]int{"stable": 0, "candidate": 0}
			s := newScheduler(t, config(), snap)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := classification(domain.ClassNormal, "a")
			c.Cohort = "candidate"
			c.Revision = "r2"
			ch := waiting(s, ctx, c)
			awaitQueued(t, s, 1)
			remaining, err := s.RollbackBatch("candidate", "stable", tc.fn)
			if !errors.Is(err, tc.want) || remaining != 1 {
				t.Fatalf("mapping error lost candidate: remaining=%d err=%v", remaining, err)
			}
			if st := s.Stats(); st.Queued != 1 || st.Cohorts["candidate"].Queued != 1 || !st.Cohorts["candidate"].Stopped {
				t.Fatalf("candidate not retained %+v", st)
			}
			noResult(t, ch)
			// Retry a successful, identity/revision-validated policy block. Only this
			// explicit result is allowed to reject the retained waiter.
			remaining, err = s.RollbackBatch("candidate", "stable", func(c domain.Classification) (domain.Classification, error) {
				c.Revision = "r1"
				c.Blocked = true
				c.ConcurrencyCap = 0
				c.QueueLimit = 0
				return c, nil
			})
			if err != nil || remaining != 0 {
				t.Fatalf("valid blocked mapping failed: remaining=%d err=%v", remaining, err)
			}
			if r := receive(t, ch); r.p != nil || !errors.Is(r.err, ErrBlocked) {
				t.Fatalf("blocked result %+v", r)
			}
		})
	}
}
