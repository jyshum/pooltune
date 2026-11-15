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
	return func(ctx context.Context, runID string, tableOID uint32, column int16, at time.Time,
		evidence hostedquery.LookupEvidence, admit func(context.Context) error) (*hostedquery.LookupDiagnosis, error) {
		identity := DiagnosticIdentity{RunID: runID, TableOID: tableOID, Column: column}
		data, err := json.Marshal(evidence)
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
	}, nil
}
