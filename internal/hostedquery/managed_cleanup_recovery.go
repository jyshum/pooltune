package hostedquery

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
	"slices"
	"time"
)

func (m *ManagedProjectIndex) dispatchCleanupSettings(ctx context.Context, state *pgx.Conn, project supabaseproject.Project, epoch int64, claim investigationClaim, targetSession func(context.Context) (*pgx.Conn, func(), error)) (*DispatchResult, error) {
	a, origin, err := readManagedCleanupAction(ctx, state, claim)
	if err != nil || a == nil {
		return nil, err
	}
	owner, err := supabaseproject.ConnectRepair(ctx, m.Config)
	if err != nil {
		return nil, err
	}
	defer closeVacuumConnection(owner)
	if err := project.VerifyManagedRepair(ctx, owner); err != nil {
		return nil, err
	}
	report := a.report("repair_interrupted", "The recorded cleanup setting was inspected without repeating an uncertain change.")
	result := DispatchResult{JobID: claim.ID, Attempt: claim.Attempt, ThreadID: a.RunID, Evidence: &a.Evidence, Proposal: &a.Proposal}
	publish := func(next *time.Time) (*DispatchResult, error) {
		result.ManagedCleanup, result.Outcome, result.DiagnosticReason = report, report.Outcome, report.Reason
		err := publishManagedCleanup(ctx, state, epoch, claim, result, managedCleanupPublication{Action: *a, Origin: origin, Next: next})
		return &result, err
	}
	current, err := ReadProjectCleanupSettings(ctx, owner, a.Intent.Target)
	if err != nil {
		return nil, err
	}
	report.Trigger.After = current
	report.Trigger.SettingChanged = current.ScaleOption == a.Intent.AfterOption
	if a.Restore != nil {
		report.Trigger.Restored = current.ScaleOption == a.Restore.AfterOption
		report.Reason = "The earlier restoration was inspected. No write was repeated and no new benefit is claimed."
		return publish(nil)
	}
	if a.Receipt == nil {
		return publish(nil)
	}
	if m.Paused || !slices.Contains(m.Capabilities, "vacuum") {
		report.Reason = "Cleanup verification stopped because the owner paused or removed this repair permission. The current setting was preserved."
		return publish(nil)
	}
	if !report.Trigger.SettingChanged || !sameOwnedCleanupGeneration(*a.Receipt, current) || !current.AutovacuumEnabled || !current.OnlyDeadTupleTrigger || current.BaseThreshold != a.Before.BaseThreshold || current.MaximumThreshold != a.Before.MaximumThreshold || current.ScaleFactor != a.Receipt.ScaleFactor || current.AutovacuumNaptimeSeconds != a.Before.AutovacuumNaptimeSeconds {
		report.Outcome = "inconclusive"
		report.Reason = "The table option, cleanup prerequisites or database counters changed independently. The live settings were preserved."
		return publish(nil)
	}
	permission := func(ctx context.Context, i ProjectCleanupSettingsIntent) error {
		if m.Paused || !slices.Contains(m.Capabilities, "vacuum") || i.Target != a.Intent.Target || i.RunID != a.RunID {
			return errClaimLost
		}
		if err := checkOwnedIndexClaim(ctx, state, project, epoch, claim); err != nil {
			return err
		}
		return project.VerifyManagedRepair(ctx, owner)
	}
	restore := func() (*DispatchResult, error) {
		var fence pgx.Tx
		restored, err := RestoreProjectCleanupSettings(ctx, m.Config, a.Intent, ProjectCleanupSettingsGuards{CheckPermission: permission, CheckCapacity: func(ctx context.Context, _ int64, _ ProjectCleanupSettingsIntent) error {
			return checkOwnedRepairCapacity(ctx, owner, nil)
		}, RecordIntent: func(ctx context.Context, i ProjectCleanupSettingsIntent) error {
			if err := recordManagedCleanupRestore(ctx, state, epoch, claim, origin, *a, i); err != nil {
				return err
			}
			a.Restore = &i
			var err error
			fence, err = lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
			return err
		}})
		if fence != nil {
			err = errors.Join(err, releaseOwnedIndexEpoch(state, fence))
		}
		if errors.Is(err, ErrProjectCleanupSettingsChanged) {
			report.Reason += " An independently changed option was preserved."
			return publish(nil)
		}
		if err != nil {
			return nil, err
		}
		if !restored.Confirmed {
			return nil, errors.New("cleanup restoration was not acknowledged")
		}
		report.Trigger.Restored = true
		report.Trigger.After = restored.After
		report.Reason += " The previous cleanup option was restored."
		return publish(nil)
	}
	report.Trigger.AutomaticCleaned = ownedAutomaticCleanupProven(a.owned(), current)
	if !report.Trigger.AutomaticCleaned {
		if current.At.Before(a.Deadline) && current.Cleanups-current.AutomaticCleanups == a.Before.Cleanups-a.Before.AutomaticCleanups {
			next := minOwnedCleanupTime(current.At.Add(min(15*time.Second, ownedCleanupNaptime(current)+time.Second)), a.Deadline)
			report.Outcome = "awaiting_verification"
			report.Reason = "Waiting for PostgreSQL to automatically clear the measured backlog."
			return publish(&next)
		}
		report.Outcome = "inconclusive"
		report.Reason = "A genuine automatic cleanup did not establish the required backlog reduction."
		return restore()
	}
	monitor, closeMonitor, err := targetSession(ctx)
	if err != nil {
		return nil, err
	}
	defer closeMonitor()
	fresh, readErr := project.ReadDiscoveredIndexEvidence(ctx, monitor, claim.Role, claim.Query)
	if readErr != nil || !managedCleanupSameQuery(fresh, a.Observed) {
		report.Outcome = "inconclusive"
		report.Reason = "The original query counter generation changed during cleanup verification."
		return restore()
	}
	after, measureErr := MeasureManagedIndexLookup(ctx, owner, fresh, nil)
	if measureErr != nil || managedCleanupInputDigest(m.Config.Password, a.Intent.Target, after.Inputs) != a.InputDigest {
		report.Outcome = "inconclusive"
		report.Reason = "The original sampled input could not be matched after automatic cleanup."
		return restore()
	}
	report.After = &after
	controls, controlErr := observeManagedControls(ctx, owner, claim.Database, claim.Role, claim.Query, a.controls())
	report.AfterTraffic = maintenanceTraffic(controls)
	final, err := ReadProjectCleanupSettings(ctx, owner, a.Intent.Target)
	if err != nil {
		return nil, err
	}
	if !ownedAutomaticCleanupProven(a.owned(), final) || !sameOwnedCleanupGeneration(current, final) || current.AutomaticCleanups != final.AutomaticCleanups || current.ScaleOption != final.ScaleOption {
		report.Outcome = "inconclusive"
		report.Reason = "The cleanup witness changed during matched verification."
		return restore()
	}
	report.Trigger.After = final
	if errors.Is(controlErr, ErrManagedTrafficRegression) {
		report.Outcome, report.Reason = "regressed", "A protected normal-traffic query became slower."
	} else if controlErr != nil {
		report.Outcome, report.Reason = "inconclusive", "Comparable normal read and write traffic was unavailable."
	} else {
		report.Outcome, report.Reason = verifyManagedMaintenance(ProjectVacuum, a.Sample, after, a.controls(), controls)
	}
	if report.Outcome != "improved_tested_inputs" {
		return restore()
	}
	report.Reason = "PostgreSQL automatically cleared the backlog, the same sampled input improved, and observed normal reads and writes stayed within their limits."
	return publish(nil)
}
