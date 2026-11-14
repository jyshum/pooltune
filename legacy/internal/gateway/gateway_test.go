package gateway

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func budgetFixture(t *testing.T, global, account int64) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, e := Open(filepath.Join(dir, "gateway.db"), Limits{GlobalMicros: global, MaxConcurrent: 4, MaxAccountConcurrent: 2, RequestsPerMinute: 100, MaxRunSteps: 8, RunTTL: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	token, e := s.Approve("account1", account, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	return s, token
}
func TestConcurrentCopiedTokensCannotOverspend(t *testing.T) {
	s, token := budgetFixture(t, 100, 100)
	run, e := s.NewRun(token)
	if e != nil {
		t.Fatal(e)
	}
	var granted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if _, e := s.Reserve(token, run, n, "same-input", 60); e == nil {
				granted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if granted.Load() != 1 {
		t.Fatalf("overspend: %d", granted.Load())
	}
	state, e := s.Usage("account1")
	if e != nil || state.ReservedMicros != 60 {
		t.Fatal("reservation accounting")
	}
}
func TestIdempotencyRevocationAndUnknownCost(t *testing.T) {
	s, token := budgetFixture(t, 1000, 1000)
	run, e := s.NewRun(token)
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.Reserve(token, run, 0, "hash", 100)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Reserve(token, run, 0, "hash", 100); e != ErrDuplicate {
		t.Fatalf("duplicate: %v", e)
	}
	if _, e = s.Reserve(token, run, 0, "changed", 100); e != ErrDuplicate {
		t.Fatal("same key changed payload")
	}
	if e = s.Uncertain(r.ID); e != nil {
		t.Fatal(e)
	}
	usage, e := s.Usage("account1")
	if e != nil || usage.ReservedMicros != 100 {
		t.Fatal("unknown spend released")
	}
	if e = s.Revoke("account1"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.NewRun(token); e != ErrDenied {
		t.Fatal("revoked token accepted")
	}
}
func TestSettlementIsBoundedAndDurable(t *testing.T) {
	s, token := budgetFixture(t, 1000, 1000)
	run, _ := s.NewRun(token)
	r, e := s.Reserve(token, run, 0, "hash", 100)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Settle(r.ID, 101); e == nil {
		t.Fatal("cost exceeded reservation without failure")
	}
	if e = s.Settle(r.ID, 40); e != nil {
		t.Fatal(e)
	}
	if e = s.Settle(r.ID, 40); e != nil {
		t.Fatal("duplicate settlement")
	}
	if e = s.Settle(r.ID, 20); e == nil {
		t.Fatal("changed settled cost")
	}
	usage, e := s.Usage("account1")
	if e != nil || usage.SpentMicros != 40 || usage.ReservedMicros != 0 {
		t.Fatalf("usage %+v %v", usage, e)
	}
}
func TestAccountAndGlobalCapsAreIndependent(t *testing.T) {
	s, token := budgetFixture(t, 100, 60)
	other, e := s.Approve("account2", 100, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	run, _ := s.NewRun(token)
	if _, e = s.Reserve(token, run, 0, "h", 70); e != ErrBudget {
		t.Fatal("account budget bypass")
	}
	if _, e = s.Reserve(token, run, 0, "h", 60); e != nil {
		t.Fatal(e)
	}
	run2, _ := s.NewRun(other)
	if _, e = s.Reserve(other, run2, 0, "h", 50); e != ErrBudget {
		t.Fatal("global budget bypass")
	}
}
func TestAccountingOutagePreventsDispatch(t *testing.T) {
	s, token := budgetFixture(t, 100, 100)
	s.Close()
	model := &fixtureModel{}
	service := Service{Store: s, Model: model, Profile: testProfile()}
	if _, e := service.Start(context.Background(), token); e == nil {
		t.Fatal("accounting failure accepted")
	}
	if model.calls.Load() != 0 {
		t.Fatal("model contacted")
	}
}

type fixtureModel struct{ calls atomic.Int32 }

func (m *fixtureModel) Infer(context.Context, ModelRequest) (ModelResponse, error) {
	m.calls.Add(1)
	return ModelResponse{Done: true, Usage: Usage{InputTokens: 1, OutputTokens: 1, Known: true}}, nil
}
func testProfile() Profile {
	return Profile{ID: "fixture", Model: "fixture-model", Verified: true, MaxInputTokens: 32768, MaxOutputTokens: 100, InputMicrosPerMillion: 1, OutputMicrosPerMillion: 1, MaxPayloadBytes: 512, Timeout: time.Second}
}

func TestServiceHasNoCallerModelOverrideAndRetainsTimeoutReservation(t *testing.T) {
	s, token := budgetFixture(t, 100, 100)
	service := Service{Store: s, Model: errorModel{}, Profile: testProfile()}
	run, e := service.Start(context.Background(), token)
	if e != nil {
		t.Fatal(e)
	}
	_, e = service.Step(context.Background(), token, StepRequest{Run: run, Step: 0, ModelRequest: ModelRequest{Operation: "priority", Context: ApprovedContext{PolicyRevision: "p1", GuardrailVersion: "g1", SnapshotID: "s1"}}})
	if e == nil {
		t.Fatal("timeout accepted")
	}
	balance, e := s.Usage("account1")
	if e != nil || balance.ReservedMicros == 0 {
		t.Fatal("timeout freed paid exposure")
	}
}

type errorModel struct{}

func (errorModel) Infer(context.Context, ModelRequest) (ModelResponse, error) {
	return ModelResponse{}, context.DeadlineExceeded
}
func TestRotationDoesNotResetAllowance(t *testing.T) {
	s, token := budgetFixture(t, 100, 100)
	run, _ := s.NewRun(token)
	r, e := s.Reserve(token, run, 0, "hash", 80)
	if e != nil {
		t.Fatal(e)
	}
	s.Settle(r.ID, 80)
	rotated, e := s.Rotate("account1", time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.NewRun(token); e != ErrDenied {
		t.Fatal("old token valid")
	}
	run, _ = s.NewRun(rotated)
	if _, e = s.Reserve(rotated, run, 0, "hash", 30); e != ErrBudget {
		t.Fatal("rotation reset spending")
	}
}

func TestRestartAndSecondConnectionPreserveCaps(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	path := filepath.Join(dir, "budget.db")
	limits := Limits{GlobalMicros: 100, MaxConcurrent: 4, MaxAccountConcurrent: 2, RequestsPerMinute: 100, MaxRunSteps: 8, RunTTL: time.Minute}
	s, e := Open(path, limits)
	if e != nil {
		t.Fatal(e)
	}
	token, e := s.Approve("first", 100, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	other, e := s.Approve("second", 100, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	peer, e := Open(path, limits)
	if e != nil {
		t.Fatal(e)
	}
	run, _ := s.NewRun(token)
	run2, _ := peer.NewRun(other)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for _, call := range []func() error{func() error { _, e := s.Reserve(token, run, 0, "hash", 60); return e }, func() error { _, e := peer.Reserve(other, run2, 0, "hash", 60); return e }} {
		wg.Add(1)
		go func(call func() error) {
			defer wg.Done()
			if call() == nil {
				wins.Add(1)
			}
		}(call)
	}
	wg.Wait()
	s.Close()
	peer.Close()
	if wins.Load() != 1 {
		t.Fatal("separate SQLite connections overspent")
	}
	restored, e := Open(path, limits)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	a, e := restored.Usage("first")
	if e != nil {
		t.Fatal(e)
	}
	b, e := restored.Usage("second")
	if e != nil || a.ReservedMicros+b.ReservedMicros != 60 {
		t.Fatal("restart forgot reservations")
	}
	limits.GlobalMicros = 101
	if changed, e := Open(path, limits); e == nil {
		changed.Close()
		t.Fatal("restart silently changed lifetime cap")
	}
}
func TestEightStepAndAccountBinding(t *testing.T) {
	s, token := budgetFixture(t, 100, 100)
	other, e := s.Approve("other", 100, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	run, _ := s.NewRun(token)
	if _, e = s.Reserve(other, run, 0, "h", 1); e == nil {
		t.Fatal("stolen run accepted")
	}
	for step := 0; step < 8; step++ {
		r, e := s.Reserve(token, run, step, "h", 1)
		if e != nil {
			t.Fatal(e)
		}
		if e = s.Settle(r.ID, 1); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.Reserve(token, run, 8, "h", 1); e == nil {
		t.Fatal("ninth step accepted")
	}
}
func TestMalformedContextNeverDispatches(t *testing.T) {
	for _, bad := range []ModelRequest{
		{Operation: "shell"},
		{Operation: "priority", Context: ApprovedContext{PolicyRevision: "p", GuardrailVersion: "g", SnapshotID: "s", Descriptions: map[string]string{"work": "secret"}}},
		{Operation: "priority", Context: ApprovedContext{PolicyRevision: "p", GuardrailVersion: "g", SnapshotID: "s"}, History: []Exchange{{Tool: "ReadState", Result: []byte(`{"raw_sql":"SELECT private"}`)}}},
	} {
		s, token := budgetFixture(t, 100, 100)
		m := &fixtureModel{}
		service := Service{Store: s, Model: m, Profile: testProfile()}
		run, _ := service.Start(t.Context(), token)
		if _, e := service.Step(t.Context(), token, StepRequest{Run: run, ModelRequest: bad}); e == nil || m.calls.Load() != 0 {
			t.Fatal("malformed input dispatched")
		}
	}
}

func TestContextRejectsUnknownPolicyWorkloadsAndBounds(t *testing.T) {
	base := ModelRequest{Operation: "priority", Context: ApprovedContext{PolicyRevision: "p1", GuardrailVersion: "g1", SnapshotID: "s1", Workloads: []Workload{{ID: "w001", Class: "normal"}}}}
	base.Context.Policy = []byte(`{"schema_version":"1","revision":"p1","guardrail_version":"g1","workloads":[{"workload_id":"unknown-private-name"}]}`)
	if validateContext(base) == nil {
		t.Fatal("unknown policy workload accepted")
	}
	base.Context.Policy = nil
	base.Context.Bounds = &PolicyBounds{Workloads: []WorkloadBoundary{{ID: "unknown-private-name"}}}
	if validateContext(base) == nil {
		t.Fatal("unknown boundary workload accepted")
	}
}
