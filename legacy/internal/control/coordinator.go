package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"pooltune/internal/domain"
	"pooltune/internal/durable"
	"pooltune/internal/store"
	"strconv"
	"sync"
)

type TrafficClient interface {
	Activate(context.Context, domain.ActivationEnvelope) (ActivationAck, error)
	Status(context.Context, domain.RevisionID) (ActivationAck, error)
}

// Coordinator is the single control writer. Its snapshot directory is separate
// from the traffic directory and contains complete intent payloads for recovery.
type Coordinator struct {
	mu       sync.Mutex
	ledger   *store.Store
	payloads *durable.Files
	client   TrafficClient
	target   string
	owner    string
	path     domain.PathKind
}

func NewCoordinator(target string, path domain.PathKind, directory string, ledger *store.Store, client TrafficClient) (*Coordinator, error) {
	if !revisionPattern.MatchString(target) || (path != domain.PathPostgres && path != domain.PathREST) || ledger == nil || client == nil {
		return nil, ErrInvalid
	}
	files, err := durable.Open(directory)
	if err != nil {
		return nil, err
	}
	scope := sha256.Sum256([]byte(target + "/" + string(path)))
	return &Coordinator{ledger: ledger, payloads: files, client: client, target: "activation-" + hex.EncodeToString(scope[:]), owner: target, path: path}, nil
}
func (c *Coordinator) LedgerTarget() string { return c.target }
func (c *Coordinator) Close() error         { return c.payloads.Close() }
func eventID(target string, revision domain.RevisionID, kind store.EventKind) string {
	sum := sha256.Sum256([]byte(target + "/" + string(revision) + "/" + string(kind)))
	return hex.EncodeToString(sum[:])
}
func (c *Coordinator) append(e domain.ActivationEnvelope, kind store.EventKind) error {
	id := eventID(c.target, e.Revision, kind)
	return c.ledger.Append(store.Event{ID: id, IdempotencyKey: id, Target: c.target, Actor: "control", Kind: kind, Revision: string(e.Revision), Predecessor: string(e.ExpectedPredecessor)})
}
func (c *Coordinator) Apply(ctx context.Context, e domain.ActivationEnvelope) (ActivationAck, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(e.Limits) != 1 || e.Limits[0].Target != c.owner || e.Limits[0].Path != c.path || e.Checksum != Checksum(e) || !revisionPattern.MatchString(string(e.Revision)) {
		return ActivationAck{}, ErrInvalid
	}
	if r, err := c.ledger.Rejection(c.target, string(e.Revision)); err == nil {
		if r.Checksum != e.Checksum {
			return ActivationAck{}, ErrStale
		}
		return ActivationAck{}, ErrRejected
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ActivationAck{}, ErrStorage
	}
	pending, err := c.ledger.Pending(c.target)
	if err != nil {
		return ActivationAck{}, ErrStorage
	}
	active, err := c.ledger.ActiveRevision(c.target)
	if err != nil && !errors.Is(err, store.ErrNoActiveRevision) {
		return ActivationAck{}, ErrStorage
	}
	for _, p := range pending {
		if p.ID != string(e.Revision) && p.Predecessor == active.ID {
			return ActivationAck{}, ErrRecovery
		}
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return ActivationAck{}, ErrInvalid
	}
	key := "intent-" + eventID(c.target, e.Revision, store.KindIntent) + ".json"
	existing, err := c.payloads.Load(key)
	if err == nil {
		if !bytes.Equal(existing, raw) {
			return ActivationAck{}, ErrStale
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ActivationAck{}, ErrStorage
	} else if c.payloads.Save(key, raw) != nil {
		return ActivationAck{}, ErrStorage
	}
	if err = c.append(e, store.KindIntent); err != nil {
		return ActivationAck{}, err
	}
	ack, err := c.client.Activate(ctx, e)
	if err != nil {
		_ = c.append(e, store.KindUncertain)
		return ActivationAck{}, err
	}
	if ack.Revision != e.Revision || ack.Epoch != e.Epoch {
		return ActivationAck{}, ErrStale
	}
	if ack.State == "rejected" {
		if ack.Checksum != e.Checksum {
			return ack, ErrStale
		}
		if err = c.ledger.RecordRejection(c.target, string(e.Revision), e.Checksum, e.Epoch); err != nil {
			return ack, ErrStorage
		}
		return ack, ErrRejected
	}
	if ack.State == "active" {
		if err = c.append(e, store.KindConfirmed); err != nil {
			return ack, ErrStorage
		}
	}
	return ack, nil
}

// Reconcile queries status after a lost ACK. It never resends a provider write.
func (c *Coordinator) Reconcile(ctx context.Context, revision domain.RevisionID) (ActivationAck, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reconcileLocked(ctx, revision, false)
}
func (c *Coordinator) reconcileLocked(ctx context.Context, revision domain.RevisionID, rejectionOnly bool) (ActivationAck, error) {
	raw, err := c.payloads.Load("intent-" + eventID(c.target, revision, store.KindIntent) + ".json")
	if err != nil {
		return ActivationAck{}, ErrStorage
	}
	var e domain.ActivationEnvelope
	if decode(raw, &e) != nil {
		return ActivationAck{}, ErrStorage
	}
	if r, err := c.ledger.Rejection(c.target, string(revision)); err == nil {
		epoch, _ := strconv.ParseUint(r.Epoch, 10, 64)
		return ActivationAck{Revision: revision, Epoch: epoch, Checksum: r.Checksum, State: "rejected"}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ActivationAck{}, ErrStorage
	}
	ack, err := c.client.Status(ctx, revision)
	if err != nil {
		return ack, err
	}
	if ack.State == "rejected" {
		if ack.Revision != revision || ack.Epoch != e.Epoch || ack.Checksum != e.Checksum {
			return ack, ErrStale
		}
		if err = c.ledger.RecordRejection(c.target, string(revision), e.Checksum, e.Epoch); err != nil {
			return ack, ErrStorage
		}
		return ack, nil
	}
	if rejectionOnly {
		return ack, ErrRecovery
	}
	if ack.State == "active" {
		if ack.Revision != revision || ack.Epoch != e.Epoch {
			return ack, ErrStale
		}
		if err = c.append(e, store.KindConfirmed); err != nil {
			return ack, ErrStorage
		}
	}
	return ack, nil
}
