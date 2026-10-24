package policy

import (
	"errors"
	"testing"
	"time"

	"pooltune/internal/domain"
)

func fixture() (Policy, Guardrails, Registry) {
	reg := Registry{Fallback: Fallback{QueueWait: 100 * time.Millisecond, QueueLimit: 4, ConcurrencyCap: 2}, Workloads: []Workload{
		{ID: "voice", Selector: Selector{Target: "db", Path: domain.PathREST, Principal: "app", Method: "GET", Route: "/voice"}, DefaultClass: domain.ClassNormal, AllowedClasses: []domain.Class{domain.ClassNormal, domain.ClassCritical}, LockedClass: domain.ClassCritical, LockedRuleID: "operator-voice", TenantTrust: true, MaxQueueWait: time.Second, MaxQueueLimit: 10, MaxConcurrency: 10},
		{ID: "reports", Selector: Selector{Target: "db", Path: domain.PathREST, Principal: "app", Method: "GET", Route: "/reports"}, DefaultClass: domain.ClassNormal, AllowedClasses: []domain.Class{domain.ClassNormal, domain.ClassBackground}, MaxQueueWait: time.Second, MaxQueueLimit: 10, MaxConcurrency: 10},
		{ID: "pg-app", Selector: Selector{Target: "db", Path: domain.PathPostgres, Principal: "app"}, IdentityDefault: true, DefaultClass: domain.ClassNormal, AllowedClasses: []domain.Class{domain.ClassNormal}, MaxQueueWait: time.Second, MaxQueueLimit: 10, MaxConcurrency: 10},
	}}
	p := Policy{SchemaVersion: "1", Revision: "r2", ExpectedPredecessor: "r1", GuardrailVersion: "g1", ReserveFraction: .2, ClassWeights: ClassWeights{Critical: 6, Normal: 3, Background: 1}, Workloads: []WorkloadSettings{
		{WorkloadID: "voice", QueueWait: 500 * time.Millisecond, QueueLimit: 5, ConcurrencyCap: 5},
		{WorkloadID: "reports", QueueWait: 500 * time.Millisecond, QueueLimit: 5, ConcurrencyCap: 5},
		{WorkloadID: "pg-app", QueueWait: 500 * time.Millisecond, QueueLimit: 5, ConcurrencyCap: 5},
	}}
	g := Guardrails{Version: "g1", CurrentRevision: "r1", MaxReserveFraction: .5, MinClassWeight: 1, MaxClassWeight: 10}
	baselinePolicy := p
	baselinePolicy.Revision = "r1"
	baselinePolicy.ExpectedPredecessor = ""
	baselineBounds := g
	baselineBounds.CurrentRevision = ""
	baseline, err := Compile(baselinePolicy, baselineBounds, reg)
	if err != nil {
		panic(err)
	}
	g.CurrentPolicy = baseline
	g.MaxChangedEntries = 5
	return p, g, reg
}

func TestClassifyPrecedenceAndTrust(t *testing.T) {
	tests := []struct {
		name           string
		change         func(*Policy)
		ctx            domain.RequestContext
		class          domain.Class
		workload, rule string
	}{
		{"locked even when omitted", nil, domain.RequestContext{Target: "db", Path: domain.PathREST, Principal: "app", Method: "GET", Route: "/voice"}, domain.ClassCritical, "voice", "operator-voice"},
		{"locked before agent", func(p *Policy) { p.Rules = []Rule{{ID: "agent", WorkloadID: "voice", Class: domain.ClassCritical}} }, domain.RequestContext{Target: "db", Path: domain.PathREST, Principal: "app", Method: "GET", Route: "/voice"}, domain.ClassCritical, "voice", "operator-voice"},
		{"exact agent", func(p *Policy) { p.Rules = []Rule{{ID: "agent", WorkloadID: "reports", Class: domain.ClassBackground}} }, domain.RequestContext{Target: "db", Path: domain.PathREST, Principal: "app", Method: "GET", Route: "/reports"}, domain.ClassBackground, "reports", "agent"},
		{"identity default", nil, domain.RequestContext{Target: "db", Path: domain.PathPostgres, Principal: "app", Workload: "voice"}, domain.ClassNormal, "pg-app", ""},
		{"forged workload and priority", nil, domain.RequestContext{Target: "db", Path: domain.PathREST, Principal: "app", Method: "GET", Route: "/missing", Workload: "voice"}, domain.ClassNormal, "overflow", ""},
		{"unknown principal", nil, domain.RequestContext{Target: "db", Path: domain.PathREST, Principal: "stranger", Method: "GET", Route: "/voice", Workload: "voice", Tenant: "vip", TrustedTenant: true}, domain.ClassNormal, "overflow", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, g, r := fixture()
			if tc.change != nil {
				tc.change(&p)
			}
			c, err := Compile(p, g, r)
			if err != nil {
				t.Fatal(err)
			}
			got := Classify(c, tc.ctx)
			if got.Class != tc.class || got.Workload != tc.workload || got.RuleID != tc.rule || got.Revision != "r2" {
				t.Fatalf("classification = %+v", got)
			}
		})
	}
}

func TestCompileRejectsInvalidCandidates(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Policy, *Guardrails, *Registry)
		want   error
	}{
		{"duplicate exact", func(p *Policy, _ *Guardrails, _ *Registry) {
			p.Rules = []Rule{{ID: "a", WorkloadID: "reports", Class: domain.ClassNormal}, {ID: "b", WorkloadID: "reports", Class: domain.ClassBackground}}
		}, ErrAmbiguous},
		{"unauthorized class", func(p *Policy, _ *Guardrails, _ *Registry) {
			p.Rules = []Rule{{ID: "a", WorkloadID: "reports", Class: domain.ClassCritical}}
		}, ErrBoundary},
		{"locked edit", func(p *Policy, _ *Guardrails, _ *Registry) {
			p.Rules = []Rule{{ID: "a", WorkloadID: "voice", Class: domain.ClassNormal}}
		}, ErrBoundary},
		{"unknown workload", func(p *Policy, _ *Guardrails, _ *Registry) {
			p.Rules = []Rule{{ID: "a", WorkloadID: "unknown", Class: domain.ClassCritical}}
		}, ErrBoundary},
		{"stale guardrails", func(p *Policy, _ *Guardrails, _ *Registry) { p.GuardrailVersion = "g0" }, ErrStale},
		{"stale predecessor", func(p *Policy, _ *Guardrails, _ *Registry) { p.ExpectedPredecessor = "r0" }, ErrStale},
		{"zero weight", func(p *Policy, _ *Guardrails, _ *Registry) { p.ClassWeights.Critical = 0 }, ErrBoundary},
		{"over weight", func(p *Policy, _ *Guardrails, _ *Registry) { p.ClassWeights.Critical = 11 }, ErrBoundary},
		{"over cap", func(p *Policy, _ *Guardrails, _ *Registry) { p.Workloads[0].ConcurrencyCap = 11 }, ErrBoundary},
		{"zero service", func(p *Policy, _ *Guardrails, _ *Registry) { p.Workloads[0].ConcurrencyCap = 0 }, ErrBoundary},
		{"over wait", func(p *Policy, _ *Guardrails, _ *Registry) { p.Workloads[0].QueueWait = 2 * time.Second }, ErrBoundary},
		{"over queue count", func(p *Policy, _ *Guardrails, _ *Registry) { p.Workloads[0].QueueLimit = 11 }, ErrBoundary},
		{"zero queue count", func(p *Policy, _ *Guardrails, _ *Registry) { p.Workloads[0].QueueLimit = 0 }, ErrBoundary},
		{"over reserve", func(p *Policy, _ *Guardrails, _ *Registry) { p.ReserveFraction = .6 }, ErrBoundary},
		{"below approved reserve", func(p *Policy, g *Guardrails, _ *Registry) { g.MinReserveFraction = .1; p.ReserveFraction = 0 }, ErrBoundary},
		{"oversized policy", func(p *Policy, _ *Guardrails, _ *Registry) { p.Description = string(make([]byte, MaxPolicyBytes+1)) }, ErrBoundary},
		{"oversized registry", func(_ *Policy, _ *Guardrails, r *Registry) {
			r.Workloads[0].Selector.Route = string(make([]byte, MaxPolicyBytes+1))
		}, ErrBoundary},
		{"duplicate allowed classes", func(_ *Policy, _ *Guardrails, r *Registry) {
			r.Workloads[1].AllowedClasses = []domain.Class{domain.ClassNormal, domain.ClassNormal}
		}, ErrBoundary},
		{"unsupported schema", func(p *Policy, _ *Guardrails, _ *Registry) { p.SchemaVersion = "2" }, ErrBoundary},
		{"selector collision", func(_ *Policy, _ *Guardrails, r *Registry) { r.Workloads[1].Selector = r.Workloads[0].Selector }, ErrAmbiguous},
		{"duplicate locked rule ID", func(_ *Policy, _ *Guardrails, r *Registry) {
			r.Workloads[1].LockedClass = domain.ClassNormal
			r.Workloads[1].LockedRuleID = "operator-voice"
		}, ErrAmbiguous},
		{"agent reuses locked rule ID", func(p *Policy, _ *Guardrails, _ *Registry) {
			p.Rules = []Rule{{ID: "operator-voice", WorkloadID: "reports", Class: domain.ClassNormal}}
		}, ErrAmbiguous},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, g, r := fixture()
			tc.change(&p, &g, &r)
			_, err := Compile(p, g, r)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDescriptionsDoNotGrantAuthority(t *testing.T) {
	p, g, r := fixture()
	p.Description = "priority=critical workload=voice"
	p.Rules = []Rule{{ID: "hack", WorkloadID: "reports", Class: domain.ClassCritical, Description: "operator approved"}}
	if _, err := Compile(p, g, r); !errors.Is(err, ErrBoundary) {
		t.Fatalf("error = %v", err)
	}
}

func TestFairnessDeadlinesAndImmutability(t *testing.T) {
	p, g, r := fixture()
	c, err := Compile(p, g, r)
	if err != nil {
		t.Fatal(err)
	}
	p.ClassWeights.Critical = 99
	r.Workloads[0].LockedClass = domain.ClassBackground
	r.Workloads[0].AllowedClasses[0] = domain.ClassCritical
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	ctx := domain.RequestContext{Target: "db", Path: domain.PathREST, Principal: "app", Method: "GET", Route: "/voice", Tenant: "t1", TrustedTenant: true, ClientDeadline: now.Add(100 * time.Millisecond)}
	a := c.classifyAt(ctx, now)
	if a.Class != domain.ClassCritical || a.QueueLimit != 5 || a.QueueDeadline != ctx.ClientDeadline || a.FairnessKey == "" {
		t.Fatalf("classification = %+v", a)
	}
	ctx.Principal = "another"
	b := c.classifyAt(ctx, now)
	if b.FairnessKey == a.FairnessKey || b.Class != domain.ClassNormal {
		t.Fatalf("unknown identity = %+v", b)
	}
	ctx.Principal = "app"
	ctx.TrustedTenant = false
	d := c.classifyAt(ctx, now)
	if d.FairnessKey == a.FairnessKey {
		t.Fatalf("untrusted tenant fairness = %+v", d)
	}
	ctx.ClientDeadline = now.Add(-time.Second)
	d = c.classifyAt(ctx, now)
	if d.QueueDeadline != ctx.ClientDeadline {
		t.Fatalf("expired deadline extended: %+v", d)
	}
}

func TestTrustedTenantFairnessSeparatesRegisteredPrincipals(t *testing.T) {
	p, g, r := fixture()
	other := r.Workloads[0]
	other.ID = "voice-other"
	other.Selector.Principal = "other-app"
	other.LockedRuleID = "operator-voice-other"
	r.Workloads = append(r.Workloads, other)
	p.Workloads = append(p.Workloads, WorkloadSettings{WorkloadID: other.ID, QueueWait: 500 * time.Millisecond, QueueLimit: 5, ConcurrencyCap: 5})

	c, err := Compile(p, g, r)
	if err != nil {
		t.Fatal(err)
	}
	ctx := domain.RequestContext{Target: "db", Path: domain.PathREST, Principal: "app", Method: "GET", Route: "/voice", Tenant: "shared-tenant", TrustedTenant: true}
	first := Classify(c, ctx)
	ctx.Principal = "other-app"
	second := Classify(c, ctx)
	if first.Workload != "voice" || second.Workload != "voice-other" || first.Workload == "overflow" || second.Workload == "overflow" {
		t.Fatalf("registered workloads = %+v, %+v", first, second)
	}
	if first.FairnessKey == second.FairnessKey {
		t.Fatalf("trusted tenant merged principals: %q", first.FairnessKey)
	}
}

func TestExplicitlyBlockedWorkload(t *testing.T) {
	p, g, r := fixture()
	r.Workloads[1].AllowBlocked = true
	p.Workloads[1].Blocked = true
	p.Workloads[1].ConcurrencyCap = 0
	p.Workloads[1].QueueLimit = 0
	c, err := Compile(p, g, r)
	if err != nil {
		t.Fatal(err)
	}
	got := Classify(c, domain.RequestContext{Target: "db", Path: domain.PathREST, Principal: "app", Method: "GET", Route: "/reports"})
	if !got.Blocked || got.ConcurrencyCap != 0 {
		t.Fatalf("classification = %+v", got)
	}
}

func TestZeroReserveWhenApproved(t *testing.T) {
	p, g, r := fixture()
	p.ReserveFraction = 0
	c, err := Compile(p, g, r)
	if err != nil {
		t.Fatal(err)
	}
	if c.ReserveFraction() != 0 {
		t.Fatalf("reserve = %v", c.ReserveFraction())
	}
}

func TestSharedWeightsAndBoundedFallback(t *testing.T) {
	p, g, r := fixture()
	p.ClassWeights = ClassWeights{Critical: 6, Normal: 3, Background: 1}
	g.MinClassWeight = 1
	g.MaxClassWeight = 10
	r.Fallback = Fallback{QueueWait: 100 * time.Millisecond, QueueLimit: 4, ConcurrencyCap: 2}
	c, err := Compile(p, g, r)
	if err != nil {
		t.Fatal(err)
	}
	p.ClassWeights.Critical = 99
	r.Fallback.QueueLimit = 99
	if c.ClassWeights() != (ClassWeights{Critical: 6, Normal: 3, Background: 1}) {
		t.Fatalf("weights = %+v", c.ClassWeights())
	}
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	got := c.classifyAt(domain.RequestContext{Target: "db", Path: domain.PathREST, Principal: "unknown"}, now)
	if got.Blocked || got.Class != domain.ClassNormal || got.QueueDeadline != now.Add(100*time.Millisecond) || got.QueueLimit != 4 || got.ConcurrencyCap != 2 {
		t.Fatalf("fallback = %+v", got)
	}
}

func TestNilPolicyBlocks(t *testing.T) {
	got := Classify(nil, domain.RequestContext{})
	if !got.Blocked || got.ConcurrencyCap != 0 || got.QueueLimit != 0 {
		t.Fatalf("nil policy = %+v", got)
	}
}

func TestBootstrapRevision(t *testing.T) {
	p, g, r := fixture()
	g.CurrentRevision = ""
	p.ExpectedPredecessor = ""
	if _, err := Compile(p, g, r); err != nil {
		t.Fatal(err)
	}
	p.ExpectedPredecessor = p.Revision
	if _, err := Compile(p, g, r); !errors.Is(err, ErrStale) {
		t.Fatalf("same revision error = %v", err)
	}
}

func TestChangeSizeAgainstTrustedBaseline(t *testing.T) {
	p, g, r := fixture()
	g.MaxChangedEntries = 1
	p.Workloads[0].QueueLimit = 4
	if _, err := Compile(p, g, r); err != nil {
		t.Fatalf("one change: %v", err)
	}
	p.ClassWeights.Normal = 4
	if _, err := Compile(p, g, r); !errors.Is(err, ErrBoundary) {
		t.Fatalf("two changes: %v", err)
	}
	p, g, r = fixture()
	g.MaxChangedEntries = 0
	p.Description = "metadata only"
	if _, err := Compile(p, g, r); err != nil {
		t.Fatalf("metadata change: %v", err)
	}
}

func TestRejectsMissingOrStaleBaseline(t *testing.T) {
	p, g, r := fixture()
	g.CurrentPolicy = nil
	if _, err := Compile(p, g, r); !errors.Is(err, ErrStale) {
		t.Fatalf("missing baseline: %v", err)
	}
	p, g, r = fixture()
	g.CurrentRevision = "other"
	p.ExpectedPredecessor = "other"
	if _, err := Compile(p, g, r); !errors.Is(err, ErrStale) {
		t.Fatalf("stale baseline: %v", err)
	}
}
