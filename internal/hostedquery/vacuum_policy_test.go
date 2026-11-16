package hostedquery

import (
	"testing"
	"time"
)

func TestVacuumRequiresVisibilityCauseAndExactRecommendation(t *testing.T) {
	now := time.Now()
	e := IndexEvidence{RunID: "run", At: now, Decision: Investigate, TableOID: 42, Column: 2, Existing: true, Label: "lookup", Plan: PlanEvidence{Node: "Index Only Scan", ReturnedRows: 2000}, Vacuum: &VacuumEvidence{HeapFetches: 1000, ScanRows: 2000, ConditionsClear: true}}
	p := IndexProposal{Outcome: "propose", Repair: "vacuum", RunID: "run", TableOID: 42, Column: 2, Reason: "visibility work"}
	for _, tc := range []struct {
		name   string
		change func(*IndexEvidence, *IndexProposal)
		want   bool
	}{
		{"eligible", func(*IndexEvidence, *IndexProposal) {}, true},
		{"discovered column", func(e *IndexEvidence, p *IndexProposal) { e.Column = 7; p.Column = 7 }, true},
		{"mismatched column", func(_ *IndexEvidence, p *IndexProposal) { p.Column = 7 }, false},
		{"unexpected compound columns", func(_ *IndexEvidence, p *IndexProposal) { p.Columns = []int16{2, 7} }, false},
		{"system column", func(e *IndexEvidence, p *IndexProposal) { e.Column = -1; p.Column = -1 }, false},
		{"healthy", func(e *IndexEvidence, _ *IndexProposal) { e.Decision = NoChange }, false},
		{"few heap fetches", func(e *IndexEvidence, _ *IndexProposal) { e.Vacuum.HeapFetches = 999 }, false},
		{"low ratio", func(e *IndexEvidence, _ *IndexProposal) { e.Vacuum.ScanRows = 2001 }, false},
		{"maintenance", func(e *IndexEvidence, _ *IndexProposal) { e.Vacuum.ConditionsClear = false }, false},
		{"wrong repair", func(_ *IndexEvidence, p *IndexProposal) { p.Repair = "analyze" }, false},
		{"wrong table", func(_ *IndexEvidence, p *IndexProposal) { p.TableOID++ }, false},
		{"stale", func(e *IndexEvidence, _ *IndexProposal) { e.At = now.Add(-31 * time.Second) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copyE, copyP := e, p
			v := *e.Vacuum
			copyE.Vacuum = &v
			tc.change(&copyE, &copyP)
			if got := qualifyVacuum(copyE, copyP, 42, now); got != tc.want {
				t.Fatalf("eligible=%v want %v", got, tc.want)
			}
		})
	}
}

func TestVacuumNeedsLatencyBenefitAndMeasuredProtection(t *testing.T) {
	before := VacuumSample{AnalyzeSample: AnalyzeSample{Reads: 10, HealthyReads: 10, Writes: 10, ReadMS: 20, ReadMAD: .1, HealthyMS: 1, WriteMS: 1}, HeapFetches: 100000, ScanRows: 100000}
	after := VacuumSample{AnalyzeSample: AnalyzeSample{Reads: 10, HealthyReads: 10, Writes: 10, ReadMS: 10, ReadMAD: .1, HealthyMS: 1, WriteMS: 1}, HeapFetches: 0, ScanRows: 100000}
	for _, tc := range []struct {
		name   string
		change func(*VacuumSample)
		want   string
	}{
		{"benefit", func(*VacuumSample) {}, "improved_tested_inputs"},
		{"mechanism alone", func(s *VacuumSample) { s.ReadMS = 18 }, "inconclusive"},
		{"missing writes", func(s *VacuumSample) { s.Writes = 0 }, "inconclusive"},
		{"unchanged heap work", func(s *VacuumSample) { s.HeapFetches = 80000 }, "inconclusive"},
		{"write harm", func(s *VacuumSample) { s.WriteMS = 4 }, "regressed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := after
			tc.change(&s)
			got, _ := verifyVacuum(before, s)
			if got != tc.want {
				t.Fatalf("outcome=%s want %s", got, tc.want)
			}
		})
	}
}
