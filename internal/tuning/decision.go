// Package tuning manages a separately running pooler within an explicit profile.
// It never handles application requests or retries database work.
package tuning

import (
	"errors"
	"math"
	"pooltune/internal/provider"
	"time"
)

var ErrUnsafe = errors.New("tuning prerequisite unavailable or unsafe")

type Bounds struct {
	Minimum       int     `json:"minimum"`
	Maximum       int     `json:"maximum"`
	CPUGrow       float64 `json:"cpu_grow"`
	CPUBackoff    float64 `json:"cpu_backoff"`
	MemoryGrow    float64 `json:"memory_grow"`
	MemoryBackoff float64 `json:"memory_backoff"`
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
func (b Bounds) Valid() error {
	if b.Minimum < 1 || b.Maximum < b.Minimum || b.Maximum > 10000 {
		return ErrUnsafe
	}
	for _, p := range [][2]float64{{b.CPUGrow, b.CPUBackoff}, {b.MemoryGrow, b.MemoryBackoff}} {
		if !finite(p[0]) || !finite(p[1]) || p[0] <= 0 || p[0] >= p[1] || p[1] >= 1 {
			return ErrUnsafe
		}
	}
	return nil
}

type Sample struct {
	Transactions   *provider.Transactions `json:"transactions"`
	PressureAt     time.Time              `json:"pressure_at,omitempty"`
	PressureMaxAge time.Duration          `json:"pressure_max_age,omitempty"`
	At             time.Time              `json:"at"`
	Version        string                 `json:"version"`
	Target         int                    `json:"target"`
	Working        int                    `json:"working"`
	Idle           int                    `json:"idle"`
	Waiting        int                    `json:"waiting"`
	CPU            float64                `json:"cpu"`
	Memory         float64                `json:"memory"`
	Pending        bool                   `json:"pending"`
}

func (m Sample) Valid(now time.Time, b Bounds) bool {
	if m.PressureMaxAge != 0 && (m.PressureMaxAge < time.Second || m.PressureMaxAge > 120*time.Second || m.PressureAt.IsZero() || m.PressureAt.After(now) || now.Sub(m.PressureAt) > m.PressureMaxAge) {
		return false
	}
	return b.Valid() == nil && m.Version != "" && !m.At.IsZero() && !m.At.After(now) && now.Sub(m.At) <= 2*time.Second && m.Target >= b.Minimum && m.Target <= b.Maximum && m.Working >= 0 && m.Working <= b.Maximum && m.Idle >= 0 && m.Idle <= m.Working && m.Waiting >= 0 && m.Waiting <= 1000000 && finite(m.CPU) && m.CPU >= 0 && m.CPU <= 1 && finite(m.Memory) && m.Memory >= 0 && m.Memory <= 1
}

type DecisionState struct{ LastTick, LastChange, DemandSince, QuietSince time.Time }
type Decision struct {
	Target int
	Reason string
	State  DecisionState
}

// Decide is an experimental occupancy controller. Its 5-second demand and
// 30-second quiet windows are correctness constraints, not calibrated promises
// of optimal throughput. PgBouncer remains responsible for request waiting.
func Decide(now time.Time, s DecisionState, m Sample, b Bounds, frozen bool) Decision {
	d := Decision{Target: m.Target, Reason: "hold", State: s}
	d.State.LastTick = now
	reset := func() { d.State.DemandSince = time.Time{}; d.State.QuietSince = time.Time{} }
	if !m.Valid(now, b) {
		reset()
		d.Reason = "invalid_observation"
		return d
	}
	if m.Pending {
		reset()
		d.Reason = "operation_pending"
		return d
	}
	if s.LastTick.IsZero() || !now.After(s.LastTick) || now.Sub(s.LastTick) > 2*time.Second {
		reset()
	}
	if !s.LastChange.IsZero() && now.Sub(s.LastChange) < 5*time.Second {
		reset()
		d.Reason = "cooldown"
		return d
	}
	if m.CPU >= b.CPUBackoff || m.Memory >= b.MemoryBackoff {
		reset()
		d.Target = max(b.Minimum, int(math.Floor(float64(m.Target)*.8)))
		d.Reason = "pressure_backoff"
		return d
	}
	if frozen {
		reset()
		d.Reason = "frozen"
		return d
	}
	healthy := m.CPU < b.CPUGrow && m.Memory < b.MemoryGrow
	busy := m.Working - m.Idle
	if healthy && m.Waiting > 0 && busy >= m.Target && m.Working == m.Target {
		d.State.QuietSince = time.Time{}
		if d.State.DemandSince.IsZero() {
			d.State.DemandSince = now
		}
		if now.Sub(d.State.DemandSince) >= 5*time.Second {
			d.Target = min(b.Maximum, m.Target+max(1, int(math.Ceil(float64(m.Target)*.25))))
			d.Reason = "sustained_demand"
		}
	} else {
		d.State.DemandSince = time.Time{}
		if m.Waiting == 0 && busy == 0 && (m.Working == m.Target || (m.PressureMaxAge > 0 && m.Working < m.Target)) {
			if d.State.QuietSince.IsZero() {
				d.State.QuietSince = now
			}
			if now.Sub(d.State.QuietSince) >= 30*time.Second {
				d.Target = max(b.Minimum, int(math.Ceil(float64(m.Target)*.9)))
				if d.Target == m.Target && m.Target > b.Minimum {
					d.Target--
				}
				d.Reason = "quiet_reduction"
			}
		} else {
			d.State.QuietSince = time.Time{}
		}
	}
	return d
}
