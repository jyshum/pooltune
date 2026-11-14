// Package benchmark stores payload-free client observations, including failures.
package benchmark

import (
	"context"
	"errors"
	"math"
	"pooltune/internal/scheduler"
	"sort"
)

type Sample struct {
	ID                                                               int
	Class, Outcome                                                   string
	ScheduledMS, StartLagMS, TotalMS, DeadlineMS, ApplicationQueueMS float64
	Status                                                           int
}
type Summary struct {
	Offered, Success, Timeout, Rejected, Failed, DeadlineMisses int
	AllP50MS, AllP95MS, AllP99MS, SuccessP95MS, MaxStartLagMS   float64
}

func Summarize(samples []Sample, class string) Summary {
	var s Summary
	var all, successful []float64
	for _, v := range samples {
		if v.Class != class {
			continue
		}
		s.Offered++
		all = append(all, v.TotalMS)
		s.MaxStartLagMS = max(s.MaxStartLagMS, v.StartLagMS)
		if v.Outcome == "timeout" || v.TotalMS > v.DeadlineMS {
			s.DeadlineMisses++
		}
		switch v.Outcome {
		case "success":
			s.Success++
			successful = append(successful, v.TotalMS)
		case "timeout":
			s.Timeout++
		case "rejected":
			s.Rejected++
		default:
			s.Failed++
		}
	}
	sort.Float64s(all)
	sort.Float64s(successful)
	quantile := func(v []float64, p float64) float64 {
		if len(v) == 0 {
			return 0
		}
		return v[int(math.Ceil(p*float64(len(v))))-1]
	}
	s.AllP50MS = quantile(all, .5)
	s.AllP95MS = quantile(all, .95)
	s.AllP99MS = quantile(all, .99)
	s.SuccessP95MS = quantile(successful, .95)
	return s
}

// QueueOutcome does not depend on which deadline timer fires first.
func QueueOutcome(err, contextErr error) string {
	if errors.Is(err, scheduler.ErrExpired) || errors.Is(err, context.DeadlineExceeded) || errors.Is(contextErr, context.DeadlineExceeded) {
		return "timeout"
	}
	if contextErr != nil {
		return "failed"
	}
	return "rejected"
}
