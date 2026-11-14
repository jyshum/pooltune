package scheduler

import (
	"pooltune/internal/domain"
	"testing"
	"time"
)

func TestCapacityEvidenceOrdersOccupancyAndRetainsUnknown(t *testing.T) {
	snap := snapshot(t, 2, 0)
	snap.Limits.CapacityRevision = "c1"
	s := newScheduler(t, config(), snap)
	now := time.Now().Truncate(time.Second)
	s.capacityClock = func() time.Time { return now }
	if err := s.EnableCapacity([]string{"work"}); err != nil {
		t.Fatal(err)
	}
	p := acquire(t, s, classification(domain.ClassNormal, "a"))
	now = now.Add(2 * time.Second)
	s.Release(p, Unknown)
	now = now.Add(3 * time.Second)
	m := s.CapacityMetrics()
	if !m.Complete || m.MeanInFlight != 1 || m.Groups[0].Uncertain != 1 || m.Groups[0].Successes != 0 {
		t.Fatalf("unknown work lost: %+v", m)
	}
	s.Reconcile(p)
	now = now.Add(5 * time.Second)
	m = s.CapacityMetrics()
	if !m.Complete || m.MeanInFlight != 0 {
		t.Fatalf("reconciliation not integrated: %+v", m)
	}
	snap.Limits.Revision = "r2"
	snap.Limits.CapacityRevision = "c2"
	if err := s.Install(snap); err != nil {
		t.Fatal(err)
	}
	if s.CapacityMetrics().Complete {
		t.Fatal("new revision inherited old evidence")
	}
}
