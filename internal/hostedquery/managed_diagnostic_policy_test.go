package hostedquery

import (
	"testing"
	"time"
)

func TestManagedDiagnosticJobIdentity(t *testing.T) {
	now := time.Now()
	c := managedCronPolicyExample(false)
	e := IndexEvidence{RunID: "job", JobID: c.JobID, Decision: Investigate, At: now, ManagedCron: &c}
	p := IndexProposal{RunID: "job", JobID: c.JobID, Repair: "cron", Outcome: "propose", Reason: "Repeated blocking within selected window"}
	if err := ValidateDiagnosticProposal(e, p, now); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*IndexEvidence, *IndexProposal){
		func(e *IndexEvidence, p *IndexProposal) { p.JobID++ },
		func(e *IndexEvidence, p *IndexProposal) { e.TableOID = 42; p.TableOID = 42 },
		func(e *IndexEvidence, p *IndexProposal) { e.Column = 1; p.Column = 1 },
		func(e *IndexEvidence, p *IndexProposal) {
			e.JobID = 0
			p.JobID = 0
			e.TableOID = 42
			p.TableOID = 42
			e.Column = 1
			p.Column = 1
		},
		func(e *IndexEvidence, p *IndexProposal) { p.Repair = "memory" },
	} {
		a, b := e, p
		mutate(&a, &b)
		if ValidateDiagnosticProposal(a, b, now) == nil {
			t.Fatal("changed or mixed job identity admitted")
		}
	}
}
func TestManagedDiagnosticMemory(t *testing.T) {
	now := time.Now()
	m := ManagedMemoryEvidence{FunctionOID: 300, BaselineWorkMemBytes: 1 << 20, CandidateBytes: 32 << 20, ConcurrentBoostSlots: 3, AvailableMemoryBytes: 700 << 20, TotalMemoryBytes: 1024 << 20, MemoryObservedAt: now, OuterTempBytes: 16 << 20, InnerTempBytes: 16 << 20, SortRows: 50000, IdentityVerified: true, FallbackPreservesBaseline: true}
	e := IndexEvidence{RunID: "memory", TableOID: 42, Column: 2, Decision: Investigate, At: now, ManagedMemory: &m, Plan: PlanEvidence{Node: "Sort", ExaminedRows: 50000, ReturnedRows: 50000}}
	p := IndexProposal{RunID: e.RunID, TableOID: e.TableOID, Column: e.Column, Repair: "memory", Outcome: "propose", Reason: "Measured spill and fresh capacity"}
	if err := ValidateDiagnosticProposal(e, p, now); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ManagedMemoryEvidence){func(m *ManagedMemoryEvidence) { m.AvailableMemoryBytes = 300 << 20 }, func(m *ManagedMemoryEvidence) { m.FallbackPreservesBaseline = false }, func(m *ManagedMemoryEvidence) { m.MemoryObservedAt = now.Add(-31 * time.Second) }, func(m *ManagedMemoryEvidence) { m.InnerTempBytes = 0 }} {
		bad := m
		mutate(&bad)
		e.ManagedMemory = &bad
		if ValidateDiagnosticProposal(e, p, now) == nil {
			t.Fatal("unsupported managed memory admitted")
		}
	}
}
