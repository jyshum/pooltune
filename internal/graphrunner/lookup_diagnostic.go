package graphrunner

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"pooltune/internal/hostedquery"
)

// ProjectLookupInvestigator screens catalog-only evidence in the same diagnostic
// graph without granting its fixture repair or evidence tools.
func ProjectLookupInvestigator(stateDSN string) (hostedquery.EnrolledLookupInvestigator, error) {
	investigate, err := DiagnosticProcess(stateDSN)
	if err != nil {
		return nil, err
	}
	return LookupWithDiagnostic(investigate), nil
}

func LookupWithDiagnostic(investigate DiagnosticInvestigator) hostedquery.EnrolledLookupInvestigator {
	return func(ctx context.Context, runID string, tableOID uint32, column int16, at time.Time,
		evidence hostedquery.LookupEvidence, admit func(context.Context) error) (*hostedquery.LookupDiagnosis, error) {
		identity := DiagnosticIdentity{RunID: runID, TableOID: tableOID, Column: column}
		var observation any = evidence
		if evidence.TrafficPlan != nil {
			observation = struct {
				Plan        *hostedquery.TrafficPlan `json:"traffic_plan"`
				ObjectiveMS float64                  `json:"objective_ms"`
				MeansMS     []float64                `json:"recent_window_mean_ms"`
				Calls       []int64                  `json:"recent_window_calls"`
				Blocks      []int64                  `json:"recent_window_shared_block_accesses"`
			}{evidence.TrafficPlan, evidence.ObjectiveMS, evidence.WindowMeansMS, evidence.WindowCalls, evidence.SharedBlockAccesses}
		}
		data, err := json.Marshal(observation)
		if err != nil {
			return nil, err
		}
		result, err := investigate(ctx, DiagnosticInput{
			DiagnosticIdentity: identity, Decision: hostedquery.Investigate, Mode: "enrolled_lookup", AdmitModel: admit,
			Initial: []DiagnosticObservation{{DiagnosticIdentity: identity, Tool: "lookup", At: at, Status: "available", Data: data}},
		}, nil)
		if err != nil {
			return nil, err
		}
		if result.StopReason == "free_plan_limit" {
			return nil, hostedquery.ErrProjectModelBudget
		}
		if result.StopReason != "" || result.ModelCalls != 1 {
			return nil, nil
		}
		if len(result.History) != 1 || result.ModelAttempts != 1 || result.Proposal.Repair != "" || result.Proposal.ModelCall == nil {
			return nil, errors.New("invalid lookup diagnosis result")
		}
		var decision struct {
			Action      string          `json:"action"`
			Repair      *string         `json:"repair"`
			Tools       []string        `json:"tools"`
			Assessments json.RawMessage `json:"assessments"`
			Reason      string          `json:"reason"`
		}
		if strictJSON(result.History[0], &decision) != nil || decision.Action != "no_change" || decision.Repair != nil ||
			len(decision.Tools) != 0 || decision.Reason != result.Proposal.Reason || len(decision.Assessments) == 0 {
			return nil, errors.New("invalid read-only diagnosis")
		}
		return &hostedquery.LookupDiagnosis{
			Reason: decision.Reason, Assessments: decision.Assessments,
			Model: result.Proposal.ModelCall.Model, ModelMS: result.ModelMS,
		}, nil
	}
}
