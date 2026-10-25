package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"pooltune/internal/controller"
	"pooltune/internal/domain"
	"time"
)

type PhysicalChange struct {
	Revision              domain.RevisionID
	Checksum, ID, Version string
	Target                int
}

func (s *Service) physicalTick(ctx context.Context, now time.Time, v StateView, m controller.MetricsSnapshot, quiet time.Time) (bool, error) {
	if m.Pool == nil || m.Pool.Pending || v.SafetyCeiling != nil {
		return false, nil
	}
	d, ok := capacityPlan(now, v, m, s.cfg.Capacity.Guardrails, false, quiet)
	if !ok || d.PhysicalDesired == nil || *d.PhysicalDesired == m.Pool.Desired || d.Demand == nil {
		return false, nil
	}
	target := *d.PhysicalDesired
	// Never reduce the pool ahead of the installed admission revision. Quiet
	// reclamation waits for idle work; a lower target does not evict transactions.
	if target < v.Active.Limits[0].Effective {
		return false, nil
	}
	if target < m.Pool.Desired {
		if quiet.IsZero() || now.Sub(quiet) < controller.QuietWindow {
			return false, nil
		}
		for _, g := range m.Local.Groups {
			if g.Active != 0 || g.Queued != 0 || g.Uncertain != 0 {
				return false, nil
			}
		}
	}
	client, ok := s.client.(interface {
		ApplyPhysical(context.Context, PhysicalChange) error
	})
	if !ok {
		return false, ErrInvalid
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%d", v.Active.Checksum, m.Pool.Version, target)))
	request := PhysicalChange{Revision: v.Active.Revision, Checksum: v.Active.Checksum, ID: "physical-" + hex.EncodeToString(sum[:]), Version: m.Pool.Version, Target: target}
	return true, client.ApplyPhysical(ctx, request)
}
func (e *Engine) applyPhysical(ctx context.Context, r PhysicalChange) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || !e.ready.Load() || e.auditGap || e.cfg.PhysicalApply == nil || e.cfg.CapacityMetrics == nil || len(e.state.Active.Limits) != 1 || e.state.Active.Revision != r.Revision || e.state.Active.Checksum != r.Checksum || e.state.Active.CandidateFraction != 0 || e.state.SafetyCeiling != nil || !revisionPattern.MatchString(r.ID) {
		return ErrStale
	}
	m := e.cfg.CapacityMetrics()
	stats := e.s.Stats()
	now := time.Now()
	if m.Pool == nil || m.Pool.Pending || m.Pool.Version != r.Version || !m.Physical.Valid || now.Sub(m.Physical.At) > 2*time.Second || stats.Uncertain != 0 || r.Target < e.state.Active.Limits[0].Effective || r.Target > m.Physical.PGAllocation {
		return ErrRejected
	}
	if r.Target < m.Pool.Desired && (stats.Active != 0 || stats.Queued != 0 || now.Sub(stats.LastQueuedAt) < controller.QuietWindow) {
		return ErrRejected
	}
	// Management is outside the scheduler accounting lock. It changes physical
	// availability only. Admission still needs its own guarded durable activation.
	return e.cfg.PhysicalApply(ctx, r)
}
func (e *Engine) physicalRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /physical", func(w http.ResponseWriter, r *http.Request) {
		var input PhysicalChange
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil || decode(raw, &input) != nil {
			http.Error(w, "invalid physical target", 400)
			return
		}
		if e.applyPhysical(r.Context(), input) != nil {
			http.Error(w, "physical target rejected or uncertain", 409)
			return
		}
		json.NewEncoder(w).Encode(struct{}{})
	})
}
func (c *Client) ApplyPhysical(ctx context.Context, r PhysicalChange) error {
	return c.call(ctx, "POST", "/physical", r, nil)
}
