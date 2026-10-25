package tuning

import (
	"math"
	"testing"
	"time"
)

func bounds() Bounds {
	return Bounds{Minimum: 2, Maximum: 8, CPUGrow: .6, CPUBackoff: .8, MemoryGrow: .7, MemoryBackoff: .9}
}
func sample(now time.Time) Sample {
	return Sample{At: now, Target: 4, Working: 4, Idle: 0, Waiting: 2, CPU: .2, Memory: .3, Version: "verified"}
}

func TestGrowthNeedsContinuousFreshHealthyDemand(t *testing.T) {
	now := time.Unix(1000, 0)
	s := DecisionState{}
	for i := 0; i < 5; i++ {
		n := now.Add(time.Duration(i) * time.Second)
		d := Decide(n, s, sample(n), bounds(), false)
		if d.Target != 4 {
			t.Fatalf("premature growth: %+v", d)
		}
		s = d.State
	}
	n := now.Add(5 * time.Second)
	d := Decide(n, s, sample(n), bounds(), false)
	if d.Target != 5 || d.Reason != "sustained_demand" {
		t.Fatalf("bounded growth: %+v", d)
	}
	for _, change := range []func(*Sample){func(m *Sample) { m.At = now.Add(-time.Minute) }, func(m *Sample) { m.CPU = .9 }, func(m *Sample) { m.Memory = math.NaN() }, func(m *Sample) { m.Pending = true }, func(m *Sample) { m.Version = "" }, func(m *Sample) { m.Idle = 5 }} {
		m := sample(n)
		change(&m)
		d := Decide(n, s, m, bounds(), false)
		if d.Target > 4 {
			t.Fatalf("unsafe growth: %+v", d)
		}
	}
	// A gap resets sustained evidence instead of counting an unobserved interval.
	n = now.Add(8 * time.Second)
	d = Decide(n, s, sample(n), bounds(), false)
	if d.Target != 4 {
		t.Fatal("gap counted as healthy demand")
	}
}

func TestQuietReductionAndFrozenPressure(t *testing.T) {
	now := time.Unix(2000, 0)
	s := DecisionState{}
	for i := 0; i <= 30; i++ {
		n := now.Add(time.Duration(i) * time.Second)
		m := sample(n)
		m.Idle = 4
		m.Waiting = 0
		d := Decide(n, s, m, bounds(), false)
		if i < 30 && d.Target != 4 {
			t.Fatal("early shrink")
		}
		if i == 30 && d.Target != 3 {
			t.Fatalf("quiet reduction missing: %+v", d)
		}
		s = d.State
	}
	m := sample(now)
	d := Decide(now, DecisionState{}, m, bounds(), true)
	if d.Target != 4 {
		t.Fatal("frozen elective change")
	}
	m.CPU = .95
	d = Decide(now, DecisionState{}, m, bounds(), true)
	if d.Target != 3 || d.Reason != "pressure_backoff" {
		t.Fatalf("freeze disabled pressure protection: %+v", d)
	}
	m.Target = 2
	m.Working = 2
	m.CPU = .95
	d = Decide(now, DecisionState{}, m, bounds(), false)
	if d.Target != 2 {
		t.Fatal("went below minimum")
	}
}

func TestPendingAndCooldownNeverCompoundChanges(t *testing.T) {
	now := time.Unix(3000, 0)
	m := sample(now)
	m.Pending = true
	m.CPU = .95
	d := Decide(now, DecisionState{}, m, bounds(), false)
	if d.Target != 4 {
		t.Fatal("compounded pending operation")
	}
	m.Pending = false
	s := DecisionState{LastChange: now.Add(-time.Second)}
	d = Decide(now, s, m, bounds(), false)
	if d.Target != 4 {
		t.Fatal("ignored cooldown")
	}
	if bounds().Valid() != nil {
		t.Fatal("valid explicit bounds rejected")
	}
	bad := bounds()
	bad.Minimum = 0
	if bad.Valid() == nil {
		t.Fatal("invalid floor accepted")
	}
}
