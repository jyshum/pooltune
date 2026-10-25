package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/telemetry"
	"strings"
	"time"
)

func CapacityChecksum(g controller.Guardrails) string {
	b, e := json.Marshal(g)
	if e != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type CapacityProfile struct {
	EvidenceSHA256 string
	Guardrails     controller.Guardrails
}

func (e *Engine) capacityMetrics() controller.MetricsSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cfg.CapacityMetrics == nil || !e.ready.Load() {
		return controller.MetricsSnapshot{}
	}
	m := e.cfg.CapacityMetrics()
	m.Local = e.s.CapacityMetrics()
	m.LastQueuedAt = e.s.Stats().LastQueuedAt
	return m
}
func (e *Engine) capacityRoutes(mux *http.ServeMux) {
	e.physicalRoutes(mux)
	mux.HandleFunc("GET /capacity", func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(e.capacityMetrics()) })
}
func (c *Client) CapacityMetrics(ctx context.Context) (controller.MetricsSnapshot, error) {
	var m controller.MetricsSnapshot
	err := c.call(ctx, "GET", "/capacity", nil, &m)
	return m, err
}
func capacityPlan(now time.Time, v StateView, m controller.MetricsSnapshot, g controller.Guardrails, frozen bool, quietSince ...time.Time) (controller.CapacityDecision, bool) {
	if m.AuthoritySHA256 != CapacityChecksum(g) || !v.Ready || v.AuditGap || !v.Drained || len(v.Active.Limits) != 1 || v.Active.CandidateFraction != 0 || frozen {
		return controller.CapacityDecision{}, false
	}
	l := v.Active.Limits[0]
	for _, co := range v.Cohorts {
		if co.Uncertain > 0 {
			return controller.CapacityDecision{}, false
		}
	}
	for _, group := range m.Local.Groups {
		if group.Uncertain > 0 {
			return controller.CapacityDecision{}, false
		}
	}
	state := controller.State{Identity: telemetry.Identity{Target: l.Target, Path: l.Path, Capacity: l.CapacityRevision, Policy: string(v.Active.Revision), Guardrail: l.GuardrailVersion, Epoch: v.Epoch}, Commanded: l.Effective, Effective: l.Effective, LastSafe: g.Ceiling, StartupVerified: true, StoreHealthy: true, EvaluationClear: true, LastChange: v.ActiveAt}
	if len(quietSince) > 0 {
		state.QuietSince = quietSince[0]
		state.LastQuietCut = v.ActiveAt
	}
	if m.Pool != nil {
		state.PhysicalDesired = m.Pool.Desired
		state.ProviderPending = m.Pool.Pending
	}
	d := controller.NextCapacity(now, state, m, g)
	return d, d.Err == ""
}

func capacityProposal(now time.Time, v StateView, m controller.MetricsSnapshot, g controller.Guardrails, frozen bool, quietSince ...time.Time) (domain.ActivationEnvelope, bool) {
	d, valid := capacityPlan(now, v, m, g, frozen, quietSince...)
	if !valid {
		return domain.ActivationEnvelope{}, false
	}
	l := v.Active.Limits[0]
	// Pressure reductions remain traffic-owned and durable. Quiet decreases
	// preserve the policy and drain existing work through ordinary activation.
	if v.SafetyCeiling != nil {
		d.Effective = min(d.Effective, *v.SafetyCeiling)
	}
	if (m.Pool != nil && m.Pool.Pending) || d.Err != "" || d.Effective == l.Effective || (d.Effective < l.Effective && (d.Reason != "quiet_reduction" || d.Effective != d.Planned)) {
		return domain.ActivationEnvelope{}, false
	}
	env := v.Active
	env.ExpectedPredecessor = env.Revision
	env.Revision = domain.RevisionID(fmt.Sprintf("capacity-%d-%d", v.Epoch, now.UnixNano()))
	var policies map[string]policy.Policy
	if json.Unmarshal(env.PolicyJSON, &policies) != nil || len(policies) != 1 {
		return domain.ActivationEnvelope{}, false
	}
	l.Revision = env.Revision
	l.CapacityRevision = string(env.Revision)
	l.Effective = d.Effective
	l.CohortBudgets = map[string]int{"stable": l.Effective}
	l.CriticalReserve = min(int(policies["stable"].ReserveFraction*float64(l.Effective)), l.Effective-1)
	env.Limits = []domain.LimitSnapshot{l}
	env.Checksum = Checksum(env)
	return env, true
}
func (s *Service) capacityTick(ctx context.Context, v StateView) (bool, error) {
	if s.cfg.Capacity == nil {
		return false, nil
	}
	state, err := s.view(ctx)
	if err != nil {
		return false, err
	}
	if state.Frozen || state.ExperimentActive {
		s.quiet = quietTracker{}
		return false, nil
	}
	pending, err := s.coordinator.ledger.Pending(s.coordinator.target)
	if err != nil {
		return false, ErrStorage
	}
	if len(pending) > 0 {
		return false, nil
	}
	client, ok := s.client.(interface {
		CapacityMetrics(context.Context) (controller.MetricsSnapshot, error)
	})
	if !ok {
		return false, ErrInvalid
	}
	m, err := client.CapacityMetrics(ctx)
	if err != nil {
		return false, err
	}
	now := time.Now()
	quiet := s.quiet.observe(now, v, m)
	env, grow := capacityProposal(now, v, m, s.cfg.Capacity.Guardrails, false, quiet)
	if !grow || env.Limits[0].Effective > v.Active.Limits[0].Effective {
		if changed, err := s.physicalTick(ctx, now, v, m, quiet); changed || err != nil {
			return changed, err
		}
	}
	if !grow {
		return false, nil
	}
	_, err = s.coordinator.Apply(ctx, env)
	s.evidence.Complete = false
	return true, err
}

// Resolve at most one capacity intent per tick before any elective work, even
// while frozen or after growth is disabled. Status queries never replay writes.
func (s *Service) reconcileCapacityIntent(ctx context.Context) (bool, error) {
	pending, err := s.coordinator.ledger.Pending(s.coordinator.target)
	if err != nil {
		return false, ErrStorage
	}
	for _, p := range pending {
		if strings.HasPrefix(p.ID, "capacity-") {
			_, err = s.coordinator.Reconcile(ctx, domain.RevisionID(p.ID))
			return true, err
		}
	}
	return false, nil
}

// Quiet credit is process-local and reset on restart, identity changes, gaps or
// incomplete evidence. The active revision time also bounds successive cuts.
type quietTracker struct {
	since, last time.Time
	identity    telemetry.Identity
}

func (q *quietTracker) observe(now time.Time, v StateView, m controller.MetricsSnapshot) time.Time {
	valid := v.Ready && !v.AuditGap && v.Drained && v.Active.CandidateFraction == 0 && len(v.Active.Limits) == 1 && m.Local.Complete && m.Local.Omitted == 0 && !m.Local.RuntimeAt.After(now) && now.Sub(m.Local.RuntimeAt) <= 2*time.Second && !m.Local.End.After(now) && now.Sub(m.Local.End) <= 2*time.Second
	if valid {
		l := v.Active.Limits[0]
		valid = m.Local.Identity == (telemetry.Identity{Target: l.Target, Path: l.Path, Capacity: l.CapacityRevision, Policy: string(v.Active.Revision), Guardrail: l.GuardrailVersion, Epoch: v.Epoch})
	}
	for _, g := range m.Local.Groups {
		if g.Queued != 0 || g.Uncertain != 0 {
			valid = false
		}
	}
	for _, c := range v.Cohorts {
		if c.Queued != 0 || c.Uncertain != 0 {
			valid = false
		}
	}
	if !valid {
		*q = quietTracker{}
		return time.Time{}
	}
	if q.since.IsZero() || q.identity != m.Local.Identity || now.Before(q.last) || now.Sub(q.last) > 2*time.Second {
		q.since = now
	}
	if !controller.ValidWindow(now, m.Local.Identity, v.ActiveAt, m.Local) {
		*q = quietTracker{}
		return time.Time{}
	}
	if m.LastQueuedAt.After(q.since) {
		q.since = m.LastQueuedAt
	}
	q.last = now
	q.identity = m.Local.Identity
	return q.since
}
