package scheduler

import (
	"errors"
	"time"

	"pooltune/internal/domain"
)

var ErrRollbackBusy = errors.New("cohort rollback batch already running")

// StopCohort is a synchronous admission fence. It persists through limit/policy
// installs. A stopped cohort can be removed only after all its work has drained.
func (s *Scheduler) StopCohort(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cohorts[name]
	if c == nil {
		return ErrInvalid
	}
	c.stopped = true
	return nil
}

// RollbackBatch fences candidate admission first, then reclassifies at most 32
// queued entries outside the mutex. Call again while remaining > 0. The callback
// must use a trusted immutable workload mapping to the supplied stable policy,
// and must not change workload, fairness identity, or buffered bytes. A new
// deadline can shorten but cannot extend the original. Callback errors preserve
// the stopped candidate entry for retry; stable blocked results reject it.
// Active permits are never relabelled. Concurrent batches on a cohort reject.
func (s *Scheduler) RollbackBatch(candidate, stable string, reclassify func(domain.Classification) (domain.Classification, error)) (remaining int, err error) {
	if candidate == stable || reclassify == nil {
		return 0, ErrInvalid
	}
	s.mu.Lock()
	co := s.cohorts[candidate]
	target := s.cohorts[stable]
	if co == nil || target == nil || target.stopped {
		s.mu.Unlock()
		return 0, ErrInvalid
	}
	co.stopped = true
	if co.rollbackBusy {
		remaining = co.queued
		s.mu.Unlock()
		return remaining, ErrRollbackBusy
	}
	co.rollbackBusy = true
	batch := make([]*entry, 0, batchSize)
	for n := co.entries.Front(); n != nil && len(batch) < batchSize; n = n.Next() {
		e := n.Value.(*entry)
		s.detach(e)
		batch = append(batch, e)
	}
	s.mu.Unlock()
	for _, e := range batch {
		// Classification is a value; no scheduler state is passed to the callback.
		updated, callbackErr := reclassifySafely(reclassify, e.classification)
		s.mu.Lock()
		if !e.finished {
			original := e.classification
			updated.Cohort = stable
			if callbackErr == nil {
				target = s.cohorts[stable]
				switch {
				case target == nil || target.stopped:
					callbackErr = ErrStale
				case updated.Workload != original.Workload || updated.FairnessKey != original.FairnessKey || updated.BufferedBytes != original.BufferedBytes:
					callbackErr = ErrInvalid
				case updated.Revision != target.policy.Revision():
					callbackErr = ErrStale
				default:
					callbackErr = s.validateClassification(updated, false)
				}
			}
			switch {
			case callbackErr == ErrBlocked:
				s.finish(e, nil, ErrBlocked)
			case callbackErr != nil:
				s.attach(e)
				if err == nil {
					err = callbackErr
				}
			default:
				if updated.QueueDeadline.After(original.QueueDeadline) {
					updated.QueueDeadline = original.QueueDeadline
				}
				co.entries.Remove(e.cohortNode)
				co.queued--
				e.classification = updated
				target.queued++
				e.cohortNode = target.entries.PushBack(e)
				// Workload/tenant occupancy, original order, enqueue time, and byte counts
				// are unchanged. Repair the deadline heap after an optional shortening.
				s.fixDeadline(e)
				s.attach(e)
			}
		}
		s.dispatch(time.Now())
		s.mu.Unlock()
	}
	s.mu.Lock()
	co.rollbackBusy = false
	remaining = co.queued
	s.mu.Unlock()
	return remaining, err
}
func reclassifySafely(fn func(domain.Classification) (domain.Classification, error), c domain.Classification) (result domain.Classification, err error) {
	defer func() {
		if recover() != nil {
			err = ErrInvalid
		}
	}()
	return fn(c)
}
