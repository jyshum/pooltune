package tuning

import (
	"context"
	"os"
	"pooltune/internal/admin"
	"pooltune/internal/domain"
	"pooltune/internal/durable"
	"pooltune/internal/provider"
	"pooltune/internal/store"
	"testing"
	"time"
)

type poolDouble struct {
	o     provider.LocalPoolStatus
	j     provider.Journal
	fail  bool
	calls int
}

func (p *poolDouble) Observe(context.Context) (provider.LocalPoolStatus, error) {
	if p.fail {
		return p.o, ErrUnsafe
	}
	return p.o, nil
}
func (p *poolDouble) Apply(_ context.Context, id, version string, n int) error {
	p.calls++
	if version != p.o.Version {
		return provider.ErrStale
	}
	op := provider.Operation{Target: provider.Target{ID: "db"}, Kind: provider.LocalPgBouncer, Revision: id, ExpectedVersion: version, Parameter: "pool_size", Value: n, StartedAt: time.Now().UTC(), State: "intent"}
	if e := p.j.Save(op); e != nil {
		return e
	}
	op.State = "accepted"
	if e := p.j.Save(op); e != nil {
		return e
	}
	p.o.Pending = true
	p.o.Target = n
	return nil
}
func serviceFixture(t *testing.T) (*Service, *poolDouble, *durable.Files, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	files, e := durable.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { files.Close() })
	db, e := store.OpenStore(dir + "/control.db")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	p := &poolDouble{o: provider.LocalPoolStatus{Identity: "fixture-resource", Version: "v1", Target: 4, Working: 4, Idle: 4, At: time.Now(), Slots: 100}, j: db}
	s, e := New("db", bounds(), p, db, files)
	if e != nil {
		t.Fatal(e)
	}
	return s, p, files, db
}
func tick(t *testing.T, s *Service, p *poolDouble, now time.Time) {
	t.Helper()
	p.o.At = now
	if e := s.Tick(context.Background(), now, domain.Resources{At: now, Source: "local-host", CPU: .2, Memory: .3}); e != nil {
		t.Fatal(e)
	}
}

func TestServiceStartsFrozenPersistsOwnerCommandsAndRejectsStale(t *testing.T) {
	s, p, files, db := serviceFixture(t)
	ctx := context.Background()
	now := time.Now()
	tick(t, s, p, now)
	view := s.View(now)
	if !view.Frozen || !view.Ready {
		t.Fatalf("unsafe initial state: %+v", view)
	}
	cmd := admin.Command{ExpectedRevision: view.Revision, IdempotencyKey: "resume-1"}
	if _, e := s.Execute(ctx, "resume", cmd); e != nil {
		t.Fatal(e)
	}
	if s.View(now).Frozen {
		t.Fatal("resume not applied")
	}
	rev := s.View(now).Revision
	if _, e := s.Execute(ctx, "resume", cmd); e != nil || s.View(now).Revision != rev {
		t.Fatal("duplicate owner command repeated")
	}
	if _, e := s.Execute(ctx, "freeze", admin.Command{ExpectedRevision: view.Revision, IdempotencyKey: "stale"}); e != admin.ErrConflict {
		t.Fatal("stale owner command accepted")
	}
	restarted, e := New("db", bounds(), p, db, files)
	if e != nil {
		t.Fatal(e)
	}
	if !restarted.View(now).Frozen {
		t.Fatal("restart resumed automation without review")
	}
	changed := bounds()
	changed.Maximum = 7
	if _, e := New("db", changed, p, db, files); e == nil {
		t.Fatal("changed approved bounds silently adopted")
	}
}

func TestServiceJournalsBeforeChangeAndReconcilesWithoutReplay(t *testing.T) {
	s, p, _, db := serviceFixture(t)
	ctx := context.Background()
	start := time.Now()
	tick(t, s, p, start)
	_, e := s.Execute(ctx, "resume", admin.Command{ExpectedRevision: s.View(start).Revision, IdempotencyKey: "resume"})
	if e != nil {
		t.Fatal(e)
	}
	p.o.Idle = 0
	p.o.Waiting = 2
	for i := 1; i <= 7; i++ {
		tick(t, s, p, start.Add(time.Duration(i)*time.Second))
	}
	if p.calls != 1 || s.View(start.Add(7*time.Second)).Pending == nil {
		t.Fatalf("missing single pending operation: calls=%d", p.calls)
	}
	op, e := db.Load("db")
	if e != nil || op == nil {
		t.Fatal("missing durable intent")
	}
	op.State = "complete"
	if e = db.Save(*op); e != nil {
		t.Fatal(e)
	}
	p.o.Working = 5
	p.o.Idle = 5
	p.o.Waiting = 0
	p.o.Pending = false
	p.o.Version = "v2"
	tick(t, s, p, start.Add(8*time.Second))
	if s.View(start.Add(8*time.Second)).Pending != nil || p.calls != 1 {
		t.Fatal("confirmation replayed or remained pending")
	}
}

func TestServiceMissingEvidenceAndPersistencePreventMutation(t *testing.T) {
	s, p, files, _ := serviceFixture(t)
	now := time.Now()
	tick(t, s, p, now)
	files.Close()
	if _, e := s.Execute(context.Background(), "resume", admin.Command{ExpectedRevision: s.View(now).Revision, IdempotencyKey: "resume"}); e == nil {
		t.Fatal("owner action without durable record")
	}
	if !s.View(now).Frozen {
		t.Fatal("failed write changed freeze")
	}
	p.fail = true
	if e := s.Tick(context.Background(), now.Add(time.Second), domain.Resources{}); e == nil {
		t.Fatal("missing observation accepted")
	}
	if s.View(now.Add(time.Second)).Ready || p.calls != 0 {
		t.Fatal("unavailable evidence reported ready or mutated")
	}
}

func TestRestoreCurrentCapacityDoesNotCreateUnresolvableIntent(t *testing.T) {
	s, p, files, db := serviceFixture(t)
	now := time.Now()
	tick(t, s, p, now)
	next := s.state
	next.History = append(next.History, Record{ID: "confirmed-capacity", Status: "confirmed", After: 4})
	if e := s.persist(next); e != nil {
		t.Fatal(e)
	}
	_, e := s.Execute(context.Background(), "revert", admin.Command{ExpectedRevision: s.state.Revision, IdempotencyKey: "same-size", Revision: "confirmed-capacity"})
	if e == nil || s.state.Pending != nil || p.calls != 0 {
		t.Fatal("same-capacity restore created an unresolved intent")
	}
	restarted, e := New("db", bounds(), p, db, files)
	if e != nil {
		t.Fatal(e)
	}
	tick(t, restarted, p, time.Now())
	if !restarted.View(time.Now()).Ready {
		t.Fatal("refused restore blocked restart")
	}
}

func TestRestoreIdempotencyRejectsDifferentHistoryRevision(t *testing.T) {
	s, p, _, _ := serviceFixture(t)
	tick(t, s, p, time.Now())
	next := s.state
	next.History = append(next.History, Record{ID: "capacity-a", Status: "confirmed", After: 3}, Record{ID: "capacity-b", Status: "confirmed", After: 2})
	if e := s.persist(next); e != nil {
		t.Fatal(e)
	}
	c := admin.Command{ExpectedRevision: s.state.Revision, IdempotencyKey: "restore-once", Revision: "capacity-a"}
	if _, e := s.Execute(context.Background(), "revert", c); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Execute(context.Background(), "revert", c); e != nil {
		t.Fatal("same request must remain idempotent", e)
	}
	c.Revision = "capacity-b"
	if _, e := s.Execute(context.Background(), "revert", c); e != admin.ErrConflict {
		t.Fatal("changed restore payload accepted")
	}
}

func TestRetainedAuthorityRejectsReplacementResourceBeforePressureBackoff(t *testing.T) {
	s, p, files, db := serviceFixture(t)
	p.o.Identity = "original-resource"
	tick(t, s, p, time.Now())
	p.o.Identity = "replacement-resource"
	// Same logical label, settings and legacy version must not confer authority.
	restarted, e := New("db", bounds(), p, db, files)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	p.o.At = now
	e = restarted.Tick(context.Background(), now, domain.Resources{At: now, Source: "local-host", CPU: .99, Memory: .3})
	if e == nil || p.calls != 0 || restarted.View(now).Ready {
		t.Fatal("retained authority mutated a replacement resource")
	}
}

func TestServiceCarriesMeasuredTransactionsAndMissingData(t *testing.T) {
	s, p, _, _ := serviceFixture(t)
	now := time.Now()
	p.o.Transactions = &provider.Transactions{At: now, Total: 42, Epoch: "pool-process"}
	tick(t, s, p, now)
	v := s.View(now)
	if v.Observation.Transactions == nil || v.Observation.Transactions.Total != 42 {
		t.Fatal("transaction counter lost")
	}
	p.o.Transactions = nil
	tick(t, s, p, now.Add(time.Second))
	if s.View(now.Add(time.Second)).Observation.Transactions != nil {
		t.Fatal("missing metric invented zero")
	}
}
