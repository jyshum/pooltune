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
