package measurement

import (
	"pooltune/internal/domain"
	"pooltune/internal/pgwire"
	"testing"
	"time"
)

func TestArrivalWindowKeepsFailuresPendingAndUncertainty(t *testing.T) {
	start := time.Now()
	c, err := New([]string{"checkout"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Begin("activation1", start, time.Second); err != nil {
		t.Fatal(err)
	}
	event := func(id uint64, kind pgwire.ObservationKind) pgwire.Observation {
		return pgwire.Observation{ID: id, Kind: kind, ArrivedAt: start.Add(time.Millisecond), AcquiredAt: start.Add(2 * time.Millisecond), FinishedAt: start.Add(5 * time.Millisecond), Classification: domain.Classification{ActivationRevision: "activation1", Revision: "p1", Workload: "checkout", Cohort: "candidate"}}
	}
	for id := uint64(1); id <= 4; id++ {
		c.Record(event(id, pgwire.Arrived))
	}
	for id := uint64(1); id <= 3; id++ {
		c.Record(event(id, pgwire.Admitted))
	}
	c.Record(event(1, pgwire.Completed))
	failed := event(2, pgwire.Completed)
	failed.Failed = true
	c.Record(failed)
	c.Record(event(3, pgwire.Indeterminate))
	if _, err = c.Snapshot(start.Add(time.Millisecond)); err == nil {
		t.Fatal("partial window published")
	}
	w, err := c.Snapshot(start.Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	g := w.Groups[0].Candidate
	if !w.Complete || g.Arrivals != 4 || g.Successes != 1 || g.Failed != 1 || g.Uncertain != 1 || g.Pending != 1 {
		t.Fatalf("outcomes %+v", g)
	}
	var count uint64
	for _, n := range g.Total {
		count += n
	}
	if count != 1 {
		t.Fatal("failure latency polluted success histogram")
	}
	if g.OldestActive <= 0 || g.OldestQueued <= 0 {
		t.Fatal("unfinished ages lost")
	}
}
func TestWindowScopeOverflowAndLateCompletion(t *testing.T) {
	start := time.Now()
	c, _ := New([]string{"checkout"}, 1)
	c.Begin("a1", start, time.Second)
	e := pgwire.Observation{ID: 1, Kind: pgwire.Arrived, ArrivedAt: start.Add(time.Millisecond), Classification: domain.Classification{ActivationRevision: "a1", Workload: "checkout", Cohort: "stable"}}
	c.Record(e)
	extra := e
	extra.ID = 2
	c.Record(extra)
	w, _ := c.Snapshot(start.Add(2 * time.Second))
	if w.Complete {
		t.Fatal("overflow hidden")
	}
	c.Begin("a2", start.Add(3*time.Second), time.Second)
	e.Kind = pgwire.Indeterminate
	c.Record(e)
	w, _ = c.Snapshot(start.Add(5 * time.Second))
	if !w.Complete || w.Groups[0].Stable.Arrivals != 0 {
		t.Fatal("old activation contaminated new window")
	}
	// An event claiming to arrive in the new window with the wrong activation is
	// an explicit coverage error, not an invisible dropped request.
	e.Kind = pgwire.Arrived
	e.ArrivedAt = start.Add(3*time.Second + time.Millisecond)
	c.Record(e)
	w, _ = c.Snapshot(start.Add(5 * time.Second))
	if w.Complete {
		t.Fatal("wrong activation hidden")
	}
}

func TestBeginWindowRetryCannotEraseEvidence(t *testing.T) {
	start := time.Now()
	c, _ := New([]string{"w"}, 10)
	c.Begin("a", start, time.Second)
	c.Record(pgwire.Observation{ID: 1, Kind: pgwire.Arrived, ArrivedAt: start.Add(time.Millisecond), Classification: domain.Classification{ActivationRevision: "a", Workload: "w", Cohort: "stable"}})
	if err := c.Begin("a", start.Add(2*time.Second), time.Second); err != nil {
		t.Fatal(err)
	}
	first, _ := c.Snapshot(start.Add(3 * time.Second))
	if first.Groups[0].Stable.Arrivals != 1 {
		t.Fatal("retry erased population")
	}
	if err := c.BeginWindow("a", start.Add(3*time.Second), time.Second, first.End); err != nil {
		t.Fatal(err)
	}
	c.Record(pgwire.Observation{ID: 2, Kind: pgwire.Arrived, ArrivedAt: start.Add(3*time.Second + time.Millisecond), Classification: domain.Classification{ActivationRevision: "a", Workload: "w", Cohort: "candidate"}})
	c.BeginWindow("a", start.Add(5*time.Second), time.Second, first.End)
	next, _ := c.Snapshot(start.Add(6 * time.Second))
	if next.Groups[0].Candidate.Arrivals != 1 || next.Start.Before(first.End) {
		t.Fatal("rotation retry erased new population")
	}
}

func TestConcurrentArrivalOrderingPreservesOldest(t *testing.T) {
	start := time.Now()
	c, _ := New([]string{"w"}, 10)
	c.Begin("a", start, time.Second)
	event := func(id uint64, at time.Time) pgwire.Observation {
		return pgwire.Observation{ID: id, Kind: pgwire.Arrived, ArrivedAt: at, Classification: domain.Classification{ActivationRevision: "a", Workload: "w", Cohort: "stable"}}
	}
	c.Record(event(1, start.Add(500*time.Millisecond)))
	c.Record(event(2, start.Add(time.Millisecond)))
	w, _ := c.Snapshot(start.Add(2 * time.Second))
	if w.Groups[0].Stable.OldestQueued != 2*time.Second-time.Millisecond {
		t.Fatal("goroutine scheduling hid older queued work")
	}
}
