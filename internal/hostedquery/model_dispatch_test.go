package hostedquery

import (
	"context"
	"testing"
	"time"
)

func TestModelProposalMustMatchTheAdmittedRepair(t *testing.T) {
	for _, repair := range []string{"", "analyze", "statistics", "vacuum", "memory", "cron"} {
		for _, outcome := range []string{"valid", "wrong repair", "wrong table", "wrong column", "wrong run", "decline", "provider failure"} {
			t.Run(repair+"/"+outcome, func(t *testing.T) {
				e := IndexEvidence{At: time.Now(), Decision: Investigate, TableOID: 42, Column: 2, Label: "lookup", Plan: PlanEvidence{Node: "Seq Scan"}}
				if repair == "cron" {
					e.Cron = &CronEvidence{}
					e.Plan.Node = "Result"
				}
				if repair == "memory" {
					e.Memory = &MemoryEvidence{}
				}
				if repair == "vacuum" {
					e.Vacuum = &VacuumEvidence{HeapFetches: 100000, ScanRows: 100000, ConditionsClear: true}
				}
				if repair == "analyze" {
					e.Analyze = &AnalyzeEvidence{EstimatedRows: 100000, ActualRows: 1}
				}
				if repair == "statistics" {
					e.Statistics = &StatisticsEvidence{Correlated: true}
				}
				result := &DispatchResult{}
				err := runIndexInvestigator(context.Background(), investigationClaim{InvestigationJob: InvestigationJob{ID: 7}, Attempt: 1}, result, e,
					func(_ context.Context, fresh IndexEvidence) (IndexProposal, error) {
						p := IndexProposal{Outcome: "propose", Repair: repair, RunID: fresh.RunID, TableOID: fresh.TableOID, Column: fresh.Column, Reason: "measured evidence"}
						switch outcome {
						case "wrong repair":
							p.Repair = "analyze"
							if repair == "analyze" {
								p.Repair = "statistics"
							}
						case "wrong table":
							p.TableOID++
						case "wrong column":
							p.Column++
						case "wrong run":
							p.RunID = "another-job"
						case "decline":
							p.Outcome, p.Repair = "no_change", ""
						case "provider failure":
							return IndexProposal{}, context.DeadlineExceeded
						}
						return p, nil
					})
				if outcome == "valid" || outcome == "decline" {
					if err != nil || result.Proposal == nil {
						t.Fatalf("valid reply rejected: %+v %v", result, err)
					}
					if outcome == "decline" && result.Outcome != "no_change" {
						t.Fatal("decline became a proposal")
					}
				} else if err == nil || result.Proposal != nil {
					t.Fatalf("invalid reply reached executor: %+v %v", result, err)
				}
			})
		}
	}
}

func TestModelRejectsMixedMemoryCauseBeforeCallingProvider(t *testing.T) {
	e := IndexEvidence{At: time.Now(), Decision: Investigate, TableOID: 42, Column: 2, Label: "lookup", Plan: PlanEvidence{Node: "Sort"}, Memory: &MemoryEvidence{}, Vacuum: &VacuumEvidence{}}
	result := &DispatchResult{}
	err := runIndexInvestigator(context.Background(), investigationClaim{}, result, e, func(context.Context, IndexEvidence) (IndexProposal, error) {
		t.Fatal("mixed evidence reached provider")
		return IndexProposal{}, nil
	})
	if err == nil && result.Outcome != "insufficient_evidence" {
		t.Fatal("mixed evidence admitted")
	}
}
