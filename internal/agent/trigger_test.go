package agent

import (
	"testing"
	"time"
)

func TestTriggersRequireContinuousEvidenceAndMergeRepeats(t *testing.T) {
	g, e := NewTriggerGate(TriggerProfile{Sustain: 2 * time.Second, MaximumGap: 1500 * time.Millisecond, Cooldown: time.Minute, MinimumSamples: 20})
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	events := 0
	for i := 0; i < 10; i++ {
		event, ok := g.Observe(Signal{At: now.Add(time.Duration(i) * time.Second), Samples: 20, Complete: true, SLOFailure: true})
		if ok {
			events++
			if event.Kind != "slo" {
				t.Fatal("wrong trigger")
			}
		}
	}
	if events != 1 {
		t.Fatal("repeat triggers not merged")
	}
	g, _ = NewTriggerGate(TriggerProfile{Sustain: 2 * time.Second, MaximumGap: 1500 * time.Millisecond, Cooldown: time.Minute, MinimumSamples: 20})
	g.Observe(Signal{At: now, Samples: 20, Complete: true, SLOFailure: true})
	if _, ok := g.Observe(Signal{At: now.Add(time.Minute), Samples: 20, Complete: true, SLOFailure: true}); ok {
		t.Fatal("missing evidence treated as sustained")
	}
}
