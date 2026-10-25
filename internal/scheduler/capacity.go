package scheduler

import (
	"pooltune/internal/telemetry"
	"time"
)

// Capacity evidence has a single owner: the accounting lock. This avoids
// reordering occupancy through asynchronous ingress callbacks. The recorder is
// bounded memory only and never calls external observers or performs I/O.
func (s *Scheduler) EnableCapacity(workloads []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.capacityClock == nil {
		s.capacityClock = time.Now
	}
	s.capacityNames = append([]string(nil), workloads...)
	return s.resetCapacity()
}
func (s *Scheduler) resetCapacity() error {
	if s.capacityClock == nil {
		return nil
	}
	l := s.limits
	r, err := telemetry.New(telemetry.Identity{Target: l.Target, Path: l.Path, Capacity: l.CapacityRevision, Policy: string(l.Revision), Guardrail: l.GuardrailVersion, Epoch: l.Epoch}, s.capacityNames, s.capacityClock)
	if err != nil {
		s.capacity = nil
		return err
	}
	s.capacity = r
	r.SetInFlight(s.active)
	s.capacityMixed = !s.drainedLocked()
	return nil
}
func (s *Scheduler) capacityRecord(w string, k telemetry.Kind, service, queue time.Duration) {
	if s.capacity != nil {
		s.capacity.RecordTiming(w, k, service, queue)
	}
}
func (s *Scheduler) CapacityMetrics() telemetry.MetricsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.capacity == nil {
		return telemetry.MetricsSnapshot{}
	}
	// After an activation, wait for every old binding to drain, then start an
	// entirely new window. No old-policy outcome contributes to growth.
	if s.capacityMixed && s.drainedLocked() {
		_ = s.resetCapacity()
	}
	now := time.Now()
	groups := make(map[string]telemetry.Runtime, len(s.workloads))
	for key, w := range s.workloads {
		r := telemetry.Runtime{Active: w.active, Uncertain: w.uncertain, Queued: w.queued}
		if n := w.activeList.Front(); n != nil {
			r.OldestActive = now.Sub(n.Value.(*Permit).started)
		}
		if n := w.queueList.Front(); n != nil {
			r.OldestQueued = now.Sub(n.Value.(*entry).enqueued)
		}
		groups[key] = r
	}
	s.capacity.ObserveRuntime(groups)
	m := s.capacity.Snapshot()
	if s.capacityMixed {
		m.Complete = false
	}
	return m
}

// OmitCapacity invalidates this revision's growth evidence when ingress cannot
// attribute an arrival (for example its bounded pre-classification slot limit).
func (s *Scheduler) OmitCapacity() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.capacity != nil {
		s.capacity.Omit()
	}
}
