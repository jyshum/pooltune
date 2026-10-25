package control

import (
	"bytes"
	"pooltune/internal/domain"
	"strings"
	"time"
)

// ResumeEvidence is collected by the local runtime, never accepted from a
// request body. The five-second health interval is reset on revision or gaps.
type ResumeEvidence struct {
	Checksum                 string
	Epoch                    uint64
	Capacity                 int
	HealthySince, ObservedAt time.Time
}

func (e *Engine) resumeCapacityLocked() int {
	if e.closed || !e.ready.Load() || e.auditGap || e.cfg.ResumeEvidence == nil || e.state.SafetyCeiling == nil || len(e.state.Active.Limits) != 1 || e.state.Active.CandidateFraction != 0 {
		return 0
	}
	st := e.s.Stats()
	if st.Active != 0 || st.Uncertain != 0 {
		return 0
	}
	p := e.cfg.ResumeEvidence()
	now := time.Now()
	if p.Checksum != e.state.Active.Checksum || p.Epoch != e.cfg.Epoch || p.Capacity <= 0 || p.Capacity > e.cfg.MaxCapacity || p.HealthySince.IsZero() || p.ObservedAt.After(now) || now.Sub(p.ObservedAt) > 2*time.Second || p.ObservedAt.Sub(p.HealthySince) < 5*time.Second {
		return 0
	}
	if current := e.state.Active.Limits[0].Effective; current > 0 && p.Capacity != current {
		return 0
	}
	return p.Capacity
}
func isSafetyResume(revision domain.RevisionID) bool {
	return strings.HasPrefix(string(revision), "safety-resume-")
}
func (e *Engine) validResumeLocked(env domain.ActivationEnvelope) bool {
	capacity := e.resumeCapacityLocked()
	return capacity > 0 && len(env.Limits) == 1 && env.Limits[0].Effective == capacity && env.CandidateFraction == 0 && env.LeaseExpiresAt.IsZero() && bytes.Equal(env.PolicyJSON, e.state.Active.PolicyJSON)
}

// Quarantine stops this process without creating an unacknowledged durable
// policy revision during shutdown. Restart still needs independent recovery.
func (e *Engine) Quarantine() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ready.Store(false)
	e.s.Quarantine()
}

func (e *Engine) rejectActivationLocked(env domain.ActivationEnvelope) (ActivationAck, error) {
	if env.Checksum != Checksum(env) || len(e.state.Rejected) >= 128 {
		return ActivationAck{}, ErrInvalid
	}
	next := e.state
	next.Rejected = make(map[domain.RevisionID]ActivationAck, len(e.state.Rejected)+1)
	for id, ack := range e.state.Rejected {
		next.Rejected[id] = ack
	}
	ack := ActivationAck{Revision: env.Revision, Epoch: env.Epoch, Checksum: env.Checksum, State: "rejected"}
	next.Rejected[env.Revision] = ack
	if err := e.persist(next); err != nil {
		e.auditGap = true
		return ActivationAck{}, ErrStorage
	}
	e.state = next
	return ack, nil
}
