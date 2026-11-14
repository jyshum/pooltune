package control

import (
	"context"
	"encoding/json"
	"fmt"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/scheduler"
	"testing"
	"time"
)

func splitEnvelope(t *testing.T, e *Engine, rev string, candidate policy.Policy, fraction float64, budgets map[string]int) domain.ActivationEnvelope {
	t.Helper()
	v := e.View().Active
	var raw map[string]policy.Policy
	if err := json.Unmarshal(v.PolicyJSON, &raw); err != nil {
		t.Fatal(err)
	}
	v.ExpectedPredecessor = v.Revision
	v.Revision = domain.RevisionID(rev)
	v.PolicyJSON, _ = json.Marshal(map[string]policy.Policy{"stable": raw["stable"], "candidate": candidate})
	v.CandidateFraction = fraction
	v.LeaseExpiresAt = time.Now().Add(30 * time.Second)
	v.Limits[0].Revision = v.Revision
	v.Limits[0].CohortBudgets = budgets
	v.Checksum = Checksum(v)
	return v
}
func readyEngine(t *testing.T) (*Engine, *scheduler.Scheduler, policy.Policy) {
	t.Helper()
	e, s := fixture(t, t.TempDir(), 1, approved)
	t.Cleanup(func() { e.Close() })
	e.cfg.CohortKey = make([]byte, 32)
	initial := envelope("p1", "", 1)
	if _, err := e.Activate(t.Context(), initial); err != nil {
		t.Fatal(err)
	}
	if err := e.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	var raw map[string]policy.Policy
	json.Unmarshal(initial.PolicyJSON, &raw)
	return e, s, raw["stable"]
}
func TestBaselineRoutingAndOldBindingsDrain(t *testing.T) {
	e, s, p := readyEngine(t)
	ctx := domain.RequestContext{Target: "db", Path: domain.PathPostgres, Principal: "app"}
	baseline := splitEnvelope(t, e, "partition", p, .1, map[string]int{"stable": 8, "candidate": 2})
	if _, err := e.Activate(t.Context(), baseline); err != nil {
		t.Fatal(err)
	}
	count := 0
	for i := 0; i < 10000; i++ {
		id := fmt.Sprint(i)
		a := e.Route(ctx, id)
		b := e.Route(ctx, id)
		if a.Cohort != b.Cohort {
			t.Fatal("unstable assignment")
		}
		if a.Cohort == "candidate" {
			count++
		}
	}
	if count < 800 || count > 1200 {
		t.Fatalf("assignment distribution %d/10000", count)
	}
	if !e.Route(ctx, "").Blocked {
		t.Fatal("untrusted empty identity routed")
	}
	held, err := s.Acquire(t.Context(), e.Classify(ctx, "candidate"))
	if err != nil {
		t.Fatal(err)
	}
	p.Revision = "p2"
	p.ExpectedPredecessor = "p1"
	p.ClassWeights.Normal = 2
	candidate := splitEnvelope(t, e, "canary", p, .1, map[string]int{"stable": 8, "candidate": 2})
	if _, err = e.Activate(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}
	if e.View().Drained {
		t.Fatal("old policy counted as new-stage evidence")
	}
	if held.Classification().Revision != "p1" {
		t.Fatal("active transaction relabelled")
	}
	s.Release(held, scheduler.Finished)
	if !e.View().Drained {
		t.Fatal("completed old work still prevents measurement")
	}
	if err = e.Rollback(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if c := e.Route(ctx, fmt.Sprint(i)); c.Cohort != "stable" || c.Revision != "p1" {
			t.Fatal("rollback retained routing")
		}
	}
}
func TestPromotionRetainsActiveAndMovesQueuedCandidate(t *testing.T) {
	e, s, p := readyEngine(t)
	p.Revision = "p2"
	p.ExpectedPredecessor = "p1"
	p.ClassWeights.Normal = 2
	env := splitEnvelope(t, e, "full", p, 1, map[string]int{"stable": 0, "candidate": 10})
	if _, err := e.Activate(t.Context(), env); err != nil {
		t.Fatal(err)
	}
	req := domain.RequestContext{Target: "db", Path: domain.PathPostgres, Principal: "app"}
	held := make([]*scheduler.Permit, 10)
	for i := range held {
		var err error
		held[i], err = s.Acquire(t.Context(), e.Route(req, "session"))
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result := make(chan *scheduler.Permit, 1)
	go func() { p, _ := s.Acquire(ctx, e.Route(req, "queued")); result <- p }()
	deadline := time.Now().Add(time.Second)
	for s.Stats().Queued != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.Stats().Queued != 1 {
		t.Fatal("queue missing")
	}
	promote := e.View().Active
	promote.ExpectedPredecessor = promote.Revision
	promote.Revision = "accepted"
	promote.CandidateFraction = 0
	promote.LeaseExpiresAt = time.Time{}
	promote.PolicyJSON, _ = json.Marshal(map[string]policy.Policy{"stable": p})
	promote.Limits[0].Revision = promote.Revision
	promote.Limits[0].CohortBudgets = map[string]int{"stable": 10}
	promote.Checksum = Checksum(promote)
	if _, err := e.Activate(t.Context(), promote); err != nil {
		t.Fatal(err)
	}
	if held[0].Classification().Cohort != "candidate" || held[0].Classification().Revision != "p2" {
		t.Fatal("active binding changed")
	}
	if e.View().Drained {
		t.Fatal("old cohort not drained")
	}
	if c := e.Route(req, "session"); c.Cohort != "stable" || c.Revision != "p2" {
		t.Fatal("new stable not routed")
	}
	s.Release(held[0], scheduler.Finished)
	select {
	case p := <-result:
		if p == nil || p.Classification().Cohort != "stable" {
			t.Fatal("queue not migrated")
		}
		s.Release(p, scheduler.Finished)
	case <-ctx.Done():
		t.Fatal("queue stalled")
	}
	for _, p := range held[1:] {
		s.Release(p, scheduler.Finished)
	}
	if !e.View().Drained {
		t.Fatal("promoted cohort failed to drain")
	}
}
