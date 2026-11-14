package telemetry

import (
	"pooltune/internal/domain"
	"sync"
	"testing"
	"time"
)

func TestWindowsFailuresAndOccupancy(t *testing.T) {
	now := time.Unix(100, 0)
	clock := func() time.Time { return now }
	id := Identity{Target: "db", Path: domain.PathPostgres, Capacity: "c", Policy: "p", Guardrail: "g", Epoch: 1}
	r, err := New(id, []string{"web"}, clock)
	if err != nil {
		t.Fatal(err)
	}
	r.Record("web", Arrival, 0)
	r.SetInFlight(2)
	now = now.Add(2 * time.Second)
	r.Record("web", Rejected, 0)
	r.Record("web", Timeout, time.Second)
	r.SetInFlight(1)
	now = now.Add(2 * time.Second)
	r.Record("web", Arrival, 0)
	r.Record("web", Success, 20*time.Millisecond)
	now = now.Add(time.Second)
	r.ObserveRuntime(map[string]Runtime{"web": {Active: 1, Uncertain: 1, Queued: 2, OldestActive: 5 * time.Second, OldestQueued: time.Second}})
	s := r.Snapshot()
	w := s.Groups[0]
	if !s.Complete || s.MeanInFlight != 1.4 || w.Arrivals1 != 1 || w.Arrivals5 != 2 || w.Terminals != 3 || w.OverloadFailures != 1 || w.Rejected != 1 || w.Successes != 1 || w.Queued != 2 || w.OldestActive != 5*time.Second {
		t.Fatalf("snapshot %+v group %+v", s, w)
	}
	if w.ServiceSeconds != .02 || w.ServiceHistogram[5] != 1 {
		t.Fatalf("latency histogram %+v", w)
	}
}

func TestBoundedUnknownAndCoverage(t *testing.T) {
	now := time.Unix(100, 0)
	r, _ := New(Identity{Target: "d", Path: domain.PathREST, Capacity: "c", Policy: "p", Guardrail: "g", Epoch: 1}, []string{"web"}, func() time.Time { return now })
	for i := 0; i < 100; i++ {
		r.Record("unregistered", Arrival, 0)
	}
	now = now.Add(5 * time.Second)
	r.ObserveRuntime(map[string]Runtime{})
	s := r.Snapshot()
	if len(s.Groups) != 2 || s.Unknown != 100 || s.Groups[1].Arrivals5 != 100 {
		t.Fatalf("unbounded/lost unknown %+v", s)
	}
	r.Omit()
	if r.Snapshot().Omitted != 1 {
		t.Fatal("lost coverage hidden")
	}
	now = now.Add(-time.Second)
	r.Record("web", Arrival, 0)
	if r.Snapshot().Complete {
		t.Fatal("clock reversal accepted")
	}
}

func TestEpochAndPartialWindows(t *testing.T) {
	now := time.Unix(100, 500000000)
	id := Identity{Target: "d", Path: domain.PathREST, Capacity: "c", Policy: "p", Guardrail: "g", Epoch: 2}
	r, _ := New(id, []string{"w"}, func() time.Time { return now })
	r.Record("w", Arrival, 0)
	now = time.Unix(105, 0)
	if r.Snapshot().Complete {
		t.Fatal("partial five-second window called complete")
	}
	now = time.Unix(106, 0)
	r.ObserveRuntime(map[string]Runtime{})
	s := r.Snapshot()
	if !s.Complete || s.Identity.Epoch != 2 || s.Groups[0].Arrivals5 != 0 {
		t.Fatal("epoch/window mixed")
	}
}

func TestConcurrentCounters(t *testing.T) {
	start := time.Unix(100, 0)
	now := start
	r, _ := New(Identity{Target: "d", Path: domain.PathREST, Capacity: "c", Policy: "p", Guardrail: "g", Epoch: 1}, []string{"w"}, func() time.Time { return now })
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Record("w", Arrival, 0)
			}
		}()
	}
	wg.Wait()
	now = now.Add(5 * time.Second)
	r.ObserveRuntime(map[string]Runtime{})
	if r.Snapshot().Groups[0].Arrivals5 != 1000 {
		t.Fatal("counter lost")
	}
}

func TestOverflowLossAndUncertainAreVisible(t *testing.T) {
	now := time.Unix(100, 0)
	r, _ := New(Identity{Target: "d", Path: domain.PathREST, Capacity: "c", Policy: "p", Guardrail: "g", Epoch: 1}, []string{"w"}, func() time.Time { return now })
	r.Record("w", Success, 20*time.Second)
	r.Record("w", Uncertain, 0)
	now = now.Add(5 * time.Second)
	r.ObserveRuntime(map[string]Runtime{})
	s := r.Snapshot()
	w := s.Groups[0]
	if w.ServiceHistogram[8] != 1 || w.UncertainOutcomes != 1 || w.Terminals != 1 {
		t.Fatal("overflow or unfinished outcome hidden")
	}
	r.Record("w", Success, -time.Second)
	if r.Snapshot().Complete || r.Snapshot().Omitted == 0 {
		t.Fatal("invalid duration became valid evidence")
	}
}

func TestRetainedOccupancyAfterLongIdle(t *testing.T) {
	now := time.Unix(100, 0)
	r, _ := New(Identity{Target: "d", Path: domain.PathREST, Capacity: "c", Policy: "p", Guardrail: "g", Epoch: 1}, nil, func() time.Time { return now })
	r.SetInFlight(3)
	now = now.Add(24 * time.Hour)
	r.ObserveRuntime(map[string]Runtime{"unknown": {Active: 3, OldestActive: 24 * time.Hour}})
	s := r.Snapshot()
	if s.MeanInFlight != 3 || s.Groups[0].Active != 3 || !s.Complete {
		t.Fatalf("idle integration %+v", s)
	}
}
