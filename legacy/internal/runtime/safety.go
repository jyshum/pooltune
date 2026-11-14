package runtime

import (
	"pooltune/internal/control"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"pooltune/internal/telemetry"
	"time"
)

// Traffic enforces pressure reductions independently of control availability.
// Elective PG admission growth uses separately collected ordered occupancy and
// verified backend evidence through the control service; physical resize is gated.
func localPressureDecision(now time.Time, s controller.State, r domain.Resources, g controller.Guardrails) controller.CapacityDecision {
	valid := r.Source == "local-host"
	m := controller.MetricsSnapshot{CPU: controller.Observation{Value: r.CPU, At: r.At, Source: r.Source, Valid: valid}, Memory: controller.Observation{Value: r.Memory, At: r.At, Source: r.Source, Valid: valid}}
	s.EvaluationClear = false
	return controller.NextCapacity(now, s, m, g)
}
func (t *Traffic) enforcePressure(now time.Time, r domain.Resources) {
	enforceLocalPressure(t.Engine, &t.capacityState, t.config.Capacity, t.config.CapacitySafety, now, r)
}
func (t *RESTTraffic) enforcePressure(now time.Time, r domain.Resources) {
	t.pressureMu.Lock()
	defer t.pressureMu.Unlock()
	enforceLocalPressure(t.Engine, &t.capacityState, t.cfg.Capacity, t.cfg.CapacitySafety, now, r)
}
func enforceLocalPressure(engine *control.Engine, state *controller.State, capacity int, guardrails *controller.Guardrails, now time.Time, r domain.Resources) {
	if guardrails == nil {
		return
	}
	v := engine.View()
	if !v.Ready || len(v.Active.Limits) != 1 {
		return
	}
	l := v.Active.Limits[0]
	// The engine owns the durable latch and only verified owner resume can clear
	// it. Do not reapply an old in-memory stop after that transition. Preserve
	// conservative local state while persistence is uncertain.
	if !v.AuditGap {
		state.SafetyStopped = v.SafetyCeiling != nil && *v.SafetyCeiling == 0
	}
	state.Identity = telemetry.Identity{Target: l.Target, Path: l.Path, Epoch: v.Epoch, Guardrail: l.GuardrailVersion, Policy: string(v.Active.Revision), Capacity: l.CapacityRevision}
	state.Commanded = l.Effective
	state.Effective = l.Effective
	state.LastSafe = capacity
	state.LastReady = capacity
	state.StartupVerified = true
	state.StoreHealthy = !v.AuditGap
	state.Canary = v.Active.CandidateFraction > 0
	d := localPressureDecision(now, *state, r, *guardrails)
	if d.Err != "" {
		return
	}
	if d.Effective < l.Effective {
		// Constrain applies during storage failure and exposes the audit gap.
		_ = engine.Constrain(d.Effective)
		if d.Reason == "backoff" {
			state.LastBackoff = now
		}
	}
	state.SafetyStopped = d.SafetyStopped
	state.LastTick = now
}
