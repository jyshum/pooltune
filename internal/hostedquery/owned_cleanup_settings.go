package hostedquery

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// CleanupTriggerReport records the table option and distinguishes a natural
// PostgreSQL autovacuum from a manually issued VACUUM.
type CleanupTriggerReport struct {
	BeforeOption     string                            `json:"before_option"`
	AfterOption      string                            `json:"after_option"`
	Earlier          ProjectCleanupSettingsObservation `json:"earlier"`
	Before           ProjectCleanupSettingsObservation `json:"before"`
	After            ProjectCleanupSettingsObservation `json:"after"`
	SettingChanged   bool                              `json:"setting_changed"`
	AutomaticCleaned bool                              `json:"automatic_cleaned"`
	Restored         bool                              `json:"restored"`
}

// OwnedCleanupSettingsAction is saved before the single bounded ALTER TABLE.
// A receipt is saved separately once PostgreSQL has acknowledged its commit.
type OwnedCleanupSettingsAction struct {
	Intent           ProjectCleanupSettingsIntent
	Request          OwnedVacuumRequest
	Earlier, Before  ProjectCleanupSettingsObservation
	Receipt          ProjectCleanupSettingsObservation
	Report           VacuumReport
	VerificationEnds time.Time
}

type OwnedCleanupSettingsDecision struct {
	Report VacuumReport
	Action *OwnedCleanupSettingsAction
	NextAt time.Time
}

type OwnedCleanupSettingsGuards struct {
	CheckPermission func(context.Context, ProjectCleanupSettingsIntent) error
	CheckCapacity   func(context.Context, int64, ProjectCleanupSettingsIntent) error
	RecordIntent    func(context.Context, OwnedCleanupSettingsAction) error
	RecordRestore   func(context.Context, ProjectCleanupSettingsIntent) error
}

const maximumCleanupNaptime = 120

// RunOwnedProjectCleanupSettings changes the cleanup threshold only when an
// enrolled workload grows a backlog that lies between the old and new
// thresholds. A subsequent invocation observes the scheduler; it never
// repeats an uncertain forward change.
func RunOwnedProjectCleanupSettings(ctx context.Context, config *pgx.ConnConfig, req OwnedVacuumRequest, prior *OwnedCleanupSettingsAction,
	investigate IndexInvestigator, guards OwnedCleanupSettingsGuards) (OwnedCleanupSettingsDecision, error) {
	w := req.Workload
	if config == nil || !w.valid("pt_hosted_vc_") || req.IndexName == "" || req.IndexOID == 0 || guards.CheckPermission == nil {
		return OwnedCleanupSettingsDecision{}, errors.New("cleanup trigger requires the exact owned workload and active permission")
	}
	if prior != nil && (prior.Request != req || prior.Intent.Target != w.Target || prior.Intent.RunID != w.RunID ||
		prior.Intent.Restoring || prior.Report.Evidence.RunID != w.RunID || prior.Report.CleanupTrigger == nil) {
		return OwnedCleanupSettingsDecision{}, errors.New("saved cleanup trigger does not match the exact workload")
	}
	db, err := pgx.ConnectConfig(ctx, config.Copy())
	if err != nil {
		return OwnedCleanupSettingsDecision{}, err
	}
	defer closeVacuumConnection(db)
	if prior != nil {
		return finishOwnedCleanupSettings(ctx, config, db, *prior, guards)
	}
	report := VacuumReport{Fixture: w.Target.Schema, IndexOID: req.IndexOID, CleanupTrigger: &CleanupTriggerReport{}}
	if w.Decision != Investigate {
		return cleanupSettingsResult(report, "no_change", "The workload did not trigger a cleanup investigation."), nil
	}
	if investigate == nil || guards.CheckCapacity == nil || guards.RecordIntent == nil {
		return OwnedCleanupSettingsDecision{}, errors.New("cleanup trigger needs an investigator, capacity and durable intent")
	}
	earlier, err := observeOwnedCleanup(ctx, db, req)
	if err != nil {
		return OwnedCleanupSettingsDecision{}, err
	}
	report.CleanupTrigger.Earlier = earlier
	if !earlier.AutovacuumEnabled || !earlier.OnlyDeadTupleTrigger || earlier.DeadTuples == 0 ||
		earlier.AutovacuumNaptimeSeconds <= 0 || earlier.AutovacuumNaptimeSeconds > maximumCleanupNaptime {
		return cleanupSettingsResult(report, "insufficient_evidence", "The table has no eligible backlog and bounded autovacuum cycle."), nil
	}
	baseline := time.Now()
	report.Before, err = sampleOwnedVacuum(ctx, db, req)
	report.BaselineMS = elapsedMS(baseline)
	if err != nil {
		return OwnedCleanupSettingsDecision{}, err
	}
	if err := waitOwnedCleanup(ctx, time.Until(baseline.Add(1300*time.Millisecond))); err != nil {
		return OwnedCleanupSettingsDecision{}, err
	}
	later, err := observeOwnedCleanup(ctx, db, req)
	if err != nil {
		return OwnedCleanupSettingsDecision{}, err
	}
	report.CleanupTrigger.Before = later
	request := ProjectCleanupSettingsRequest{RunID: w.RunID, Earlier: earlier, Later: later}
	intent, err := planProjectCleanupSettings(request, later)
	if err != nil || !later.OnlyDeadTupleTrigger || !grewOwnedCleanupBacklog(earlier, later) ||
		earlier.AutovacuumNaptimeSeconds != later.AutovacuumNaptimeSeconds {
		return cleanupSettingsResult(report, "insufficient_evidence", "Two fresh observations did not show a growing backlog between the cleanup thresholds."), nil
	}
	if err := guards.CheckPermission(ctx, intent); err != nil {
		return OwnedCleanupSettingsDecision{}, err
	}
	if err := guards.CheckCapacity(ctx, later.RelationBytes, intent); err != nil {
		return OwnedCleanupSettingsDecision{}, err
	}
	report.Evidence = ownedVacuumEvidence(w, report.Before, time.Now().UTC())
	if !vacuumCause(report.Evidence) {
		return cleanupSettingsResult(report, "insufficient_evidence", "The enrolled query did not show excess heap visibility work."), nil
	}
	model := time.Now()
	report.Proposal, err = investigate(ctx, report.Evidence)
	report.GraphMS = elapsedMS(model)
	if err != nil {
		return OwnedCleanupSettingsDecision{}, err
	}
	if ownedRepairDeclined(report.Evidence, report.Proposal, "vacuum") {
		return cleanupSettingsResult(report, "no_change", report.Proposal.Reason), nil
	}
	if len(report.Proposal.Columns) != 0 {
		return OwnedCleanupSettingsDecision{}, errors.New("cleanup proposal changed its enrolled column binding")
	}
	if err := ValidateDiagnosticProposal(report.Evidence, report.Proposal, time.Now()); err != nil {
		return OwnedCleanupSettingsDecision{}, err
	}
	action := OwnedCleanupSettingsAction{Intent: intent, Request: req, Earlier: earlier, Report: report}
	nativeGuards := ProjectCleanupSettingsGuards{CheckPermission: guards.CheckPermission, CheckCapacity: guards.CheckCapacity,
		recordObservedIntent: func(ctx context.Context, planned ProjectCleanupSettingsIntent, before ProjectCleanupSettingsObservation) error {
			clear, err := recheckOwnedVacuum(ctx, db, req)
			if err != nil {
				return err
			}
			if !clear || planned != intent || !before.OnlyDeadTupleTrigger || !grewOwnedCleanupBacklog(earlier, before) ||
				before.AutovacuumNaptimeSeconds != later.AutovacuumNaptimeSeconds {
				return errCleanupSettingsEvidence
			}
			action.Before = before
			action.VerificationEnds = before.At.Add(2*ownedCleanupNaptime(before) + 20*time.Second)
			action.Report.CleanupTrigger.Before = before
			action.Report.CleanupTrigger.BeforeOption = intent.BeforeOption
			action.Report.CleanupTrigger.AfterOption = intent.AfterOption
			return guards.RecordIntent(ctx, action)
		},
	}
	applied, err := ApplyProjectCleanupSettings(ctx, config, request, nativeGuards)
	if !applied.Confirmed {
		if applied.WriteAttempted || !action.Before.At.IsZero() {
			return cleanupSettingsResult(action.Report, "repair_interrupted", "PostgreSQL did not acknowledge the recorded cleanup setting."), nil
		}
		if errors.Is(err, errCleanupSettingsEvidence) || errors.Is(err, errCleanupSettingsThreshold) {
			return cleanupSettingsResult(report, "no_change", "The cleanup evidence changed before the setting could be applied."), nil
		}
		return OwnedCleanupSettingsDecision{}, err
	}
	action.Receipt = applied.After
	if !validOwnedCleanupReceipt(action, action.Receipt) {
		return cleanupSettingsResult(action.Report, "repair_interrupted", "The cleanup setting acknowledgement did not match the recorded action."), nil
	}
	trigger := *action.Report.CleanupTrigger
	trigger.SettingChanged = true
	report.CleanupTrigger = &trigger
	return OwnedCleanupSettingsDecision{
		Report: cleanupSettingsResult(report, "awaiting_verification", "The table setting changed. Waiting for PostgreSQL to clean the measured backlog.").Report,
		Action: &action,
		NextAt: action.Receipt.At.Add(ownedCleanupNaptime(action.Before) + time.Second),
	}, nil
}

func finishOwnedCleanupSettings(ctx context.Context, config *pgx.ConnConfig, db *pgx.Conn, action OwnedCleanupSettingsAction,
	guards OwnedCleanupSettingsGuards) (OwnedCleanupSettingsDecision, error) {
	report := action.Report
	trigger := *report.CleanupTrigger
	report.CleanupTrigger = &trigger
	if err := guards.CheckPermission(ctx, action.Intent); err != nil {
		return OwnedCleanupSettingsDecision{}, err
	}
	after, err := observeOwnedCleanup(ctx, db, action.Request)
	if err != nil {
		return OwnedCleanupSettingsDecision{}, err
	}
	trigger.After = after
	trigger.SettingChanged = after.ScaleOption == action.Intent.AfterOption
	if !validOwnedCleanupReceipt(action, action.Receipt) {
		return cleanupSettingsResult(report, "repair_interrupted", "The prior write has no matching acknowledgement. The current option was inspected without another change."), nil
	}
	if !trigger.SettingChanged || !sameOwnedCleanupGeneration(action.Receipt, after) ||
		!after.AutovacuumEnabled || !after.OnlyDeadTupleTrigger || after.BaseThreshold != action.Before.BaseThreshold ||
		after.MaximumThreshold != action.Before.MaximumThreshold || after.ScaleFactor != action.Receipt.ScaleFactor ||
		after.AutovacuumNaptimeSeconds != action.Before.AutovacuumNaptimeSeconds {
		return cleanupSettingsResult(report, "inconclusive", "The option or PostgreSQL counter generation changed while waiting for cleanup."), nil
	}
	trigger.AutomaticCleaned = ownedAutomaticCleanupProven(action, after)
	if !trigger.AutomaticCleaned {
		if after.At.Before(action.VerificationEnds) &&
			after.Cleanups-after.AutomaticCleanups == action.Before.Cleanups-action.Before.AutomaticCleanups {
			next := after.At.Add(min(15*time.Second, ownedCleanupNaptime(after)+time.Second))
			return OwnedCleanupSettingsDecision{
				Report: cleanupSettingsResult(report, "awaiting_verification", "Waiting for the measured backlog to fall after an automatic cleanup.").Report,
				Action: &action, NextAt: minOwnedCleanupTime(next, action.VerificationEnds),
			}, nil
		}
		return restoreOwnedCleanupSettings(ctx, config, action, report, "inconclusive", "The automatic cleanup did not establish the required backlog improvement.", guards)
	}
	verify := time.Now()
	report.After, err = sampleOwnedVacuum(ctx, db, action.Request)
	report.VerifyMS = elapsedMS(verify)
	if err != nil {
		return OwnedCleanupSettingsDecision{}, err
	}
	final, err := observeOwnedCleanup(ctx, db, action.Request)
	if err != nil {
		return OwnedCleanupSettingsDecision{}, err
	}
	if !ownedAutomaticCleanupProven(action, final) || !sameOwnedCleanupGeneration(after, final) ||
		after.AutomaticCleanups != final.AutomaticCleanups || after.ScaleOption != final.ScaleOption {
		report.CleanupTrigger.After = final
		report.CleanupTrigger.AutomaticCleaned = false
		return cleanupSettingsResult(report, "inconclusive", "The automatic cleanup witness changed during matched verification."), nil
	}
	trigger.After = final
	outcome, reason := verifyVacuum(report.Before, report.After)
	if outcome != "improved_tested_inputs" {
		return restoreOwnedCleanupSettings(ctx, config, action, report, outcome, reason, guards)
	}
	return cleanupSettingsResult(report, outcome, "PostgreSQL automatically cleared the backlog, and the measured query improved without a sampled regression."), nil
}

func restoreOwnedCleanupSettings(ctx context.Context, config *pgx.ConnConfig, action OwnedCleanupSettingsAction,
	report VacuumReport, outcome, reason string, guards OwnedCleanupSettingsGuards) (OwnedCleanupSettingsDecision, error) {
	if guards.RecordRestore == nil || guards.CheckCapacity == nil {
		return cleanupSettingsResult(report, outcome, reason), nil
	}
	restored, err := RestoreProjectCleanupSettings(ctx, config, action.Intent, ProjectCleanupSettingsGuards{
		CheckPermission: guards.CheckPermission, CheckCapacity: guards.CheckCapacity, RecordIntent: guards.RecordRestore,
	})
	if errors.Is(err, ErrProjectCleanupSettingsChanged) {
		return cleanupSettingsResult(report, outcome, reason+" An independently changed option was preserved."), nil
	}
	if err != nil || !restored.Confirmed {
		return cleanupSettingsResult(report, "repair_interrupted", "PostgreSQL did not confirm restoration of the prior cleanup option."), nil
	}
	report.CleanupTrigger.Restored = true
	return cleanupSettingsResult(report, outcome, reason+" The prior cleanup option was restored."), nil
}

func observeOwnedCleanup(ctx context.Context, db *pgx.Conn, req OwnedVacuumRequest) (observation ProjectCleanupSettingsObservation, err error) {
	tx, err := beginOwnedSamples(ctx, db, req.Workload, true)
	if err != nil {
		return observation, err
	}
	defer func() { err = errors.Join(err, rollbackOwnedSamples(tx)) }()
	if err := validateOwnedVacuumIndex(ctx, tx, req); err != nil {
		return observation, err
	}
	return readProjectCleanupSettings(ctx, tx, req.Workload.Target)
}

func cleanupSettingsResult(report VacuumReport, outcome, reason string) OwnedCleanupSettingsDecision {
	report.Outcome, report.Reason = outcome, reason
	return OwnedCleanupSettingsDecision{Report: report}
}

func grewOwnedCleanupBacklog(a, b ProjectCleanupSettingsObservation) bool {
	return b.DeadTuples > a.DeadTuples && b.Updated >= a.Updated && b.Deleted >= a.Deleted &&
		(b.Updated > a.Updated || b.Deleted > a.Deleted)
}

func ownedCleanupNaptime(o ProjectCleanupSettingsObservation) time.Duration {
	return time.Duration(o.AutovacuumNaptimeSeconds) * time.Second
}

func sameOwnedCleanupGeneration(a, b ProjectCleanupSettingsObservation) bool {
	return a.Target == b.Target && !a.Boot.IsZero() && a.Boot.Equal(b.Boot) && a.StatsReset.Equal(b.StatsReset)
}

func validOwnedCleanupReceipt(action OwnedCleanupSettingsAction, current ProjectCleanupSettingsObservation) bool {
	before := action.Before
	expected, err := strconv.ParseFloat(action.Intent.AfterOption, 64)
	return err == nil && current.ScaleFactor == expected && current.ScaleOption == action.Intent.AfterOption &&
		current.At.After(before.At) && current.At.Before(action.VerificationEnds) && sameOwnedCleanupGeneration(current, before) &&
		current.Cleanups == before.Cleanups && current.AutomaticCleanups == before.AutomaticCleanups &&
		current.LastAutovacuum.Equal(before.LastAutovacuum) && current.AutovacuumEnabled && current.OnlyDeadTupleTrigger
}

func ownedAutomaticCleanupProven(action OwnedCleanupSettingsAction, after ProjectCleanupSettingsObservation) bool {
	before := action.Before
	return after.OnlyDeadTupleTrigger && after.At.After(action.Receipt.At) && !after.At.After(action.VerificationEnds) &&
		after.LastAutovacuum.After(action.Receipt.At) && after.LastAutovacuum.After(before.LastAutovacuum) &&
		!after.LastAutovacuum.After(after.At) && sameOwnedCleanupGeneration(before, after) &&
		after.AutomaticCleanups > before.AutomaticCleanups && after.Cleanups >= after.AutomaticCleanups &&
		after.Cleanups-after.AutomaticCleanups == before.Cleanups-before.AutomaticCleanups &&
		after.Updated >= before.Updated && after.Deleted >= before.Deleted &&
		after.DeadTuples >= 0 && after.DeadTuples <= before.DeadTuples/2 &&
		float64(after.DeadTuples) < cleanupThreshold(after, after.ScaleFactor)
}

func minOwnedCleanupTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func waitOwnedCleanup(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
