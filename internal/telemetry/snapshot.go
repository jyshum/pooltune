// Package telemetry collects bounded, payload-free local evidence. A recorder
// belongs to one immutable revision identity and counter epoch.
package telemetry

import (
	"errors"
	"math"
	"pooltune/internal/domain"
	"sync"
	"time"
)

const MaxWorkloads = 128
const UnknownWorkload = "__unknown__"
const MaxCounter uint64 = 1 << 52 // integers stay exact when used by control math

type Identity struct {
	Target                      string
	Path                        domain.PathKind
	Capacity, Policy, Guardrail string
	Epoch                       uint64
}

func (i Identity) Valid() bool {
	return len(i.Target) > 0 && len(i.Target) <= 128 && (i.Path == domain.PathPostgres || i.Path == domain.PathREST) && len(i.Capacity) > 0 && len(i.Capacity) <= 128 && len(i.Policy) > 0 && len(i.Policy) <= 128 && len(i.Guardrail) > 0 && len(i.Guardrail) <= 128 && i.Epoch > 0
}

type Kind uint8

const (
	Arrival Kind = iota
	Success
	Failure
	Overload
	Timeout
	Rejected
	Uncertain
)

// Each bucket is exclusive of its predecessor and inclusive of this bound.
// Bucket 8 is overflow (>10 seconds), never a finite percentile estimate.
var latencyBounds = [...]time.Duration{time.Microsecond, 10 * time.Microsecond, 100 * time.Microsecond, time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond, time.Second, 10 * time.Second}

func LatencyBounds() []time.Duration { return append([]time.Duration(nil), latencyBounds[:]...) }

type Histogram [9]uint64

type Runtime struct {
	Active, Uncertain, Queued  int
	OldestActive, OldestQueued time.Duration
}
type Group struct {
	Workload                                                                                            string
	Arrivals1, Arrivals5, Successes, Terminals, OverloadFailures, Rejected, TimedOut, UncertainOutcomes uint64
	ServiceSeconds                                                                                      float64
	Active, Uncertain, Queued                                                                           int
	OldestActive, OldestQueued                                                                          time.Duration
	ServiceHistogram, TotalHistogram, QueueHistogram                                                    Histogram
}
type MetricsSnapshot struct {
	Identity                   Identity
	At, Start, End, RuntimeAt  time.Time
	Complete                   bool
	MeanInFlight               float64
	Groups                     []Group
	Recorded, Omitted, Unknown uint64
}
type counts struct {
	arrivals, successes, terminals, overload, rejected, timedout, uncertain uint64
	service                                                                 float64
	serviceHist, totalHist, queueHist                                       Histogram
}
type bucket struct {
	second   int64
	counts   []counts
	occupied float64
}
type Recorder struct {
	mu                         sync.Mutex
	identity                   Identity
	clock                      func() time.Time
	started, last              time.Time
	names                      []string
	index                      map[string]int
	buckets                    [6]bucket
	active                     int
	runtime                    []Runtime
	runtimeAt                  time.Time
	recorded, omitted, unknown uint64
	corrupt                    bool
}

func New(id Identity, workloads []string, clock func() time.Time) (*Recorder, error) {
	if !id.Valid() || clock == nil || len(workloads) > MaxWorkloads {
		return nil, errors.New("invalid telemetry configuration")
	}
	r := &Recorder{identity: id, clock: clock, index: make(map[string]int, len(workloads))}
	for _, w := range workloads {
		if w == "" || len(w) > 128 || w == UnknownWorkload {
			return nil, errors.New("invalid workload")
		}
		if _, ok := r.index[w]; ok {
			return nil, errors.New("duplicate workload")
		}
		r.index[w] = len(r.names)
		r.names = append(r.names, w)
	}
	r.names = append(r.names, UnknownWorkload)
	r.runtime = make([]Runtime, len(r.names))
	for i := range r.buckets {
		r.buckets[i].second = math.MinInt64
		r.buckets[i].counts = make([]counts, len(r.names))
	}
	r.started = clock()
	if r.started.IsZero() || r.started.Unix() < 0 {
		return nil, errors.New("invalid clock")
	}
	r.last = r.started
	return r, nil
}
func (r *Recorder) slot(second int64) *bucket {
	b := &r.buckets[second%6]
	if b.second != second {
		b.second = second
		clear(b.counts)
		b.occupied = 0
	}
	return b
}
func (r *Recorder) advance(now time.Time) bool {
	if now.Before(r.last) {
		r.corrupt = true
		return false
	}
	// Only the six retained seconds can influence a snapshot. Integrating a
	// long idle gap is bounded to six buckets, not a loop over elapsed seconds.
	from := r.last
	cut := now.Truncate(time.Second).Add(-5 * time.Second)
	if from.Before(cut) {
		from = cut
	}
	for from.Before(now) {
		end := from.Truncate(time.Second).Add(time.Second)
		if end.After(now) {
			end = now
		}
		r.slot(from.Unix()).occupied += float64(r.active) * end.Sub(from).Seconds()
		from = end
	}
	r.last = now
	return true
}
func (r *Recorder) inc(v *uint64) {
	if *v >= MaxCounter {
		r.corrupt = true
		return
	}
	*v++
}
func (r *Recorder) hist(h *Histogram, d time.Duration) {
	i := 0
	for i < len(latencyBounds) && d > latencyBounds[i] {
		i++
	}
	r.inc(&h[i])
}
func (r *Recorder) group(w string) int {
	if i, ok := r.index[w]; ok {
		return i
	}
	return len(r.names) - 1
}

// Record counts an eligible arrival or a once-only outcome. Unauthorized/quota
// rejected traffic is excluded by the ingress. Uncertain is not terminal.
func (r *Recorder) Record(w string, k Kind, service time.Duration) { r.RecordTiming(w, k, service, 0) }
func (r *Recorder) RecordTiming(w string, k Kind, service, queue time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.advance(r.clock()) {
		return
	}
	if k > Uncertain || service < 0 || queue < 0 || service > time.Duration(math.MaxInt64)-queue {
		r.inc(&r.omitted)
		r.corrupt = true
		return
	}
	i := r.group(w)
	if i == len(r.names)-1 {
		r.inc(&r.unknown)
	}
	r.inc(&r.recorded)
	c := &r.slot(r.last.Unix()).counts[i]
	if k == Arrival {
		r.inc(&c.arrivals)
		return
	}
	if k == Uncertain {
		r.inc(&c.uncertain)
		return
	}
	r.inc(&c.terminals)
	r.hist(&c.totalHist, service+queue)
	r.hist(&c.queueHist, queue)
	switch k {
	case Success:
		r.inc(&c.successes)
		c.service += service.Seconds()
		r.hist(&c.serviceHist, service)
	case Overload:
		r.inc(&c.overload)
	case Timeout:
		r.inc(&c.overload)
		r.inc(&c.timedout)
	case Rejected:
		r.inc(&c.rejected)
	}
}

// SetInFlight is called at every occupancy transition, including retaining
// uncertain operations. It is not periodic sampling of instantaneous counts.
func (r *Recorder) SetInFlight(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.advance(r.clock()) {
		return
	}
	if n < 0 || n > 1000000 {
		r.corrupt = true
		return
	}
	r.active = n
}

// ObserveRuntime consumes the scheduler's bounded workload summaries. No
// request scan or I/O occurs here. The owning scheduler serializes this with
// occupancy transitions; callers must not acquire scheduler locks from a clock.
func (r *Recorder) ObserveRuntime(groups map[string]Runtime) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.advance(r.clock()) {
		return
	}
	if len(groups) > MaxWorkloads+1 {
		r.corrupt = true
		return
	}
	clear(r.runtime)
	for w, v := range groups {
		if v.Active < 0 || v.Active > 1000000 || v.Queued < 0 || v.Queued > 1000000 || v.Uncertain < 0 || v.Uncertain > v.Active || v.OldestActive < 0 || v.OldestQueued < 0 {
			r.corrupt = true
			return
		}
		i := r.group(w)
		x := &r.runtime[i]
		x.Active += v.Active
		x.Queued += v.Queued
		x.Uncertain += v.Uncertain
		x.OldestActive = max(x.OldestActive, v.OldestActive)
		x.OldestQueued = max(x.OldestQueued, v.OldestQueued)
	}
	r.runtimeAt = r.last
}
func (r *Recorder) Omit() { r.mu.Lock(); defer r.mu.Unlock(); r.inc(&r.omitted) }

func (r *Recorder) Snapshot() MetricsSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock()
	valid := r.advance(now)
	end := now.Truncate(time.Second)
	start := end.Add(-5 * time.Second)
	out := MetricsSnapshot{Identity: r.identity, At: now, Start: start, End: end, RuntimeAt: r.runtimeAt, Complete: valid && !r.corrupt && !start.Before(r.started), Recorded: r.recorded, Omitted: r.omitted, Unknown: r.unknown, Groups: make([]Group, len(r.names))}
	var active int
	for i, name := range r.names {
		v := r.runtime[i]
		active += v.Active
		out.Groups[i] = Group{Workload: name, Active: v.Active, Queued: v.Queued, Uncertain: v.Uncertain, OldestActive: v.OldestActive, OldestQueued: v.OldestQueued}
	}
	if active != r.active {
		out.Complete = false
	}
	for second := start.Unix(); second < end.Unix(); second++ {
		if second < 0 {
			out.Complete = false
			continue
		}
		b := &r.buckets[second%6]
		if b.second != second {
			continue
		}
		out.MeanInFlight += b.occupied / 5
		for i, c := range b.counts {
			g := &out.Groups[i]
			g.Arrivals5 += c.arrivals
			if second == end.Unix()-1 {
				g.Arrivals1 = c.arrivals
			}
			g.Successes += c.successes
			g.Terminals += c.terminals
			g.OverloadFailures += c.overload
			g.Rejected += c.rejected
			g.TimedOut += c.timedout
			g.UncertainOutcomes += c.uncertain
			g.ServiceSeconds += c.service
			for j := range g.ServiceHistogram {
				g.ServiceHistogram[j] += c.serviceHist[j]
				g.TotalHistogram[j] += c.totalHist[j]
				g.QueueHistogram[j] += c.queueHist[j]
			}
		}
	}
	return out
}
