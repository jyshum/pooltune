//go:build managedlive

package tuning

import (
	"fmt"
	"time"
)

type livePhase struct {
	Name     string
	Duration time.Duration
	Rate     int
}
type liveExperimentProfile struct {
	Name                      string
	Minimum, Maximum, Clients int
	QueryHold                 time.Duration
	Cases                     []string
	Phases                    []livePhase
}

// These are controlled occupancy experiments, not estimates of customer SQL.
// QueryHold must match executeLiveRead. The default profile tests the documented
// Azure limit without manufacturing an undersized baseline.
func liveExperiment(name string) (liveExperimentProfile, error) {
	var p liveExperimentProfile
	switch name {
	case "":
		p = liveExperimentProfile{"legacy-undersized", 5, 8, 16, 200 * time.Millisecond,
			[]string{"fixed-warm-8", "adaptive-5-to-8"},
			[]livePhase{{"steady", 8 * time.Second, 8}, {"burst", 35 * time.Second, 30}, {"quiet", 35 * time.Second, 0}, {"return", 15 * time.Second, 30}}}
	case "provider-default":
		p = liveExperimentProfile{name, 50, 64, 96, 200 * time.Millisecond,
			[]string{"fixed-warm-50", "adaptive-50-to-64"},
			[]livePhase{{"steady", 10 * time.Second, 100}, {"short-burst", 10 * time.Second, 300}, {"quiet", 5 * time.Second, 0}, {"sustained", 35 * time.Second, 300}}}
	default:
		return p, fmt.Errorf("unknown managed experiment profile")
	}
	return p, p.validate()
}
func (p liveExperimentProfile) validate() error {
	if p.Minimum <= 0 || p.Maximum <= p.Minimum || p.Clients <= p.Maximum || p.QueryHold <= 0 {
		return fmt.Errorf("generator must have more clients than the tested pool ceiling")
	}
	maxRate := 0
	for _, phase := range p.Phases {
		if phase.Duration <= 0 || phase.Rate < 0 {
			return fmt.Errorf("invalid workload phase")
		}
		if phase.Rate > maxRate {
			maxRate = phase.Rate
		}
	}
	occupancy := float64(maxRate) * p.QueryHold.Seconds()
	if occupancy <= float64(p.Minimum) || occupancy >= float64(p.Maximum) {
		return fmt.Errorf("synthetic offered occupancy must cross baseline and fit comparison ceiling")
	}
	return nil
}

// Fix offered arrivals in advance. A ticker can silently drop offered work when
// the driver is delayed, making two trials appear matched when they are not.
func liveArrivals(origin time.Time, phase livePhase) []liveRequest {
	count := int(phase.Duration * time.Duration(phase.Rate) / time.Second)
	arrivals := make([]liveRequest, count)
	for i := range arrivals {
		arrivals[i] = liveRequest{Scheduled: origin.Add(time.Duration(i) * time.Second / time.Duration(phase.Rate)), Phase: phase.Name}
	}
	return arrivals
}
