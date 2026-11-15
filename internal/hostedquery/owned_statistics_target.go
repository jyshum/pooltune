package hostedquery

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func runOwnedStatisticsTarget(ctx context.Context, db *pgx.Conn, writer *pgx.ConnConfig, request OwnedStatisticsRepairRequest,
	investigate IndexInvestigator, guards OwnedStatisticsRepairGuards,
) (DispatchResult, error) {
	var report StatisticsReport
	result := DispatchResult{Statistics: &report, ThreadID: request.RunID, Evidence: &report.Evidence, Proposal: &report.Proposal}
	complete := func(outcome, reason string) DispatchResult {
		report.Outcome, report.Reason = outcome, reason
		result.Outcome, result.DiagnosticReason, result.GraphMS = outcome, reason, report.GraphMS
		return result
	}

	start := time.Now()
	prior, before, err := observeOwnedColumnTarget(ctx, db, request)
	report.BaselineMS = elapsedMS(start)
	logOwnedStatisticsPhase(request, "matched_samples", start, err, "sample_number", 1)
	if err != nil {
		return result, err
	}
	report.Before.Samples = []AnalyzeSample{before}
	evidence := IndexEvidence{
		RunID: request.RunID, At: time.Now().UTC(), Decision: Investigate, TableOID: request.Target.TableOID,
		Column: request.Columns[0], Existing: true, Label: "lookup", ExecutionMS: before.ReadMS,
		Plan: PlanEvidence{Node: before.Plan, ExaminedRows: before.PlanExamined, ReturnedRows: before.PlanReturned, SharedBlocks: int64(before.Blocks)},
		Statistics: &StatisticsEvidence{
			Cases:        []AnalyzeEvidence{{EstimatedRows: before.EstimatedRows, ActualRows: before.PlanReturned}},
			ColumnTarget: &StatisticsColumnTargetEvidence{CurrentTarget: prior.effective, NextTarget: request.NextTarget},
		},
	}
	report.Evidence = evidence
	start = time.Now()
	proposal, err := investigate(ctx, evidence)
	report.GraphMS = elapsedMS(start)
	logOwnedStatisticsPhase(request, "investigation", start, err)
	if err != nil {
		return result, err
	}
	if proposal.RunID != evidence.RunID || proposal.TableOID != evidence.TableOID || proposal.Column != evidence.Column ||
		len(proposal.Columns) != 0 || len(proposal.Reason) == 0 || len(proposal.Reason) > 1000 ||
		(proposal.Repair != "" && proposal.Repair != "statistics") {
		return complete("no_change", "the proposed action does not match the authorized statistics fixture"), nil
	}
	report.Proposal = proposal
	if proposal.Outcome == "no_change" {
		return complete("no_change", proposal.Reason), nil
	}
	if proposal.Repair != "statistics" || ValidateDiagnosticProposal(evidence, proposal, time.Now()) != nil {
		return complete("no_change", "the investigation did not authorize an adjustment to the column statistics target"), nil
	}
	report.ColumnTarget = &StatisticsColumnTargetReport{
		PriorTarget: prior.effective, PriorUsesDefault: prior.setting.UsesDefault, TrialTarget: request.NextTarget,
	}
	start = time.Now()
	outcome, reason, err := applyOwnedStatisticsTarget(ctx, writer, request, prior, guards, &report)
	report.ApplyMS = elapsedMS(start)
	logOwnedStatisticsPhase(request, "postgres_action", start, err, "outcome", outcome,
		"analyze_ms", report.After.AnalyzeMS, "verify_ms", report.After.VerifyMS)
	if err != nil {
		return result, err
	}
	return complete(outcome, reason), nil
}

func observeOwnedColumnTarget(ctx context.Context, db *pgx.Conn, request OwnedStatisticsRepairRequest) (
	prior projectStatisticsState, sample AnalyzeSample, err error,
) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return prior, sample, err
	}
	defer func() {
		cleanup, finish := context.WithTimeout(context.Background(), time.Second)
		defer finish()
		err = errors.Join(err, tx.Rollback(cleanup))
	}()
	return sampleOwnedColumnTarget(ctx, tx, request)
}

func sampleOwnedColumnTarget(ctx context.Context, tx pgx.Tx, request OwnedStatisticsRepairRequest) (
	prior projectStatisticsState, sample AnalyzeSample, err error,
) {
	if err := lockOwnedStatisticsRelations(ctx, tx, request, false); err != nil {
		return prior, sample, err
	}
	info, err := ownedStatisticsCatalogRead(ctx, tx, request)
	if err != nil {
		return prior, sample, err
	}
	if info.selectedFraction != nil {
		return prior, sample, errOwnedStatisticsEvidence
	}
	key := ProjectIndexKey{Number: request.Columns[0], Name: info.names[0]}
	prior, err = readProjectStatisticsState(ctx, tx, request.Target, key, false)
	if err != nil {
		return prior, sample, err
	}
	if !boundedProjectStatisticsTarget(prior.effective, request.NextTarget) {
		return prior, sample, errOwnedStatisticsEvidence
	}
	var sampleIsFull bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT FROM pg_stat_all_tables
			WHERE relid=$1 AND last_analyze > statement_timestamp()-interval '60 minutes' AND n_mod_since_analyze=0)
		AND EXISTS (SELECT FROM pg_stats WHERE schemaname=$2 AND tablename=$3 AND attname=$4
			AND NOT inherited AND cardinality(most_common_freqs)=$5)
	`, request.Target.TableOID, request.Target.Schema, request.Target.Relation, key.Name, prior.effective).Scan(&sampleIsFull)
	if err != nil {
		return prior, sample, err
	}
	if !sampleIsFull {
		return prior, sample, errOwnedStatisticsEvidence
	}
	samples, err := sampleOwnedStatistics(ctx, tx, request, info)
	if err != nil {
		return prior, sample, err
	}
	sample = samples[0]
	if sample.PlanReturned <= sample.EstimatedRows || sample.EstimateError < 10 {
		return prior, sample, errOwnedStatisticsEvidence
	}
	return prior, sample, nil
}

func applyOwnedStatisticsTarget(ctx context.Context, writer *pgx.ConnConfig, request OwnedStatisticsRepairRequest,
	prior projectStatisticsState, guards OwnedStatisticsRepairGuards, report *StatisticsReport,
) (outcome, reason string, err error) {
	var recorded bool
	before := report.Before.Samples[0]
	start := time.Now()
	applied, err := applyProjectStatisticsTarget(ctx, writer, ProjectStatisticsRequest{
		Target: request.Target, RunID: request.RunID, ObservedAt: report.Evidence.At, Column: request.Columns[0], NextTarget: request.NextTarget,
	}, ProjectStatisticsGuards{
		CheckPermission: func(ctx context.Context, _ ProjectStatisticsIntent) error { return guards.CheckPermission(ctx) },
		CheckCapacity:   func(ctx context.Context, _ ProjectStatisticsIntent) error { return guards.CheckCapacity(ctx) },
		RecordIntent: func(ctx context.Context, i ProjectStatisticsIntent) error {
			if err := guards.RecordIntent(ctx, i); err != nil {
				return err
			}
			recorded = true
			return nil
		},
	}, projectStatisticsTrial{
		recheck: func(ctx context.Context, tx pgx.Tx, i ProjectStatisticsIntent) error {
			if i.Prior != prior.setting || i.PriorEffective != prior.effective || i.RelationBytes != prior.relationBytes {
				return errOwnedStatisticsEvidence
			}
			// Keep the action session active while rechecking its exact
			// target and roll back probe writes before journaling the action.
			return withOwnedStatisticsSavepoint(ctx, tx, func(sampleTx pgx.Tx) error {
				current, matched, err := sampleOwnedColumnTarget(ctx, sampleTx, request)
				if err != nil {
					return err
				}
				if current != prior || matched.PlanReturned != before.PlanReturned ||
					matched.EstimatedRows != before.EstimatedRows || matched.Plan != before.Plan {
					return errOwnedStatisticsEvidence
				}
				return nil
			})
		},
		verify: func(ctx context.Context, tx pgx.Tx, _ ProjectStatisticsIntent) (bool, error) {
			start := time.Now()
			samples, err := verifyOwnedColumnTarget(ctx, tx, request)
			report.After.VerifyMS = elapsedMS(start)
			if err != nil {
				return false, err
			}
			report.After.Samples = samples
			if samples[0].PlanReturned != before.PlanReturned {
				outcome, reason = "inconclusive", "lookup results changed during verification"
				return false, nil
			}
			outcome, reason = verifyAnalyze(before, samples[0])
			if outcome != "improved_tested_inputs" {
				return false, nil
			}
			if err := guards.CheckPermission(ctx); err != nil {
				return false, err
			}
			return true, nil
		},
	})
	logOwnedStatisticsPhase(request, "column_target_execution", start, err,
		"native_phase", applied.phase, "intent_saved", recorded, "write_attempted", applied.WriteAttempted,
		"confirmed", applied.Confirmed, "restored", applied.Restored)
	report.After.AnalyzeMS = float64(applied.AnalyzeDuration) / float64(time.Millisecond)
	report.After.Applied = applied.Confirmed
	report.After.Retained = applied.Confirmed && !applied.Restored
	report.ColumnTarget.Restored = applied.Restored
	if applied.Restored && err != nil {
		return "inconclusive", "PostgreSQL restored the original statistics target and sample after verification could not finish", nil
	}
	if recorded && !applied.WriteAttempted {
		return "inconclusive", "the column statistics adjustment stopped before changing PostgreSQL", nil
	}
	if recorded && !applied.Confirmed {
		return "repair_interrupted", "the recorded column statistics adjustment requires readback before retry", nil
	}
	return outcome, reason, err
}

func withOwnedStatisticsSavepoint(ctx context.Context, tx pgx.Tx, measure func(pgx.Tx) error) (err error) {
	sampleTx, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, finish := context.WithTimeout(context.Background(), time.Second)
		defer finish()
		err = errors.Join(err, sampleTx.Rollback(cleanup))
	}()
	return measure(sampleTx)
}

func verifyOwnedColumnTarget(ctx context.Context, tx pgx.Tx, request OwnedStatisticsRepairRequest) (samples []AnalyzeSample, err error) {
	// Preserve the parent's trial statistics while discarding probe writes.
	err = withOwnedStatisticsSavepoint(ctx, tx, func(sampleTx pgx.Tx) error {
		if err := lockOwnedStatisticsRelations(ctx, sampleTx, request, false); err != nil {
			return err
		}
		info, err := ownedStatisticsCatalogRead(ctx, sampleTx, request)
		if err != nil {
			return err
		}
		if info.selectedFraction == nil {
			return errOwnedStatisticsEvidence
		}
		samples, err = sampleOwnedStatistics(ctx, sampleTx, request, info)
		return err
	})
	return samples, err
}
