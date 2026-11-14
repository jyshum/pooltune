package control

import (
	"context"
	"pooltune/internal/domain"
	"pooltune/internal/store"
)

type recoveryClient interface {
	View(context.Context) (StateView, error)
	RecoverConfirmed(context.Context, domain.ActivationEnvelope) error
}

// Recover compares the independently held ledger and its full payload before
// asking traffic to perform its own epoch and backend reconciliation.
func (c *Coordinator) Recover(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	client, ok := c.client.(recoveryClient)
	if !ok {
		return ErrRecovery
	}
	v, err := client.View(ctx)
	if err != nil || v.Ready || v.Active.Epoch >= v.Epoch {
		return ErrRecovery
	}
	pending, err := c.ledger.Pending(c.target)
	if err != nil {
		return ErrStorage
	}
	// A durable rejection proves this intent never changed traffic. Other
	// uncertain old-epoch operations still require independent lineage repair.
	for _, p := range pending {
		if !isSafetyResume(domain.RevisionID(p.ID)) {
			return ErrRecovery
		}
		if ack, err := c.reconcileLocked(ctx, domain.RevisionID(p.ID), true); err != nil || ack.State != "rejected" {
			return ErrRecovery
		}
	}
	pending, err = c.ledger.Pending(c.target)
	if err != nil || len(pending) != 0 {
		return ErrRecovery
	}
	active, err := c.ledger.ActiveRevision(c.target)
	if err != nil || active.ID != string(v.Active.Revision) {
		return ErrRecovery
	}
	key := eventID(c.target, v.Active.Revision, store.KindIntent)
	raw, err := c.payloads.Load("intent-" + key + ".json")
	var confirmed domain.ActivationEnvelope
	if err == nil {
		err = decode(raw, &confirmed)
	} else {
		raw, err = c.payloads.Load("observed-" + key + ".json")
		var record struct {
			Observed      domain.ActivationEnvelope
			LastConfirmed string
		}
		if err == nil {
			err = decode(raw, &record)
			confirmed = record.Observed
		}
	}
	if err != nil || confirmed.Checksum != Checksum(confirmed) || confirmed.Checksum != v.Active.Checksum {
		return ErrRecovery
	}
	return client.RecoverConfirmed(ctx, confirmed)
}
