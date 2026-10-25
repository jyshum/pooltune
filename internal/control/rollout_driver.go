package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/rollout"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type RolloutClient interface {
	TrafficClient
	View(context.Context) (StateView, error)
	Rollback(context.Context, domain.RevisionID) error
}

// RolloutDriver bridges the slow manager to durable activation. Its pending
// command is not resumed after restart: the manager's journal requires abort.
// A distinct drain revision routes new work before retiring the stable budget.
type RolloutDriver struct {
	frozen         atomic.Bool
	windowDuration time.Duration
	resourceSource string
	mu             sync.Mutex
	target         string
	path           domain.PathKind
	coordinator    *Coordinator
	client         RolloutClient
	metrics        func(context.Context, rollout.Experiment) (rollout.MetricsSnapshot, error)
	command        *rollout.Command
	final          domain.ActivationEnvelope
	draining       bool
	activatedAt    time.Time
}

func NewRolloutDriver(target string, path domain.PathKind, c *Coordinator, client RolloutClient, metrics func(context.Context, rollout.Experiment) (rollout.MetricsSnapshot, error)) (*RolloutDriver, error) {
	if c == nil || client == nil || c.owner != target || c.path != path {
		return nil, ErrInvalid
	}
	return &RolloutDriver{target: target, path: path, coordinator: c, client: client, metrics: metrics}, nil
}
func (d *RolloutDriver) Inspect(ctx context.Context, path domain.PathKind) (rollout.Runtime, error) {
	if path != d.path {
		return rollout.Runtime{}, ErrInvalid
	}
	v, err := d.client.View(ctx)
	if err != nil {
		return rollout.Runtime{}, err
	}
	if len(v.Active.Limits) != 1 || v.Active.Checksum != Checksum(v.Active) {
		return rollout.Runtime{}, ErrInvalid
	}
	l := v.Active.Limits[0]
	if l.Target != d.target || l.Path != d.path {
		return rollout.Runtime{}, ErrInvalid
	}
	var raw map[string]policy.Policy
	if decode(v.Active.PolicyJSON, &raw) != nil {
		return rollout.Runtime{}, ErrInvalid
	}
	healthy := v.Ready
	for _, co := range v.Cohorts {
		if co.Uncertain > 0 {
			healthy = false
		}
	}
	return rollout.Runtime{ActivationRevision: string(v.Active.Revision), PolicyRevision: string(raw["stable"].Revision), GuardrailVersion: l.GuardrailVersion, CapacityRevision: l.CapacityRevision, Epoch: v.Epoch, Effective: l.Effective, Healthy: healthy, Frozen: d.frozen.Load(), AuditGap: v.AuditGap}, nil
}
func experimentPrefix(id string) string {
	s := sha256.Sum256([]byte(id))
	return "rollout-" + hex.EncodeToString(s[:8]) + "-"
}
func (d *RolloutDriver) Apply(ctx context.Context, c rollout.Command) (rollout.Ack, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.command != nil && d.command.Revision == c.Revision {
		a, _ := json.Marshal(d.command)
		b, _ := json.Marshal(c)
		if string(a) != string(b) {
			return rollout.Ack{}, ErrStale
		}
		return d.status(ctx, c.Revision)
	}
	if c.Path != d.path || !revisionPattern.MatchString(c.Revision) || !strings.HasPrefix(c.Revision, experimentPrefix(c.ExperimentID)) || len(c.Revision) > 110 || c.ExperimentID == "" || c.Effective < 1 || c.CandidateBudget < 2 || c.StableBudget < 0 || c.StableBudget+c.CandidateBudget != c.Effective || !c.Expires.After(time.Now()) {
		return rollout.Ack{}, ErrInvalid
	}
	if c.Fraction != .05 && c.Fraction != .1 && c.Fraction != .25 && c.Fraction != .5 && c.Fraction != 1 {
		return rollout.Ack{}, ErrInvalid
	}
	if int(math.Floor(c.Fraction*float64(c.Effective))) != c.CandidateBudget {
		return rollout.Ack{}, ErrInvalid
	}
	if c.Phase != rollout.Partitioning && c.Phase != rollout.Measuring && c.Phase != rollout.Observing && c.Phase != rollout.Accepted {
		return rollout.Ack{}, ErrInvalid
	}
	v, err := d.client.View(ctx)
	if err != nil {
		return rollout.Ack{}, err
	}
	if !v.Ready || v.AuditGap || v.Active.Revision != domain.RevisionID(c.ExpectedRevision) || v.Epoch != c.Epoch || v.Active.GuardrailVersion != c.GuardrailVersion || len(v.Active.Limits) != 1 {
		return rollout.Ack{}, ErrStale
	}
	l := v.Active.Limits[0]
	if l.Effective != c.Effective || l.CapacityRevision != c.CapacityRevision || l.Target != d.target || l.Path != d.path {
		return rollout.Ack{}, ErrStale
	}
	var current map[string]policy.Policy
	if decode(v.Active.PolicyJSON, &current) != nil {
		return rollout.Ack{}, ErrInvalid
	}
	a, _ := json.Marshal(current["stable"])
	b, _ := json.Marshal(c.StablePolicy)
	if string(a) != string(b) {
		return rollout.Ack{}, ErrStale
	}
	raw := map[string]policy.Policy{"stable": c.StablePolicy, "candidate": c.CandidatePolicy}
	fraction := c.Fraction
	expires := c.Expires
	budgets := map[string]int{"stable": c.StableBudget, "candidate": c.CandidateBudget}
	if c.Phase == rollout.Accepted {
		before, _ := json.Marshal(current["candidate"])
		after, _ := json.Marshal(c.CandidatePolicy)
		if v.Active.CandidateFraction != 1 || string(before) != string(after) || !v.Drained {
			return rollout.Ack{}, ErrStale
		}
		raw = map[string]policy.Policy{"stable": c.CandidatePolicy}
		fraction = 0
		expires = time.Time{}
		budgets = map[string]int{"stable": c.Effective}
	}
	l.Revision = domain.RevisionID(c.Revision)
	l.CohortBudgets = budgets
	l.CriticalReserve = 0
	for cohort, budget := range budgets {
		if budget > 0 {
			l.CriticalReserve += min(int(raw[cohort].ReserveFraction*float64(budget)), budget-1)
		}
	}
	data, _ := json.Marshal(raw)
	env := domain.ActivationEnvelope{Revision: domain.RevisionID(c.Revision), ExpectedPredecessor: domain.RevisionID(c.ExpectedRevision), Epoch: c.Epoch, SchemaVersion: "1", GuardrailVersion: c.GuardrailVersion, PolicyJSON: data, Limits: []domain.LimitSnapshot{l}, LeaseExpiresAt: expires, CandidateFraction: fraction}
	env.Checksum = Checksum(env)
	owned, _ := json.Marshal(c)
	var copy rollout.Command
	json.Unmarshal(owned, &copy)
	d.command = &copy
	d.final = env
	d.activatedAt = time.Time{}
	d.draining = c.Fraction == 1 && c.Phase != rollout.Accepted && v.Active.CandidateFraction != 1
	if d.draining {
		drain := v.Active
		drain.ExpectedPredecessor = drain.Revision
		drain.Revision = domain.RevisionID(c.Revision + ".drain")
		drain.CandidateFraction = 1
		drain.LeaseExpiresAt = c.Expires
		drain.Limits[0].Revision = drain.Revision
		drain.Checksum = Checksum(drain)
		d.final.ExpectedPredecessor = drain.Revision
		d.final.Checksum = Checksum(d.final)
		if _, err = d.coordinator.Apply(ctx, drain); err != nil {
			return rollout.Ack{}, err
		}
	} else if _, err = d.coordinator.Apply(ctx, env); err != nil {
		return rollout.Ack{}, err
	}
	return d.status(ctx, c.Revision)
}
func (d *RolloutDriver) Status(ctx context.Context, revision string) (rollout.Ack, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.status(ctx, revision)
}
func (d *RolloutDriver) status(ctx context.Context, revision string) (rollout.Ack, error) {
	if d.command == nil {
		// On controller restart, only a durable stable-only acceptance can be
		// resolved without replay. All leased experiments are aborted instead.
		v, err := d.client.View(ctx)
		if err != nil {
			return rollout.Ack{}, err
		}
		var policies map[string]policy.Policy
		if !v.Ready || v.AuditGap || string(v.Active.Revision) != revision || v.ActiveAt.IsZero() || !v.Active.LeaseExpiresAt.IsZero() || v.Active.CandidateFraction != 0 || decode(v.Active.PolicyJSON, &policies) != nil || len(policies) != 1 {
			return rollout.Ack{}, ErrStale
		}
		ack, err := d.coordinator.Reconcile(ctx, domain.RevisionID(revision))
		if err != nil {
			return rollout.Ack{}, err
		}
		return rollout.Ack{Revision: revision, Active: ack.State == "active" && v.Drained, At: v.ActiveAt}, nil
	}
	if d.command.Revision != revision {
		return rollout.Ack{}, ErrStale
	}
	result := rollout.Ack{Revision: revision}
	v, err := d.client.View(ctx)
	if err != nil {
		return result, err
	}
	if !v.Ready || v.AuditGap || v.Epoch != d.command.Epoch {
		return result, ErrRecovery
	}
	expected := domain.RevisionID(revision)
	if d.draining {
		expected = domain.RevisionID(revision + ".drain")
		if v.Active.Revision != expected {
			return result, ErrStale
		}
		if _, err = d.coordinator.Reconcile(ctx, expected); err != nil {
			return result, err
		}
		old := v.Cohorts["stable"]
		if old.Active > 0 || old.Queued > 0 || old.Uncertain > 0 {
			return result, nil
		}
		// Persist a new final intent, never replay a previous unknown activation.
		d.draining = false
		if _, err = d.coordinator.Apply(ctx, d.final); err != nil {
			return result, err
		}
		v, err = d.client.View(ctx)
		if err != nil {
			return result, err
		}
	}
	if v.Active.Revision != domain.RevisionID(revision) {
		return result, ErrStale
	}
	ack, err := d.coordinator.Reconcile(ctx, domain.RevisionID(revision))
	if err != nil {
		return result, err
	}
	if ack.State != "active" || !v.Drained {
		return result, nil
	}
	if d.activatedAt.IsZero() {
		d.activatedAt = time.Now()
		if d.command.Phase == rollout.Accepted {
			d.activatedAt = v.ActiveAt
		}
	}
	result.Active = true
	result.At = d.activatedAt
	return result, nil
}
func (d *RolloutDriver) Abort(ctx context.Context, id, reason string) (rollout.Ack, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.client.View(ctx)
	if err != nil {
		return rollout.Ack{}, err
	}
	if strings.HasPrefix(string(v.Active.Revision), experimentPrefix(id)) {
		if err = d.client.Rollback(ctx, v.Active.Revision); err != nil {
			return rollout.Ack{}, err
		}
		v, err = d.client.View(ctx)
		if err != nil {
			return rollout.Ack{}, err
		}
	}
	// A later human edit is left untouched. An observed fallback is reconciled
	// without inventing confirmation for an earlier unknown activation.
	if strings.HasPrefix(string(v.Active.Revision), "fallback-") || strings.HasPrefix(string(v.Active.Revision), "recovery-") {
		if _, err = d.coordinator.SyncFallback(ctx); err != nil {
			return rollout.Ack{}, err
		}
	}
	if !v.Ready {
		return rollout.Ack{}, ErrRecovery
	}
	return rollout.Ack{Revision: string(v.Active.Revision), Active: true, At: time.Now()}, nil
}

// ConfigureMeasurements binds the calibrated window and resource source before
// any experiment. The local profile uses local-host; managed sources must supply
// their own verified observer rather than reusing host readings.
func (d *RolloutDriver) ConfigureMeasurements(window time.Duration, source string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.command != nil || window <= 0 || window > time.Hour || source == "" || len(source) > 128 {
		return ErrInvalid
	}
	d.windowDuration = window
	d.resourceSource = source
	return nil
}
func (d *RolloutDriver) Metrics(ctx context.Context, e rollout.Experiment) (rollout.MetricsSnapshot, error) {
	if d.metrics != nil {
		return d.metrics(ctx, e)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	client, ok := d.client.(interface {
		BeginWindow(context.Context, WindowRequest) error
		ReadWindow(context.Context) (WindowSnapshot, error)
	})
	if !ok || d.windowDuration == 0 || d.command == nil || d.command.ExperimentID != e.ID {
		return rollout.MetricsSnapshot{}, errors.New("measured rollout window unavailable")
	}
	revision := domain.RevisionID(d.command.Revision)
	v, err := d.client.View(ctx)
	if err != nil || v.Active.Revision != revision || !v.Ready {
		return rollout.MetricsSnapshot{}, ErrStale
	}
	sample, err := client.ReadWindow(ctx)
	if err != nil || sample.Revision != revision {
		if err = client.BeginWindow(ctx, WindowRequest{Revision: revision, Duration: d.windowDuration}); err != nil {
			return rollout.MetricsSnapshot{}, err
		}
		sample, err = client.ReadWindow(ctx)
		if err != nil {
			return rollout.MetricsSnapshot{}, err
		}
	}
	if !sample.Window.End.IsZero() && !e.LastWindowEnd.IsZero() && !sample.Window.End.After(e.LastWindowEnd) {
		if err = client.BeginWindow(ctx, WindowRequest{Revision: revision, Duration: d.windowDuration, PreviousEnd: sample.Window.End}); err != nil {
			return rollout.MetricsSnapshot{}, err
		}
		sample, err = client.ReadWindow(ctx)
		if err != nil {
			return rollout.MetricsSnapshot{}, err
		}
	}
	if sample.Revision != revision {
		return rollout.MetricsSnapshot{}, ErrStale
	}
	resources := sample.Resources
	if resources.Source != d.resourceSource {
		resources = domain.Resources{CPU: -1, Memory: -1}
	}
	return rollout.MetricsSnapshot{ExperimentID: e.ID, Target: d.target, Path: d.path, Epoch: v.Epoch, StableRevision: e.StableRevision, CandidateRevision: e.CandidateRevision, GuardrailVersion: v.Active.GuardrailVersion, CapacityRevision: v.Active.Limits[0].CapacityRevision, Now: time.Now(), At: sample.At, ResourceAt: resources.At, CPU: resources.CPU, Memory: resources.Memory, AuditGap: v.AuditGap, Live: sample.Live, Window: sample.Window}, nil
}
func (c *Client) Rollback(ctx context.Context, r domain.RevisionID) error {
	return c.call(ctx, "POST", "/rollback", struct{ Revision domain.RevisionID }{r}, nil)
}
