//go:build integration

package main

import (
	"math"
	"pooltune/internal/provider"
	"time"
)

// Preview-only counter. The demo flag travels with the data and is shown in UI.
// Production paths read actual SHOW STATS counters and never call this function.
func demoTransactions() func() *provider.Transactions {
	start, last := time.Now(), time.Now()
	total := float64(0)
	return func() *provider.Transactions {
		now := time.Now()
		phase := math.Mod(now.Sub(start).Seconds(), 24)
		rate := 8.0
		if phase >= 8 && phase < 14 {
			rate = 64
		} else if phase >= 14 && phase < 18 {
			rate = 24
		}
		total += now.Sub(last).Seconds() * rate
		last = now
		return &provider.Transactions{At: now, Total: uint64(total), Epoch: "controlled-browser-preview", Demo: true}
	}
}
