package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"pooltune/internal/domain"
	"pooltune/internal/rollout"
	"pooltune/internal/scheduler"
	"pooltune/internal/store"
	"testing"
	"time"
)

type engineClient struct{ e *Engine }

func (c engineClient) Activate(ctx context.Context, env domain.ActivationEnvelope) (ActivationAck, error) {
	return c.e.Activate(ctx, env)
}
func (c engineClient) Status(_ context.Context, r domain.RevisionID) (ActivationAck, error) {
	return c.e.Status(r), nil
}
func (c engineClient) View(context.Context) (StateView, error) { return c.e.View(), nil }
func (c engineClient) AcknowledgeFallback(_ context.Context, r domain.RevisionID, s string) error {
	return c.e.AcknowledgeFallback(r, s)
}
func (c engineClient) Rollback(_ context.Context, r domain.RevisionID) error {
	c.e.mu.Lock()
	defer c.e.mu.Unlock()
	if c.e.state.Active.Revision != r {
		return ErrStale
	}
	return c.e.fallbackLocked()
}

func TestDriverDrainsStableBeforeFullBudgetAndAccepts(t *testing.T) {
	e, s, p := readyEngine(t)
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	client := engineClient{e}
	coord, err := NewCoordinator("db", domain.PathPostgres, dir, db, client)
	if err != nil {
		t.Fatal(err)
	}
	defer coord.Close()
	// Import initial activation through the normal intent/confirmation path.
	initial := e.View().Active
	initial.Limits[0].CapacityRevision = "capacity1"
	initial.Limits[0].Effective = 20
	initial.Limits[0].CohortBudgets = map[string]int{"stable": 20}
	// Existing initial checksum cannot change; use a separate capacity activation.
	initial.ExpectedPredecessor = initial.Revision
	initial.Revision = "capacity1"
	initial.Limits[0].Revision = initial.Revision
	initial.Checksum = Checksum(initial)
	// First mirror original bootstrap before its successor.
	if _, err = coord.Apply(t.Context(), e.View().Active); err != nil {
		t.Fatal(err)
	}
	if _, err = coord.Apply(t.Context(), initial); err != nil {
		t.Fatal(err)
	}
	d, err := NewRolloutDriver("db", domain.PathPostgres, coord, client, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidate := p
	candidate.Revision = "p2"
	candidate.ExpectedPredecessor = "p1"
	candidate.ClassWeights.Normal = 2
	hash := sha256.Sum256([]byte("e1"))
	prefix := "rollout-" + hex.EncodeToString(hash[:8]) + "-"
	command := rollout.Command{ExperimentID: "e1", Revision: prefix + "1", ExpectedRevision: "capacity1", GuardrailVersion: "g1", CapacityRevision: "capacity1", Path: domain.PathPostgres, Epoch: 1, Phase: rollout.Measuring, Effective: 20, StableBudget: 18, CandidateBudget: 2, Fraction: .1, StablePolicy: p, CandidatePolicy: candidate, Expires: time.Now().Add(30 * time.Second)}
	if a, err := d.Apply(t.Context(), command); err != nil || !a.Active {
		t.Fatalf("initial %+v %v", a, err)
	}
	held, err := s.Acquire(t.Context(), e.Classify(domain.RequestContext{Target: "db", Path: domain.PathPostgres}, "stable"))
	if err != nil {
		t.Fatal(err)
	}
	command.ExpectedRevision = command.Revision
	command.Revision = prefix + "2"
	command.Phase = rollout.Observing
	command.Fraction = 1
	command.StableBudget = 0
	command.CandidateBudget = 20
	if a, err := d.Apply(t.Context(), command); err != nil || a.Active {
		t.Fatalf("must wait for stable drain: %+v %v", a, err)
	}
	if s.Stats().Cohorts["stable"].Budget != 18 || e.View().Active.CandidateFraction != 1 {
		t.Fatal("drain cut old capacity or kept old routing")
	}
	s.Release(held, scheduler.Finished)
	if a, err := d.Status(t.Context(), command.Revision); err != nil || !a.Active {
		t.Fatalf("full transition %+v %v", a, err)
	}
	command.ExpectedRevision = command.Revision
	command.Revision = prefix + "3"
	command.Phase = rollout.Accepted
	if a, err := d.Apply(t.Context(), command); err != nil || !a.Active {
		t.Fatalf("accept %+v %v", a, err)
	}
	var policies map[string]json.RawMessage
	json.Unmarshal(e.View().Active.PolicyJSON, &policies)
	restarted, err := NewRolloutDriver("db", domain.PathPostgres, coord, client, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ack, err := restarted.Status(t.Context(), command.Revision); err != nil || !ack.Active || ack.At != e.View().ActiveAt {
		t.Fatal("accepted revision cannot be reconciled after restart")
	}
	if len(policies) != 1 || e.View().Active.CandidateFraction != 0 {
		t.Fatal("acceptance not stable-only")
	}
	r, err := d.Inspect(t.Context(), domain.PathPostgres)
	if err != nil || r.PolicyRevision != "p2" || r.ActivationRevision != command.Revision {
		t.Fatal("mixed revision identities")
	}
}

func TestFullRoutingFencesLateStableClassification(t *testing.T) {
	e, s, p := readyEngine(t)
	old := e.Classify(domain.RequestContext{Target: "db", Path: domain.PathPostgres}, "stable")
	p.Revision = "p2"
	p.ExpectedPredecessor = "p1"
	p.ClassWeights.Normal = 2
	full := splitEnvelope(t, e, "all-routing", p, 1, map[string]int{"stable": 8, "candidate": 2})
	if _, err := e.Activate(t.Context(), full); err != nil {
		t.Fatal(err)
	}
	permit, err := s.Acquire(t.Context(), old)
	if permit != nil {
		s.Release(permit, scheduler.Finished)
	}
	if err != scheduler.ErrCohortStopped {
		t.Fatal("late stable classification escaped routing fence")
	}
}
