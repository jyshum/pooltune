package benchmark

import (
	"context"
	"fmt"
	"pooltune/internal/scheduler"
	"testing"
)

func TestSummaryIncludesFailuresAndDeadlineMisses(t *testing.T) {
	samples := []Sample{{Class: "critical", Outcome: "success", TotalMS: 10, DeadlineMS: 100}, {Class: "critical", Outcome: "timeout", TotalMS: 100, DeadlineMS: 100}, {Class: "critical", Outcome: "rejected", TotalMS: 20, DeadlineMS: 100}, {Class: "background", Outcome: "success", TotalMS: 140, DeadlineMS: 100}}
	got := Summarize(samples, "critical")
	if got.Offered != 3 || got.Success != 1 || got.Timeout != 1 || got.Rejected != 1 || got.DeadlineMisses != 1 || got.SuccessP95MS != 10 || got.AllP95MS != 100 {
		t.Fatalf("hidden unsuccessful requests: %+v", got)
	}
	bg := Summarize(samples, "background")
	if bg.DeadlineMisses != 1 || bg.Success != 1 {
		t.Fatal("late success hidden")
	}
}

func TestQueueDeadlineIsTimeoutBeforeContextTimerFires(t *testing.T) {
	for _, tc := range []struct {
		name            string
		err, contextErr error
		want            string
	}{
		{"queue timer first", scheduler.ErrExpired, nil, "timeout"},
		{"wrapped queue expiration", fmt.Errorf("acquire: %w", scheduler.ErrExpired), nil, "timeout"},
		{"request deadline", context.DeadlineExceeded, nil, "timeout"},
		{"context timer first", scheduler.ErrExpired, context.DeadlineExceeded, "timeout"},
		{"queue full", scheduler.ErrQueueFull, nil, "rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := QueueOutcome(tc.err, tc.contextErr); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
