package tuning

import (
	"context"
	"os"
	"pooltune/internal/admin"
	"pooltune/internal/domain"
	"pooltune/internal/durable"
	"testing"
	"time"
)

func TestManagedDecisionRetainsSeparatePressureAge(t *testing.T) {
	now := time.Now()
	m := Sample{At: now, Version: "v1", Target: 4, Working: 4, Idle: 0, Waiting: 1, CPU: .2, Memory: .3, PressureAt: now.Add(-60 * time.Second), PressureMaxAge: 75 * time.Second}
	if !m.Valid(now, bounds()) {
		t.Fatal("valid delayed managed pressure rejected")
	}
	m.PressureAt = now.Add(-76 * time.Second)
	if m.Valid(now, bounds()) {
		t.Fatal("old cloud pressure accepted")
	}
}
func TestManagedSourceCannotMasqueradeAsLocal(t *testing.T) {
	s, p, _, _ := serviceFixture(t)
	now := time.Now()
	p.o.At = now
	if e := s.Tick(context.Background(), now, domain.Resources{At: now, CPU: .2, Memory: .3, Source: "supabase:project"}); e == nil {
		t.Fatal("local authority accepted cloud input")
	}
}

func TestManagedServiceAcceptsOnlyBoundCloudPressure(t *testing.T) {
	_, p, local, j := serviceFixture(t)
	if _, e := NewManaged("db", bounds(), p, j, local, "azure:resource"); e == nil {
		t.Fatal("local authority silently converted")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	f, e := durable.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	s, e := NewManaged("db", bounds(), p, j, f, "azure:resource")
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	p.o.At = now
	r := domain.Resources{At: now.Add(-60 * time.Second), Source: "azure:resource", CPU: .2, Memory: .3}
	if e = s.Tick(context.Background(), now, r); e != nil {
		t.Fatal(e)
	}
	v := s.View(now)
	if !v.Ready || !v.Frozen {
		t.Fatalf("bad managed initial view: %+v", v)
	}
	if _, e = s.Execute(context.Background(), "resume", admin.Command{ExpectedRevision: v.Revision, IdempotencyKey: "go"}); e != nil {
		t.Fatal(e)
	}
	for _, source := range []string{"local-host", "azure:other", "supabase:resource"} {
		r.Source = source
		if e = s.Tick(context.Background(), now, r); e == nil || s.View(now).Ready {
			t.Fatal("unbound source accepted")
		}
	}
	r.Source = "azure:resource"
	r.At = now.Add(-121 * time.Second)
	if e = s.Tick(context.Background(), now, r); e == nil {
		t.Fatal("expired cloud sample accepted")
	}
}

func TestManagedQuietShrinksAnAlreadyPartlyIdlePool(t *testing.T) {
	now := time.Now()
	m := Sample{At: now, Version: "v1", Target: 5, Working: 3, Idle: 3, CPU: .2, Memory: .3, PressureAt: now, PressureMaxAge: 120 * time.Second}
	d := Decide(now, DecisionState{LastTick: now.Add(-time.Second), QuietSince: now.Add(-31 * time.Second)}, m, Bounds{Minimum: 2, Maximum: 8, CPUGrow: .7, CPUBackoff: .85, MemoryGrow: .8, MemoryBackoff: .9}, false)
	if d.Target != 4 {
		t.Fatalf("native idle cleanup prevented safe ceiling reduction: %+v", d)
	}
}

func TestManagedConfirmationAcceptsDrainedShrinkWithoutRefilling(t *testing.T) {
	s, p, _, j := serviceFixture(t)
	s.pressureSource = "azure:resource"
	s.pressureAge = 120 * time.Second
	ctx := context.Background()
	now := time.Now()
	p.o.At = now
	r := domain.Resources{At: now, Source: s.pressureSource, CPU: .2, Memory: .3}
	if e := s.Tick(ctx, now, r); e != nil {
		t.Fatal(e)
	}
	if e := s.apply(ctx, now, 3, "quiet", "controller", "", "", ""); e != nil {
		t.Fatal(e)
	}
	op, _ := j.Load("db")
	op.State = "complete"
	if e := j.Save(*op); e != nil {
		t.Fatal(e)
	}
	p.o.Pending = false
	p.o.Working = 2
	p.o.Idle = 2
	p.o.Version = "v2"
	if e := s.Tick(ctx, now, r); e != nil {
		t.Fatal(e)
	}
	if s.View(now).Pending != nil {
		t.Fatal("safe shrink still awaiting a fully populated ceiling")
	}
}

func TestManagedPressureDoesNotCreateUnsafeBusyShrinkIntent(t *testing.T) {
	s, p, _, _ := serviceFixture(t)
	s.pressureSource = "azure:resource"
	s.pressureAge = 120 * time.Second
	now := time.Now()
	p.o.At = now
	p.o.Working = 4
	p.o.Idle = 0
	p.o.Waiting = 2
	e := s.Tick(context.Background(), now, domain.Resources{At: now, Source: s.pressureSource, CPU: .99, Memory: .3})
	if e != nil || p.calls != 0 || s.View(now).Pending != nil {
		t.Fatalf("busy shrink attempted: calls %d, %v", p.calls, e)
	}
}
