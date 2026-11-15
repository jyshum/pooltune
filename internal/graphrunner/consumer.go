package graphrunner

import (
	"context"
	"errors"
	"pooltune/internal/hostedquery"
	"time"
)

// AdaptDiagnostic preserves the existing worker/action contract. Execution still
// checks the exact enrolled repair family, current target and measured benefit.
func AdaptDiagnostic(investigate DiagnosticInvestigator, read hostedquery.DiagnosticEvidenceReader) hostedquery.IndexInvestigator {
	return func(ctx context.Context, e hostedquery.IndexEvidence) (hostedquery.IndexProposal, error) {
		if investigate == nil {
			return hostedquery.IndexProposal{}, errors.New("diagnostic investigator is required")
		}
		input := DiagnosticInput{DiagnosticIdentity: DiagnosticIdentity{e.RunID, e.TableOID, e.Column}, Decision: e.Decision, Initial: SnapshotObservations(e)}
		result, err := investigate(ctx, input, func(ctx context.Context, tool string) (DiagnosticObservation, error) {
			if !diagnosticTools[tool] {
				return DiagnosticObservation{}, errors.New("unknown diagnostic tool")
			}
			unavailable := DiagnosticObservation{DiagnosticIdentity: input.DiagnosticIdentity, Tool: tool, At: time.Now().UTC(), Status: "unavailable"}
			if read == nil {
				return unavailable, nil
			}
			fresh, ok, err := read(ctx, tool, e)
			if err != nil {
				return DiagnosticObservation{}, err
			}
			if !ok {
				return unavailable, nil
			}
			if fresh.RunID != e.RunID || fresh.TableOID != e.TableOID || fresh.Column != e.Column {
				return DiagnosticObservation{}, errors.New("diagnostic reader changed enrollment")
			}
			for _, o := range SnapshotObservations(fresh) {
				if o.Tool == tool {
					if !validObservation(o, input, tool) {
						return DiagnosticObservation{}, errors.New("diagnostic reader returned invalid evidence")
					}
					return o, nil
				}
			}
			return DiagnosticObservation{}, errors.New("diagnostic reader omitted requested evidence")
		})
		if err != nil {
			return hostedquery.IndexProposal{}, err
		}
		p := result.Proposal
		p.Diagnostic = &hostedquery.DiagnosticTrace{ModelAttempts: result.ModelAttempts, ModelCalls: result.ModelCalls, Reads: result.Reads, ModelMS: result.ModelMS, ToolMS: result.ToolMS, TotalMS: result.TotalMS, StopReason: result.StopReason, History: result.History}
		return p, nil
	}
}
