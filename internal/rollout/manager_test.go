package rollout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"pooltune/internal/domain"
	"pooltune/internal/policy"
	"pooltune/internal/store"
	"testing"
	"time"
)

type fixtureDriver struct {
	incomplete    bool
	metricLatency time.Duration
	now           *time.Time
	runtime       Runtime
	commands      []Command
	aborts        int
	loseACK       bool
}

func (d *fixtureDriver) Inspect(context.Context, domain.PathKind) (Runtime, error) {
	return d.runtime, nil
}
func (d *fixtureDriver) Apply(_ context.Context, c Command) (Ack, error) {
	d.commands = append(d.commands, c)
	d.runtime.ActivationRevision = c.Revision
	if d.loseACK {
		d.loseACK = false
		return Ack{}, context.DeadlineExceeded
	}
	return Ack{Revision: c.Revision, Active: true, At: *d.now}, nil
}
func (d *fixtureDriver) Status(_ context.Context, revision string) (Ack, error) {
	return Ack{Revision: revision, Active: true, At: *d.now}, nil
}
func (d *fixtureDriver) Abort(context.Context, string, string) (Ack, error) {
	d.aborts++
	return Ack{Revision: "fallback-new", Active: true, At: *d.now}, nil
}
func (d *fixtureDriver) Metrics(_ context.Context, e Experiment) (MetricsSnapshot, error) {
	*d.now = d.now.Add(d.metricLatency)
	_, m, _ := fixture()
	m.Now = *d.now
	m.At = *d.now
	m.ResourceAt = *d.now
	m.Start = d.now.Add(-10 * time.Second)
	m.End = *d.now
	m.ExperimentID = e.ID
	m.Target = e.Target
	m.Path = e.Path
	m.Epoch = e.Epoch
	m.StableRevision = e.StableRevision
	m.CandidateRevision = e.CandidateRevision
	m.GuardrailVersion = e.GuardrailVersion
	m.CapacityRevision = e.CapacityRevision
	c := uint64(e.Fractions[e.Stage] * 10000)
	s := 10000 - c
	current := Cohort{Arrivals: c, Successes: c}
	stable := Cohort{Arrivals: s, Successes: s}
	stable.Queue[5] = s
	stable.Total[6] = s
	if e.Phase == Partitioning {
		current.Queue[5] = c
		current.Total[6] = c
	} else {
		current.Queue[4] = c
		current.Total[5] = c
	}
	m.Groups = []Group{{Workload: "interactive", Stable: stable, Candidate: current}}
	if d.incomplete {
		m.Window = Window{}
	}
	return m, nil
}
func managerFixture(t *testing.T) (*Manager, *fixtureDriver, *store.Store, StartRequest) {
	t.Helper()
	_, _, profile := fixture()
	now := time.Now()
	d := &fixtureDriver{now: &now, runtime: Runtime{ActivationRevision: "initial-activation", PolicyRevision: "p1", GuardrailVersion: "g1", CapacityRevision: "c1", Epoch: 1, Effective: 100, Healthy: true}}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	db, e := store.OpenStore(filepath.Join(dir, "control.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	m, e := NewManager(ManagerConfig{Target: "db", Directory: dir, Profile: profile, Lease: 90 * time.Second, Interval: 10 * time.Second, Clock: func() time.Time { return *d.now }}, db, d)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { m.Close() })
	reg := policy.Registry{Fallback: policy.Fallback{QueueWait: time.Second, QueueLimit: 10, ConcurrencyCap: 10}, Workloads: []policy.Workload{{ID: "interactive", Selector: policy.Selector{Target: "db", Path: domain.PathREST, Principal: "app", Method: "GET", Route: "/test"}, DefaultClass: domain.ClassNormal, AllowedClasses: []domain.Class{domain.ClassNormal}, MaxQueueWait: time.Second, MaxQueueLimit: 10, MaxConcurrency: 100}}}
	a := policy.Policy{SchemaVersion: "1", Revision: "p1", GuardrailVersion: "g1", ReserveFraction: .1, ClassWeights: policy.ClassWeights{Critical: 6, Normal: 3, Background: 1}, Workloads: []policy.WorkloadSettings{{WorkloadID: "interactive", QueueWait: time.Second, QueueLimit: 5, ConcurrencyCap: 100}}}
	bounds := policy.Guardrails{Version: "g1", MaxChangedEntries: 5, MaxReserveFraction: .5, MinClassWeight: 1, MaxClassWeight: 10}
	cp, e := policy.Compile(a, bounds, reg)
	if e != nil {
		t.Fatal(e)
	}
	bounds.CurrentRevision = "p1"
	bounds.CurrentPolicy = cp
	b := a
	b.Revision = "p2"
	b.ExpectedPredecessor = "p1"
	b.ClassWeights.Normal = 4
	req := StartRequest{ID: "e1", Key: "request1", Path: domain.PathREST, CandidateCreatedAt: now.Add(-time.Minute), Stable: a, Candidate: b, Guardrails: bounds, Registry: reg, Fraction: .1}
	return m, d, db, req
}
func TestManagerRunsBaselineAllStagesAndFinalObservation(t *testing.T) {
	m, d, _, req := managerFixture(t)
	if e := m.Start(t.Context(), req); e != nil {
		t.Fatal(e)
	}
	if !m.HoldElectiveChanges() || len(d.commands) != 1 || d.commands[0].CandidatePolicy.Revision != "p1" {
		t.Fatal("baseline not split on stable policy")
	}
	other := req
	other.ID = "e2"
	other.Key = "second"
	other.Path = domain.PathPostgres
	if e := m.Start(t.Context(), other); e == nil {
		t.Fatal("second experiment on same database")
	}
	for i := 0; i < 10; i++ {
		*d.now = d.now.Add(10 * time.Second)
		if e := m.Tick(t.Context()); e != nil {
			t.Fatal(e)
		}
	}
	e, reason := m.View()
	if e.Phase != Accepted || m.HoldElectiveChanges() {
		t.Fatalf("phase %s reason %s", e.Phase, reason)
	}
	if len(d.commands) != 6 {
		t.Fatalf("stages skipped: %+v", d.commands)
	}
	for _, c := range d.commands {
		if c.StableBudget+c.CandidateBudget != c.Effective {
			t.Fatal("capacity invented")
		}
	}
}
func TestLostACKNeverResendsAndRestartAborts(t *testing.T) {
	m, d, db, req := managerFixture(t)
	d.loseACK = true
	if e := m.Start(t.Context(), req); !errors.Is(e, ErrUncertain) {
		t.Fatal(e)
	}
	if e := m.Tick(t.Context()); e != nil || len(d.commands) != 1 {
		t.Fatal("lost ACK replayed")
	}
	cfg := m.cfg
	m.Close()
	restored, e := NewManager(cfg, db, d)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	if e = restored.Tick(t.Context()); e != nil || d.aborts != 1 || len(d.commands) != 1 {
		t.Fatal("restart resumed candidate")
	}
}
func TestHistoryOutageCannotBlockSafetyAbort(t *testing.T) {
	m, d, db, req := managerFixture(t)
	if e := m.Start(t.Context(), req); e != nil {
		t.Fatal(e)
	}
	db.Close()
	d.runtime.Frozen = true
	if e := m.Tick(t.Context()); !errors.Is(e, ErrStorage) || d.aborts != 1 || !m.HoldElectiveChanges() {
		t.Fatal("audit failure disabled safety or hidden gap")
	}
}
func TestRepeatedWindowDoesNotCountTwice(t *testing.T) {
	e, m, p := fixture()
	e.PassingWindows = 0
	d := Evaluate(e, m, p)
	if !d.Passing || d.Action != domain.DecisionWait {
		t.Fatal(d)
	}
	e.PassingWindows = 1
	e.LastWindowEnd = m.End
	if d = Evaluate(e, m, p); d.Action == domain.DecisionAdvance {
		t.Fatal("same samples counted twice")
	}
}

func TestProfileCannotOmitAffectedWorkload(t *testing.T) {
	m, _, _, req := managerFixture(t)
	req.Registry.Workloads[0].ID = "unmeasured"
	req.Stable.Workloads[0].WorkloadID = "unmeasured"
	req.Candidate.Workloads[0].WorkloadID = "unmeasured"
	// Recompile the stable snapshot for this legitimate different registry.
	g := req.Guardrails
	g.CurrentRevision = ""
	g.CurrentPolicy = nil
	p := req.Stable
	p.ExpectedPredecessor = ""
	cp, e := policy.Compile(p, g, req.Registry)
	if e != nil {
		t.Fatal(e)
	}
	req.Guardrails.CurrentPolicy = cp
	if e = m.Start(t.Context(), req); e == nil {
		t.Fatal("unmeasured workload experiment accepted")
	}
}
func TestHumanPolicyEditAbortsExperiment(t *testing.T) {
	m, d, _, req := managerFixture(t)
	if e := m.Start(t.Context(), req); e != nil {
		t.Fatal(e)
	}
	d.runtime.PolicyRevision = "human-new"
	if e := m.Tick(t.Context()); e != nil || d.aborts != 1 {
		t.Fatal("human edit left stale experiment running")
	}
}

func TestRejectedCandidateCannotBeRetriedWithNewKey(t *testing.T) {
	m, d, _, req := managerFixture(t)
	if e := m.Start(t.Context(), req); e != nil {
		t.Fatal(e)
	}
	d.runtime.Frozen = true
	if e := m.Tick(t.Context()); e != nil {
		t.Fatal(e)
	}
	d.runtime.Frozen = false
	*d.now = d.now.Add(time.Hour)
	req.ID = "try-again"
	req.Key = "new-key"
	if e := m.Start(t.Context(), req); e == nil || len(d.commands) != 1 {
		t.Fatal("reran same candidate until it passed")
	}
}

func TestInitialCommandUsesActivationPredecessor(t *testing.T) {
	m, d, _, request := managerFixture(t)
	d.runtime.ActivationRevision = "activation-17"
	if err := m.Start(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if d.commands[0].ExpectedRevision != "activation-17" {
		t.Fatal("policy ID used as activation predecessor")
	}
}

func TestSamePolicyHumanActivationFencesExperiment(t *testing.T) {
	m, d, _, req := managerFixture(t)
	if err := m.Start(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	d.runtime.ActivationRevision = "human-limits-edit"
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	e, _ := m.View()
	if e.Phase != Reverted || d.aborts != 1 {
		t.Fatal("human activation not fenced")
	}
}

func TestRestartResolvesCommittedAcceptanceInsteadOfRevertingIt(t *testing.T) {
	m, d, db, req := managerFixture(t)
	if err := m.Start(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	// Crash after the traffic process committed acceptance, before its ACK was
	// saved in manager history. Only this pending acceptance can be reconciled.
	m.state.Pending = &Command{Revision: "accepted-runtime", Phase: Accepted, Expires: d.now.Add(time.Minute), Stage: 3}
	m.state.Kind = "activation_intent"
	if err := m.persist("activation_intent", ""); err != nil {
		t.Fatal(err)
	}
	cfg := m.cfg
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewManager(cfg, db, d)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if err = recovered.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	e, _ := recovered.View()
	if e.Phase != Accepted || d.aborts != 0 {
		t.Fatal("committed acceptance was incorrectly rolled back")
	}
}

func TestPendingAcceptanceRecognizesNewStablePolicy(t *testing.T) {
	m, d, _, req := managerFixture(t)
	if err := m.Start(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	m.state.Pending = &Command{Revision: "accepted-runtime", Phase: Accepted, Expires: d.now.Add(time.Minute), Stage: 3}
	d.runtime.ActivationRevision = "accepted-runtime"
	d.runtime.PolicyRevision = "p2"
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	e, _ := m.View()
	if e.Phase != Accepted || d.aborts != 0 {
		t.Fatal("pending acceptance misread as human edit")
	}
}

func TestTickUsesClockAfterReadingLiveMetrics(t *testing.T) {
	m, d, _, req := managerFixture(t)
	m.cfg.Profile.MinimumPassingWindows = 1
	if err := m.Start(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	*d.now = d.now.Add(20 * time.Second)
	d.metricLatency = time.Millisecond
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	e, _ := m.View()
	if e.Phase != Measuring {
		t.Fatal("fresh live metrics were classified as future evidence")
	}
}

func TestOpenWindowDoesNotConsumeEvaluationInterval(t *testing.T) {
	m, d, _, req := managerFixture(t)
	if err := m.Start(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	d.incomplete = true
	if err := m.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !m.state.LastTick.IsZero() {
		t.Fatal("open window postponed the next complete window")
	}
}
