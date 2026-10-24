package config

import (
	"math"
	"time"
)

type EvaluationProfile struct {
	Version                string        `json:"version"`
	MinWindow              time.Duration `json:"min_window"`
	MaxWindow              time.Duration `json:"max_window"`
	MinSamplesPerWorkload  int           `json:"min_samples_per_workload"`
	MaxFailureRateIncrease float64       `json:"max_failure_rate_increase"`
	MaxLatencyRatio        float64       `json:"max_latency_ratio"`
}

func (p EvaluationProfile) Valid() bool {
	finite := func(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
	return p.MinWindow > 0 && p.MaxWindow >= p.MinWindow &&
		p.MinSamplesPerWorkload > 0 &&
		finite(p.MaxFailureRateIncrease) && p.MaxFailureRateIncrease >= 0 && p.MaxFailureRateIncrease <= 1 &&
		finite(p.MaxLatencyRatio) && p.MaxLatencyRatio >= 1
}
