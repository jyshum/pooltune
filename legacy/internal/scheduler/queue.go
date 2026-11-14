package scheduler

import (
	"container/heap"
	"container/list"
	"context"
	"pooltune/internal/telemetry"
	"time"

	"pooltune/internal/domain"
)

const batchSize = 32

type entry struct {
	order                    uint64
	queueIndex               int
	classification           domain.Classification
	ctx                      context.Context
	enqueued                 time.Time
	done                     chan struct{}
	finished                 bool
	permit                   *Permit
	err                      error
	heapIndex                int
	fifo                     *tenantFIFO
	cohortNode, workloadNode *list.Element
}
type tenantFIFO struct {
	key          string
	entries      entryHeap
	rotationNode *list.Element
}
type classQueue struct {
	tenants  map[string]*tenantFIFO
	rotation list.List
}

func (s *Scheduler) enqueue(e *entry) {
	c := e.classification
	co := s.cohorts[c.Cohort]
	t := s.tenants[c.FairnessKey]
	w := s.workloads[c.Workload]
	s.bind(c)
	s.queued++
	s.queuedBytes += c.BufferedBytes
	co.queued++
	t.queued++
	w.queued++
	e.cohortNode = co.entries.PushBack(e)
	e.workloadNode = w.queueList.PushBack(e)
	s.attach(e)
	heap.Push(&s.deadlines, e)
}
func (s *Scheduler) attach(e *entry) {
	c := e.classification
	q := &s.cohorts[c.Cohort].classes[classIndex(c.Class)]
	if q.tenants == nil {
		q.tenants = make(map[string]*tenantFIFO)
	}
	f := q.tenants[c.FairnessKey]
	if f == nil {
		f = &tenantFIFO{key: c.FairnessKey}
		f.rotationNode = q.rotation.PushBack(f)
		q.tenants[f.key] = f
	}
	e.fifo = f
	heap.Push(&f.entries, e)
}
func (s *Scheduler) detach(e *entry) {
	if e.queueIndex < 0 {
		return
	}
	c := e.classification
	q := &s.cohorts[c.Cohort].classes[classIndex(c.Class)]
	f := e.fifo
	heap.Remove(&f.entries, e.queueIndex)
	e.fifo = nil
	if f.entries.Len() == 0 {
		q.rotation.Remove(f.rotationNode)
		delete(q.tenants, f.key)
	}
}
func (s *Scheduler) finish(e *entry, p *Permit, err error) {
	if e.finished {
		return
	}
	c := e.classification
	co := s.cohorts[c.Cohort]
	t := s.tenants[c.FairnessKey]
	w := s.workloads[c.Workload]
	s.detach(e)
	co.entries.Remove(e.cohortNode)
	w.queueList.Remove(e.workloadNode)
	heap.Remove(&s.deadlines, e.heapIndex)
	s.queued--
	s.queuedBytes -= c.BufferedBytes
	co.queued--
	t.queued--
	w.queued--
	if p == nil {
		kind := telemetry.Rejected
		if err == ErrExpired || err == context.DeadlineExceeded {
			kind = telemetry.Timeout
		}
		s.capacityRecord(c.Workload, kind, 0, time.Since(e.enqueued))
		s.unbind(c)
	}
	e.finished = true
	e.permit = p
	e.err = err
	s.idle(t, time.Now())
	s.removeWorkload(c.Workload, w)
	close(e.done)
}

type deadlineHeap []*entry

func (h deadlineHeap) Len() int { return len(h) }
func (h deadlineHeap) Less(i, j int) bool {
	return h[i].classification.QueueDeadline.Before(h[j].classification.QueueDeadline)
}
func (h deadlineHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i]; h[i].heapIndex = i; h[j].heapIndex = j }
func (h *deadlineHeap) Push(v any)   { e := v.(*entry); e.heapIndex = len(*h); *h = append(*h, e) }
func (h *deadlineHeap) Pop() any {
	a := *h
	n := len(a) - 1
	e := a[n]
	a[n] = nil
	*h = a[:n]
	e.heapIndex = -1
	return e
}

type tenantHeap []*tenantState

func (h tenantHeap) Len() int           { return len(h) }
func (h tenantHeap) Less(i, j int) bool { return h[i].expires.Before(h[j].expires) }
func (h tenantHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].heapIndex = i; h[j].heapIndex = j }
func (h *tenantHeap) Push(v any)        { t := v.(*tenantState); t.heapIndex = len(*h); *h = append(*h, t) }
func (h *tenantHeap) Pop() any {
	a := *h
	n := len(a) - 1
	t := a[n]
	a[n] = nil
	*h = a[:n]
	t.heapIndex = -1
	return t
}
func (s *Scheduler) refill(t *tenantState, now time.Time) {
	if now.Before(t.updated) {
		return
	}
	t.tokens = min(float64(s.cfg.RateBurst), t.tokens+now.Sub(t.updated).Seconds()*s.cfg.RatePerSecond)
	t.updated = now
}
func (s *Scheduler) unidle(t *tenantState) {
	if t.heapIndex >= 0 {
		heap.Remove(&s.idleTenants, t.heapIndex)
	}
}
func (s *Scheduler) idle(t *tenantState, now time.Time) {
	if t.active > 0 || t.queued > 0 {
		return
	}
	s.refill(t, now)
	s.unidle(t)
	if t.tokens >= float64(s.cfg.RateBurst) {
		delete(s.tenants, t.key)
		return
	}
	// Clamp the delay before duration conversion to avoid overflow for tiny rates.
	seconds := (float64(s.cfg.RateBurst) - t.tokens) / s.cfg.RatePerSecond
	seconds = min(seconds, float64(time.Hour.Seconds()*24*365))
	t.expires = t.updated.Add(time.Duration(seconds*float64(time.Second)) + time.Nanosecond)
	heap.Push(&s.idleTenants, t)
}
func (s *Scheduler) cleanup(now time.Time) bool {
	for n := 0; n < batchSize && len(s.deadlines) > 0 && !s.deadlines[0].classification.QueueDeadline.After(now); n++ {
		s.finish(s.deadlines[0], nil, ErrExpired)
	}
	for n := 0; n < batchSize && len(s.idleTenants) > 0 && !s.idleTenants[0].expires.After(now); n++ {
		t := heap.Pop(&s.idleTenants).(*tenantState)
		s.idle(t, now)
	}
	return len(s.deadlines) > 0 && !s.deadlines[0].classification.QueueDeadline.After(now) || len(s.idleTenants) > 0 && !s.idleTenants[0].expires.After(now)
}
func (s *Scheduler) armTimer(now time.Time) {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	var due time.Time
	if len(s.deadlines) > 0 {
		due = s.deadlines[0].classification.QueueDeadline
	}
	if len(s.idleTenants) > 0 && (due.IsZero() || s.idleTenants[0].expires.Before(due)) {
		due = s.idleTenants[0].expires
	}
	if due.IsZero() {
		return
	}
	s.timer = time.AfterFunc(max(time.Duration(0), due.Sub(now)), func() { s.mu.Lock(); defer s.mu.Unlock(); s.dispatch(time.Now()) })
}

// Entries remain FIFO by original enqueue order even when rollback merges queues.
// The heap allows bounded O(log Q) reinsertion without scanning existing waiters.
type entryHeap []*entry

func (h entryHeap) Len() int           { return len(h) }
func (h entryHeap) Less(i, j int) bool { return h[i].order < h[j].order }
func (h entryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].queueIndex = i; h[j].queueIndex = j }
func (h *entryHeap) Push(v any)        { e := v.(*entry); e.queueIndex = len(*h); *h = append(*h, e) }
func (h *entryHeap) Pop() any {
	a := *h
	n := len(a) - 1
	e := a[n]
	a[n] = nil
	*h = a[:n]
	e.queueIndex = -1
	return e
}
func (s *Scheduler) fixDeadline(e *entry) { heap.Fix(&s.deadlines, e.heapIndex) }
