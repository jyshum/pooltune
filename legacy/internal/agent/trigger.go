package agent

import (
	"sync"
	"time"
)

// TriggerGate merges repeated measured events without adding an agent worker or
// calling a model from the traffic path. Settings must be calibrated beforehand.
type TriggerProfile struct {
	Sustain, MaximumGap, Cooldown time.Duration
	MinimumSamples                uint64
}
type Signal struct {
	At                                                              time.Time
	Samples                                                         uint64
	Complete                                                        bool
	SLOFailure, Starvation, MixChanged, QueueExpiry, OperationIssue bool
}
type TriggerGate struct {
	mu                    sync.Mutex
	profile               TriggerProfile
	kind                  string
	since, last, lastEmit time.Time
}

func NewTriggerGate(p TriggerProfile) (*TriggerGate, error) {
	if p.Sustain <= 0 || p.MaximumGap <= 0 || p.Cooldown <= 0 || p.MinimumSamples == 0 {
		return nil, ErrInvalid
	}
	return &TriggerGate{profile: p}, nil
}
func (g *TriggerGate) Observe(s Signal) (Trigger, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !s.At.After(g.last) {
		return Trigger{}, false
	}
	kind := ""
	switch {
	case s.Starvation:
		kind = "starvation"
	case s.SLOFailure:
		kind = "slo"
	case s.QueueExpiry:
		kind = "queue_expiry"
	case s.MixChanged:
		kind = "mix"
	case s.OperationIssue:
		kind = "operation"
	}
	continuous := !g.last.IsZero() && s.At.Sub(g.last) <= g.profile.MaximumGap
	g.last = s.At
	if !s.Complete || s.Samples < g.profile.MinimumSamples || kind == "" {
		g.kind = ""
		g.since = time.Time{}
		return Trigger{}, false
	}
	if !continuous || kind != g.kind {
		g.kind = kind
		g.since = s.At
	}
	if s.At.Sub(g.since) < g.profile.Sustain || (!g.lastEmit.IsZero() && s.At.Sub(g.lastEmit) < g.profile.Cooldown) {
		return Trigger{}, false
	}
	g.lastEmit = s.At
	g.since = s.At
	return Trigger{Kind: kind}, true
}
