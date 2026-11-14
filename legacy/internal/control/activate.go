// Package control applies versioned snapshots outside the request path.
package control

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"pooltune/internal/durable"
	"pooltune/internal/policy"
	"pooltune/internal/scheduler"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrInvalid  = errors.New("invalid activation")
	ErrStale    = errors.New("stale activation")
	ErrStorage  = errors.New("activation storage unavailable")
	ErrRejected = errors.New("activation definitively rejected")
	ErrRecovery = errors.New("recovery evidence required")
)

type Config struct {
	PhysicalBlocked   func() bool
	PhysicalApply     func(context.Context, PhysicalChange) error
	ResumeEvidence    func() ResumeEvidence
	CapacityMetrics   func() controller.MetricsSnapshot
	RecoveryDirectory string
	Resources         func() domain.Resources
	Measurements      WindowCollector
	CohortKey         []byte
	Target            string
	Path              domain.PathKind
	Epoch             uint64
	Directory         string
	Guardrails        policy.Guardrails
	Registry          policy.Registry
	MaxCapacity       int
	MaxLease          time.Duration
	Reconcile         func(context.Context, RecoveryRequest) (RecoveryEvidence, error)
}
type ActivationAck struct {
	Checksum string
	Revision domain.RevisionID
	Epoch    uint64
	State    string
	AuditGap bool
}
type snapshot struct {
	Rejected       map[domain.RevisionID]ActivationAck
	SafetyCeiling  *int
	ActiveAt       time.Time
	Active, Stable domain.ActivationEnvelope
	PreviousEpoch  uint64
	Fallbacks      []domain.RevisionID
}
type runtimeSnapshot struct {
	activation domain.RevisionID
	fraction   float64
	policies   map[string]*policy.CompiledPolicy
	raw        map[string]policy.Policy
}
type Engine struct {
	anchor                    *durable.Files
	highWater                 recoveryAnchor
	measurementRevision       domain.RevisionID
	mu                        sync.Mutex
	cfg                       Config
	files                     *durable.Files
	s                         *scheduler.Scheduler
	state                     snapshot
	runtime                   atomic.Pointer[runtimeSnapshot]
	ready                     atomic.Bool
	persist                   func(snapshot) error
	timer                     *time.Timer
	closed, auditGap, serving bool
}

func Checksum(e domain.ActivationEnvelope) string {
	e.Checksum = ""
	raw, _ := json.Marshal(e)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func New(cfg Config, s *scheduler.Scheduler) (*Engine, error) {
	if s == nil || cfg.Epoch == 0 || cfg.MaxCapacity < 1 || cfg.MaxLease <= 0 || cfg.MaxLease > time.Hour {
		return nil, ErrInvalid
	}
	target, path := s.Identity()
	if target != cfg.Target || path != cfg.Path || s.Stats().Effective != 0 || s.Stats().Active != 0 {
		return nil, ErrInvalid
	}
	cfg.CohortKey = append([]byte(nil), cfg.CohortKey...)
	// Deep-copy owner boundaries so callers cannot mutate accepted authority.
	raw, err := json.Marshal(cfg.Registry)
	if err != nil {
		return nil, ErrInvalid
	}
	if json.Unmarshal(raw, &cfg.Registry) != nil {
		return nil, ErrInvalid
	}
	files, err := durable.Open(cfg.Directory)
	if err != nil {
		return nil, err
	}
	e := &Engine{cfg: cfg, files: files, s: s}
	e.persist = func(st snapshot) error {
		data, err := json.Marshal(st)
		if err != nil {
			return ErrStorage
		}
		if e.anchor != nil {
			a := e.highWater
			a.Digest = digest(data)
			if e.saveAnchor(a) != nil {
				return ErrStorage
			}
		}
		if files.Save("activation.json", data) != nil {
			return ErrStorage
		}
		return nil
	}
	data, err := files.Load("activation.json")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		files.Close()
		return nil, ErrStorage
	}
	if anchorErr := e.openAnchor(data); anchorErr != nil {
		if e.anchor != nil {
			e.anchor.Close()
		}
		files.Close()
		return nil, anchorErr
	}
	success := false
	defer func() {
		if !success && e.anchor != nil {
			e.anchor.Close()
		}
	}()
	if err == nil {
		if decode(data, &e.state) != nil || e.state.Stable.Checksum != Checksum(e.state.Stable) || e.state.Active.Checksum != Checksum(e.state.Active) || e.state.Active.Epoch >= cfg.Epoch {
			files.Close()
			return nil, ErrStale
		}
		e.state.PreviousEpoch = e.state.Active.Epoch
		runtime, err := e.prepare(e.state.Stable, true)
		if err != nil {
			files.Close()
			return nil, err
		}
		e.runtime.Store(runtime)
	}
	success = true
	return e, nil
}
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	if e.timer != nil {
		e.timer.Stop()
	}
	e.ready.Store(false)
	if e.anchor != nil {
		e.anchor.Close()
	}
	return e.files.Close()
}
func (e *Engine) Ready() bool { return e.ready.Load() }
func (e *Engine) Status(revision domain.RevisionID) ActivationAck {
	e.mu.Lock()
	defer e.mu.Unlock()
	if rejected, ok := e.state.Rejected[revision]; ok {
		return rejected
	}
	state := "unknown"
	if e.state.Active.Revision == revision {
		state = "prepared"
		if e.ready.Load() {
			state = "active"
		}
	}
	return ActivationAck{Revision: revision, Epoch: e.cfg.Epoch, State: state, AuditGap: e.auditGap}
}
func (e *Engine) Classify(ctx domain.RequestContext, cohort string) domain.Classification {
	r := e.runtime.Load()
	if r == nil || !e.ready.Load() {
		return domain.Classification{Blocked: true}
	}
	c := policy.Classify(r.policies[cohort], ctx)
	c.ActivationRevision = r.activation
	c.Cohort = cohort
	return c
}
func decode(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return ErrInvalid
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func (e *Engine) prepare(env domain.ActivationEnvelope, recovery bool) (*runtimeSnapshot, error) {
	return e.prepareBounded(env, recovery, e.state.SafetyCeiling)
}
func (e *Engine) prepareBounded(env domain.ActivationEnvelope, recovery bool, ceiling *int) (*runtimeSnapshot, error) {
	if math.IsNaN(env.CandidateFraction) || math.IsInf(env.CandidateFraction, 0) || env.CandidateFraction < 0 || env.CandidateFraction > 1 || (env.CandidateFraction > 0 && len(e.cfg.CohortKey) != 32) {
		return nil, ErrInvalid
	}
	if env.SchemaVersion != "1" || env.GuardrailVersion != e.cfg.Guardrails.Version || env.Checksum != Checksum(env) || len(env.Limits) != 1 || len(env.PolicyJSON) > policy.MaxPolicyBytes {
		return nil, ErrInvalid
	}
	l := env.Limits[0]
	if ceiling != nil && l.Effective > *ceiling {
		return nil, ErrRecovery
	}
	if l.Target != e.cfg.Target || l.Path != e.cfg.Path || l.Revision != env.Revision || l.GuardrailVersion != env.GuardrailVersion || l.Epoch != env.Epoch || l.Effective < 0 || l.Effective > e.cfg.MaxCapacity {
		return nil, ErrInvalid
	}
	var raw map[string]policy.Policy
	if decode(env.PolicyJSON, &raw) != nil || raw["stable"].Revision == "" || len(raw) == 0 || len(raw) > 2 {
		return nil, ErrInvalid
	}
	if len(raw) == 2 {
		if raw["candidate"].Revision == "" || env.LeaseExpiresAt.IsZero() {
			return nil, ErrInvalid
		}
	} else if !env.LeaseExpiresAt.IsZero() || env.CandidateFraction != 0 {
		return nil, ErrInvalid
	}
	r := &runtimeSnapshot{activation: env.Revision, fraction: env.CandidateFraction, policies: map[string]*policy.CompiledPolicy{}, raw: raw}
	prior := e.runtime.Load()
	for name, p := range raw {
		if name != "stable" && name != "candidate" {
			return nil, ErrInvalid
		}
		bounds := e.cfg.Guardrails
		bounds.CurrentPolicy = nil
		bounds.CurrentRevision = ""
		if !recovery && prior != nil {
			b, _ := json.Marshal(p)
			for oldName, before := range prior.raw {
				a, _ := json.Marshal(before)
				if bytes.Equal(a, b) {
					r.policies[name] = prior.policies[oldName]
					break
				}
			}
			if r.policies[name] != nil {
				continue
			}
			bounds.CurrentPolicy = prior.policies["stable"]
			bounds.CurrentRevision = bounds.CurrentPolicy.Revision()
		}
		if recovery {
			p.ExpectedPredecessor = ""
		}
		compiled, err := policy.Compile(p, bounds, e.cfg.Registry)
		if err != nil {
			return nil, err
		}
		r.policies[name] = compiled
	}
	// Validate all scheduler bounds before persisting; this does not install.
	proposed := scheduler.Snapshot{Limits: l, Policies: r.policies}
	var validation error
	if recovery || e.isPromotion(r) {
		proposed.Limits.Epoch = e.cfg.Epoch
		validation = e.s.ValidateDefinition(proposed)
	} else {
		validation = e.s.Validate(proposed)
	}
	if validation != nil {
		return nil, validation
	}
	return r, nil
}
func (e *Engine) Activate(ctx context.Context, env domain.ActivationEnvelope) (ActivationAck, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || ctx.Err() != nil {
		return ActivationAck{}, ErrInvalid
	}
	if strings.HasPrefix(string(env.Revision), "fallback-") || strings.HasPrefix(string(env.Revision), "recovery-") {
		return ActivationAck{}, ErrInvalid
	}
	if env.Epoch != e.cfg.Epoch {
		return ActivationAck{}, ErrStale
	}
	if e.state.Active.Revision == env.Revision {
		if e.state.Active.Checksum != env.Checksum || env.Checksum != Checksum(env) {
			return ActivationAck{}, ErrStale
		}
		state := "prepared"
		if e.ready.Load() {
			state = "active"
		}
		return ActivationAck{Revision: env.Revision, Epoch: e.cfg.Epoch, State: state, AuditGap: e.auditGap}, nil
	}
	if rejected, ok := e.state.Rejected[env.Revision]; ok {
		if rejected.Checksum != env.Checksum {
			return ActivationAck{}, ErrStale
		}
		return rejected, nil
	}
	if env.ExpectedPredecessor != e.state.Active.Revision || e.auditGap || len(e.state.Fallbacks) >= 128 {
		return ActivationAck{}, ErrStale
	}
	if !e.ready.Load() && e.state.Active.Revision != "" {
		return ActivationAck{}, ErrRecovery
	}
	if !env.LeaseExpiresAt.IsZero() && (!e.ready.Load() || !env.LeaseExpiresAt.After(time.Now()) || time.Until(env.LeaseExpiresAt) > e.cfg.MaxLease) {
		return ActivationAck{}, ErrInvalid
	}
	if cohort, ok := e.s.Stats().Cohorts["candidate"]; ok && cohort.Stopped {
		if cohort.Active > 0 || cohort.Queued > 0 {
			return ActivationAck{}, ErrRecovery
		}
		old := e.runtime.Load()
		stable := &runtimeSnapshot{activation: e.state.Active.Revision, policies: map[string]*policy.CompiledPolicy{"stable": old.policies["stable"]}, raw: map[string]policy.Policy{"stable": old.raw["stable"]}}
		if err := e.s.Install(scheduler.Snapshot{Limits: e.state.Active.Limits[0], Policies: stable.policies}); err != nil {
			return ActivationAck{}, err
		}
		e.runtime.Store(stable)
	}
	// Clone the envelope; caller-owned maps/slices must not mutate persisted state.
	encoded, err := json.Marshal(env)
	if err != nil {
		return ActivationAck{}, ErrInvalid
	}
	if decode(encoded, &env) != nil {
		return ActivationAck{}, ErrInvalid
	}
	ceiling := e.state.SafetyCeiling
	resume := isSafetyResume(env.Revision)
	if resume {
		if !e.validResumeLocked(env) {
			return e.rejectActivationLocked(env)
		}
		ceiling = nil
	}
	runtime, err := e.prepareBounded(env, false, ceiling)
	if err != nil {
		return ActivationAck{}, err
	}
	if e.ready.Load() && e.cfg.PhysicalBlocked != nil && e.cfg.PhysicalBlocked() {
		return e.rejectActivationLocked(env)
	}
	next := e.state
	next.Active = env
	if resume {
		next.SafetyCeiling = nil
	}
	next.ActiveAt = time.Now()
	if len(runtime.policies) == 1 {
		next.Stable = env
	}
	install := scheduler.Snapshot{Limits: env.Limits[0], Policies: runtime.policies, ClosedAdmissions: map[string]bool{"stable": runtime.fraction == 1}}
	// Capacity-only growth and safety recovery need a final check serialized
	// with request accounting, including the durable write. Promotion has its
	// separate old-cohort drain protocol below.
	growth := len(e.state.Active.Limits) == 1 && env.Limits[0].Effective > e.state.Active.Limits[0].Effective && !e.isPromotion(runtime)
	guarded := e.ready.Load() && (resume || growth || strings.HasPrefix(string(env.Revision), "capacity-"))
	if guarded {
		err = e.s.InstallDurably(install, resume, func() error {
			if e.persist(next) != nil {
				return ErrStorage
			}
			return nil
		})
		if errors.Is(err, scheduler.ErrUnsafe) {
			return e.rejectActivationLocked(env)
		}
		if err != nil {
			e.auditGap = true
			return ActivationAck{}, err
		}
	} else if err = e.persist(next); err != nil {
		e.auditGap = true
		return ActivationAck{}, ErrStorage
	}
	if e.ready.Load() && !guarded {
		if e.isPromotion(runtime) {
			// The durable stable-only recovery snapshot is already saved. Keep
			// active candidate permits intact while moving its queued work.
			old := e.runtime.Load()
			if err = e.s.StopCohort("candidate"); err != nil {
				e.auditGap = true
				return ActivationAck{}, err
			}
			install.Policies = map[string]*policy.CompiledPolicy{"stable": runtime.policies["stable"], "candidate": old.policies["candidate"]}
			install.Limits.CohortBudgets = map[string]int{"stable": env.Limits[0].Effective, "candidate": 0}
		}
		if err = e.s.Install(install); err != nil {
			e.auditGap = true
			return ActivationAck{}, err
		}
	}
	if e.isPromotion(runtime) {
		for {
			remaining, moveErr := e.s.RollbackBatch("candidate", "stable", runtime.policies["stable"].Reclassify)
			if moveErr != nil {
				e.auditGap = true
				return ActivationAck{}, moveErr
			}
			if remaining == 0 {
				break
			}
		}
	}
	e.state = next
	e.runtime.Store(runtime)
	if e.timer != nil {
		e.timer.Stop()
	}
	if !env.LeaseExpiresAt.IsZero() {
		revision := env.Revision
		e.timer = time.AfterFunc(time.Until(env.LeaseExpiresAt), func() { e.expire(revision) })
	}
	state := "prepared"
	if e.ready.Load() {
		state = "active"
	}
	return ActivationAck{Revision: env.Revision, Epoch: e.cfg.Epoch, State: state}, nil
}

// Fallback uses the same path for expired leases, freeze, and explicit rollback.
func (e *Engine) expire(revision domain.RevisionID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed && e.state.Active.Revision == revision {
		_ = e.fallbackLocked()
	}
}
func (e *Engine) Rollback() error { e.mu.Lock(); defer e.mu.Unlock(); return e.fallbackLocked() }

// Constrain only tightens the local safety ceiling. It never kills active work,
// increases capacity, or depends on control/gateway availability. Raising this
// ceiling requires a separately verified recovery workflow, not an activation.
func (e *Engine) Constrain(capacity int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if capacity < 0 || len(e.state.Active.Limits) != 1 || capacity > e.state.Active.Limits[0].Effective {
		return ErrInvalid
	}
	if capacity == e.state.Active.Limits[0].Effective {
		return nil
	}
	return e.fallbackWithLimit(&capacity)
}
func (e *Engine) fallbackLocked() error { return e.fallbackWithLimit(nil) }
func (e *Engine) fallbackWithLimit(capacity *int) error {
	if e.closed || !e.ready.Load() {
		return ErrRecovery
	}
	r := e.runtime.Load()
	if r == nil {
		return ErrRecovery
	}
	if r.policies["candidate"] == nil && capacity == nil {
		return nil
	}
	if r.policies["candidate"] != nil {
		if err := e.s.StopCohort("candidate"); err != nil {
			return err
		}
	}
	stable, err := e.prepare(e.state.Stable, true)
	if err != nil {
		return err
	}
	for r.policies["candidate"] != nil {
		remaining, err := e.s.RollbackBatch("candidate", "stable", stable.policies["stable"].Reclassify)
		if err != nil {
			return err
		}
		if remaining == 0 {
			break
		}
	}
	env := e.state.Stable
	env.ExpectedPredecessor = e.state.Active.Revision
	env.Revision = domain.RevisionID(fmt.Sprintf("fallback-%d-%d", e.cfg.Epoch, time.Now().UnixNano()))
	env.Epoch = e.cfg.Epoch
	env.LeaseExpiresAt = time.Time{}
	env.Limits = append([]domain.LimitSnapshot(nil), env.Limits...)
	env.Limits[0].Epoch = e.cfg.Epoch
	env.Limits[0].Revision = env.Revision
	// Keep the stopped cohort while its original transactions drain.
	if e.s.Stats().Cohorts["candidate"].Active > 0 {
		stable.policies["candidate"] = r.policies["candidate"]
		stable.raw["candidate"] = r.raw["candidate"]
		if stable.policies["candidate"] == nil {
			// Acceptance makes runtime stable-only, while the scheduler still
			// owns stopped permits bound to that same accepted policy.
			stable.policies["candidate"] = r.policies["stable"]
			stable.raw["candidate"] = r.raw["stable"]
		}
	}
	limits := env.Limits[0]
	if capacity != nil {
		limits.Effective = min(limits.Effective, *capacity)
		limits.CapacityRevision = string(env.Revision)
	}
	limits.CohortBudgets = map[string]int{"stable": limits.Effective}
	if stable.policies["candidate"] != nil {
		limits.CohortBudgets["candidate"] = 0
	}
	limits.CriticalReserve = reserve(stable.policies["stable"], limits.Effective)
	env.Limits[0] = limits
	env.Limits[0].CohortBudgets = map[string]int{"stable": limits.Effective}
	// Persist only the stable recovery state; draining candidate permits remain in memory.
	pure := map[string]policy.Policy{"stable": stable.raw["stable"]}
	env.PolicyJSON, _ = json.Marshal(pure)
	env.Checksum = Checksum(env)
	next := e.state
	next.Active = env
	next.ActiveAt = time.Now()
	next.Stable = env
	next.Fallbacks = append(append([]domain.RevisionID(nil), next.Fallbacks...), env.Revision)
	if capacity != nil {
		ceiling := limits.Effective
		next.SafetyCeiling = &ceiling
	}
	persistenceErr := e.persist(next)
	if persistenceErr != nil {
		e.auditGap = true
	}
	if err := e.s.Install(scheduler.Snapshot{Limits: limits, Policies: stable.policies}); err != nil {
		e.auditGap = true
		return err
	}
	e.state = next
	stable.activation = env.Revision
	e.runtime.Store(stable)
	if e.timer != nil {
		e.timer.Stop()
	}
	return persistenceErr
}
func reserve(p *policy.CompiledPolicy, capacity int) int {
	if capacity == 0 {
		return 0
	}
	return min(int(p.ReserveFraction()*float64(capacity)), capacity-1)
}

func (e *Engine) isPromotion(next *runtimeSnapshot) bool {
	prior := e.runtime.Load()
	return prior != nil && prior.fraction == 1 && prior.policies["candidate"] != nil && len(next.policies) == 1 && next.policies["stable"] == prior.policies["candidate"]
}

// Route consumes only an ingress-verified identity. PostgreSQL supplies a random
// server session ID; HTTP must supply a verified tenant/subject. The key is local
// and secret, and never crosses the control socket or model boundary.
func (e *Engine) Route(ctx domain.RequestContext, identity string) domain.Classification {
	r := e.runtime.Load()
	if r == nil || !e.ready.Load() || identity == "" || len(identity) > 1024 {
		return domain.Classification{Blocked: true}
	}
	cohort := "stable"
	if r.fraction > 0 {
		mac := hmac.New(sha256.New, e.cfg.CohortKey)
		for _, v := range []string{ctx.Target, string(ctx.Path), ctx.Principal, identity} {
			var n [8]byte
			binary.BigEndian.PutUint64(n[:], uint64(len(v)))
			mac.Write(n[:])
			mac.Write([]byte(v))
		}
		sum := mac.Sum(nil)
		value := float64(binary.BigEndian.Uint64(sum[:8])>>11) / float64(uint64(1)<<53)
		if value < r.fraction {
			cohort = "candidate"
		}
	}
	c := policy.Classify(r.policies[cohort], ctx)
	c.ActivationRevision = r.activation
	c.Cohort = cohort
	return c
}

// RouteFallback preserves bounded anonymous/unverifiable REST admission when a
// full rollout has closed the stable cohort. It never accepts claimed principal
// or tenant identity and therefore cannot select an identity-specific priority.
func (e *Engine) RouteFallback(ctx domain.RequestContext) domain.Classification {
	r := e.runtime.Load()
	if r == nil || !e.ready.Load() {
		return domain.Classification{Blocked: true}
	}
	ctx.Principal = ""
	ctx.Tenant = ""
	ctx.TrustedTenant = false
	cohort := "stable"
	if r.fraction == 1 {
		cohort = "candidate"
	}
	c := policy.Classify(r.policies[cohort], ctx)
	c.ActivationRevision = r.activation
	c.Cohort = cohort
	return c
}
