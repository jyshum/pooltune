package control

import (
	"errors"
	"pooltune/internal/domain"
	"pooltune/internal/scheduler"
	"testing"
	"time"
)

// A deliberately blocked durable write exposes the interval in which ingress
// used to invalidate the successful safety check before installation.
func TestSafetyActivationSerializesAccountingWithPersistence(t *testing.T) {
	for _, kind := range []string{"resume", "growth"} {
		for _, fail := range []bool{false, true} {
			name := kind
			if fail {
				name += "-storage-failure"
			}
			t.Run(name, func(t *testing.T) {
				e, s, _ := readyEngine(t)
				if kind == "resume" {
					if err := e.Constrain(3); err != nil {
						t.Fatal(err)
					}
					v := e.View()
					if err := e.AcknowledgeFallback(v.Active.Revision, v.Active.Checksum); err != nil {
						t.Fatal(err)
					}
					now := time.Now()
					e.cfg.ResumeEvidence = func() ResumeEvidence {
						return ResumeEvidence{Checksum: v.Active.Checksum, Epoch: v.Epoch, Capacity: 3, HealthySince: now.Add(-6 * time.Second), ObservedAt: now}
					}
				}
				before := e.View()
				env := before.Active
				env.ExpectedPredecessor = env.Revision
				env.Revision = "capacity-fenced"
				effective := env.Limits[0].Effective + 1
				if kind == "resume" {
					env.Revision = "safety-resume-fenced"
					effective--
				}
				env.Limits = append([]domain.LimitSnapshot(nil), env.Limits...)
				env.Limits[0].Revision = env.Revision
				env.Limits[0].CapacityRevision = string(env.Revision)
				env.Limits[0].Effective = effective
				env.Limits[0].CohortBudgets = map[string]int{"stable": effective}
				env.Checksum = Checksum(env)
				classification := e.Classify(domain.RequestContext{Target: "db", Path: domain.PathPostgres, Principal: "app"}, "stable")
				var held *scheduler.Permit
				if kind == "growth" {
					var err error
					held, err = s.Acquire(t.Context(), classification)
					if err != nil {
						t.Fatal(err)
					}
					defer s.Reconcile(held)
				}
				entered, proceed := make(chan struct{}), make(chan struct{})
				persist := e.persist
				e.persist = func(st snapshot) error {
					close(entered)
					<-proceed
					if fail {
						return ErrStorage
					}
					return persist(st)
				}
				activation := make(chan error, 1)
				go func() {
					ack, err := e.Activate(t.Context(), env)
					if err == nil && ack.State != "active" {
						err = ErrRejected
					}
					activation <- err
				}()
				<-entered
				attempted, accounted := make(chan struct{}), make(chan struct{})
				go func() {
					defer close(accounted)
					close(attempted)
					if held != nil {
						s.Release(held, scheduler.Unknown)
						return
					}
					p, err := s.Acquire(t.Context(), classification)
					if err == nil {
						s.Release(p, scheduler.Unknown)
						s.Reconcile(p)
					}
				}()
				<-attempted
				crossed := false
				select {
				case <-accounted:
					crossed = true
				case <-time.After(50 * time.Millisecond):
				}
				close(proceed)
				err := <-activation
				<-accounted
				if crossed {
					t.Error("scheduler accounting crossed the durable safety activation")
				}
				if fail {
					if !errors.Is(err, ErrStorage) {
						t.Fatal(err)
					}
					if e.View().Active.Revision != before.Active.Revision || s.Stats().Effective != before.Active.Limits[0].Effective {
						t.Fatal("storage failure changed admission")
					}
					if kind == "resume" && e.View().SafetyCeiling == nil {
						t.Fatal("storage failure cleared safety ceiling")
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if held != nil && s.Stats().Uncertain != 1 {
					t.Fatal("concurrent unknown result was lost")
				}
			})
		}
	}
}

func TestGrowthRejectsLateUncertaintyWithoutPendingIntent(t *testing.T) {
	s, e, _, _, db, coord := serviceFixture(t, nil, false)
	env := e.View().Active
	env.ExpectedPredecessor = env.Revision
	env.Revision = "lower-start"
	env.Limits[0].Revision = env.Revision
	env.Limits[0].Effective = 3
	env.Limits[0].CohortBudgets = map[string]int{"stable": 3}
	env.Checksum = Checksum(env)
	if _, err := coord.Apply(t.Context(), env); err != nil {
		t.Fatal(err)
	}
	configureGrowthEvidence(s, e)
	before := e.View().Active
	var held *scheduler.Permit
	coord.client = resumeExpiryClient{engineClient{e}, func() {
		var err error
		held, err = e.s.Acquire(t.Context(), e.Classify(domain.RequestContext{Target: "db", Path: domain.PathPostgres, Principal: "app"}, "stable"))
		if err != nil {
			t.Fatal(err)
		}
		e.s.Release(held, scheduler.Unknown)
	}}
	defer func() {
		if held != nil {
			e.s.Reconcile(held)
		}
	}()
	if err := s.Tick(t.Context()); !errors.Is(err, ErrRejected) {
		t.Fatalf("late uncertainty did not reject growth: %v", err)
	}
	if e.View().Active.Revision != before.Revision {
		t.Fatal("growth installed with uncertainty")
	}
	if err := db.Rebuild(); err != nil {
		t.Fatal(err)
	}
	pending, err := db.Pending(coord.target)
	if err != nil || len(pending) != 0 {
		t.Fatalf("rejected growth stranded intent: %v %v", pending, err)
	}
}

func TestResumeRechecksIdleAfterProofCollection(t *testing.T) {
	e, s, _ := readyEngine(t)
	if err := e.Constrain(3); err != nil {
		t.Fatal(err)
	}
	v := e.View()
	if err := e.AcknowledgeFallback(v.Active.Revision, v.Active.Checksum); err != nil {
		t.Fatal(err)
	}
	env := v.Active
	env.ExpectedPredecessor = env.Revision
	env.Revision = "safety-resume-late-active"
	env.Limits[0].Revision = env.Revision
	env.Checksum = Checksum(env)
	now := time.Now()
	var held *scheduler.Permit
	e.cfg.ResumeEvidence = func() ResumeEvidence {
		if held == nil {
			var err error
			held, err = s.Acquire(t.Context(), e.Classify(domain.RequestContext{Target: "db", Path: domain.PathPostgres, Principal: "app"}, "stable"))
			if err != nil {
				t.Fatal(err)
			}
		}
		return ResumeEvidence{Checksum: v.Active.Checksum, Epoch: v.Epoch, Capacity: 3, HealthySince: now.Add(-6 * time.Second), ObservedAt: now}
	}
	defer func() {
		if held != nil {
			s.Release(held, scheduler.Finished)
		}
	}()
	ack, err := e.Activate(t.Context(), env)
	if err != nil || ack.State != "rejected" {
		t.Fatalf("late active work did not reject resume: %+v %v", ack, err)
	}
	if e.View().SafetyCeiling == nil || s.Stats().Active != 1 {
		t.Fatal("rejection cleared ceiling or cancelled work")
	}
}

func TestDurableResumePreservesQueuedWork(t *testing.T) {
	e, s, _ := readyEngine(t)
	if err := e.Constrain(0); err != nil {
		t.Fatal(err)
	}
	v := e.View()
	if err := e.AcknowledgeFallback(v.Active.Revision, v.Active.Checksum); err != nil {
		t.Fatal(err)
	}
	classification := e.Classify(domain.RequestContext{Target: "db", Path: domain.PathPostgres, Principal: "app"}, "stable")
	type result struct {
		permit *scheduler.Permit
		err    error
	}
	acquired := make(chan result, 1)
	go func() { p, err := s.Acquire(t.Context(), classification); acquired <- result{p, err} }()
	deadline := time.Now().Add(time.Second)
	for s.Stats().Queued != 1 {
		if time.Now().After(deadline) {
			t.Fatal("request did not queue")
		}
		time.Sleep(time.Millisecond)
	}
	env := v.Active
	env.ExpectedPredecessor = env.Revision
	env.Revision = "safety-resume-queued"
	env.Limits[0].Revision = env.Revision
	env.Limits[0].Effective = 2
	env.Limits[0].CohortBudgets = map[string]int{"stable": 2}
	env.Checksum = Checksum(env)
	now := time.Now()
	e.cfg.ResumeEvidence = func() ResumeEvidence {
		return ResumeEvidence{Checksum: v.Active.Checksum, Epoch: v.Epoch, Capacity: 2, HealthySince: now.Add(-6 * time.Second), ObservedAt: now}
	}
	if ack, err := e.Activate(t.Context(), env); err != nil || ack.State != "active" {
		t.Fatalf("resume: %+v %v", ack, err)
	}
	r := <-acquired
	if r.err != nil {
		t.Fatal("queued request lost", r.err)
	}
	defer s.Release(r.permit, scheduler.Finished)
	if r.permit.Classification().ActivationRevision != classification.ActivationRevision || s.Stats().Active != 1 {
		t.Fatal("queued binding not preserved")
	}
}
