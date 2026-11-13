// Package hostedquery implements the hosted query-performance evidence path.
package hostedquery

import (
	"errors"
	"math"
	"time"
)

// Counter describes one top-level pg_stat_statements entry.
type Counter struct {
	Database, Role     uint32
	Query              int64
	Boot, Reset, Entry time.Time
	Calls, Blocks      int64
	ExecutionMS        float64
}

type Observation struct {
	Calls         int64   `json:"calls"`
	MeanMS        float64 `json:"mean_execution_ms"`
	BlocksPerCall float64 `json:"shared_blocks_per_call"`
	context       observationContext
}

type observationContext struct {
	database, role     uint32
	query              int64
	boot, reset, entry time.Time
}

func (c observationContext) same(other observationContext) bool {
	return c.database == other.database && c.role == other.role && c.query == other.query &&
		c.boot.Equal(other.boot) && c.reset.Equal(other.reset) && c.entry.Equal(other.entry)
}

type Decision string

const (
	Investigate          Decision = "investigate"
	NoChange             Decision = "no_change"
	InsufficientEvidence Decision = "insufficient_evidence"
)

// Measure requires exactly the executions the fixture issued in this batch.
func Measure(before, after Counter, expectedCalls int64) (Observation, error) {
	if before.Database == 0 || before.Role == 0 || before.Query == 0 ||
		before.Database != after.Database || before.Role != after.Role || before.Query != after.Query {
		return Observation{}, errors.New("statement identity changed or missing")
	}
	if before.Boot.IsZero() || before.Reset.IsZero() || before.Entry.IsZero() ||
		!before.Boot.Equal(after.Boot) || !before.Reset.Equal(after.Reset) || !before.Entry.Equal(after.Entry) {
		return Observation{}, errors.New("counter epoch changed or missing")
	}
	if expectedCalls <= 0 || before.Calls < 0 || after.Calls < before.Calls ||
		after.Calls-before.Calls != expectedCalls || before.Blocks < 0 || after.Blocks < before.Blocks ||
		!nonnegativeFinite(before.ExecutionMS) || !nonnegativeFinite(after.ExecutionMS) ||
		after.ExecutionMS < before.ExecutionMS {
		return Observation{}, errors.New("incomplete or invalid counter interval")
	}
	calls := float64(expectedCalls)
	return Observation{
		Calls: expectedCalls, MeanMS: (after.ExecutionMS - before.ExecutionMS) / calls,
		BlocksPerCall: float64(after.Blocks-before.Blocks) / calls,
		context:       observationContext{before.Database, before.Role, before.Query, before.Boot, before.Reset, before.Entry},
	}, nil
}

// DetectObjective requires the latest five measurements from the same counter context.
func DetectObjective(observations []Observation, objectiveMS float64) (Decision, error) {
	if !nonnegativeFinite(objectiveMS) || objectiveMS == 0 {
		return InsufficientEvidence, errors.New("invalid mean latency objective")
	}
	if len(observations) < 5 {
		return InsufficientEvidence, nil
	}
	latest := observations[len(observations)-5:]
	context := latest[0].context
	breaches := 0
	for _, observation := range latest {
		if context.query == 0 || !context.same(observation.context) {
			return InsufficientEvidence, errors.New("measurements lack a common statement and counter epoch")
		}
		if observation.Calls <= 0 || !nonnegativeFinite(observation.MeanMS) {
			return InsufficientEvidence, errors.New("invalid latency observation")
		}
		if observation.MeanMS > objectiveMS {
			breaches++
		}
	}
	if breaches >= 3 {
		return Investigate, nil
	}
	return NoChange, nil
}

func nonnegativeFinite(n float64) bool {
	return n >= 0 && !math.IsInf(n, 0)
}
