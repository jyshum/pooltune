// Package burst admits transactions against capacity established before traffic.
// It never changes a provider setting, retries SQL, or terminates active work.
package burst

import (
	"errors"
	"fmt"
	"math"
	"pooltune/internal/admin"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/provider"
	"pooltune/internal/scheduler"
	"strings"
	"sync"
	"time"
)

var ErrUnsafe = errors.New("burst capacity is not verified")

type Config struct {
	historyScope         string
	Target               string
	Base, Ceiling, Queue int
	QueueWait            time.Duration
	PressureSource       string
	PressureMaxAge       time.Duration
}
type Health struct {
	Transactions   *provider.Transactions
	At, PressureAt time.Time
	Source         string
	CPU, Memory    float64
	Warm           int
	BudgetVerified bool
}
type State struct {
	AuditFault  bool
	Version     int
	Scope       string
	Revision    string
	ManualLimit int
	History     []Revision
	At          time.Time
	Effective   int
	Reason      string
	Frozen      bool
	Changes     uint64
}
type Controller struct {
	mu               sync.Mutex
	cfg              Config
	Scheduler        *scheduler.Scheduler
	policy           *policy.CompiledPolicy
	health           Health
	state            State
	enabled, latched bool
	auditFailed      bool
	closing          bool
	manualLimit      int
	lastBusy         time.Time
	persist          func(State) error
}

func NewController(c Config, persist func(State) error) (*Controller, error) {
	maxAge := 120 * time.Second
	if strings.HasPrefix(c.PressureSource, "google:") {
		maxAge = provider.GooglePressureMaxAge
	} else if strings.HasPrefix(c.PressureSource, "azure:") {
		maxAge = provider.AzurePressureMaxAge
	}
	if c.Target == "" || c.Base < 1 || c.Ceiling < c.Base || c.Ceiling > 1000 || c.Queue < 1 || c.Queue > 10000 || c.QueueWait <= 0 || c.QueueWait > 30*time.Second || c.PressureSource == "" || c.PressureMaxAge < time.Second || c.PressureMaxAge > maxAge {
		return nil, ErrUnsafe
	}
	p, e := policy.Compile(policy.Policy{SchemaVersion: "1", Revision: "burst-policy", GuardrailVersion: "burst-guard", ClassWeights: policy.ClassWeights{Critical: 1, Normal: 1, Background: 1}}, policy.Guardrails{Version: "burst-guard", MaxReserveFraction: .9, MinClassWeight: 1, MaxClassWeight: 10}, policy.Registry{Fallback: policy.Fallback{QueueWait: c.QueueWait, QueueLimit: c.Queue, ConcurrencyCap: c.Ceiling}})
	if e != nil {
		return nil, e
	}
	b := &Controller{cfg: c, policy: p, persist: persist, state: State{Version: 1, Scope: configScope(c), Revision: revisionID(), Frozen: true, Reason: "startup_frozen"}}
	b.Scheduler, e = scheduler.New(scheduler.Config{MaxActive: c.Ceiling, MaxQueued: c.Queue, MaxQueuedBytes: 16 << 20, MaxTenants: 1024, MaxWorkloads: 1, MaxTenantQueued: c.Queue, MaxTenantActive: c.Ceiling, RatePerSecond: 100000, RateBurst: 100000}, b.snapshot(0, 0))
	return b, e
}
func (b *Controller) snapshot(n int, rev uint64) scheduler.Snapshot {
	return scheduler.Snapshot{Limits: domain.LimitSnapshot{Target: b.cfg.Target, Path: domain.PathPostgres, Revision: domain.RevisionID(fmt.Sprintf("burst-%d", rev)), Effective: n, CohortBudgets: map[string]int{"stable": n}, GuardrailVersion: "burst-guard", Epoch: 1}, Policies: map[string]*policy.CompiledPolicy{"stable": b.policy}}
}
func (b *Controller) Update(h Health) { b.mu.Lock(); b.health = h; b.mu.Unlock() }
func (b *Controller) valid(now time.Time) bool {
	h := b.health
	return h.BudgetVerified && h.Warm >= b.cfg.Ceiling && !h.At.IsZero() && !h.At.After(now) && now.Sub(h.At) <= 2*time.Second && !h.PressureAt.IsZero() && !h.PressureAt.After(now) && now.Sub(h.PressureAt) <= b.cfg.PressureMaxAge && h.Source == b.cfg.PressureSource && !math.IsNaN(h.CPU) && !math.IsNaN(h.Memory) && h.CPU >= 0 && h.CPU <= 1 && h.Memory >= 0 && h.Memory <= 1
}
func (b *Controller) apply(now time.Time, n int, reason string) error {
	return b.transition(now, n, reason, "controller", "", admin.Command{})
}
func (b *Controller) Resume(now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.safeToEnable(now) {
		return ErrUnsafe
	}
	b.enabled = true
	b.latched = false
	b.manualLimit = 0
	b.lastBusy = now
	return b.apply(now, b.cfg.Base, "ready")
}
func (b *Controller) Tick(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	stats := b.Scheduler.Stats()
	if b.auditFailed {
		return
	}
	if stats.Uncertain != 0 {
		b.latched = true
		_ = b.apply(now, 0, "uncertain_work")
		return
	}
	if !b.enabled || b.latched {
		_ = b.apply(now, 0, "frozen")
		return
	}
	if !b.valid(now) {
		_ = b.apply(now, 0, "unverified_health")
		return
	}
	if b.health.CPU >= .85 || b.health.Memory >= .9 {
		b.latched = true
		_ = b.apply(now, 0, "pressure_stop")
		return
	}
	if b.health.CPU >= .7 || b.health.Memory >= .8 {
		_ = b.apply(now, b.cfg.Base, "pressure_backoff")
		return
	}
	if b.manualLimit > 0 {
		_ = b.apply(now, b.manualLimit, "owner_hold")
		return
	}
	if stats.Active > 0 || stats.Queued > 0 {
		b.lastBusy = now
	}
	if stats.Queued > 0 && stats.Active >= stats.Effective {
		_ = b.apply(now, b.cfg.Ceiling, "warm_reserve")
		return
	}
	if stats.Effective == 0 || now.Sub(b.lastBusy) >= 30*time.Second {
		_ = b.apply(now, b.cfg.Base, "base")
		return
	}
}
func (b *Controller) State() State { b.mu.Lock(); defer b.mu.Unlock(); return cloneState(b.state) }
func (b *Controller) Ready() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.enabled && !b.latched && b.state.Effective > 0 && b.valid(time.Now())
}
func (b *Controller) Classify(user, database, session string) domain.Classification {
	return domain.Classification{Class: domain.ClassNormal, Cohort: "stable", Workload: "app", FairnessKey: session, Revision: "burst-policy", QueueDeadline: time.Now().Add(b.cfg.QueueWait), ConcurrencyCap: b.cfg.Ceiling, QueueLimit: b.cfg.Queue, Blocked: !b.Ready()}
}

// Freeze blocks new transactions without releasing active or uncertain permits.
func (b *Controller) Freeze() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.enabled = false
	_ = b.apply(time.Now(), 0, "frozen")
}

// Shutdown permanently fences this process while its active work drains.
// A browser Resume cannot race SIGTERM or Runtime.Close to reopen admission.
func (b *Controller) Shutdown() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closing = true
	b.enabled = false
	_ = b.apply(time.Now(), 0, "shutdown")
}
