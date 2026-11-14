package control

import (
	"context"
	"encoding/json"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/scheduler"
	"testing"
	"time"
)

func TestLocalSafetyCeilingSurvivesStorageFailureAndPreservesTransaction(t *testing.T) {
	e, s, p := readyEngine(t)
	p.Revision = "candidate"
	p.ExpectedPredecessor = "p1"
	p.ClassWeights.Normal++
	env := splitEnvelope(t, e, "experiment", p, .1, map[string]int{"stable": 8, "candidate": 2})
	if _, err := e.Activate(t.Context(), env); err != nil {
		t.Fatal(err)
	}
	held, err := s.Acquire(t.Context(), e.Classify(domain.RequestContext{Target: "db", Path: domain.PathPostgres}, "candidate"))
	if err != nil {
		t.Fatal(err)
	}
	persist := e.persist
	e.persist = func(snapshot) error { return ErrStorage }
	if err = e.Constrain(0); err != ErrStorage {
		t.Fatal("missing audit failure", err)
	}
	if s.Stats().Effective != 0 || s.Stats().Active != 1 || held.Classification().Revision != "candidate" || !e.View().AuditGap {
		t.Fatal("safety failed or transaction interrupted")
	}
	e.persist = persist
	if err = e.AcknowledgeFallback(e.View().Active.Revision, e.View().Active.Checksum); err != nil {
		t.Fatal(err)
	}
	next := e.View().Active
	next.ExpectedPredecessor = next.Revision
	next.Revision = "bypass"
	next.Limits[0].Revision = next.Revision
	next.Limits[0].Effective = 10
	next.Limits[0].CohortBudgets = map[string]int{"stable": 10}
	next.Checksum = Checksum(next)
	if _, err = e.Activate(context.Background(), next); err == nil {
		t.Fatal("activation bypassed latched safety ceiling")
	}
	s.Release(held, scheduler.Finished)
	if s.Stats().Active != 0 {
		t.Fatal("transaction did not release")
	}
}
func TestLocalSafetyBackoffCannotIncreaseCapacity(t *testing.T) {
	e, s, _ := readyEngine(t)
	if err := e.Constrain(8); err != nil {
		t.Fatal(err)
	}
	if s.Stats().Effective != 8 || e.View().Active.Limits[0].CapacityRevision == "" {
		t.Fatal("reduction not versioned")
	}
	if err := e.Constrain(9); err == nil || s.Stats().Effective != 8 {
		t.Fatal("safety method grew capacity")
	}
}

func TestSafetyStopWhileAcceptedCandidateTransactionsDrain(t *testing.T) {
	e, s, p := readyEngine(t)
	p.Revision = "promoted"
	p.ExpectedPredecessor = "p1"
	p.ClassWeights.Normal++
	full := splitEnvelope(t, e, "full", p, 1, map[string]int{"stable": 0, "candidate": 10})
	if _, err := e.Activate(t.Context(), full); err != nil {
		t.Fatal(err)
	}
	held, err := s.Acquire(t.Context(), e.Classify(domain.RequestContext{Target: "db", Path: domain.PathPostgres}, "candidate"))
	if err != nil {
		t.Fatal(err)
	}
	accept := e.View().Active
	accept.ExpectedPredecessor = accept.Revision
	accept.Revision = "accepted"
	accept.CandidateFraction = 0
	accept.LeaseExpiresAt = time.Time{}
	accept.PolicyJSON, _ = json.Marshal(map[string]policy.Policy{"stable": p})
	accept.Limits[0].Revision = accept.Revision
	accept.Limits[0].CohortBudgets = map[string]int{"stable": 10}
	accept.Checksum = Checksum(accept)
	if _, err = e.Activate(t.Context(), accept); err != nil {
		t.Fatal(err)
	}
	if err = e.Constrain(0); err != nil {
		t.Fatal("promoted drain prevented safety stop", err)
	}
	if s.Stats().Effective != 0 || s.Stats().Active != 1 || held.Classification().Revision != "promoted" {
		t.Fatal("safety or active binding lost")
	}
	s.Release(held, scheduler.Finished)
}
