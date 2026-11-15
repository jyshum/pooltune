package hostedquery

import (
	"errors"
	"time"
)

// SamplingPolicy sets the length of a polling interval and objective window.
type SamplingPolicy struct {
	Interval time.Duration
	Window   time.Duration
}

func (policy SamplingPolicy) validate() error {
	if policy.Interval < 100*time.Millisecond || policy.Window < policy.Interval ||
		policy.Window > time.Hour || policy.Window%policy.Interval != 0 {
		return errors.New("window must be a multiple of the sampling interval, from 100ms through 1h")
	}
	return nil
}

type WindowResult struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Calls    int64     `json:"calls"`
	MeanMS   float64   `json:"mean_execution_ms,omitempty"`
	Decision Decision  `json:"decision"`
}

// The exported fields allow the worker to resume an unfinished window from PostgreSQL.
type windowState struct {
	LastAt    time.Time
	Last      *Counter
	Start     time.Time
	Intervals int64
	Pending   counterChange
	Recent    []counterChange
}

func (s *windowState) restart(at time.Time, sample *Counter) {
	*s = windowState{LastAt: at, Start: at}
	if sample != nil {
		copy := *sample
		s.Last = &copy
	}
}

func (s *windowState) advance(at time.Time, sample *Counter, policy SamplingPolicy, objective float64) *WindowResult {
	if !at.After(s.LastAt) {
		return nil
	}
	unknown := WindowResult{Start: s.Start, End: at, Decision: InsufficientEvidence}
	if s.Last == nil || sample == nil {
		s.restart(at, sample)
		if unknown.Start.IsZero() {
			return nil
		}
		return &unknown
	}
	delta, err := counterDelta(*s.Last, *sample)
	if err != nil || at.Sub(s.LastAt) > policy.Interval+policy.Interval/2 {
		s.restart(at, sample)
		return &unknown
	}

	s.LastAt = at
	*s.Last = *sample
	s.Intervals++
	s.Pending.Calls += delta.Calls
	s.Pending.ExecutionMS += delta.ExecutionMS
	s.Pending.Blocks += delta.Blocks
	if s.Intervals < int64(policy.Window/policy.Interval) || at.Sub(s.Start) < policy.Window {
		return nil
	}

	result := unknown
	result.Calls = s.Pending.Calls
	if s.Pending.Calls <= 0 || !nonnegativeFinite(s.Pending.ExecutionMS) || s.Pending.Blocks < 0 {
		s.restart(at, sample)
		return &result
	}
	result.MeanMS = s.Pending.observation(*sample).MeanMS
	s.Recent = append(s.Recent, s.Pending)
	if len(s.Recent) > 5 {
		s.Recent = s.Recent[1:]
	}
	var observations [5]Observation
	for index, previous := range s.Recent {
		observations[index] = previous.observation(*sample)
	}
	result.Decision, _ = DetectObjective(observations[:len(s.Recent)], objective)
	s.Start, s.Intervals, s.Pending = at, 0, counterChange{}
	return &result
}
