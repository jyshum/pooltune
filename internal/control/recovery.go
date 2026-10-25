package control

import (
	"context"
	"encoding/json"
	"fmt"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/scheduler"
	"time"
)

type RecoveryRequest struct {
	Target                  string
	Path                    domain.PathKind
	PreviousEpoch, NewEpoch uint64
	StableRevision          domain.RevisionID
}
type RecoveryEvidence struct {
	CapacityLimit                  *int
	PreviousEpoch                  uint64
	StableRevision                 domain.RevisionID
	ObservedAt                     time.Time
	NoOrphanWork, CapacityVerified bool
}

// Recover requires a trusted reconciler to compare the independent control ledger
// and inspect old backend work. A process restart never proves completion.
func (e *Engine) Recover(ctx context.Context) error {
	return e.RecoverConfirmed(ctx, domain.ActivationEnvelope{})
}

func (e *Engine) RecoverConfirmed(ctx context.Context, confirmed domain.ActivationEnvelope) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.ready.Load() || e.cfg.Reconcile == nil || e.state.Stable.Revision == "" {
		return ErrRecovery
	}
	if e.anchor != nil && e.state.PreviousEpoch > 0 && (confirmed.Checksum != Checksum(confirmed) || confirmed.Checksum != e.state.Active.Checksum) {
		return ErrRecovery
	}
	request := RecoveryRequest{Target: e.cfg.Target, Path: e.cfg.Path, PreviousEpoch: e.state.PreviousEpoch, NewEpoch: e.cfg.Epoch, StableRevision: e.state.Stable.Revision}
	proof, err := e.cfg.Reconcile(ctx, request)
	now := time.Now()
	if err != nil || ctx.Err() != nil || proof.PreviousEpoch != request.PreviousEpoch || proof.StableRevision != request.StableRevision || !proof.NoOrphanWork || !proof.CapacityVerified || proof.ObservedAt.After(now) || now.Sub(proof.ObservedAt) > time.Second {
		return ErrRecovery
	}
	env := e.state.Stable
	if e.state.PreviousEpoch > 0 {
		env.ExpectedPredecessor = e.state.Active.Revision
		env.Revision = domain.RevisionID(fmt.Sprintf("recovery-%d", e.cfg.Epoch))
	}
	env.Epoch = e.cfg.Epoch
	env.Limits = append([]domain.LimitSnapshot(nil), env.Limits...)
	if proof.CapacityLimit != nil {
		if *proof.CapacityLimit < 0 {
			return ErrRecovery
		}
		env.Limits[0].Effective = min(env.Limits[0].Effective, *proof.CapacityLimit)
		var policies map[string]policy.Policy
		if json.Unmarshal(env.PolicyJSON, &policies) != nil {
			return ErrRecovery
		}
		limit := env.Limits[0].Effective
		env.Limits[0].CriticalReserve = max(0, min(int(policies["stable"].ReserveFraction*float64(limit)), limit-1))
	}
	env.Limits[0].Epoch = e.cfg.Epoch
	env.Limits[0].Revision = env.Revision
	env.Limits[0].CohortBudgets = map[string]int{"stable": env.Limits[0].Effective}
	env.Checksum = Checksum(env)
	runtime, err := e.prepare(env, true)
	if err != nil {
		return err
	}
	next := e.state
	next.Active = env
	next.ActiveAt = time.Now()
	if e.state.PreviousEpoch > 0 {
		next.Fallbacks = append(append([]domain.RevisionID(nil), next.Fallbacks...), env.Revision)
	}
	next.Stable = env
	if err = e.persist(next); err != nil {
		return ErrStorage
	}
	if err = e.s.Install(scheduler.Snapshot{Limits: env.Limits[0], Policies: runtime.policies}); err != nil {
		return err
	}
	e.state = next
	e.runtime.Store(runtime)
	e.ready.Store(true)
	return nil
}
