package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/scheduler"
	"pooltune/internal/store"
	"strings"
	"time"
)

// AuthorityChecksum binds the immutable approved routing and numeric boundaries,
// not just their operator-supplied version label. No selectors cross this API.
func AuthorityChecksum(bounds policy.Guardrails, registry policy.Registry) string {
	bounds.CurrentPolicy = nil
	bounds.CurrentRevision = ""
	raw, err := json.Marshal(struct {
		Bounds   policy.Guardrails
		Registry policy.Registry
	}{bounds, registry})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

type StateView struct {
	PhysicalBlocked          bool
	ResumeCapacity           int
	SafetyCeiling            *int
	AuthoritySHA256          string
	ActiveAt                 time.Time
	Cohorts                  map[string]scheduler.CohortStats
	Active                   domain.ActivationEnvelope
	Epoch                    uint64
	Ready, AuditGap, Drained bool
}

func (e *Engine) View() StateView {
	e.mu.Lock()
	defer e.mu.Unlock()
	raw, _ := json.Marshal(e.state.Active)
	var env domain.ActivationEnvelope
	_ = json.Unmarshal(raw, &env)
	var ceiling *int
	if e.state.SafetyCeiling != nil {
		value := *e.state.SafetyCeiling
		ceiling = &value
	}
	blocked := e.cfg.PhysicalBlocked != nil && e.cfg.PhysicalBlocked()
	return StateView{PhysicalBlocked: blocked, ResumeCapacity: e.resumeCapacityLocked(), SafetyCeiling: ceiling, AuthoritySHA256: AuthorityChecksum(e.cfg.Guardrails, e.cfg.Registry), ActiveAt: e.state.ActiveAt, Cohorts: e.s.Stats().Cohorts, Active: env, Epoch: e.cfg.Epoch, Ready: e.ready.Load(), AuditGap: e.auditGap, Drained: e.s.Drained()}
}
func (e *Engine) AcknowledgeFallback(revision domain.RevisionID, checksum string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if (!strings.HasPrefix(string(revision), "fallback-") && !strings.HasPrefix(string(revision), "recovery-")) || e.state.Active.Revision != revision || e.state.Active.Checksum != checksum {
		return ErrStale
	}
	next := e.state
	next.Fallbacks = nil
	if e.persist(next) != nil {
		return ErrStorage
	}
	e.state = next
	e.auditGap = false
	return nil
}

type historyClient interface {
	View(context.Context) (StateView, error)
	AcknowledgeFallback(context.Context, domain.RevisionID, string) error
}

func (c *Client) View(ctx context.Context) (StateView, error) {
	var out StateView
	err := c.call(ctx, "GET", "/state", nil, &out)
	return out, err
}
func (c *Client) AcknowledgeFallback(ctx context.Context, revision domain.RevisionID, checksum string) error {
	return c.call(ctx, "POST", "/ack-fallback", struct {
		Revision domain.RevisionID
		Checksum string
	}{revision, checksum}, nil)
}

// SyncFallback records the observed recovery/fallback as a NEW revision. A prior
// missing ACK stays uncertain; it is not retroactively fabricated as confirmed.
func (c *Coordinator) SyncFallback(ctx context.Context) (ActivationAck, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	client, ok := c.client.(historyClient)
	if !ok {
		return ActivationAck{}, ErrInvalid
	}
	view, err := client.View(ctx)
	if err != nil {
		return ActivationAck{}, err
	}
	env := view.Active
	if !view.Ready || env.Epoch != view.Epoch || env.Checksum != Checksum(env) || len(env.Limits) != 1 || env.Limits[0].Target != c.owner || env.Limits[0].Path != c.path || (!strings.HasPrefix(string(env.Revision), "fallback-") && !strings.HasPrefix(string(env.Revision), "recovery-")) {
		return ActivationAck{}, ErrInvalid
	}
	active, err := c.ledger.ActiveRevision(c.target)
	if err != nil && !errors.Is(err, store.ErrNoActiveRevision) {
		return ActivationAck{}, ErrStorage
	}
	if active.ID != string(env.Revision) {
		// Keep both the actual runtime lineage and the last confirmed ledger parent.
		record := struct {
			Observed      domain.ActivationEnvelope
			LastConfirmed string
		}{env, active.ID}
		raw, _ := json.Marshal(record)
		if c.payloads.Save("observed-"+eventID(c.target, env.Revision, store.KindIntent)+".json", raw) != nil {
			return ActivationAck{}, ErrStorage
		}
		for _, kind := range []store.EventKind{store.KindIntent, store.KindConfirmed} {
			id := eventID(c.target, env.Revision, kind)
			if err = c.ledger.Append(store.Event{ID: id, IdempotencyKey: id, Target: c.target, Actor: "traffic-recovery", Kind: kind, Revision: string(env.Revision), Predecessor: active.ID}); err != nil {
				return ActivationAck{}, ErrStorage
			}
		}
	}
	if err = client.AcknowledgeFallback(ctx, env.Revision, env.Checksum); err != nil {
		return ActivationAck{}, err
	}
	return ActivationAck{Revision: env.Revision, Epoch: view.Epoch, State: "active"}, nil
}

// AdoptInitial records a separately bootstrapped, ready, stable-only genesis.
// It never sends an activation. A successor without its control history is not
// a new installation and cannot be adopted as one after a database restore.
func (c *Coordinator) AdoptInitial(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	client, ok := c.client.(historyClient)
	if !ok {
		return ErrInvalid
	}
	v, err := client.View(ctx)
	if err != nil {
		return err
	}
	env := v.Active
	if !v.Ready || v.AuditGap || env.Checksum != Checksum(env) || env.Epoch != v.Epoch || len(env.Limits) != 1 || env.Limits[0].Target != c.owner || env.Limits[0].Path != c.path || env.ExpectedPredecessor != "" || env.CandidateFraction != 0 || !env.LeaseExpiresAt.IsZero() {
		return ErrRecovery
	}
	active, err := c.ledger.ActiveRevision(c.target)
	if err == nil {
		if active.ID == string(env.Revision) {
			return nil
		}
		return ErrStale
	}
	if !errors.Is(err, store.ErrNoActiveRevision) {
		return ErrStorage
	}
	pending, err := c.ledger.Pending(c.target)
	if err != nil {
		return ErrStorage
	}
	for _, p := range pending {
		if p.ID != string(env.Revision) {
			return ErrRecovery
		}
	}
	raw, _ := json.Marshal(env)
	if c.payloads.Save("intent-"+eventID(c.target, env.Revision, store.KindIntent)+".json", raw) != nil {
		return ErrStorage
	}
	if err = c.append(env, store.KindIntent); err != nil {
		return err
	}
	return c.append(env, store.KindConfirmed)
}
