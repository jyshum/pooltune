// Package hostedquery implements the hosted query-performance evidence path.
package hostedquery

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"time"
)

// Counter describes one top-level pg_stat_statements entry.
type Counter struct {
	Database, Role     uint32
	Query              int64 `json:"Query,string"`
	Boot, Reset, Entry time.Time
	Calls, Blocks      int64
	ExecutionMS        float64
}

// Historical worker state used JSON numbers. Accept both representations without
// a float conversion; the field tag still writes strings for JavaScript readers.
func (c *Counter) UnmarshalJSON(data []byte) error {
	type counterJSON Counter
	next := counterJSON(*c)
	var wire struct {
		*counterJSON
		Query json.RawMessage
	}
	wire.counterJSON = &next
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if len(wire.Query) != 0 {
		number := wire.Query
		if number[0] == '"' {
			var digits string
			if err := json.Unmarshal(number, &digits); err != nil {
				return err
			}
			number = []byte(digits)
		}
		if len(number) == 0 || bytes.Equal(number, []byte("null")) || !bytes.Equal(number, bytes.TrimSpace(number)) {
			return errors.New("invalid counter query integer")
		}
		if err := json.Unmarshal(number, &next.Query); err != nil {
			return err
		}
	}
	*c = Counter(next)
	return nil
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
	delta, err := counterDelta(before, after)
	if err != nil {
		return Observation{}, err
	}
	if expectedCalls <= 0 || delta.Calls != expectedCalls {
		return Observation{}, errors.New("incomplete or invalid counter interval")
	}
	return delta.observation(after), nil
}

type counterChange struct {
	Calls, Blocks int64
	ExecutionMS   float64
}

func (c counterChange) observation(identity Counter) Observation {
	return Observation{
		Calls: c.Calls, MeanMS: c.ExecutionMS / float64(c.Calls),
		BlocksPerCall: float64(c.Blocks) / float64(c.Calls),
		context:       observationContext{identity.Database, identity.Role, identity.Query, identity.Boot, identity.Reset, identity.Entry},
	}
}

func counterDelta(before, after Counter) (counterChange, error) {
	if before.Database == 0 || before.Role == 0 || before.Query == 0 ||
		before.Database != after.Database || before.Role != after.Role || before.Query != after.Query {
		return counterChange{}, errors.New("statement identity changed or missing")
	}
	if before.Boot.IsZero() || before.Reset.IsZero() || before.Entry.IsZero() ||
		!before.Boot.Equal(after.Boot) || !before.Reset.Equal(after.Reset) || !before.Entry.Equal(after.Entry) {
		return counterChange{}, errors.New("counter epoch changed or missing")
	}
	if before.Calls < 0 || after.Calls < before.Calls || before.Blocks < 0 || after.Blocks < before.Blocks ||
		!nonnegativeFinite(before.ExecutionMS) || !nonnegativeFinite(after.ExecutionMS) ||
		after.ExecutionMS < before.ExecutionMS {
		return counterChange{}, errors.New("incomplete or invalid counter interval")
	}
	change := counterChange{after.Calls - before.Calls, after.Blocks - before.Blocks, after.ExecutionMS - before.ExecutionMS}
	if change.Calls == 0 && (change.Blocks != 0 || change.ExecutionMS != 0) {
		return counterChange{}, errors.New("counters changed without a completed call")
	}
	return change, nil
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
