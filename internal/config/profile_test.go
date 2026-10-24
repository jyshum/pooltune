package config

import (
	"math"
	"testing"
	"time"
)

// Synthetic fixture. These numbers do not authorize a production rollout.
func profileFixture() EvaluationProfile {
	return EvaluationProfile{
		MinWindow: 30 * time.Second, MaxWindow: 5 * time.Minute,
		MinSamplesPerWorkload:  2000,
		MaxFailureRateIncrease: 0.01, MaxLatencyRatio: 1.05,
	}
}

func TestEvaluationProfileRejectsMissingAndInvalidEvidenceBounds(t *testing.T) {
	tests := []struct {
		name   string
		change func(*EvaluationProfile)
	}{
		{"missing samples", func(p *EvaluationProfile) { p.MinSamplesPerWorkload = 0 }},
		{"missing minimum window", func(p *EvaluationProfile) { p.MinWindow = 0 }},
		{"reversed windows", func(p *EvaluationProfile) { p.MaxWindow = p.MinWindow - 1 }},
		{"invalid failure margin", func(p *EvaluationProfile) { p.MaxFailureRateIncrease = math.NaN() }},
		{"unbounded latency", func(p *EvaluationProfile) { p.MaxLatencyRatio = math.Inf(1) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := profileFixture()
			tc.change(&p)
			if p.Valid() {
				t.Fatal("invalid evaluation profile was accepted")
			}
		})
	}
}

func TestEvaluationProfileAcceptsCompleteFiniteBounds(t *testing.T) {
	if !profileFixture().Valid() {
		t.Fatal("valid profile rejected")
	}
	if (EvaluationProfile{}).Valid() {
		t.Fatal("zero profile accepted")
	}
}
