package control

import (
	"context"
	"encoding/json"
	"os"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/scheduler"
	"testing"
	"time"
)

func fixture(t *testing.T, dir string, epoch uint64, reconcile func(context.Context, RecoveryRequest) (RecoveryEvidence, error)) (*Engine, *scheduler.Scheduler) {
	t.Helper()
	os.Chmod(dir, 0700)
	g := policy.Guardrails{Version: "g1", MaxReserveFraction: .9, MinClassWeight: 1, MaxClassWeight: 10, MaxChangedEntries: 10}
	reg := policy.Registry{Fallback: policy.Fallback{QueueWait: time.Second, QueueLimit: 10, ConcurrencyCap: 10}}
	p, e := policy.Compile(policy.Policy{SchemaVersion: "1", Revision: "bootstrap", GuardrailVersion: "g1", ClassWeights: policy.ClassWeights{Critical: 1, Normal: 1, Background: 1}}, g, reg)
	if e != nil {
		t.Fatal(e)
	}
	s, e := scheduler.New(scheduler.Config{MaxActive: 20, MaxQueued: 10, MaxQueuedBytes: 1 << 20, MaxTenants: 10, MaxWorkloads: 10, MaxTenantQueued: 10, MaxTenantActive: 10, RatePerSecond: 1000, RateBurst: 100}, scheduler.Snapshot{Limits: domain.LimitSnapshot{Target: "db", Path: domain.PathPostgres, Revision: "bootstrap", Effective: 0, CohortBudgets: map[string]int{"stable": 0}, GuardrailVersion: "g1", Epoch: epoch}, Policies: map[string]*policy.CompiledPolicy{"stable": p}})
	if e != nil {
		t.Fatal(e)
	}
	engine, e := New(Config{Target: "db", Path: domain.PathPostgres, Epoch: epoch, Directory: dir, Guardrails: g, Registry: reg, MaxCapacity: 20, MaxLease: time.Minute, Reconcile: reconcile}, s)
	if e != nil {
		t.Fatal(e)
	}
	return engine, s
}
func envelope(revision, prior string, epoch uint64) domain.ActivationEnvelope {
	p := policy.Policy{SchemaVersion: "1", Revision: domain.RevisionID(revision), ExpectedPredecessor: domain.RevisionID(prior), GuardrailVersion: "g1", ClassWeights: policy.ClassWeights{Critical: 1, Normal: 1, Background: 1}}
	raw, _ := json.Marshal(map[string]policy.Policy{"stable": p})
	e := domain.ActivationEnvelope{Revision: domain.RevisionID(revision), ExpectedPredecessor: domain.RevisionID(prior), Epoch: epoch, SchemaVersion: "1", GuardrailVersion: "g1", PolicyJSON: raw, Limits: []domain.LimitSnapshot{{Target: "db", Path: domain.PathPostgres, Revision: domain.RevisionID(revision), Effective: 10, CohortBudgets: map[string]int{"stable": 10}, GuardrailVersion: "g1", Epoch: epoch}}}
	e.Checksum = Checksum(e)
	return e
}
func approved(_ context.Context, r RecoveryRequest) (RecoveryEvidence, error) {
	return RecoveryEvidence{PreviousEpoch: r.PreviousEpoch, StableRevision: r.StableRevision, ObservedAt: time.Now(), NoOrphanWork: true, CapacityVerified: true}, nil
}
func TestActivationIdempotencyAndRestartQuarantine(t *testing.T) {
	dir := t.TempDir()
	e, s := fixture(t, dir, 1, approved)
	initial := envelope("p1", "", 1)
	ack, err := e.Activate(context.Background(), initial)
	if err != nil || ack.State != "prepared" || s.Stats().Effective != 0 {
		t.Fatalf("bootstrap %v %+v", err, ack)
	}
	if err = e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Stats().Effective != 10 {
		t.Fatal("recovery did not install")
	}
	ack, err = e.Activate(context.Background(), initial)
	if err != nil || ack.State != "active" {
		t.Fatal("lost ACK retry not idempotent")
	}
	stale := envelope("p2", "wrong", 1)
	if _, err = e.Activate(context.Background(), stale); err == nil {
		t.Fatal("stale predecessor accepted")
	}
	next := envelope("p2", "p1", 1)
	if _, err = e.Activate(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	e.Close()
	restarted, s2 := fixture(t, dir, 2, func(_ context.Context, r RecoveryRequest) (RecoveryEvidence, error) { return RecoveryEvidence{}, nil })
	defer restarted.Close()
	if s2.Stats().Effective != 0 || restarted.Ready() {
		t.Fatal("restart resumed old capacity")
	}
	if err = restarted.Recover(context.Background()); err == nil || s2.Stats().Effective != 0 {
		t.Fatal("orphan uncertainty cleared")
	}
}
func TestStorageFailureNeverInstalls(t *testing.T) {
	e, s := fixture(t, t.TempDir(), 1, approved)
	defer e.Close()
	e.Activate(context.Background(), envelope("p1", "", 1))
	if err := e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.persist = func(snapshot) error { return ErrStorage }
	if _, err := e.Activate(context.Background(), envelope("p2", "p1", 1)); err != ErrStorage {
		t.Fatal(err)
	}
	if e.Status("p1").State != "active" || s.Stats().Effective != 10 {
		t.Fatal("unpersisted state installed")
	}
}

func TestLeaseExpiryRetainsActiveCandidateAndCreatesRevision(t *testing.T) {
	e, s := fixture(t, t.TempDir(), 1, approved)
	defer e.Close()
	initial := envelope("p1", "", 1)
	if _, err := e.Activate(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	if err := e.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	candidate := envelope("c1", "p1", 1)
	var stable map[string]policy.Policy
	json.Unmarshal(initial.PolicyJSON, &stable)
	p2 := stable["stable"]
	p2.Revision = "p2"
	p2.ExpectedPredecessor = "p1"
	candidate.PolicyJSON, _ = json.Marshal(map[string]policy.Policy{"stable": stable["stable"], "candidate": p2})
	candidate.Limits[0].CohortBudgets = map[string]int{"stable": 9, "candidate": 1}
	candidate.LeaseExpiresAt = time.Now().Add(80 * time.Millisecond)
	candidate.Checksum = Checksum(candidate)
	if _, err := e.Activate(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	c := e.Classify(domain.RequestContext{Target: "db", Path: domain.PathPostgres}, "candidate")
	permit, err := s.Acquire(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !s.Stats().Cohorts["candidate"].Stopped && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	e.mu.Lock()
	current := e.state.Active.Revision
	e.mu.Unlock()
	if current == "p1" || current == "c1" || s.Stats().Active != 1 || !s.Stats().Cohorts["candidate"].Stopped {
		t.Fatal("lease fallback lost binding or reused revision")
	}
	if permit.Classification().Revision != "p2" {
		t.Fatal("active work relabeled")
	}
	s.Release(permit, scheduler.Finished)
	c2 := candidate
	c2.Revision = "c2"
	c2.ExpectedPredecessor = current
	c2.Limits = append([]domain.LimitSnapshot(nil), candidate.Limits...)
	c2.Limits[0].Revision = "c2"
	c2.LeaseExpiresAt = time.Now().Add(time.Second)
	c2.Checksum = Checksum(c2)
	if _, err = e.Activate(context.Background(), c2); err != nil {
		t.Fatal(err)
	}
	if s.Stats().Cohorts["candidate"].Stopped {
		t.Fatal("fresh cohort retained retired stop")
	}
}
func TestRestartUsesNewRevisionAndRejectsStaleLedger(t *testing.T) {
	dir := t.TempDir()
	e, _ := fixture(t, dir, 1, approved)
	e.Activate(context.Background(), envelope("p1", "", 1))
	e.Recover(context.Background())
	e.Close()
	newer, s := fixture(t, dir, 2, approved)
	defer newer.Close()
	if err := newer.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if newer.Status("p1").State == "active" || s.Stats().Effective != 10 {
		t.Fatal("restart reused prior activation revision")
	}
}
func TestPersistenceUncertaintyFreezesFurtherChanges(t *testing.T) {
	e, _ := fixture(t, t.TempDir(), 1, approved)
	defer e.Close()
	e.Activate(context.Background(), envelope("p1", "", 1))
	e.Recover(context.Background())
	save := e.persist
	e.persist = func(st snapshot) error {
		if err := save(st); err != nil {
			return err
		}
		return ErrStorage
	}
	if _, err := e.Activate(context.Background(), envelope("p2", "p1", 1)); err != ErrStorage {
		t.Fatal(err)
	}
	e.persist = save
	if _, err := e.Activate(context.Background(), envelope("p3", "p1", 1)); err == nil {
		t.Fatal("compounded an uncertain persisted activation")
	}
}

func TestStaleBackupCannotPassIndependentRecoveryProof(t *testing.T) {
	dir := t.TempDir()
	e, _ := fixture(t, dir, 1, approved)
	e.Activate(context.Background(), envelope("p1", "", 1))
	e.Recover(context.Background())
	backup, err := os.ReadFile(dir + "/activation.json")
	if err != nil {
		t.Fatal(err)
	}
	e.Activate(context.Background(), envelope("p2", "p1", 1))
	e.Close()
	if err = os.WriteFile(dir+"/activation.json", backup, 0600); err != nil {
		t.Fatal(err)
	}
	restored, s := fixture(t, dir, 2, func(_ context.Context, r RecoveryRequest) (RecoveryEvidence, error) {
		return RecoveryEvidence{PreviousEpoch: r.PreviousEpoch, StableRevision: "p2", ObservedAt: time.Now(), NoOrphanWork: true, CapacityVerified: true}, nil
	})
	defer restored.Close()
	if err = restored.Recover(context.Background()); err == nil || s.Stats().Effective != 0 {
		t.Fatal("stale backup resumed against newer control ledger")
	}
}
