package burst

import (
	"context"
	"fmt"
	"pooltune/internal/admin"
	"pooltune/internal/provider"
	"strings"
	"time"
)

// DashboardBackend exposes observations only. Runtime ownership and admission
// controls remain with the gateway process, not browser requests.
type DashboardBackend struct{ Controller *Controller }
type DashboardState struct {
	Transactions          *provider.Transactions `json:"transactions"`
	Mode                  string                 `json:"mode"`
	Revision              string                 `json:"revision"`
	Target                string                 `json:"target"`
	Provider              string                 `json:"provider"`
	ObservedAt            time.Time              `json:"observed_at"`
	HealthAt              time.Time              `json:"health_at"`
	PressureAt            time.Time              `json:"pressure_at"`
	PressureAgeSeconds    float64                `json:"pressure_age_seconds"`
	PressureMaxAgeSeconds float64                `json:"pressure_max_age_seconds"`
	CPU                   float64                `json:"cpu"`
	Memory                float64                `json:"memory"`
	Base                  int                    `json:"base"`
	Ceiling               int                    `json:"ceiling"`
	Warm                  int                    `json:"warm"`
	Effective             int                    `json:"effective"`
	Active                int                    `json:"active"`
	Queued                int                    `json:"queued"`
	Uncertain             int                    `json:"uncertain"`
	Ready                 bool                   `json:"ready"`
	Frozen                bool                   `json:"frozen"`
	HealthVerified        bool                   `json:"health_verified"`
	Reason                string                 `json:"reason"`
}

func (b DashboardBackend) Read(_ context.Context, resource string) (any, error) {
	if resource != "state" || b.Controller == nil {
		return nil, admin.ErrDenied
	}
	c := b.Controller
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	s := c.Scheduler.Stats()
	h := c.health
	kind := strings.SplitN(c.cfg.PressureSource, ":", 2)[0]
	if kind != "azure" && kind != "google" && kind != "supabase" {
		kind = "local"
	}
	verified := c.valid(now)
	age := -1.0
	if !h.PressureAt.IsZero() {
		age = now.Sub(h.PressureAt).Seconds()
	}
	return DashboardState{Transactions: h.Transactions, Mode: "managed-burst", Revision: fmt.Sprintf("burst-%d", c.state.Changes), Target: c.cfg.Target, Provider: kind, ObservedAt: now, HealthAt: h.At, PressureAt: h.PressureAt, PressureAgeSeconds: age, PressureMaxAgeSeconds: c.cfg.PressureMaxAge.Seconds(), CPU: h.CPU, Memory: h.Memory, Base: c.cfg.Base, Ceiling: c.cfg.Ceiling, Warm: h.Warm, Effective: s.Effective, Active: s.Active, Queued: s.Queued, Uncertain: s.Uncertain, Ready: c.enabled && !c.latched && s.Effective > 0 && verified, Frozen: c.state.Frozen, HealthVerified: verified, Reason: c.state.Reason}, nil
}
func (b DashboardBackend) Execute(context.Context, string, admin.Command) (any, error) {
	return nil, admin.ErrDenied
}
