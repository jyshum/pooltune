package controller

import (
	"math"
	"time"
)

// Observation explicitly separates missing values from a measured zero.
type Observation struct {
	Value  float64
	At     time.Time
	Source string
	Valid  bool
}

func (o Observation) fresh(now time.Time, age time.Duration) bool {
	return o.Valid && o.Source != "" && !o.At.IsZero() && !o.At.After(now) && now.Sub(o.At) <= age && finite(o.Value) && o.Value >= 0
}
func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

type Thresholds struct{ Grow, Backoff, Stop float64 }

func (t Thresholds) valid() bool {
	return finite(t.Grow) && finite(t.Backoff) && finite(t.Stop) && t.Grow > 0 && t.Grow < t.Backoff && t.Backoff < t.Stop && t.Stop <= 1
}

// PhysicalBudget is one approved accounting scope. Other excludes both the PG
// allocation and REST envelope; Reserved excludes Other. Units are connections.
type PhysicalBudget struct {
	Valid                                                                                              bool
	At                                                                                                 time.Time
	Source, Owner, Version                                                                             string
	Max, Reserved, Other, PGAllocation, RESTEnvelope, User, Provider, Memory, ObservedPG, ObservedREST int
	RESTVerified, ConservativeRESTApproved                                                             bool
}

func (b PhysicalBudget) valid() bool {
	for _, v := range []int{b.Max, b.Reserved, b.Other, b.PGAllocation, b.RESTEnvelope, b.User, b.Provider, b.Memory, b.ObservedPG, b.ObservedREST} {
		if v < 0 || v > 1000000000 {
			return false
		}
	}
	return b.Valid && b.Source != "" && b.Owner != "" && b.Version != "" && b.Max > 0 && (b.RESTEnvelope == 0 || b.RESTVerified || b.ConservativeRESTApproved)
}
func (b PhysicalBudget) fresh(now time.Time, age time.Duration) bool {
	return b.valid() && !b.At.IsZero() && !b.At.After(now) && now.Sub(b.At) <= age
}
func (b PhysicalBudget) safe() int {
	return max(0, min(b.PGAllocation, b.User, b.Provider, b.Memory, b.Max-b.Reserved-b.Other-b.RESTEnvelope))
}
func (b PhysicalBudget) breached() bool {
	return b.ObservedPG+b.ObservedREST+b.Other+b.Reserved > b.Max || b.ObservedREST > b.RESTEnvelope
}

// SharedSafety gives both paths the same database pressure. Allocations never
// transfer automatically. The controller still applies path-specific units.
type SharedSafety struct {
	PGPhysicalLimit      int
	HardStop, Restricted bool
	RESTTopologyVerified bool
}

func Coordinate(now time.Time, b PhysicalBudget, cpu, mem Observation, g Guardrails) SharedSafety {
	fresh := b.fresh(now, g.MaxAge)
	c := cpu.fresh(now, g.MaxAge) && cpu.Value <= 1
	m := mem.fresh(now, g.MaxAge) && mem.Value <= 1
	out := SharedSafety{Restricted: !fresh || !c || !m, RESTTopologyVerified: b.RESTVerified}
	if fresh {
		out.PGPhysicalLimit = b.safe()
		out.HardStop = b.breached()
	}
	out.HardStop = out.HardStop || (c && cpu.Value >= g.CPU.Stop) || (m && mem.Value >= g.Memory.Stop)
	return out
}
