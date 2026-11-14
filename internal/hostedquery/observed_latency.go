package hostedquery

import (
	"encoding/json"
	"time"
)

// ObservedLookupLatency records normal traffic from the detector. It does not
// describe the work done by an individual execution plan.
type ObservedLookupLatency struct {
	ObjectiveMS   float64   `json:"objective_ms"`
	WindowMeansMS []float64 `json:"recent_window_mean_ms"`
	WindowCalls   []int64   `json:"recent_window_calls"`
	Through       time.Time `json:"observed_through"`
}

func observedLookupLatency(raw []byte, planObservedAt time.Time) *ObservedLookupLatency {
	const maxAge = 10 * time.Minute
	var queued struct {
		ObjectiveMS float64      `json:"objective_ms"`
		WindowCalls []int64      `json:"recent_window_calls"`
		Means       []*float64   `json:"recent_window_mean_ms"`
		Latest      WindowResult `json:"latest_window"`
	}
	if json.Unmarshal(raw, &queued) != nil {
		return nil
	}
	observed := ObservedLookupLatency{ObjectiveMS: queued.ObjectiveMS, WindowCalls: queued.WindowCalls, Through: queued.Latest.End}
	last := queued.Latest
	if !nonnegativeFinite(observed.ObjectiveMS) || observed.ObjectiveMS == 0 ||
		len(queued.Means) != 5 || len(observed.WindowCalls) != 5 ||
		last.Start.IsZero() || !last.End.After(last.Start) || last.End.After(planObservedAt) ||
		planObservedAt.Sub(last.End) > maxAge || last.Decision != Investigate ||
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
	return &observed
}
