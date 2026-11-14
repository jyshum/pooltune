package burst

import (
	"pooltune/internal/provider"
	"testing"
	"time"
)

func TestRefreshFailureRetainsOnlyUnexpiredSourceEvidence(t *testing.T) {
	now := time.Now()
	p := provider.Pressure{At: now, Source: "fixture", CPU: .1, Memory: .1}
	r := &Runtime{pressure: p}
	r.recordPressure(provider.Pressure{}, provider.ErrAPI)
	if r.pressure != p {
		t.Fatal("failed refresh erased still-valid source evidence")
	}
	c, _ := NewController(Config{Target: "test", Base: 1, Ceiling: 2, Queue: 8, QueueWait: time.Second, PressureSource: "fixture", PressureMaxAge: time.Second}, nil)
	c.Update(Health{At: now, PressureAt: r.pressure.At, Source: r.pressure.Source, CPU: r.pressure.CPU, Memory: r.pressure.Memory, Warm: 2, BudgetVerified: true})
	if e := c.Resume(now); e != nil {
		t.Fatal(e)
	}
	c.Tick(now.Add(1500 * time.Millisecond))
	if c.Scheduler.Stats().Effective != 0 {
		t.Fatal("refresh failure extended source lifetime")
	}
}
