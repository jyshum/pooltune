package scheduler

import "time"

func (s *Scheduler) dispatch(now time.Time) {
	if s.cleanup(now) {
		s.continueSoon()
		return
	}
	processed := 0
	for ; processed < batchSize; processed++ {
		if s.active >= s.limits.Effective {
			break
		}
		e := s.choose(now)
		if e == nil {
			break
		}
		// Cancellation and dispatch compete for this same accounting lock.
		if err := e.ctx.Err(); err != nil {
			s.finish(e, nil, err)
			continue
		}
		grantNow := time.Now()
		if !e.classification.QueueDeadline.After(grantNow) {
			s.finish(e, nil, ErrExpired)
			continue
		}
		c := e.classification
		co := s.cohorts[c.Cohort]
		t := s.tenants[c.FairnessKey]
		w := s.workloads[c.Workload]
		p := &Permit{owner: s, classification: c, limitRevision: s.limits.Revision, started: grantNow}
		p.self = p
		s.active++
		co.active++
		co.classActive[classIndex(c.Class)]++
		t.active++
		w.active++
		p.activeNode = w.activeList.PushBack(p)
		s.finish(e, p, nil)
	}
	// Every selected entry consumes the batch budget, including cancellation and
	// expiry removals. A no-progress pass breaks early and does not spin.
	if processed == batchSize && s.queued > 0 && s.active < s.limits.Effective {
		s.continueSoon()
	}
	s.armTimer(time.Now())
}
func (s *Scheduler) continueSoon() {
	if s.continuing {
		return
	}
	s.continuing = true
	go func() { s.mu.Lock(); defer s.mu.Unlock(); s.continuing = false; s.dispatch(time.Now()) }()
}
func (s *Scheduler) choose(now time.Time) *entry {
	for n := 0; n < len(s.order); n++ {
		index := (s.cohortPosition + n) % len(s.order)
		co := s.cohorts[s.order[index]]
		if co.stopped || co.active >= co.budget {
			continue
		}
		if co.classActive[0] < co.reserve {
			if e := s.eligible(co, 0, now); e != nil {
				s.cohortPosition = (index + 1) % len(s.order)
				return e
			}
		}
		shared := max(0, co.classActive[0]-co.reserve) + co.classActive[1] + co.classActive[2]
		if shared >= co.budget-co.reserve {
			continue
		}
		var tried [3]bool
		for step := 0; step < len(co.sequence); step++ {
			pos := (co.position + step) % len(co.sequence)
			cl := co.sequence[pos]
			if tried[cl] {
				continue
			}
			tried[cl] = true
			if e := s.eligible(co, cl, now); e != nil {
				co.position = (pos + 1) % len(co.sequence)
				s.cohortPosition = (index + 1) % len(s.order)
				return e
			}
		}
	}
	return nil
}

// Inspect only tenant FIFO heads. Never walk the waiting requests behind them.
// An entirely capped set returns without scheduling a spinning continuation.
func (s *Scheduler) eligible(co *cohort, cl int, now time.Time) *entry {
	q := &co.classes[cl]
	for n := q.rotation.Len(); n > 0; n-- {
		node := q.rotation.Front()
		f := node.Value.(*tenantFIFO)
		q.rotation.MoveToBack(node)
		e := f.entries[0]
		c := e.classification
		// Return cancelled/expired heads for bounded removal by dispatch.
		if e.ctx.Err() != nil || !c.QueueDeadline.After(now) {
			return e
		}
		if s.tenants[c.FairnessKey].active >= min(s.cfg.MaxTenantActive, c.ConcurrencyCap) || s.workloads[c.Workload].active >= c.ConcurrencyCap {
			continue
		}
		return e
	}
	return nil
}
