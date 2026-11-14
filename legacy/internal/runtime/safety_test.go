package runtime

import (
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"pooltune/internal/telemetry"
	"testing"
	"time"
)

func TestLocalPressureControllerNeverInventsGrowthEvidence(t *testing.T) {
	now := time.Now()
	g := controller.Guardrails{Approved: true, Version: "g1", Floor: 1, Ceiling: 20, Start: 5, CPU: controller.Thresholds{Grow: .4, Backoff: .7, Stop: .9}, Memory: controller.Thresholds{Grow: .4, Backoff: .7, Stop: .9}, MaxAge: 2 * time.Second, ErrorLimit: .1, WarmRatio: .1}
	s := controller.State{Identity: telemetry.Identity{Target: "db", Path: domain.PathPostgres, Policy: "p1", Capacity: "c1", Guardrail: "g1", Epoch: 1}, Commanded: 20, Effective: 20, LastSafe: 20, LastReady: 20, StartupVerified: true, StoreHealthy: true}
	for _, tc := range []struct {
		cpu    float64
		at     time.Time
		source string
		want   int
	}{{.1, now, "local-host", 20}, {.75, now, "local-host", 16}, {.95, now, "local-host", 0}, {.95, now.Add(-time.Minute), "local-host", 20}, {.95, now, "managed-server", 20}} {
		d := localPressureDecision(now, s, domain.Resources{CPU: tc.cpu, Memory: .1, At: tc.at, Source: tc.source}, g)
		if d.Err != "" || d.Effective != tc.want || d.PhysicalDesired != nil {
			t.Fatalf("pressure %.2f: effective=%d err=%s", tc.cpu, d.Effective, d.Err)
		}
	}
}
