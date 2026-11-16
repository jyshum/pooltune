package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

func (m *ManagedProjectIndex) recoverCommittedStatistics(ctx context.Context, state *pgx.Conn, project supabaseproject.Project, claim investigationClaim, raw json.RawMessage, result *DispatchResult) (bool, error) {
	var intent ManagedStatisticsIntent
	if err := json.Unmarshal(raw, &intent); err != nil {
		return true, err
	}
	parts := managedStatisticsRunID.FindStringSubmatch(intent.RunID)
	if !validManagedStatisticsIntent(intent, false) || len(parts) != 2 || parts[1] != fmt.Sprint(claim.ID) || intent.Target.DatabaseOID != claim.Database {
		return true, errors.New("committed statistics recovery identity does not match its job")
	}
	owner, err := pgx.ConnectConfig(ctx, m.Config.Copy())
	if err != nil {
		return true, err
	}
	defer closeVacuumConnection(owner)
	if err := project.VerifyManagedRepair(ctx, owner); err != nil {
		return true, err
	}
	if err := lockManagedStatistics(ctx, owner, intent.Target.TableOID); err != nil {
		return true, err
	}
	var epoch int64
	if err := state.QueryRow(ctx, `SELECT monitor_epoch FROM pooltune_worker.projects WHERE project_id=$1 AND monitor_epoch IS NOT NULL`, project.ID()).Scan(&epoch); err != nil {
		return true, err
	}
	journal, err := pgx.ConnectConfig(ctx, state.Config().Copy())
	if err != nil {
		return true, err
	}
	defer closeVacuumConnection(journal)
	fence, err := lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
	if err != nil {
		return true, err
	}
	permission := func(ctx context.Context) error {
		if err := checkOwnedIndexClaim(ctx, state, project, epoch, claim); err != nil {
			return err
		}
		return project.VerifyManagedRepair(ctx, owner)
	}
	report := ManagedMaintenanceReport{Operation: "statistics", Measurement: "interrupted_committed_statistics_recovery", Outcome: "repair_interrupted"}
	if intent.Phase == "intent" && intent.TrialVersion == "" {
		report.RecoveryStatus, report.Reason = "statistics_trial_not_committed", "The durable record never reached commit authorization. The uncertain trial was not repeated."
	} else {
		recovered, recoverErr := restoreManagedStatisticsCommitted(ctx, owner, intent, permission, func(ctx context.Context, prior, next ManagedStatisticsIntent) error {
			return saveManagedStatisticsIntent(ctx, journal, project.ID(), epoch, claim, prior, next)
		})
		err = recoverErr
		intent = recovered.Intent
		if err == nil {
			switch {
			case recovered.OwnerChanged:
				report.RecoveryStatus, report.Reason = "statistics_owner_change_preserved", "The owner changed the recorded statistics configuration. Pooltune preserved that change and did not replay the trial. Performance remains unverified."
			case recovered.Restored && recovered.Regenerated && intent.Variant == "joint":
				report.RecoveryStatus, report.Reason = "statistics_object_removed", "The exact trial statistics object was removed and statistics regenerated after interruption. No performance benefit is claimed."
			case recovered.Restored && recovered.Regenerated:
				report.RecoveryStatus, report.Reason = "statistics_configuration_restored", "The prior column setting was restored and statistics regenerated after interruption. The exact previous sample is not restored and no performance benefit is claimed."
			case recovered.Restored:
				report.RecoveryStatus, report.Reason = "statistics_trial_not_committed", "The original configuration is present and no committed trial needs restoration. The command was not repeated."
			default:
				err = errors.New("statistics recovery did not establish a terminal state")
			}
		}
	}
	err = errors.Join(err, releaseOwnedIndexEpoch(state, fence))
	if err != nil {
		return true, err
	}
	current, err := json.Marshal(intent)
	if err != nil {
		return true, err
	}
	result.ownedOperation = "statistics"
	result.managedRecoveryIntent = current
	result.Outcome, result.DiagnosticReason, result.ManagedMaintenance = report.Outcome, report.Reason, &report
	return true, nil
}
