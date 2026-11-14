package hostedquery

import (
	"testing"
	"time"
)

func TestMemoryCauseRequiresAttributionIdentityAndBoundedConcurrency(t *testing.T) {
	e := IndexEvidence{RunID: "run", At: time.Now(), Decision: Investigate, TableOID: 42, Column: 2, Plan: PlanEvidence{Node: "Sort", ExaminedRows: 100000, ReturnedRows: 100000}, Memory: &MemoryEvidence{FunctionOID: 43, TempBytes: 14688256, InternalTempBytes: 14688256, SortNodes: 1, MaxConcurrentCalls: 3, CandidateBytes: 33554432, AggregateBytes: 100663296, CurrentWorkMem: "4MB", CandidateWorkMem: "32MB", IdentityVerified: true}}
	p := IndexProposal{Outcome: "propose", Repair: "memory", RunID: "run", TableOID: 42, Column: 2, Reason: "attributed spill"}
	if !qualifyMemory(e, p, 42, time.Now()) {
		t.Fatal("bounded spill refused")
	}
	for _, mutate := range []func(*IndexEvidence){func(e *IndexEvidence) { e.Memory.TempBytes = 0 }, func(e *IndexEvidence) { e.Memory.InternalTempBytes = 0 }, func(e *IndexEvidence) { e.Memory.SortNodes = 2 }, func(e *IndexEvidence) { e.Memory.MaxConcurrentCalls = 4 }, func(e *IndexEvidence) { e.Memory.AggregateBytes = 33554432 }, func(e *IndexEvidence) { e.Memory.IdentityVerified = false }, func(e *IndexEvidence) { e.Memory.CurrentWorkMem = "32MB" }, func(e *IndexEvidence) { e.Vacuum = &VacuumEvidence{} }} {
		bad := e
		copy := *e.Memory
		bad.Memory = &copy
		mutate(&bad)
		if qualifyMemory(bad, p, 42, time.Now()) {
			t.Fatalf("unsafe evidence accepted: %+v", bad)
		}
	}
}
func TestMemoryVerificationRequiresAllBenefitAndProtectionGates(t *testing.T) {
	b := MemorySample{AnalyzeSample: AnalyzeSample{Reads: 10, HealthyReads: 10, Writes: 10, ReadMS: 30, ReadMAD: .1, HealthyMS: 1, WriteMS: 1}, TempBytes: 14688256, InternalTempBytes: 14688256, SortRows: 100000, ResultsCorrect: true}
	a := b
	a.ReadMS = 20
	a.TempBytes = 0
	a.InternalTempBytes = 0
	if o, _ := verifyMemory(b, a); o != "improved_tested_inputs" {
		t.Fatal(o)
	}
	for _, mutate := range []func(*MemorySample){func(s *MemorySample) { s.ReadMS = 26 }, func(s *MemorySample) { s.TempBytes = 10000000 }, func(s *MemorySample) { s.InternalTempBytes = 10000000 }, func(s *MemorySample) { s.ResultsCorrect = false }, func(s *MemorySample) { s.ReadMAD = 5 }, func(s *MemorySample) { s.Writes = 0 }, func(s *MemorySample) { s.HealthyMS = 10 }} {
		bad := a
		mutate(&bad)
		if o, _ := verifyMemory(b, bad); o == "improved_tested_inputs" {
			t.Fatalf("false success %+v", bad)
		}
	}
}
