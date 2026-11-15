package hostedquery

import (
	"testing"
	"time"
)

func TestCompoundProposalPreservesOrderedKeys(t *testing.T) {
	now := time.Now()
	e := IndexEvidence{RunID: "compound", At: now, Decision: Investigate, TableOID: 42, Column: 2, Columns: []int16{2, 3}, Plan: PlanEvidence{Node: "Seq Scan", ExaminedRows: 100000, ReturnedRows: 1}}
	p := IndexProposal{Outcome: "propose", RunID: e.RunID, TableOID: 42, Column: 2, Columns: []int16{2, 3}, Reason: "Both equality predicates are selective together."}
	if err := qualifyIndex(e, p, now); err != nil {
		t.Fatal(err)
	}
	for _, keys := range [][]int16{nil, {3, 2}, {2}, {2, 2}, {2, 4}, {2, 3, 4}} {
		bad := p
		bad.Columns = keys
		if qualifyIndex(e, bad, now) == nil {
			t.Fatalf("accepted keys %v", keys)
		}
	}
	for _, keys := range [][]int16{{3, 2}, {2, 2}, {2, 4}} {
		badE, badP := e, p
		badE.Columns = keys
		badP.Columns = keys
		if qualifyIndex(badE, badP, now) == nil {
			t.Fatalf("accepted unsupported enrollment %v", keys)
		}
	}
	scalar := e
	scalar.Columns = nil
	if qualifyIndex(scalar, p, now) == nil {
		t.Fatal("compound proposal crossed into scalar enrollment")
	}
	p.Repair = "analyze"
	if ValidateDiagnosticProposal(e, p, now) == nil {
		t.Fatal("compound identity accepted for other repair")
	}
}

func TestCompoundBitmapBenefitKeepsAllExistingProtectionGates(t *testing.T) {
	before := TrialSample{ReadMS: 11, HealthyMS: .1, WriteMS: .1, Reads: 40, HealthyReads: 40, Writes: 40, Blocks: 1300, Plan: "Seq Scan", PlanReturned: 1}
	for _, input := range inputIDs {
		before.Inputs = append(before.Inputs, TrialInput{Input: input, ReadMS: 11, HealthyMS: .1, WriteMS: .1})
	}
	after := before
	after.Inputs = append([]TrialInput(nil), before.Inputs...)
	for i := range after.Inputs {
		after.Inputs[i].ReadMS = .03
	}
	after.ReadMS, after.Blocks, after.Plan = .03, 4, "Bitmap Heap Scan"
	if outcome, _ := compareIndexTrial(before, after, true); outcome != "improved_tested_inputs" {
		t.Fatal(outcome)
	}
	if outcome, _ := compareTrial(before, after); outcome != "inconclusive" {
		t.Fatal("scalar plan gate changed")
	}
	after.Inputs[0].WriteMS = 5
	if outcome, _ := compareIndexTrial(before, after, true); outcome != "regressed" {
		t.Fatal(outcome)
	}
}
