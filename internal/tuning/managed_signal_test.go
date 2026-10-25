//go:build managedlive

package tuning

import (
	"testing"
	"time"
)

func TestManagedSignalRejectsGeneratorBelowProviderLimit(t *testing.T) {
	p, err := liveExperiment("provider-default")
	if err != nil {
		t.Fatal(err)
	}
	p.Clients = 16
	if err := p.validate(); err == nil {
		t.Fatal("16 clients cannot establish growth benefit above a 50-connection default")
	}
}

func TestManagedSignalProfileCanReachDefaultAndRetainsFixedComparator(t *testing.T) {
	p, err := liveExperiment("provider-default")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	if p.Minimum != 50 || p.Maximum != 64 || p.Clients != 96 {
		t.Fatalf("wrong default comparison: %+v", p)
	}
	var duration time.Duration
	for _, phase := range p.Phases {
		duration += phase.Duration
	}
	if duration != time.Minute {
		t.Fatalf("workload expanded to %v", duration)
	}
	if len(p.Cases) != 2 || p.Cases[0] != "fixed-warm-50" || p.Cases[1] != "adaptive-50-to-64" {
		t.Fatalf("missing matched comparison: %v", p.Cases)
	}
}

func TestManagedSignalRejectsUnreachableOfferedLoad(t *testing.T) {
	p, _ := liveExperiment("provider-default")
	for i := range p.Phases {
		p.Phases[i].Rate = 30
	}
	if err := p.validate(); err == nil {
		t.Fatal("30/s at 200ms does not exercise a 50-connection limit")
	}
}

func TestManagedSignalRejectsUnknownProfile(t *testing.T) {
	if _, err := liveExperiment("typo"); err == nil {
		t.Fatal("unknown profile must not silently select old benchmark")
	}
}

func TestManagedSignalSchedulesEveryArrivalEvenIfDriverRunsLate(t *testing.T) {
	p, _ := liveExperiment("provider-default")
	origin := time.Unix(100, 0)
	count := 0
	for _, phase := range p.Phases {
		arrivals := liveArrivals(origin, phase)
		count += len(arrivals)
		for i, arrival := range arrivals {
			if arrival.Phase != phase.Name || arrival.Scheduled.Before(origin) || !arrival.Scheduled.Before(origin.Add(phase.Duration)) {
				t.Fatal("arrival outside phase")
			}
			if i > 0 && !arrival.Scheduled.After(arrivals[i-1].Scheduled) {
				t.Fatal("unordered arrival")
			}
		}
		origin = origin.Add(phase.Duration)
	}
	if count != 14500 {
		t.Fatalf("offered workload must have exactly 14500 requests, got %d", count)
	}
}
