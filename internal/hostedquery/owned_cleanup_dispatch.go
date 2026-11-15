package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"pooltune/internal/supabaseproject"
)

func (r *OwnedProjectRepair) ensureCleanupClaim(ctx context.Context, state *pgx.Conn, project supabaseproject.Project, epoch int64) error {
	if !r.Matches(project) || r.Cleanup == nil || r.AdmitModel == nil {
		return nil
	}
	return enqueueOwnedCleanupRecovery(ctx, state, project.ID(), epoch, *r.Cleanup, r.Role, r.Query)
}

// dispatchCleanup also accepts durable successors, whose original detection
// window has expired by the time PostgreSQL can finish its cleanup cycle.
func (r *OwnedProjectRepair) dispatchCleanup(ctx context.Context, state *pgx.Conn, project supabaseproject.Project, epoch int64,
	claim investigationClaim, windows *ObservedLookupLatency, targetSession func(context.Context) (*pgx.Conn, func(), error)) (*DispatchResult, error) {
	if !r.matchesClaim(project, claim) || r.Cleanup == nil || r.AdmitModel == nil || claim.RecoveryOnly {
		return nil, nil
	}
	started := time.Now()
	checkpoint, err := readOwnedCleanupCheckpoint(ctx, state, claim)
	if err != nil {
		return nil, err
	}
	if checkpoint == nil && (windows == nil || len(windows.SharedBlockAccesses) != 5) {
		return nil, nil
	}
	req := *r.Cleanup
	req.Workload.Project = project.ID()
	req.Workload.RoleOID, req.Workload.QueryID, req.Workload.Decision = claim.Role, claim.Query, Investigate
	req.Workload.RunID = fmt.Sprintf("owned-vacuum-%d-%d", claim.ID, claim.Attempt)
	var prior *OwnedCleanupSettingsAction
	if checkpoint != nil {
		prior = &checkpoint.Action
		req.Workload.RunID = prior.Request.Workload.RunID
		if req != prior.Request {
			return nil, errors.New("recorded cleanup settings do not match the configured target")
		}
	}
	publish := func(decision OwnedCleanupSettingsDecision) (*DispatchResult, error) {
		report := decision.Report
		if checkpoint != nil {
			report.JobID = checkpoint.Origin
		} else {
			report.JobID = claim.ID
		}
		result := DispatchResult{JobID: claim.ID, Attempt: claim.Attempt, ObservedLatency: windows,
			Outcome: report.Outcome, DiagnosticReason: report.Reason, Vacuum: &report, ElapsedMS: elapsedMS(started), GraphMS: report.GraphMS}
		if report.Evidence.RunID != "" {
			result.Evidence = &report.Evidence
			result.Proposal = &report.Proposal
		}
		if _, err := publishOwnedCleanupCheckpoint(ctx, state, epoch, claim, result, decision); err != nil {
			return nil, err
		}
		return &result, nil
	}
	if checkpoint == nil {
		var blocked bool
		if err := state.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pooltune_worker.owned_project_actions
			WHERE project_id=$1 AND outcome='pending')`, project.ID()).Scan(&blocked); err != nil {
			return nil, err
		}
		if blocked {
			return publish(cleanupSettingsResult(VacuumReport{Fixture: req.Workload.Target.Schema, CleanupTrigger: &CleanupTriggerReport{}},
				"repair_interrupted", "A recorded repair must be checked before another action can start."))
		}
	}
	var enrolled struct {
		Objective float64 `json:"objective_ms"`
	}
	if err := json.Unmarshal(claim.queueEvidence, &enrolled); err != nil || enrolled.Objective <= 0 ||
		!nonnegativeFinite(enrolled.Objective) || (windows != nil && windows.ObjectiveMS != enrolled.Objective) {
		return nil, errLookupScope
	}
	monitor, closeMonitor, err := targetSession(ctx)
	if err != nil {
		return nil, err
	}
	defer closeMonitor()
	verify := func(ctx context.Context, intent ProjectCleanupSettingsIntent) error {
		if intent.Target != req.Workload.Target || intent.RunID != req.Workload.RunID {
			return errClaimLost
		}
		return verifyOwnedRepairWindow(ctx, state, monitor, project, epoch, claim, enrolled.Objective)
	}
	writer, err := pgx.ConnectConfig(ctx, r.Config.Copy())
	if err != nil {
		return nil, err
	}
	defer closeVacuumConnection(writer)
	if prior != nil {
		if err := verify(ctx, prior.Intent); errors.Is(err, pgx.ErrNoRows) || err == errLookupScope {
			return publish(cleanupSettingsResult(prior.Report, "repair_interrupted", "The enrolled query or its counter generation changed during verification."))
		} else if err != nil {
			return nil, err
		}
		if checkpoint.Restore != nil {
			return publish(inspectOwnedCleanupRestore(ctx, writer, *prior, *checkpoint.Restore))
		}
	}
	investigate := func(ctx context.Context, e IndexEvidence) (IndexProposal, error) {
		if err := r.AdmitModel(ctx); err != nil {
			return IndexProposal{}, err
		}
		return r.Investigate(ctx, e)
	}
	var fence pgx.Tx
	acquireFence := func(ctx context.Context) error {
		var err error
		fence, err = lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
		return err
	}
	guards := OwnedCleanupSettingsGuards{CheckPermission: verify,
		CheckCapacity: func(ctx context.Context, _ int64, _ ProjectCleanupSettingsIntent) error {
			return checkOwnedRepairCapacity(ctx, writer, r.Capacity)
		},
		RecordIntent: func(ctx context.Context, action OwnedCleanupSettingsAction) error {
			if prior != nil || fence != nil {
				return errClaimLost
			}
			if err := verify(ctx, action.Intent); err != nil {
				return err
			}
			if err := recordOwnedProjectAction(ctx, state, project.ID(), epoch, claim, "vacuum", action); err != nil {
				return err
			}
			return acquireFence(ctx)
		},
		RecordRestore: func(ctx context.Context, restore ProjectCleanupSettingsIntent) error {
			if fence != nil {
				return errClaimLost
			}
			if err := verify(ctx, restore); err != nil {
				return err
			}
			if err := recordOwnedCleanupRestore(ctx, state, epoch, claim, checkpoint, restore); err != nil {
				return err
			}
			return acquireFence(ctx)
		},
	}
	decision, err := RunOwnedProjectCleanupSettings(ctx, r.Config, req, prior, investigate, guards)
	if fence != nil {
		err = errors.Join(err, releaseOwnedIndexEpoch(state, fence))
	}
	if errors.Is(err, ErrProjectModelBudget) && prior == nil {
		decision = cleanupSettingsResult(VacuumReport{Fixture: req.Workload.Target.Schema, CleanupTrigger: &CleanupTriggerReport{}},
			"diagnosis_unavailable", "daily_model_limit")
	} else if err != nil {
		return nil, err
	}
	if decision.Report.Outcome == "repair_interrupted" {
		saved, err := readOwnedCleanupCheckpoint(ctx, state, claim)
		if err != nil {
			return nil, err
		}
		if saved != nil && (prior == nil || (checkpoint.Restore == nil && saved.Restore != nil)) {
			decision = cleanupSettingsResult(decision.Report, "awaiting_verification", "The write was recorded. A follow-up will inspect its outcome.")
			decision.NextAt = time.Now().UTC().Add(time.Second)
		}
	}
	return publish(decision)
}

// A lost restoration acknowledgement authorizes inspection only. Even if the
// old option is visible, no second restoration or improvement is published.
func inspectOwnedCleanupRestore(ctx context.Context, db *pgx.Conn, action OwnedCleanupSettingsAction,
	restore ProjectCleanupSettingsIntent) OwnedCleanupSettingsDecision {
	report := action.Report
	trigger := *report.CleanupTrigger
	report.CleanupTrigger = &trigger
	current, err := observeOwnedCleanup(ctx, db, action.Request)
	if err != nil {
		return cleanupSettingsResult(report, "repair_interrupted", "The recorded restore could not be inspected. No change was repeated.")
	}
	trigger.After = current
	trigger.Restored = current.ScaleOption == restore.AfterOption
	reason := "The recorded restore could not be confirmed. The current option was inspected and preserved."
	if trigger.Restored {
		reason = "The previous option is present after an interrupted restore."
	}
	return cleanupSettingsResult(report, "repair_interrupted", reason)
}
