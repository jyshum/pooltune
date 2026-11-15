package hostedquery

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

type ProjectAnalyzeRequest struct {
	Target          ProjectIndexTarget
	RunID           string
	DatabaseRoleOID uint32
	QueryID         int64
	Column          int16
	Decision        Decision
}

type ProjectAnalyzeObservation struct {
	Request              ProjectAnalyzeRequest
	At                   time.Time
	Sample               AnalyzeSample
	CoveringIndex        bool
	SampledValueFraction *float64
}

// ProjectAnalyzeReader returns fresh, measured observations of the same approved
// input and protected workload. It must bind the returned database, role, query
// and column identities to the actual measurements and use the worker clock
// for At. It runs while the action holds a relation pin, so probe reads must
// use compatible locks.
type ProjectAnalyzeReader func(context.Context, ProjectAnalyzeRequest) (ProjectAnalyzeObservation, error)

// RunProjectAnalyze consumes the detector's decision and an enrolled measurement
// reader. An interrupted outcome requires reconciliation of the recorded action
// before any retry. A confirmed refresh with unavailable verification remains
// inconclusive because PostgreSQL provides no exact undo.
func RunProjectAnalyze(ctx context.Context, config *pgx.ConnConfig, req ProjectAnalyzeRequest, read ProjectAnalyzeReader,
	investigate IndexInvestigator, guards ProjectMaintenanceGuards,
) (AnalyzeReport, error) {
	return runProjectAnalyze(ctx, config, req, read, investigate, guards, nil)
}

func runProjectAnalyze(ctx context.Context, config *pgx.ConnConfig, req ProjectAnalyzeRequest, read ProjectAnalyzeReader,
	investigate IndexInvestigator, guards ProjectMaintenanceGuards,
	traceMaintenance func(time.Time, ProjectMaintenanceResult, error),
) (report AnalyzeReport, err error) {
	if config == nil || read == nil || investigate == nil || guards.CheckPermission == nil ||
		guards.CheckCapacity == nil || guards.RecordIntent == nil || req.Target.DatabaseOID == 0 ||
		req.Target.TableOID == 0 || req.Target.Schema == "" || req.Target.Relation == "" ||
		req.RunID == "" || len(req.RunID) > 64 || req.DatabaseRoleOID == 0 || req.QueryID == 0 || req.Column <= 0 {
		return report, errors.New("statistics refresh requires an enrolled lookup and repair guards")
	}
	switch req.Decision {
	case Investigate:
	case NoChange:
		report.Outcome, report.Reason = "no_change", "the detector did not admit a statistics investigation"
		return report, nil
	case InsufficientEvidence:
		report.Outcome, report.Reason = "insufficient_evidence", "the detector has no sufficient workload measurement"
		return report, nil
	default:
		return report, errors.New("statistics refresh needs a valid detector decision")
	}
	baseline := time.Now()
	before, err := read(ctx, req)
	report.BaselineMS = elapsedMS(baseline)
	if err != nil {
		return report, err
	}
	evidence := projectAnalyzeEvidence(before)
	if !validProjectAnalyzeObservation(req, before, time.Now()) || !supportsAnalyzeEvidence(evidence) {
		report.Outcome, report.Reason = "insufficient_evidence", "measured evidence did not establish stale statistics for the enrolled lookup"
		return report, nil
	}
	report.Before, report.Evidence = before.Sample, evidence
	report.Before.EstimateError = rowEstimateError(before.Sample.EstimatedRows, before.Sample.PlanReturned)
	graphStart := time.Now()
	proposal, err := investigate(ctx, evidence)
	report.GraphMS = elapsedMS(graphStart)
	if err != nil {
		return report, err
	}
	if proposal.RunID != req.RunID || proposal.TableOID != req.Target.TableOID || proposal.Column != req.Column ||
		len(proposal.Columns) != 0 || len(proposal.Reason) == 0 || len(proposal.Reason) > 1000 {
		return report, errors.New("statistics proposal did not match the enrolled lookup")
	}
	report.Proposal = proposal
	if proposal.Outcome == "no_change" && (proposal.Repair == "" || proposal.Repair == "analyze") {
		report.Outcome, report.Reason = "no_change", proposal.Reason
		return report, nil
	}
	if proposal.Repair != "analyze" {
		return report, errors.New("statistics proposal selected a different repair")
	}
	if err := ValidateDiagnosticProposal(evidence, proposal, time.Now()); err != nil {
		return report, err
	}

	record := guards.RecordIntent
	stale := errors.New("statistics evidence changed before refresh")
	guards.RecordIntent = func(ctx context.Context, intent ProjectMaintenanceIntent) error {
		recheckStart := time.Now()
		current, err := read(ctx, req)
		if err != nil {
			return err
		}
		check := projectAnalyzeEvidence(current)
		if !validProjectAnalyzeObservation(req, current, time.Now()) || current.At.Before(recheckStart) || !supportsAnalyzeEvidence(check) ||
			current.Sample.PlanReturned != before.Sample.PlanReturned ||
			current.Sample.EstimatedRows != before.Sample.EstimatedRows ||
			*check.Analyze.SampledValueFraction != *evidence.Analyze.SampledValueFraction {
			return stale
		}
		return record(ctx, intent)
	}
	actionStart := time.Now()
	applied, err := ApplyProjectMaintenance(ctx, config, ProjectMaintenanceRequest{
		Target: req.Target, RunID: req.RunID, Operation: ProjectAnalyze, ObservedAt: evidence.At, Columns: []int16{req.Column},
	}, guards)
	if traceMaintenance != nil {
		traceMaintenance(actionStart, applied, err)
	}
	if errors.Is(err, stale) {
		report.Outcome, report.Reason = "no_change", stale.Error()
		return report, nil
	}
	if err != nil && !applied.WriteAttempted {
		return report, err
	}
	if err != nil || !applied.Confirmed {
		report.Outcome, report.Reason = "repair_interrupted", "statistics may have changed; inspect the recorded action before trying again"
		return report, nil
	}
	report.AnalyzeMS = elapsedMS(actionStart)
	verifyStart := time.Now()
	after, err := read(ctx, req)
	report.VerifyMS = elapsedMS(verifyStart)
	if err != nil || !validProjectAnalyzeObservation(req, after, time.Now()) ||
		after.At.Before(verifyStart) || after.Sample.PlanReturned != before.Sample.PlanReturned {
		report.Outcome, report.Reason = "inconclusive", "PostgreSQL confirmed the refresh; matched after measurements are unavailable"
		return report, nil
	}
	report.After = after.Sample
	report.After.EstimateError = rowEstimateError(after.Sample.EstimatedRows, after.Sample.PlanReturned)
	report.Outcome, report.Reason = verifyAnalyze(report.Before, report.After)
	return report, nil
}

func projectAnalyzeEvidence(observation ProjectAnalyzeObservation) IndexEvidence {
	r, s := observation.Request, observation.Sample
	var sampled *float64
	if f := observation.SampledValueFraction; f != nil {
		value := *f
		sampled = &value
	}
	return IndexEvidence{
		RunID: r.RunID, At: observation.At, Decision: r.Decision, TableOID: r.Target.TableOID,
		Column: r.Column, Label: "lookup", Existing: observation.CoveringIndex, ExecutionMS: s.ReadMS,
		Analyze: &AnalyzeEvidence{EstimatedRows: s.EstimatedRows, ActualRows: s.PlanReturned, SampledValueFraction: sampled},
		Plan:    PlanEvidence{Node: s.Plan, ExaminedRows: s.PlanExamined, ReturnedRows: s.PlanReturned, SharedBlocks: int64(s.Blocks)},
	}
}

func validProjectAnalyzeObservation(req ProjectAnalyzeRequest, observed ProjectAnalyzeObservation, now time.Time) bool {
	s := observed.Sample
	if req != observed.Request || observed.At.IsZero() || observed.At.After(now) || now.Sub(observed.At) > 30*time.Second ||
		s.Reads != analyzeSampleCount || s.HealthyReads != analyzeSampleCount || s.Writes != analyzeSampleCount ||
		s.Plan == "" || s.EstimatedRows < 1 || s.PlanReturned < 1 || s.PlanExamined < s.PlanReturned || s.Blocks >= math.MaxInt64 {
		return false
	}
	for _, value := range [...]float64{s.ReadMS, s.ReadMAD, s.HealthyMS, s.HealthyMAD, s.WriteMS, s.WriteMAD, s.Blocks} {
		if !nonnegativeFinite(value) {
			return false
		}
	}
	return true
}

func supportsAnalyzeEvidence(e IndexEvidence) bool {
	a := e.Analyze
	if a == nil || !e.Existing || e.Plan.Node != "Seq Scan" || e.Plan.ExaminedRows < a.ActualRows ||
		a.ActualRows <= 0 || a.ActualRows != e.Plan.ReturnedRows || a.EstimatedRows <= 0 ||
		a.SampledValueFraction == nil || !nonnegativeFinite(*a.SampledValueFraction) || *a.SampledValueFraction > 1 {
		return false
	}
	observed := float64(a.ActualRows) / float64(e.Plan.ExaminedRows)
	return rowEstimateError(a.EstimatedRows, a.ActualRows) >= 10 && *a.SampledValueFraction/observed >= 10
}
