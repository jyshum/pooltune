package burst

import (
	"context"
	"testing"
	"time"
)

func TestWarmReserveReleasesQueuedTransactionOnFirstTick(t *testing.T) {
	c, e := NewController(Config{Target: "test", Base: 1, Ceiling: 2, Queue: 8, QueueWait: time.Second, PressureSource: "supabase:test", PressureMaxAge: 75 * time.Second}, nil)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	c.Update(Health{At: now, PressureAt: now, Source: "supabase:test", CPU: .1, Memory: .2, Warm: 2, BudgetVerified: true})
	if e = c.Resume(now); e != nil {
		t.Fatal(e)
	}
	first, e := c.Scheduler.Acquire(context.Background(), c.Classify("u", "d", "one"))
	if e != nil {
		t.Fatal(e)
	}
	acquired := make(chan bool, 1)
	go func() {
		p, e := c.Scheduler.Acquire(context.Background(), c.Classify("u", "d", "two"))
		if e == nil {
			c.Scheduler.Release(p, 0)
		}
		acquired <- e == nil
	}()
	deadline := time.Now().Add(time.Second)
	for c.Scheduler.Stats().Queued != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	c.Tick(time.Now())
	select {
	case ok := <-acquired:
		if !ok {
			t.Fatal("queued transaction rejected")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("reserve waited for a management API or demand window")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("slow reserve release")
	}
	c.Scheduler.Release(first, 0)
}
func TestStaleHealthStopsAdmissionWithoutReleasingActiveTransaction(t *testing.T) {
	c, _ := NewController(Config{Target: "test", Base: 1, Ceiling: 2, Queue: 8, QueueWait: time.Second, PressureSource: "supabase:test", PressureMaxAge: 75 * time.Second}, nil)
	now := time.Now()
	c.Update(Health{At: now, PressureAt: now, Source: "supabase:test", CPU: .1, Memory: .2, Warm: 2, BudgetVerified: true})
	c.Resume(now)
	p, e := c.Scheduler.Acquire(context.Background(), c.Classify("u", "d", "one"))
	if e != nil {
		t.Fatal(e)
	}
	c.Tick(now.Add(3 * time.Second))
	s := c.Scheduler.Stats()
	if s.Effective != 0 || s.Active != 1 {
		t.Fatalf("stale health must stop new admission and retain active work: %+v", s)
	}
	c.Scheduler.Release(p, 0)
}

func TestUnsafeHealthAndUncertaintyNeverReleaseWarmReserve(t *testing.T) {
	for _, kind := range []string{"source", "warm", "pressure", "uncertainty", "persistence"} {
		t.Run(kind, func(t *testing.T) {
			fail := false
			c, _ := NewController(Config{Target: "test", Base: 1, Ceiling: 2, Queue: 8, QueueWait: time.Second, PressureSource: "source", PressureMaxAge: time.Minute}, func(State) error {
				if fail {
					return ErrUnsafe
				}
				return nil
			})
			now := time.Now()
			h := Health{At: now, PressureAt: now, Source: "source", CPU: .1, Memory: .1, Warm: 2, BudgetVerified: true}
			c.Update(h)
			if e := c.Resume(now); e != nil {
				t.Fatal(e)
			}
			p, e := c.Scheduler.Acquire(context.Background(), c.Classify("u", "d", "one"))
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "source":
				h.Source = "wrong"
			case "warm":
				h.Warm = 1
			case "pressure":
				h.CPU = .9
			case "uncertainty":
				c.Scheduler.Release(p, 1)
			case "persistence":
				fail = true
				h.CPU = .9
			}
			c.Update(h)
			c.Tick(time.Now())
			if c.Scheduler.Stats().Effective != 0 {
				t.Fatal("unsafe capacity enabled")
			}
			if kind == "uncertainty" {
				if c.Scheduler.Stats().Uncertain != 1 {
					t.Fatal("unknown reservation lost")
				}
			} else {
				if c.Scheduler.Stats().Active != 1 {
					t.Fatal("active reservation lost")
				}
				c.Scheduler.Release(p, 0)
			}
		})
	}
}

func TestGoogleDelayedPressureDoesNotStopVerifiedWarmCapacity(t *testing.T) {
	now := time.Now()
	cfg := Config{Target: "google", Base: 1, Ceiling: 2, Queue: 8, QueueWait: time.Second, PressureSource: "google:project:instance", PressureMaxAge: 240 * time.Second}
	c, e := NewController(cfg, nil)
	if e != nil {
		t.Fatal(e)
	}
	h := Health{At: now, PressureAt: now.Add(-150 * time.Second), Source: cfg.PressureSource, CPU: .1, Memory: .1, Warm: 2, BudgetVerified: true}
	c.Update(h)
	if e = c.Resume(now); e != nil {
		t.Fatal("normal Google source delay froze startup", e)
	}
	for sec := 0; sec <= 60; sec++ {
		at := now.Add(time.Duration(sec) * time.Second)
		h.At = at
		c.Update(h)
		c.Tick(at)
		if c.Scheduler.Stats().Effective != 1 {
			t.Fatalf("valid delayed source stopped traffic at %ds", sec)
		}
	}
	at := now.Add(91 * time.Second)
	h.At = at
	c.Update(h)
	c.Tick(at)
	if c.Scheduler.Stats().Effective != 0 {
		t.Fatal("expired source was accepted")
	}
	cfg.PressureSource = "azure:resource"
	if _, e = NewController(cfg, nil); e == nil {
		t.Fatal("Google allowance leaked to other providers")
	}
}

func TestAzurePublicationAllowanceStillStopsOnExpiry(t *testing.T) {
	now := time.Now()
	cfg := Config{Target: "azure", Base: 1, Ceiling: 2, Queue: 8, QueueWait: time.Second, PressureSource: "azure:resource", PressureMaxAge: 135 * time.Second}
	c, e := NewController(cfg, nil)
	if e != nil {
		t.Fatal(e)
	}
	h := Health{At: now, PressureAt: now.Add(-125 * time.Second), Source: cfg.PressureSource, CPU: .1, Memory: .1, Warm: 2, BudgetVerified: true}
	c.Update(h)
	if e = c.Resume(now); e != nil {
		t.Fatal(e)
	}
	at := now.Add(11 * time.Second)
	h.At = at
	c.Update(h)
	c.Tick(at)
	if c.Scheduler.Stats().Effective != 0 {
		t.Fatal("expired Azure source left admission enabled")
	}
	cfg.PressureSource = "supabase:project"
	if _, e = NewController(cfg, nil); e == nil {
		t.Fatal("Azure allowance leaked to Supabase")
	}
}
