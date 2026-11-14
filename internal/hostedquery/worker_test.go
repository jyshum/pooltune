package hostedquery

import (
	"testing"
	"time"
)

func TestWorkerScreensWeightedWindows(t *testing.T) {
	now := time.Unix(100, 0)
	for _, tc := range []struct {
		name      string
		windows   []float64
		objective float64
		want      Decision
	}{
		{"healthy", []float64{5, 5, 5, 5, 5}, 10, NoChange},
		{"one spike", []float64{5, 5, 40, 5, 5}, 10, NoChange},
		{"repeated breach", []float64{11, 14, 5, 11, 5}, 10, Investigate},
		{"different objective", []float64{11, 14, 5, 11, 5}, 20, NoChange},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counter := Counter{Database: 1, Role: 2, Query: 3, Boot: now, Reset: now, Entry: now}
			var state windowState
			policy := SamplingPolicy{Interval: time.Second, Window: 2 * time.Second}
			state.advance(now, &counter, policy, tc.objective)
			for i, mean := range tc.windows {
				// The first interval is slow but has one call. The window mean must use all ten calls.
				counter.Calls++
				counter.ExecutionMS += 4 * mean
				result := state.advance(now.Add(time.Duration(2*i+1)*time.Second), &counter, policy, tc.objective)
				if result != nil {
					t.Fatalf("partial window yielded %+v", result)
				}
				counter.Calls += 9
				counter.ExecutionMS += 6 * mean
				result = state.advance(now.Add(time.Duration(2*i+2)*time.Second), &counter, policy, tc.objective)
				if result == nil || result.Calls != 10 || result.MeanMS != mean {
					t.Fatalf("window %d: %+v, want 10 calls at %v ms", i, result, mean)
				}
				want := InsufficientEvidence
				if i == 4 {
					want = tc.want
				}
				if result.Decision != want {
					t.Fatalf("window %d: got %s, want %s", i, result.Decision, want)
				}
			}
		})
	}
}

func TestWorkerDiscardsBrokenHistory(t *testing.T) {
	now := time.Unix(100, 0)
	for _, problem := range []string{"reset", "missing", "gap", "eviction"} {
		t.Run(problem, func(t *testing.T) {
			policy := SamplingPolicy{Interval: time.Second, Window: time.Second}
			counter := Counter{Database: 1, Role: 2, Query: 3, Boot: now, Reset: now, Entry: now}
			var state windowState
			at := now
			state.advance(at, &counter, policy, 10)
			for range 4 {
				at = at.Add(time.Second)
				counter.Calls++
				counter.ExecutionMS += 20
				state.advance(at, &counter, policy, 10)
			}
			at = at.Add(time.Second)
			sample := &counter
			switch problem {
			case "reset":
				counter.Reset = at
			case "eviction":
				counter.Entry = at
			case "gap":
				at = at.Add(time.Second)
			case "missing":
				sample = nil
			}
			result := state.advance(at, sample, policy, 10)
			if result == nil || result.Decision != InsufficientEvidence {
				t.Fatalf("%s yielded %+v", problem, result)
			}
			for range 4 {
				at = at.Add(time.Second)
				counter.Calls++
				counter.ExecutionMS += 20
				if result = state.advance(at, &counter, policy, 10); result != nil && result.Decision == Investigate {
					t.Fatalf("old history survived %s: %+v", problem, result)
				}
			}
		})
	}
}

func TestWorkerCountsIdleIntervalWithinActiveWindow(t *testing.T) {
	now := time.Unix(100, 0)
	policy := SamplingPolicy{Interval: time.Second, Window: 2 * time.Second}
	counter := Counter{Database: 1, Role: 2, Query: 3, Boot: now, Reset: now, Entry: now}
	var state windowState
	state.advance(now, &counter, policy, 10)
	if result := state.advance(now.Add(time.Second), &counter, policy, 10); result != nil {
		t.Fatalf("partial idle interval yielded %+v", result)
	}
	counter.Calls, counter.ExecutionMS = 4, 48
	result := state.advance(now.Add(2*time.Second), &counter, policy, 10)
	if result == nil || result.Calls != 4 || result.MeanMS != 12 {
		t.Fatalf("active window: %+v", result)
	}
	for _, second := range []int{3, 4} {
		result = state.advance(now.Add(time.Duration(second)*time.Second), &counter, policy, 10)
	}
	if result == nil || result.Calls != 0 || result.Decision != InsufficientEvidence {
		t.Fatalf("idle window: %+v", result)
	}
}

func TestWorkerCannotFinishWindowWithEarlyPolls(t *testing.T) {
	now := time.Unix(100, 0)
	policy := SamplingPolicy{Interval: time.Second, Window: 2 * time.Second}
	counter := Counter{Database: 1, Role: 2, Query: 3, Boot: now, Reset: now, Entry: now}
	var state windowState
	state.advance(now, &counter, policy, 10)
	for _, offset := range []time.Duration{800 * time.Millisecond, 1600 * time.Millisecond} {
		counter.Calls++
		counter.ExecutionMS += 20
		if result := state.advance(now.Add(offset), &counter, policy, 10); result != nil {
			t.Fatalf("two early polls closed the window at %v: %+v", offset, result)
		}
	}
	counter.Calls++
	counter.ExecutionMS += 20
	result := state.advance(now.Add(2400*time.Millisecond), &counter, policy, 10)
	if result == nil || result.Calls != 3 {
		t.Fatalf("expected a full window: %+v", result)
	}
}
