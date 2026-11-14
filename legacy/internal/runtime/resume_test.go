package runtime

import (
	"errors"
	"pooltune/internal/control"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"sync/atomic"
	"testing"
	"time"
)

func TestResumeHealthRequiresContinuousFreshWindow(t *testing.T) {
	now := time.Now()
	zero := 0
	v := control.StateView{Ready: true, Epoch: 1, SafetyCeiling: &zero, Active: domain.ActivationEnvelope{Checksum: "stopped", Limits: []domain.LimitSnapshot{{Effective: 0}}}}
	g := controller.Guardrails{Start: 2, MaxAge: 2 * time.Second, CPU: controller.Thresholds{Grow: .6}, Memory: controller.Thresholds{Grow: .6}}
	tracker := &resumeTracker{}
	sample := func(at time.Time, healthy bool) {
		tracker.observe(at, v, domain.Resources{Source: "local-host", At: at, CPU: .2, Memory: .2}, &g, 4, healthy)
	}
	for i := 0; i <= 5; i++ {
		sample(now.Add(time.Duration(i)*time.Second), true)
	}
	p := tracker.snapshot()
	if p.Capacity != 2 || p.ObservedAt.Sub(p.HealthySince) != 5*time.Second {
		t.Fatal("healthy window not accumulated")
	}
	sample(now.Add(6*time.Second), false)
	if tracker.snapshot().Capacity != 0 {
		t.Fatal("backend failure retained resume proof")
	}
	sample(now.Add(7*time.Second), true)
	sample(now.Add(10*time.Second), true)
	if !tracker.snapshot().HealthySince.Equal(now.Add(10 * time.Second)) {
		t.Fatal("sampling gap accepted")
	}
	v.Active.Checksum = "new-stop"
	sample(now.Add(11*time.Second), true)
	if !tracker.snapshot().HealthySince.Equal(now.Add(11 * time.Second)) {
		t.Fatal("old revision evidence reused")
	}
}

func TestFailedHostSampleInvalidatesResumeImmediately(t *testing.T) {
	for _, path := range []string{"postgres", "rest"} {
		t.Run(path, func(t *testing.T) {
			now := time.Now()
			proof := control.ResumeEvidence{Checksum: "stopped", Epoch: 1, Capacity: 2, HealthySince: now.Add(-6 * time.Second), ObservedAt: now}
			tracker := &resumeTracker{proof: proof}
			if path == "postgres" {
				traffic := &Traffic{resume: tracker, resources: &atomic.Pointer[domain.Resources]{}, capacityMetrics: &atomic.Pointer[controller.MetricsSnapshot]{}}
				traffic.recordHostSample(t.Context(), domain.Resources{}, errors.New("monitor failed"))
			} else {
				traffic := &RESTTraffic{}
				traffic.resume.proof = proof
				traffic.recordHostSample(t.Context(), domain.Resources{}, errors.New("monitor failed"))
				tracker = &traffic.resume
			}
			if tracker.snapshot().Capacity != 0 {
				t.Fatal("failed sample retained healthy resume proof")
			}
			zero := 0
			view := control.StateView{Ready: true, Epoch: 1, SafetyCeiling: &zero, Active: domain.ActivationEnvelope{Checksum: "stopped", Limits: []domain.LimitSnapshot{{Effective: 0}}}}
			g := controller.Guardrails{Start: 2, MaxAge: 2 * time.Second, CPU: controller.Thresholds{Grow: .6}, Memory: controller.Thresholds{Grow: .6}}
			later := now.Add(time.Second)
			tracker.observe(later, view, domain.Resources{Source: "local-host", At: later, CPU: .2, Memory: .2}, &g, 2, true)
			if !tracker.snapshot().HealthySince.Equal(later) {
				t.Fatal("failed sample did not reset five-second interval")
			}
		})
	}
}

func TestReducedCeilingRequiresEnoughVerifiedCapacity(t *testing.T) {
	now := time.Now()
	ceiling := 6
	v := control.StateView{Ready: true, Epoch: 1, SafetyCeiling: &ceiling, Active: domain.ActivationEnvelope{Checksum: "reduced", Limits: []domain.LimitSnapshot{{Effective: 6}}}}
	g := controller.Guardrails{Start: 2, Ceiling: 8, MaxAge: 2 * time.Second, CPU: controller.Thresholds{Grow: .6}, Memory: controller.Thresholds{Grow: .6}}
	tracker := &resumeTracker{}
	for i := 0; i <= 5; i++ {
		at := now.Add(time.Duration(i) * time.Second)
		tracker.observe(at, v, domain.Resources{Source: "local-host", At: at, CPU: .1, Memory: .1}, &g, 8, true)
	}
	if tracker.snapshot().Capacity != 6 {
		t.Fatal("nonzero recovery must preserve current admission, not restart at Start")
	}
	at := now.Add(6 * time.Second)
	tracker.observe(at, v, domain.Resources{Source: "local-host", At: at, CPU: .1, Memory: .1}, &g, 5, true)
	if tracker.snapshot().Capacity != 0 {
		t.Fatal("insufficient verified capacity released ceiling")
	}
}
