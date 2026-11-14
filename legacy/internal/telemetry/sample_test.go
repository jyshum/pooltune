package telemetry

import (
	"testing"
	"time"
)

func TestMapFourPathsAndCounterEpoch(t *testing.T) {
	now := time.Now().UTC()
	for _, provider := range []string{"azure", "google", "supabase", "local"} {
		state := map[string]any{"mode": "managed-burst", "provider": provider, "transactions": map[string]any{"at": now, "total": "18446744073709551600", "epoch": "a"}, "effective": 4, "warm": 8, "active": 2, "queued": 3, "target": "never-export-secret"}
		if provider == "local" {
			state = map[string]any{"mode": "pool-tuning", "observation": map[string]any{"at": now, "transactions": state["transactions"], "target": 4, "working": 8, "idle": 6, "waiting": 3}}
		}
		s, ok := Decode(state, now)
		if !ok || s.Provider != provider || s.Active != 2 || s.Limit != 4 || s.Warm != 8 {
			t.Fatalf("mapping %s: %#v", provider, s)
		}
		next := s
		next.At = now.Add(time.Second)
		next.Total = "18446744073709551610"
		p := Measure(&s, next)
		if p.Rate == nil || *p.Rate != 10 {
			t.Fatal("exact counter delta")
		}
		next.Epoch = "b"
		if Measure(&s, next).Rate != nil {
			t.Fatal("restart must create gap")
		}
		next.Epoch = "a"
		next.At = now.Add(4 * time.Second)
		if Measure(&s, next).Rate != nil {
			t.Fatal("long gap")
		}
		next.At = now.Add(time.Second)
		next.Total = "1"
		if Measure(&s, next).Rate != nil {
			t.Fatal("reset")
		}
	}
}
func TestRejectStaleUnknownMalformedAndMarkDemo(t *testing.T) {
	now := time.Now().UTC()
	state := map[string]any{"mode": "managed-burst", "provider": "azure", "transactions": map[string]any{"at": now, "total": "5", "epoch": "a", "demo": true}}
	s, ok := Decode(state, now)
	if !ok || !s.Demo {
		t.Fatal("demo provenance")
	}
	if _, ok = Decode(state, now.Add(5*time.Second)); ok {
		t.Fatal("stale")
	}
	state["provider"] = "not-a-provider"
	if _, ok = Decode(state, now); ok {
		t.Fatal("unknown provider")
	}
	state["provider"] = "azure"
	state["transactions"] = map[string]any{"at": now, "total": "18446744073709551616", "epoch": "a"}
	if _, ok = Decode(state, now); ok {
		t.Fatal("overflow")
	}
}
