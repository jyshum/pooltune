package runtime

import (
	"context"
	"math"
	"pooltune/internal/control"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"sync"
	"time"
)

type resumeTracker struct {
	mu    sync.Mutex
	proof control.ResumeEvidence
}

func (r *resumeTracker) snapshot() control.ResumeEvidence {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.proof
}
func (r *resumeTracker) observe(now time.Time, v control.StateView, resources domain.Resources, g *controller.Guardrails, capacity int, backendHealthy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	healthy := g != nil && backendHealthy && capacity > 0 && v.Ready && !v.AuditGap && v.SafetyCeiling != nil && len(v.Active.Limits) == 1 && v.Active.CandidateFraction == 0 && resources.Source == "local-host" && !resources.At.IsZero() && !resources.At.After(now)
	if g != nil {
		healthy = healthy && now.Sub(resources.At) <= min(g.MaxAge, 2*time.Second) && !math.IsNaN(resources.CPU) && !math.IsNaN(resources.Memory) && resources.CPU >= 0 && resources.CPU < g.CPU.Grow && resources.Memory >= 0 && resources.Memory < g.Memory.Grow
	}
	for _, co := range v.Cohorts {
		healthy = healthy && co.Active == 0 && co.Uncertain == 0
	}
	if !healthy {
		r.proof = control.ResumeEvidence{}
		return
	}
	target := min(capacity, g.Start)
	if current := v.Active.Limits[0].Effective; current > 0 {
		if capacity < current || current > g.Ceiling {
			r.proof = control.ResumeEvidence{}
			return
		}
		target = current
	}
	since := r.proof.HealthySince
	if r.proof.Checksum != v.Active.Checksum || r.proof.Epoch != v.Epoch || now.Before(r.proof.ObservedAt) || now.Sub(r.proof.ObservedAt) > 2*time.Second {
		since = now
	}
	r.proof = control.ResumeEvidence{Checksum: v.Active.Checksum, Epoch: v.Epoch, Capacity: target, HealthySince: since, ObservedAt: now}
}

func (t *Traffic) recordHostSample(ctx context.Context, r domain.Resources, err error) {
	if err != nil {
		t.resume.invalidate()
		t.resources.Store(nil)
		t.capacityMetrics.Store(nil)
		return
	}
	t.resources.Store(&r)
	t.enforcePressure(time.Now(), r)
	t.sampleCapacity(ctx, r)
}
func (t *RESTTraffic) recordHostSample(ctx context.Context, r domain.Resources, err error) {
	if err != nil {
		t.resume.invalidate()
		t.resources.Store(nil)
		t.capacityMetrics.Store(nil)
		return
	}
	t.resources.Store(&r)
	t.enforcePressure(time.Now(), r)
	t.sampleCapacity(ctx, r)
}

func (r *resumeTracker) invalidate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.proof = control.ResumeEvidence{}
}
