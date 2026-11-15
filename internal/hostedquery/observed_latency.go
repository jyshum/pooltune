package hostedquery

import (
	"encoding/json"
	"time"
)

// ObservedLookupLatency records normal traffic from the detector. It does not
// describe the work done by an individual execution plan.
type ObservedLookupLatency struct {
	ObjectiveMS         float64   `json:"objective_ms"`
	WindowMeansMS       []float64 `json:"recent_window_mean_ms"`
	WindowCalls         []int64   `json:"recent_window_calls"`
	Through             time.Time `json:"observed_through"`
	SharedBlockAccesses []int64   `json:"recent_window_shared_block_accesses,omitempty"`
}

const maxObservedLookupAge = 10 * time.Minute

func observedLookupLatency(raw []byte, planObservedAt time.Time) *ObservedLookupLatency {
	var queued struct {
		ObjectiveMS  float64         `json:"objective_ms"`
		WindowCalls  []int64         `json:"recent_window_calls"`
		Means        []*float64      `json:"recent_window_mean_ms"`
		Latest       WindowResult    `json:"latest_window"`
		SharedBlocks json.RawMessage `json:"recent_window_shared_block_accesses"`
	}
	if json.Unmarshal(raw, &queued) != nil {
		return nil
	}
	observed := ObservedLookupLatency{ObjectiveMS: queued.ObjectiveMS, WindowCalls: queued.WindowCalls, Through: queued.Latest.End}
	last := queued.Latest
	if !nonnegativeFinite(observed.ObjectiveMS) || observed.ObjectiveMS == 0 ||
		len(queued.Means) != 5 || len(observed.WindowCalls) != 5 ||
		last.Start.IsZero() || !last.End.After(last.Start) || last.End.After(planObservedAt) ||
		planObservedAt.Sub(last.End) > maxObservedLookupAge || last.Decision != Investigate ||
		last.Calls != observed.WindowCalls[4] {
		return nil
	}
	breaches := 0
	observed.WindowMeansMS = make([]float64, len(queued.Means))
	for i, mean := range queued.Means {
		if mean == nil || !nonnegativeFinite(*mean) || observed.WindowCalls[i] <= 0 {
			return nil
		}
		observed.WindowMeansMS[i] = *mean
		if *mean > observed.ObjectiveMS {
			breaches++
		}
	}
	if breaches < 3 || last.MeanMS != observed.WindowMeansMS[4] {
		return nil
	}
	observed.SharedBlockAccesses = observedBlockAccesses(queued.SharedBlocks, len(observed.WindowCalls))
	return &observed
}

// Page counts are optional on older jobs. An incomplete count never represents zero.
func observedBlockAccesses(raw json.RawMessage, windows int) []int64 {
	var counts []*int64
	if len(raw) == 0 || json.Unmarshal(raw, &counts) != nil || len(counts) != windows {
		return nil
	}
	blocks := make([]int64, windows)
	for index, count := range counts {
		if count == nil || *count < 0 {
			return nil
		}
		blocks[index] = *count
	}
	return blocks
}
