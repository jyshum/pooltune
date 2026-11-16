package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
	"slices"
	"strconv"
	"time"
)

func managedCleanupSameQuery(a, b supabaseproject.DiscoveredIndexEvidence) bool {
	return a.DatabaseOID == b.DatabaseOID && a.RoleOID == b.RoleOID && a.QueryID == b.QueryID && a.TableOID == b.TableOID && a.Schema == b.Schema && a.Relation == b.Relation && a.Only == b.Only && slices.Equal(a.Columns, b.Columns) && a.ObjectiveMS == b.ObjectiveMS && a.Boot.Equal(b.Boot) && a.ResetAt.Equal(b.ResetAt) && a.EntrySince.Equal(b.EntrySince)
}
func (m *ManagedProjectIndex) runCleanupSettings(ctx context.Context, state, monitor *pgx.Conn, project supabaseproject.Project, epoch int64, claim investigationClaim, windows *ObservedLookupLatency, result *DispatchResult, observed supabaseproject.DiscoveredIndexEvidence, owner *pgx.Conn, sample ManagedIndexMeasurement) (bool, error) {
	if !slices.Contains(m.Capabilities, "vacuum") || len(observed.Columns) != 1 || sample.HeapFetches == nil || !sample.Catalog.Covering || sample.Evidence.Node != "Index Only Scan" {
		return false, nil
	}
	earlier, err := ReadProjectCleanupSettings(ctx, owner, sample.Catalog.Target)
	if err != nil {
		return true, err
	}
	if !earlier.AutovacuumEnabled || !earlier.OnlyDeadTupleTrigger || earlier.AutovacuumNaptimeSeconds <= 0 || earlier.AutovacuumNaptimeSeconds > maximumCleanupNaptime {
		return false, nil
	}
	controls, err := observeManagedControls(ctx, owner, claim.Database, claim.Role, claim.Query, nil)
	if err != nil {
		result.DiagnosticReason = "protected_normal_traffic_unavailable"
		return true, nil
	}
	if err := waitOwnedCleanup(ctx, time.Until(earlier.At.Add(1300*time.Millisecond))); err != nil {
		return true, err
	}
	later, err := ReadProjectCleanupSettings(ctx, owner, sample.Catalog.Target)
	if err != nil {
		return true, err
	}
	runID := fmt.Sprintf("managed-maintenance-%d-%d", claim.ID, claim.Attempt)
	request := ProjectCleanupSettingsRequest{RunID: runID, Earlier: earlier, Later: later}
	intent, err := planProjectCleanupSettings(request, later)
	if err != nil || !later.OnlyDeadTupleTrigger || !grewOwnedCleanupBacklog(earlier, later) || earlier.AutovacuumNaptimeSeconds != later.AutovacuumNaptimeSeconds {
		return false, nil
	}
	evidence, operation, err := m.maintenanceEvidence(ctx, owner, observed, sample, runID)
	if err != nil {
		return true, err
	}
	if operation != ProjectVacuum {
		return false, nil
	}
	nextScale, _ := strconv.ParseFloat(intent.AfterOption, 64)
	evidence.Vacuum.CleanupTrigger = &ManagedCleanupEvidence{Earlier: earlier, Later: later, NextScaleFactor: nextScale}
	result.Evidence, result.ThreadID, result.ObservedLatency = &evidence, runID, windows
	permission := func(ctx context.Context, i ProjectCleanupSettingsIntent) error {
		if i != intent || m.Paused || !slices.Contains(m.Capabilities, "vacuum") {
			return errClaimLost
		}
		if err := checkOwnedIndexClaim(ctx, state, project, epoch, claim); err != nil {
			return err
		}
		fresh, err := project.ReadDiscoveredIndexEvidence(ctx, monitor, claim.Role, claim.Query)
		if err != nil {
			return err
		}
		if !managedCleanupSameQuery(fresh, observed) {
			return errLookupScope
		}
		return project.VerifyManagedRepair(ctx, owner)
	}
	if err := permission(ctx, intent); err != nil {
		return true, err
	}
	if err := m.AdmitModel(ctx); err != nil {
		return true, err
	}
	proposal, err := m.Investigate(ctx, evidence)
	if err != nil {
		return true, err
	}
	result.Proposal = &proposal
	if ownedRepairDeclined(evidence, proposal, "vacuum") {
		result.Outcome, result.DiagnosticReason = "no_change", proposal.Reason
		return true, nil
	}
	if proposal.Repair != "vacuum" || ValidateDiagnosticProposal(evidence, proposal, time.Now()) != nil {
		result.Outcome, result.DiagnosticReason = "no_change", "the proposed repair did not match the supported cleanup evidence"
		return true, nil
	}
	action := managedCleanupAction{Version: 2, RunID: runID, Intent: intent, Observed: observed, Earlier: earlier, Sample: sample, InputDigest: managedCleanupInputDigest(m.Config.Password, intent.Target, sample.Inputs), ControlMeans: controls.means, ControlCounters: controls.counters, Controls: *maintenanceTraffic(controls), Evidence: evidence, Proposal: proposal}
	var fence pgx.Tx
	applied, applyErr := ApplyProjectCleanupSettings(ctx, m.Config, request, ProjectCleanupSettingsGuards{CheckPermission: permission, CheckCapacity: func(ctx context.Context, _ int64, _ ProjectCleanupSettingsIntent) error {
		return checkOwnedRepairCapacity(ctx, owner, nil)
	}, recordLockedIntent: func(ctx context.Context, planned ProjectCleanupSettingsIntent, before ProjectCleanupSettingsObservation, locked pgx.Tx) error {
		if planned != intent || !before.OnlyDeadTupleTrigger || !grewOwnedCleanupBacklog(earlier, before) || before.AutovacuumNaptimeSeconds != later.AutovacuumNaptimeSeconds {
			return fmt.Errorf("%w: native trigger guard", errCleanupSettingsEvidence)
		}
		fresh, err := MeasureManagedIndexLookupTx(ctx, locked, observed, sample.Catalog, sample.Inputs)
		if err != nil {
			return err
		}
		clear, err := vacuumConditions(ctx, locked, observed.TableOID, 0)
		if err != nil {
			return err
		}
		current := evidence
		current.At = time.Now().UTC()
		current.Plan = fresh.Evidence
		if fresh.HeapFetches == nil {
			return errCleanupSettingsEvidence
		}
		current.Vacuum = &VacuumEvidence{HeapFetches: float64(*fresh.HeapFetches), ScanRows: fresh.ScanRows, ConditionsClear: clear}
		if current.Vacuum != nil {
			current.Vacuum.CleanupTrigger = &ManagedCleanupEvidence{Earlier: earlier, Later: before, NextScaleFactor: nextScale}
		}
		if validation := ValidateDiagnosticProposal(current, proposal, time.Now()); validation != nil || fresh.Sample.PlanReturned != sample.Sample.PlanReturned {
			return fmt.Errorf("%w: sampled visibility guard (conditions=%t,cause=%t,trigger=%t,plan=%s,heap=%.0f,validation=%v)", errCleanupSettingsEvidence, clear, vacuumCause(current), current.Vacuum.CleanupTrigger.Valid(current.TableOID, current.RunID, time.Now()), current.Plan.Node, current.Vacuum.HeapFetches, validation)
		}
		action.Evidence = current
		action.Before = before
		action.Deadline = before.At.Add(2*ownedCleanupNaptime(before) + 20*time.Second)
		if err := validManagedCleanupAction(action, claim); err != nil {
			return err
		}
		if err := recordOwnedProjectAction(ctx, state, project.ID(), epoch, claim, "vacuum", action); err != nil {
			return err
		}
		fence, err = lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
		return err
	}})
	if fence != nil {
		applyErr = errors.Join(applyErr, releaseOwnedIndexEpoch(state, fence))
	}
	if applyErr != nil {
		return true, applyErr
	}
	if !applied.Confirmed {
		return true, errors.New("cleanup setting lacks native acknowledgement")
	}
	action.Receipt = &applied.After
	if !validOwnedCleanupReceipt(action.owned(), applied.After) {
		return true, errors.New("cleanup changed before its acknowledged receipt")
	}
	next := minOwnedCleanupTime(applied.After.At.Add(ownedCleanupNaptime(applied.After)+time.Second), action.Deadline)
	report := action.report("awaiting_verification", "The table's cleanup threshold changed. Waiting for PostgreSQL's automatic cleanup and matched traffic checks.")
	result.ManagedCleanup, result.Outcome, result.DiagnosticReason = report, report.Outcome, report.Reason
	result.managedCleanupPublication = &managedCleanupPublication{Action: action, Origin: claim.ID, Next: &next, Ack: true}
	return true, nil
}
