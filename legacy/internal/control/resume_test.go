package control

import (
	"context"
	"errors"
	"fmt"
	"pooltune/internal/admin"
	"pooltune/internal/domain"
	"pooltune/internal/scheduler"
	"testing"
	"time"
)

func TestSafetyResumeRequiresFreshBoundedProof(t *testing.T) {
	e, s, _ := readyEngine(t)
	if err := e.Constrain(0); err != nil {
		t.Fatal(err)
	}
	v := e.View()
	if err := e.AcknowledgeFallback(v.Active.Revision, v.Active.Checksum); err != nil {
		t.Fatal(err)
	}
	env := e.View().Active
	env.ExpectedPredecessor = env.Revision
	env.Revision = "safety-resume-owner1"
	env.Limits = append([]domain.LimitSnapshot(nil), env.Limits...)
	env.Limits[0].Revision = env.Revision
	env.Limits[0].CapacityRevision = string(env.Revision)
	env.Limits[0].Effective = 2
	env.Limits[0].CohortBudgets = map[string]int{"stable": 2}
	env.Limits[0].CriticalReserve = 0
	env.Checksum = Checksum(env)
	now := time.Now()
	proof := ResumeEvidence{Checksum: v.Active.Checksum, Epoch: v.Epoch, Capacity: 2, HealthySince: now.Add(-6 * time.Second), ObservedAt: now}
	e.cfg.ResumeEvidence = func() ResumeEvidence { return proof }
	for _, kind := range []string{"short", "stale", "wrong-revision", "too-large"} {
		env.Revision = domain.RevisionID("safety-resume-" + kind)
		env.Limits[0].Revision = env.Revision
		env.Checksum = Checksum(env)
		old := proof
		switch kind {
		case "short":
			proof.HealthySince = now
		case "stale":
			proof.ObservedAt = now.Add(-3 * time.Second)
		case "wrong-revision":
			proof.Checksum = "other"
		case "too-large":
			proof.Capacity = 1
		}
		if ack, err := e.Activate(t.Context(), env); err != nil || ack.State != "rejected" {
			t.Fatalf("accepted %s proof: %+v %v", kind, ack, err)
		}
		if s.Stats().Effective != 0 {
			t.Fatal("rejected resume admitted work")
		}
		proof = old
	}
	env.Revision = "safety-resume-final"
	env.Limits[0].Revision = env.Revision
	env.Checksum = Checksum(env)
	if _, err := e.Activate(t.Context(), env); err != nil {
		t.Fatal(err)
	}
	if s.Stats().Effective != 2 || e.View().SafetyCeiling != nil {
		t.Fatal("verified resume not installed")
	}
	if _, err := e.Activate(t.Context(), env); err != nil {
		t.Fatal("resume retry not idempotent", err)
	}
}

func TestOwnerSafetyResumePreflightAndIdempotency(t *testing.T) {
	s, e, _, _, db, coord := serviceFixture(t, nil, false)
	if err := e.Constrain(0); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	c := admin.Command{ExpectedRevision: s.ownerRevision(e.View()), IdempotencyKey: "resume-safe"}
	if _, err := s.Execute(t.Context(), "safety-resume", c); err == nil {
		t.Fatal("owner resumed without live proof")
	}
	pending, _ := db.Pending(coord.LedgerTarget())
	if len(pending) != 0 || len(s.persisted.Commands) != 0 {
		t.Fatal("preflight rejection created intent")
	}
	now := time.Now()
	v := e.View()
	e.cfg.ResumeEvidence = func() ResumeEvidence {
		return ResumeEvidence{Checksum: v.Active.Checksum, Epoch: v.Epoch, Capacity: 2, HealthySince: now.Add(-6 * time.Second), ObservedAt: now}
	}
	if _, err := s.Execute(t.Context(), "safety-resume", c); err != nil {
		t.Fatal(err)
	}
	if e.View().Active.Limits[0].Effective != 2 || e.View().SafetyCeiling != nil {
		t.Fatal("owner resume not applied")
	}
	if _, err := s.Execute(t.Context(), "safety-resume", c); err != nil {
		t.Fatal("owner retry not idempotent", err)
	}
}

func TestExpiredResumeProofIsRejectedWithoutStrandingOwner(t *testing.T) {
	s, e, _, _, db, coord := serviceFixture(t, nil, false)
	e.Constrain(0)
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	v := e.View()
	proof := ResumeEvidence{Checksum: v.Active.Checksum, Epoch: v.Epoch, Capacity: 2, HealthySince: now.Add(-6 * time.Second), ObservedAt: now}
	e.cfg.ResumeEvidence = func() ResumeEvidence { return proof }
	// The evidence becomes unavailable after preflight, before the traffic write.
	coord.client = resumeExpiryClient{engineClient{e}, func() { proof = ResumeEvidence{} }}
	c := admin.Command{ExpectedRevision: s.ownerRevision(e.View()), IdempotencyKey: "expired-resume"}
	if _, err := s.Execute(t.Context(), "safety-resume", c); err == nil {
		t.Fatal("expired proof applied")
	}
	pending, _ := db.Pending(coord.LedgerTarget())
	if len(pending) != 0 {
		t.Fatal("definite rejection stranded pending intent")
	}
	if e.View().Active.Limits[0].Effective != 0 {
		t.Fatal("rejected resume admitted")
	}
	if err := db.Rebuild(); err != nil {
		t.Fatal(err)
	}
	pending, _ = db.Pending(coord.LedgerTarget())
	if len(pending) != 0 {
		t.Fatal("rebuild lost rejection")
	}
	now = time.Now()
	proof = ResumeEvidence{Checksum: v.Active.Checksum, Epoch: v.Epoch, Capacity: 2, HealthySince: now.Add(-6 * time.Second), ObservedAt: now}
	coord.client = engineClient{e}
	c = admin.Command{ExpectedRevision: s.ownerRevision(e.View()), IdempotencyKey: "retry-new-resume"}
	if _, err := s.Execute(t.Context(), "safety-resume", c); err != nil {
		t.Fatal("new owner attempt blocked", err)
	}
}

type resumeExpiryClient struct {
	engineClient
	expire func()
}

func (c resumeExpiryClient) Activate(ctx context.Context, env domain.ActivationEnvelope) (ActivationAck, error) {
	c.expire()
	return c.engineClient.Activate(ctx, env)
}

func TestSafetyResumeLostACKReconcilesOwnerWithoutReplay(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint(reject), func(t *testing.T) {
			s, e, _, _, db, coord := serviceFixture(t, nil, false)
			e.Constrain(0)
			if err := s.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			v := e.View()
			now := time.Now()
			proof := ResumeEvidence{Checksum: v.Active.Checksum, Epoch: v.Epoch, Capacity: 2, HealthySince: now.Add(-6 * time.Second), ObservedAt: now}
			e.cfg.ResumeEvidence = func() ResumeEvidence { return proof }
			calls := 0
			coord.client = resumeLostACKClient{engineClient{e}, func() {
				calls++
				if reject {
					proof = ResumeEvidence{}
				}
			}}
			c := admin.Command{ExpectedRevision: s.ownerRevision(e.View()), IdempotencyKey: "lost-resume"}
			if _, err := s.Execute(t.Context(), "safety-resume", c); err == nil {
				t.Fatal("lost ACK hidden")
			}
			if err := s.Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
			pending, _ := db.Pending(coord.target)
			expected := "confirmed"
			if reject {
				expected = "rejected"
			}
			if len(pending) != 0 || s.persisted.Commands[0].Status != expected || calls != 1 {
				t.Fatalf("not reconciled: pending=%v commands=%+v writes=%d", pending, s.persisted.Commands, calls)
			}
		})
	}
}

type resumeLostACKClient struct {
	engineClient
	before func()
}

func (c resumeLostACKClient) Activate(ctx context.Context, env domain.ActivationEnvelope) (ActivationAck, error) {
	c.before()
	if _, err := c.engineClient.Activate(ctx, env); err != nil {
		return ActivationAck{}, err
	}
	return ActivationAck{}, errors.New("lost response")
}

func TestRejectedResumeReconcilesAfterTrafficRestart(t *testing.T) {
	s, e, _, _, db, coord := serviceFixture(t, nil, false)
	e.Constrain(0)
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	v := e.View()
	now := time.Now()
	proof := ResumeEvidence{Checksum: v.Active.Checksum, Epoch: v.Epoch, Capacity: 2, HealthySince: now.Add(-6 * time.Second), ObservedAt: now}
	e.cfg.ResumeEvidence = func() ResumeEvidence { return proof }
	coord.client = resumeLostACKClient{engineClient{e}, func() { proof = ResumeEvidence{} }}
	c := admin.Command{ExpectedRevision: s.ownerRevision(e.View()), IdempotencyKey: "lost-rejected"}
	s.Execute(t.Context(), "safety-resume", c)
	cfg := e.cfg
	e.Close()
	cfg.Epoch++
	bootstrap, fresh := fixture(t, t.TempDir(), 2, approved)
	bootstrap.Close()
	restarted, err := New(cfg, fresh)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	coord.client = recoveryEngineClient{engineClient{restarted}}
	if err = coord.Recover(t.Context()); err != nil {
		t.Fatal("definite rejection blocked independent recovery", err)
	}
	pending, _ := db.Pending(coord.target)
	if len(pending) != 0 {
		t.Fatal(pending)
	}
	if restarted.View().Active.Limits[0].Effective != 0 {
		t.Fatal("restart cleared safety stop")
	}
}

type recoveryEngineClient struct{ engineClient }

func (c recoveryEngineClient) RecoverConfirmed(ctx context.Context, env domain.ActivationEnvelope) error {
	return c.e.RecoverConfirmed(ctx, env)
}

func TestNonzeroSafetyRecoveryPreservesAdmissionAndPolicy(t *testing.T) {
	s, e, _, _, _, _ := serviceFixture(t, nil, false)
	if err := e.Constrain(3); err != nil {
		t.Fatal(err)
	}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	v := e.View()
	now := time.Now()
	proof := ResumeEvidence{Checksum: v.Active.Checksum, Epoch: v.Epoch, Capacity: 3, HealthySince: now.Add(-6 * time.Second), ObservedAt: now}
	e.cfg.ResumeEvidence = func() ResumeEvidence { return proof }
	if e.View().ResumeCapacity != 3 {
		t.Fatal("reduced ceiling cannot recover")
	}
	proof.Capacity = 4
	if e.View().ResumeCapacity != 0 {
		t.Fatal("ceiling recovery permits an immediate increase")
	}
	proof.Capacity = 3
	permit, err := e.s.Acquire(t.Context(), e.Classify(domain.RequestContext{Target: "db", Path: domain.PathPostgres, Principal: "app"}, "stable"))
	if err != nil {
		t.Fatal(err)
	}
	if e.View().ResumeCapacity != 0 {
		t.Fatal("recovery accepted active work")
	}
	e.s.Release(permit, scheduler.Unknown)
	if e.View().ResumeCapacity != 0 {
		t.Fatal("recovery accepted uncertain work")
	}
	e.s.Reconcile(permit)
	before := string(v.Active.PolicyJSON)
	c := admin.Command{ExpectedRevision: s.ownerRevision(e.View()), IdempotencyKey: "release-reduced-ceiling"}
	if _, err := s.Execute(t.Context(), "safety-resume", c); err != nil {
		t.Fatal(err)
	}
	after := e.View()
	if after.SafetyCeiling != nil || after.Active.Limits[0].Effective != 3 || string(after.Active.PolicyJSON) != before {
		t.Fatal("recovery changed admission/policy or retained ceiling")
	}
}
