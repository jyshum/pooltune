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

func (m *ManagedProjectIndex) runCommittedStatistics(ctx context.Context, state, monitor *pgx.Conn, project supabaseproject.Project, epoch int64, claim investigationClaim, windows *ObservedLookupLatency, result *DispatchResult, observed supabaseproject.DiscoveredIndexEvidence, owner *pgx.Conn, first ManagedIndexMeasurement) (bool, error) {
	if !slices.Contains(m.Capabilities, "statistics") || !first.Catalog.Covering {
		return false, nil
	}
	runID := fmt.Sprintf("managed-statistics-%d-%d", claim.ID, claim.Attempt)
	var before managedStatisticsObservation
	err := pgx.BeginFunc(ctx, owner, func(tx pgx.Tx) error {
		var err error
		before, err = readManagedStatistics(ctx, tx, observed, first, nil, runID)
		return err
	})
	if errors.Is(err, errOwnedStatisticsEvidence) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	permission := func(ctx context.Context) error {
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
	if err := permission(ctx); err != nil {
		return true, err
	}
	result.Evidence, result.ThreadID, result.ObservedLatency = &before.evidence, runID, windows
	controls, err := observeManagedControls(ctx, owner, claim.Database, claim.Role, claim.Query, nil)
	if err != nil {
		result.DiagnosticReason = "protected_normal_traffic_unavailable"
		return true, nil
	}
	if err := m.AdmitModel(ctx); err != nil {
		return true, err
	}
	proposal, err := m.Investigate(ctx, before.evidence)
	if err != nil {
		return true, err
	}
	result.Proposal = &proposal
	if proposal.Outcome == "no_change" && proposal.RunID == runID && proposal.TableOID == observed.TableOID && proposal.Column == observed.Columns[0] && len(proposal.Columns) == 0 && len(proposal.Reason) > 0 && len(proposal.Reason) <= 1000 {
		result.Outcome, result.DiagnosticReason = "no_change", proposal.Reason
		return true, nil
	}
	if proposal.Repair != "statistics" || len(proposal.Columns) != 0 || ValidateDiagnosticProposal(before.evidence, proposal, time.Now()) != nil {
		result.Outcome, result.DiagnosticReason = "no_change", "the proposal does not match supported statistics evidence"
		return true, nil
	}
	deadline := time.Now().UTC().Add(12 * time.Second)
	if outer, ok := ctx.Deadline(); ok && outer.Add(-3*time.Second).Before(deadline) {
		deadline = outer.Add(-3 * time.Second)
	}
	if time.Until(deadline) < time.Second {
		result.Outcome, result.DiagnosticReason = "inconclusive", "insufficient time remains for a bounded committed trial"
		return true, nil
	}
	initial := ManagedStatisticsIntent{Version: 1, RunID: runID, Target: first.Catalog.Target, Keys: slices.Clone(first.Catalog.Keys), Variant: "joint", Deadline: deadline}
	if before.next > 0 {
		initial.Variant = "column_target"
		initial.NextTarget = before.next
	} else {
		initial.ObjectName = managedJointStatisticsName(observed.TableOID, observed.Columns)
	}
	report := ManagedStatisticsTrialReport{Measurement: "managed_committed_statistics_trial", VerificationScope: "committed_candidate_inputs_and_normal_traffic", Variant: initial.Variant, Before: before.samples, BeforeTraffic: maintenanceTraffic(controls), Deadline: deadline}
	journal, err := pgx.ConnectConfig(ctx, state.Config().Copy())
	if err != nil {
		return true, err
	}
	defer closeVacuumConnection(journal)
	var fence pgx.Tx
	guards := managedStatisticsCommitGuards{
		Permission: permission, Capacity: func(ctx context.Context) error { return checkOwnedRepairCapacity(ctx, owner, nil) },
		Record: func(ctx context.Context, i ManagedStatisticsIntent) error {
			if err := permission(ctx); err != nil {
				return err
			}
			if err := recordOwnedProjectAction(ctx, state, project.ID(), epoch, claim, "statistics", i); err != nil {
				return err
			}
			var err error
			fence, err = lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
			return err
		},
		Save: func(ctx context.Context, prior, next ManagedStatisticsIntent) error {
			return saveManagedStatisticsIntent(ctx, journal, project.ID(), epoch, claim, prior, next)
		},
		Recheck: func(ctx context.Context, tx pgx.Tx) error {
			current, err := readManagedStatistics(ctx, tx, observed, first, managedStatisticsInputs(before.samples), runID)
			if err != nil {
				return err
			}
			if current.next != before.next || current.prior != before.prior || len(current.samples) != len(before.samples) {
				return errOwnedStatisticsEvidence
			}
			for i, s := range current.samples {
				b := before.samples[i]
				if s.EstimatedRows != b.EstimatedRows || s.Sample.PlanReturned != b.Sample.PlanReturned || s.Evidence.Node != b.Evidence.Node {
					return errOwnedStatisticsEvidence
				}
			}
			return permission(ctx)
		},
	}
	action, err := applyManagedStatisticsCommitted(ctx, m.Config, initial, guards, func(ctx context.Context) (string, string, error) {
		for _, b := range before.samples {
			a, err := MeasureManagedIndexLookup(ctx, owner, observed, b.Inputs)
			if err != nil {
				return "inconclusive", "the same input could not be measured after commit", err
			}
			report.After = append(report.After, a)
		}
		afterControls, err := observeManagedControls(ctx, owner, claim.Database, claim.Role, claim.Query, controls)
		report.AfterTraffic = maintenanceTraffic(afterControls)
		if errors.Is(err, ErrManagedTrafficRegression) {
			return "regressed", "a protected normal query regressed after commit", nil
		}
		if err != nil {
			return "inconclusive", "comparable normal application traffic was unavailable after commit", err
		}
		outcome, reason := verifyManagedStatistics(before.samples, report.After, controls, afterControls)
		report.SampledOutcome = outcome
		if err := permission(ctx); err != nil {
			return "inconclusive", "project authority changed during verification", err
		}
		return outcome, reason, nil
	})
	if fence != nil {
		err = errors.Join(err, releaseOwnedIndexEpoch(state, fence))
	}
	if err != nil {
		return true, err
	}
	report.Applied = action.Committed
	report.Retained = action.Committed && action.Outcome == "improved_tested_inputs" && !action.Restored && !action.OwnerChanged
	report.Restored, report.StatisticsRegenerated, report.OwnerChanged = action.Restored, action.Regenerated, action.OwnerChanged
	report.Outcome, report.Reason = action.Outcome, action.Reason
	if initial.Variant == "column_target" {
		report.ColumnTarget = &ProjectStatisticsResult{WriteAttempted: action.Applied, Confirmed: action.Committed, Restored: action.Restored, Prior: action.Intent.Prior, Current: ProjectStatisticsSetting{Value: action.Intent.NextTarget}, AnalyzeDuration: action.AnalyzeDuration}
		if action.Restored {
			report.ColumnTarget.Current = action.Intent.Prior
		}
	}
	result.ownedOperation = "statistics"
	result.Outcome, result.DiagnosticReason, result.ManagedStatistics = report.Outcome, report.Reason, &ManagedStatisticsReport{ManagedStatisticsTrialReport: report}
	return true, nil
}
