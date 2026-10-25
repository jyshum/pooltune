// Package measurement counts outcomes by arrival window for canary evaluation.
// It is separate from the controller's rolling rate telemetry: a completion in a
// later second must still settle the arrival that belongs to this experiment.
package measurement

import (
	"container/heap"
	"errors"
	"pooltune/internal/observation"
	"pooltune/internal/rollout"
	"pooltune/internal/telemetry"
	"sync"
	"time"
)

var bounds = telemetry.LatencyBounds()

var ErrWindow = errors.New("measurement window unavailable")

type cohortKey struct{ workload, cohort string }
type outstanding struct {
	group             int
	candidate         bool
	arrival, acquired time.Time
	index             int
	state             observation.Kind
}
type ages struct{ queued, active pendingHeap }
type Collector struct {
	mu                   sync.Mutex
	names                []string
	index                map[string]int
	max                  int
	activation           string
	start, end, previous time.Time
	duration             time.Duration
	bad                  bool
	groups               []rollout.Group
	ages                 map[cohortKey]*ages
	requests             map[uint64]*outstanding
}

func New(workloads []string, maxPending int) (*Collector, error) {
	if len(workloads) == 0 || len(workloads) > 128 || maxPending < 1 || maxPending > 100000 {
		return nil, ErrWindow
	}
	c := &Collector{names: append([]string(nil), workloads...), index: map[string]int{}, max: maxPending}
	for i, w := range c.names {
		if w == "" || len(w) > 128 {
			return nil, ErrWindow
		}
		if _, ok := c.index[w]; ok {
			return nil, ErrWindow
		}
		c.index[w] = i
	}
	return c, nil
}
func (c *Collector) Begin(revision string, start time.Time, duration time.Duration) error {
	return c.BeginWindow(revision, start, duration, time.Time{})
}

// BeginWindow is idempotent by activation and previous-window end. Retrying a
// request cannot silently reset the sampled population and discard bad outcomes.
func (c *Collector) BeginWindow(revision string, start time.Time, duration time.Duration, previous time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if revision == "" || len(revision) > 128 || start.IsZero() || duration <= 0 || duration > time.Hour {
		return ErrWindow
	}
	if c.activation == revision {
		if duration != c.duration {
			return ErrWindow
		}
		if previous.IsZero() || previous.Equal(c.previous) {
			return nil
		}
		if !previous.Equal(c.end) || start.Before(c.end) {
			return ErrWindow
		}
	} else if !previous.IsZero() {
		return ErrWindow
	}
	c.activation = revision
	c.start = start
	c.end = start.Add(duration)
	c.previous = previous
	c.duration = duration
	c.bad = false
	c.groups = make([]rollout.Group, len(c.names))
	c.ages = make(map[cohortKey]*ages, 2*len(c.names))
	c.requests = make(map[uint64]*outstanding)
	for i, name := range c.names {
		c.groups[i].Workload = name
		for _, co := range []string{"stable", "candidate"} {
			c.ages[cohortKey{name, co}] = &ages{}
		}
	}
	return nil
}
func (c *Collector) cohort(r *outstanding) *rollout.Cohort {
	if r.candidate {
		return &c.groups[r.group].Candidate
	}
	return &c.groups[r.group].Stable
}
func (c *Collector) age(r *outstanding) *ages {
	co := "stable"
	if r.candidate {
		co = "candidate"
	}
	return c.ages[cohortKey{c.names[r.group], co}]
}
func (c *Collector) increment(v *uint64) {
	if *v >= telemetry.MaxCounter {
		c.bad = true
		return
	}
	*v++
}
func (c *Collector) Record(o observation.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activation == "" || o.ArrivedAt.Before(c.start) || !o.ArrivedAt.Before(c.end) {
		return
	}
	if o.Kind == observation.Unattributed {
		c.bad = true
		return
	}
	i, known := c.index[o.Classification.Workload]
	if !known || o.ID == 0 || string(o.Classification.ActivationRevision) != c.activation || (o.Classification.Cohort != "stable" && o.Classification.Cohort != "candidate") {
		c.bad = true
		return
	}
	r := c.requests[o.ID]
	if o.Kind == observation.Arrived {
		if r != nil || len(c.requests) >= c.max {
			c.bad = true
			return
		}
		r = &outstanding{group: i, candidate: o.Classification.Cohort == "candidate", arrival: o.ArrivedAt, state: observation.Arrived}
		c.requests[o.ID] = r
		heap.Push(&c.age(r).queued, r)
		g := c.cohort(r)
		c.increment(&g.Arrivals)
		c.increment(&g.Pending)
		return
	}
	if r == nil || r.group != i || r.candidate != (o.Classification.Cohort == "candidate") || !r.arrival.Equal(o.ArrivedAt) {
		c.bad = true
		return
	}
	g := c.cohort(r)
	a := c.age(r)
	switch o.Kind {
	case observation.Admitted:
		if r.state != observation.Arrived || o.AcquiredAt.Before(r.arrival) {
			c.bad = true
			return
		}
		heap.Remove(&a.queued, r.index)
		r.acquired = o.AcquiredAt
		heap.Push(&a.active, r)
		r.state = observation.Admitted
	case observation.AdmissionRejected:
		if r.state != observation.Arrived || o.FinishedAt.Before(r.arrival) {
			c.bad = true
			return
		}
		heap.Remove(&a.queued, r.index)
		g.Pending--
		if o.TimedOut {
			c.increment(&g.TimedOut)
		} else {
			c.increment(&g.Rejected)
		}
		delete(c.requests, o.ID)
	case observation.Completed, observation.Indeterminate:
		if r.state != observation.Admitted || o.FinishedAt.Before(r.acquired) || !o.AcquiredAt.Equal(r.acquired) {
			c.bad = true
			return
		}
		g.Pending--
		if o.Kind == observation.Indeterminate {
			c.increment(&g.Uncertain)
			r.state = observation.Indeterminate
			return
		}
		heap.Remove(&a.active, r.index)
		delete(c.requests, o.ID)
		if o.Failed {
			c.increment(&g.Failed)
			return
		}
		c.increment(&g.Successes)
		c.hist(&g.Queue, r.acquired.Sub(r.arrival))
		c.hist(&g.Total, o.FinishedAt.Sub(r.arrival))
	default:
		c.bad = true
	}
}
func (c *Collector) hist(h *telemetry.Histogram, d time.Duration) {
	i := 0
	for _, bound := range bounds {
		if d <= bound {
			break
		}
		i++
	}
	c.increment(&h[i])
}

// Snapshot copies only bounded workload/cohort aggregates, never scans requests.
// Oldest live work outside this window and database pressure are separate safety
// inputs from the scheduler/provider, and must also be supplied to the evaluator.
func (c *Collector) Snapshot(now time.Time) (rollout.Window, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activation == "" || now.Before(c.end) {
		return rollout.Window{}, ErrWindow
	}
	result := rollout.Window{Start: c.start, End: c.end, Complete: !c.bad, Groups: append([]rollout.Group(nil), c.groups...)}
	for i, name := range c.names {
		for _, co := range []string{"stable", "candidate"} {
			g := &result.Groups[i].Stable
			if co == "candidate" {
				g = &result.Groups[i].Candidate
			}
			a := c.ages[cohortKey{name, co}]
			if len(a.queued) > 0 {
				g.OldestQueued = now.Sub(a.queued[0].arrival)
			}
			if len(a.active) > 0 {
				g.OldestActive = now.Sub(a.active[0].acquired)
			}
		}
	}
	return result, nil
}

// Heap order follows event time, not goroutine callback order. A goroutine can
// be descheduled after timestamping an arrival/admission but before recording it.
type pendingHeap []*outstanding

func (h pendingHeap) Len() int { return len(h) }
func (h pendingHeap) Less(i, j int) bool {
	a, b := h[i].arrival, h[j].arrival
	if !h[i].acquired.IsZero() {
		a = h[i].acquired
	}
	if !h[j].acquired.IsZero() {
		b = h[j].acquired
	}
	return a.Before(b)
}
func (h pendingHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *pendingHeap) Push(v any)   { r := v.(*outstanding); r.index = len(*h); *h = append(*h, r) }
func (h *pendingHeap) Pop() any {
	a := *h
	n := len(a) - 1
	r := a[n]
	a[n] = nil
	*h = a[:n]
	r.index = -1
	return r
}
