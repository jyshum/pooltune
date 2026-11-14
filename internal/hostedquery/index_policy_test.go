package hostedquery

import (
	"testing"
	"time"
)

func TestQualifyIndex(t *testing.T) {
	now := time.Now()
	valid := IndexEvidence{RunID: "run", At: now, Decision: Investigate, TableOID: 42, Column: 2,
		Plan: PlanEvidence{Node: "Seq Scan", ExaminedRows: 100_000, ReturnedRows: 1}}
	proposal := IndexProposal{Outcome: "propose", RunID: "run", TableOID: 42, Column: 2, Reason: "selective lookup"}
	if err := qualifyIndex(valid, proposal, now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*IndexEvidence, *IndexProposal)
	}{
		{"healthy", func(e *IndexEvidence, _ *IndexProposal) { e.Decision = NoChange }},
		{"unselective", func(e *IndexEvidence, _ *IndexProposal) { e.Plan.ReturnedRows = 11_000 }},
		{"existing", func(e *IndexEvidence, _ *IndexProposal) { e.Existing = true }},
		{"stale", func(e *IndexEvidence, _ *IndexProposal) { e.At = now.Add(-time.Minute) }},
		{"other evidence", func(_ *IndexEvidence, p *IndexProposal) { p.RunID = "other" }},
		{"unknown column", func(_ *IndexEvidence, p *IndexProposal) { p.Column++ }},
		{"other relation", func(_ *IndexEvidence, p *IndexProposal) { p.TableOID++ }},
		{"declined", func(_ *IndexEvidence, p *IndexProposal) { p.Outcome = "no_change" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, p := valid, proposal
			tc.change(&e, &p)
			if err := qualifyIndex(e, p, now); err == nil {
				t.Fatal("unsafe proposal accepted")
			}
		})
	}
}

func TestTrialOutcomeRequiresWriteEvidence(t *testing.T) {
	before := TrialSample{ReadMS: 11, HealthyMS: 0.1, WriteMS: 0.1, Reads: 40, HealthyReads: 40, Writes: 40, Blocks: 1334, Plan: "Seq Scan", PlanReturned: 1}
	for _, input := range inputIDs {
		before.Inputs = append(before.Inputs, TrialInput{Input: input, ReadMS: 11, HealthyMS: .1, WriteMS: .1})
	}
	after := before
	after.Inputs = append([]TrialInput(nil), before.Inputs...)
	for i := range after.Inputs {
		after.Inputs[i].ReadMS = .03
	}
	after.ReadMS, after.Blocks, after.Plan = 0.03, 3, "Index Scan"
	if outcome, _ := compareTrial(before, after); outcome != "improved_tested_inputs" {
		t.Fatal(outcome)
	}
	after.Writes = 0
	if outcome, _ := compareTrial(before, after); outcome != "inconclusive" {
		t.Fatal(outcome)
	}
	after.Writes, after.Inputs[0].WriteMS = 40, 5
	if outcome, _ := compareTrial(before, after); outcome != "regressed" {
		t.Fatal(outcome)
	}
}
