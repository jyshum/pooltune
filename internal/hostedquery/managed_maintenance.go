package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

// ManagedMaintenanceReport separates one sampled input from aggregate traffic
// windows. It does not claim per-call latency distributions or synthetic writes.
type ManagedMaintenanceReport struct {
	RecoveryStatus      string                         `json:"recovery_status,omitempty"`
	RecoveryObservation *ProjectMaintenanceObservation `json:"recovery_observation,omitempty"`
	Operation           ProjectMaintenanceOperation    `json:"operation"`
	Measurement         string                         `json:"measurement"`
	Before              ManagedIndexMeasurement        `json:"before"`
	After               *ManagedIndexMeasurement       `json:"after,omitempty"`
	BeforeTraffic       *ManagedMaintenanceTraffic     `json:"before_traffic,omitempty"`
	AfterTraffic        *ManagedMaintenanceTraffic     `json:"after_traffic,omitempty"`
	Action              ProjectMaintenanceResult       `json:"action"`
	Outcome             string                         `json:"outcome"`
	Reason              string                         `json:"reason"`
}

type ManagedMaintenanceTraffic struct {
	ReadMeanMS  float64 `json:"read_mean_ms"`
	WriteMeanMS float64 `json:"write_mean_ms"`
	ReadCalls   int64   `json:"read_calls"`
	WriteCalls  int64   `json:"write_calls"`
}

func maintenanceTraffic(c *managedTrafficControls) *ManagedMaintenanceTraffic {
	if c == nil {
		return nil
	}
	return &ManagedMaintenanceTraffic{c.ReadMS, c.WriteMS, c.ReadCalls, c.WriteCalls}
}

func (m *ManagedProjectIndex) runMaintenance(ctx context.Context, state, monitor *pgx.Conn, project supabaseproject.Project, epoch int64, claim investigationClaim, windows *ObservedLookupLatency, result *DispatchResult, observed supabaseproject.DiscoveredIndexEvidence, owner *pgx.Conn, before ManagedIndexMeasurement) (bool, error) {
	if len(observed.Columns) != 1 || (!slices.Contains(m.Capabilities, "analyze") && !slices.Contains(m.Capabilities, "vacuum")) {
		return false, nil
	}
	runID := fmt.Sprintf("managed-maintenance-%d-%d", claim.ID, claim.Attempt)
	evidence, operation, err := m.maintenanceEvidence(ctx, owner, observed, before, runID)
	if err != nil {
		return true, err
	}
	if operation == "" {
		return false, nil
	}
	result.Evidence, result.ThreadID, result.ObservedLatency = &evidence, runID, windows
	verify := func(ctx context.Context) error {
		if err := checkOwnedIndexClaim(ctx, state, project, epoch, claim); err != nil {
			return err
		}
		fresh, err := project.ReadDiscoveredIndexEvidence(ctx, monitor, claim.Role, claim.Query)
		if err != nil {
			return err
		}
		if len(claim.Generation) != 3 || fresh.DatabaseOID != claim.Database || fresh.RoleOID != claim.Role || fresh.QueryID != claim.Query || fresh.ObjectiveMS != windows.ObjectiveMS || !fresh.Boot.Equal(claim.Generation[0]) || !fresh.ResetAt.Equal(claim.Generation[1]) || !fresh.EntrySince.Equal(claim.Generation[2]) || fresh.TableOID != observed.TableOID || fresh.Schema != observed.Schema || fresh.Relation != observed.Relation || fresh.Only != observed.Only || !slices.Equal(fresh.Columns, observed.Columns) {
			return errLookupScope
		}
		return project.VerifyManagedRepair(ctx, owner)
	}
	if err := verify(ctx); err != nil {
		return true, err
	}
	controls, err := observeManagedControls(ctx, owner, claim.Database, claim.Role, claim.Query, nil)
	if err != nil {
		result.DiagnosticReason = "protected_normal_traffic_unavailable"
		return true, nil
	}
	if err := m.AdmitModel(ctx); err != nil {
		return true, err
	}
	proposal, err := m.Investigate(ctx, evidence)
	if err != nil {
		return true, err
	}
	result.Proposal = &proposal
	if proposal.Outcome == "no_change" && proposal.RunID == runID && proposal.TableOID == evidence.TableOID && proposal.Column == evidence.Column && len(proposal.Columns) == 0 && len(proposal.Reason) > 0 && len(proposal.Reason) <= 1000 {
		result.Outcome, result.DiagnosticReason = "no_change", proposal.Reason
		return true, nil
	}
	if proposal.Repair != string(operation) {
		result.Outcome, result.DiagnosticReason = "no_change", "the proposed operation does not match the selected evidence-supported repair"
		return true, nil
	}
	if err := ValidateDiagnosticProposal(evidence, proposal, time.Now()); err != nil {
		result.Outcome, result.DiagnosticReason = "no_change", err.Error()
		return true, nil
	}
	// Re-read the same sample and exact cause immediately before applying. The
	// executor independently pins and validates the native relation and authority.
	current, err := MeasureManagedIndexLookup(ctx, owner, observed, before.Inputs)
	if err != nil {
		return true, err
	}
	currentEvidence, currentOperation, err := m.maintenanceEvidence(ctx, owner, observed, current, runID)
	if err != nil {
		return true, err
	}
	if currentOperation != operation || current.Sample.PlanReturned != before.Sample.PlanReturned || current.EstimatedRows != before.EstimatedRows || ValidateDiagnosticProposal(currentEvidence, proposal, time.Now()) != nil {
		return true, errors.New("maintenance cause changed before execution")
	}
	request := ProjectMaintenanceRequest{Target: before.Catalog.Target, RunID: runID, Operation: operation, ObservedAt: currentEvidence.At}
	if operation == ProjectAnalyze {
		request.Columns = slices.Clone(observed.Columns)
	}
	guard := func(ctx context.Context, i ProjectMaintenanceIntent) error {
		if i.Target != request.Target || i.RunID != runID || i.Operation != operation || !slices.Equal(i.Columns, request.Columns) {
			return ErrProjectMaintenanceTarget
		}
		return verify(ctx)
	}
	var fence pgx.Tx
	action, actionErr := ApplyProjectMaintenance(ctx, m.Config, request, ProjectMaintenanceGuards{
		CheckPermission: guard,
		CheckCapacity: func(ctx context.Context, _ ProjectMaintenanceIntent) error {
			return checkOwnedRepairCapacity(ctx, owner, nil)
		},
		RecordIntent: func(ctx context.Context, i ProjectMaintenanceIntent) error {
			if err := guard(ctx, i); err != nil {
				return err
			}
			if err := recordOwnedProjectAction(ctx, state, project.ID(), epoch, claim, string(operation), i); err != nil {
				return err
			}
			var err error
			fence, err = lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
			return err
		},
	})
	if fence != nil {
		actionErr = errors.Join(actionErr, releaseOwnedIndexEpoch(state, fence))
	}
	// Uncertain command completion retains its durable pending intent and blocks
	// another action. ANALYZE and plain VACUUM cannot be rolled back afterward.
	if actionErr != nil {
		return true, actionErr
	}
	report := ManagedMaintenanceReport{Operation: operation, Measurement: "managed_sampled_input_and_normal_traffic_windows", Before: before, BeforeTraffic: maintenanceTraffic(controls), Action: action, Outcome: "inconclusive", Reason: "maintenance completed; benefit has not been demonstrated"}
	result.ownedOperation = string(operation)
	after, measureErr := MeasureManagedIndexLookup(ctx, owner, observed, before.Inputs)
	if measureErr == nil {
		report.After = &after
		afterControls, controlErr := observeManagedControls(ctx, owner, claim.Database, claim.Role, claim.Query, controls)
		report.AfterTraffic = maintenanceTraffic(afterControls)
		if errors.Is(controlErr, ErrManagedTrafficRegression) {
			report.Outcome, report.Reason = "regressed", "a protected normal-traffic query regressed; completed maintenance cannot be undone"
		} else if controlErr == nil && action.Confirmed {
			report.Outcome, report.Reason = verifyManagedMaintenance(operation, before, after, controls, afterControls)
		}
	}
	if err := verify(ctx); err != nil {
		return true, err
	}
	result.Outcome, result.DiagnosticReason, result.ManagedMaintenance = report.Outcome, report.Reason, &report
	return true, nil
}

func (m *ManagedProjectIndex) maintenanceEvidence(ctx context.Context, owner *pgx.Conn, observed supabaseproject.DiscoveredIndexEvidence, sample ManagedIndexMeasurement, runID string) (IndexEvidence, ProjectMaintenanceOperation, error) {
	e := IndexEvidence{RunID: runID, At: time.Now().UTC(), Decision: Investigate, TableOID: observed.TableOID, Column: observed.Columns[0], Existing: sample.Catalog.Covering, Plan: sample.Evidence, ExecutionMS: sample.Sample.ReadMS, Label: sample.Catalog.Keys[0].Name}
	if slices.Contains(m.Capabilities, "analyze") && e.Existing && e.Plan.Node == "Seq Scan" {
		var frequency *float64
		err := pgx.BeginTxFunc(ctx, owner, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='750ms'; SET LOCAL search_path=pg_catalog`); err != nil {
				return err
			}
			var err error
			frequency, err = ownedMarginalFrequency(ctx, tx, sample.Catalog.Target, sample.Catalog.Keys[0].Name, sample.Inputs[0])
			return err
		})
		if err != nil {
			return e, "", err
		}
		e.Analyze = &AnalyzeEvidence{EstimatedRows: sample.EstimatedRows, ActualRows: e.Plan.ReturnedRows, SampledValueFraction: frequency}
		if supportsAnalyzeEvidence(e) {
			return e, ProjectAnalyze, nil
		}
		e.Analyze = nil
	}
	if slices.Contains(m.Capabilities, "vacuum") && sample.HeapFetches != nil && e.Existing && e.Plan.Node == "Index Only Scan" {
		clear, err := vacuumConditions(ctx, owner, observed.TableOID, 0)
		if err != nil {
			return e, "", err
		}
		e.Vacuum = &VacuumEvidence{HeapFetches: float64(*sample.HeapFetches), ScanRows: sample.ScanRows, ConditionsClear: clear}
		if vacuumCause(e) {
			return e, ProjectVacuum, nil
		}
		e.Vacuum = nil
	}
	return e, "", nil
}

func verifyManagedMaintenance(operation ProjectMaintenanceOperation, before, after ManagedIndexMeasurement, controls, afterControls *managedTrafficControls) (string, string) {
	if controls == nil || afterControls == nil || controls.ReadCalls <= 0 || controls.WriteCalls <= 0 || afterControls.ReadCalls <= 0 || afterControls.WriteCalls <= 0 {
		return "inconclusive", "matching normal read and write traffic was not available"
	}
	for _, v := range []float64{before.Sample.ReadMS, after.Sample.ReadMS, controls.ReadMS, controls.WriteMS, afterControls.ReadMS, afterControls.WriteMS} {
		if !nonnegativeFinite(v) {
			return "inconclusive", "invalid measured latency"
		}
	}
	if after.Sample.ReadMS > before.Sample.ReadMS*1.25 || afterControls.ReadMS > controls.ReadMS*1.25 || afterControls.WriteMS > controls.WriteMS*1.25 {
		return "regressed", "the sampled lookup or protected normal traffic regressed; maintenance cannot be undone"
	}
	if before.Sample.PlanReturned <= 0 || before.Sample.PlanReturned != after.Sample.PlanReturned {
		return "inconclusive", "the sampled input returned a different number of rows"
	}
	mechanism := false
	switch operation {
	case ProjectAnalyze:
		mechanism = before.EstimatedRows > 0 && after.EstimatedRows > 0 && rowEstimateError(before.EstimatedRows, before.Sample.PlanReturned) >= 10 && rowEstimateError(after.EstimatedRows, after.Sample.PlanReturned) <= rowEstimateError(before.EstimatedRows, before.Sample.PlanReturned)*.5 && after.Sample.Blocks < before.Sample.Blocks
	case ProjectVacuum:
		mechanism = before.HeapFetches != nil && after.HeapFetches != nil && before.ScanRows > 0 && before.ScanRows == after.ScanRows && *before.HeapFetches >= 1000 && float64(*before.HeapFetches)/float64(before.ScanRows) >= .5 && *after.HeapFetches >= 0 && float64(*after.HeapFetches) <= float64(*before.HeapFetches)*.5
	}
	gain := before.Sample.ReadMS - after.Sample.ReadMS
	if mechanism && gain >= 5 && gain >= before.Sample.ReadMS*.2 {
		return "improved_tested_inputs", "the same sampled input improved and observed normal reads and writes stayed within their limits"
	}
	return "inconclusive", "sampled latency and the repair mechanism did not meet every benefit requirement"
}
