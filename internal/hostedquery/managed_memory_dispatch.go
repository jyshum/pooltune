package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

var managedMemoryCall = regexp.MustCompile(`(?is)^\s*SELECT\s+(` + managedIdent + `)\s*\.\s*(` + managedIdent + `)\s*\(\s*\)\s*;?\s*$`)

type managedMemoryJournal struct {
	ManagedMemoryIntent
	Applied *ManagedMemoryChange `json:"applied,omitempty"`
}

func (m *ManagedProjectIndex) runMemory(ctx context.Context, state, monitor *pgx.Conn, project supabaseproject.Project, epoch int64, claim investigationClaim, windows *ObservedLookupLatency, result *DispatchResult) (bool, error) {
	if !slices.Contains(m.Capabilities, "memory") || len(m.MemoryEnrollments) == 0 || m.MemoryHeadroom == nil || windows == nil {
		return false, nil
	}
	owner, err := pgx.ConnectConfig(ctx, m.Config.Copy())
	if err != nil {
		return true, err
	}
	defer closeVacuumConnection(owner)
	if err := project.VerifyManagedRepair(ctx, owner); err != nil {
		return true, err
	}
	var schema string
	if err := owner.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pg_stat_statements'`).Scan(&schema); err != nil {
		return true, err
	}
	var query string
	if err := owner.QueryRow(ctx, "SELECT query FROM "+qualified(schema, "pg_stat_statements")+" WHERE dbid=$1 AND userid=$2 AND queryid=$3 AND toplevel", claim.Database, claim.Role, claim.Query).Scan(&query); err != nil {
		return false, nil
	}
	parts := managedMemoryCall.FindStringSubmatch(query)
	if len(parts) != 3 {
		return false, nil
	}
	var enrolled *ManagedMemoryEnrollment
	for i := range m.MemoryEnrollments {
		e := &m.MemoryEnrollments[i]
		if e.Target.Project == project.ID() && e.Target.DatabaseOID == claim.Database && e.Target.Schema == managedUnquoteIdentifier(parts[1]) && e.Target.Function == managedUnquoteIdentifier(parts[2]) {
			enrolled = e
			break
		}
	}
	if enrolled == nil {
		return false, nil
	}
	result.Outcome = "insufficient_evidence"
	fn, err := ReadManagedMemoryFunction(ctx, owner, *enrolled)
	if err != nil {
		result.DiagnosticReason = "selected_function_needs_supported_read_only_shape"
		return true, nil
	}
	verify := func(ctx context.Context) error {
		if err := checkOwnedIndexClaim(ctx, state, project, epoch, claim); err != nil {
			return err
		}
		var boot, reset, entry time.Time
		var objective float64
		err := monitor.QueryRow(ctx, "SELECT boot,reset_at,entry_since,mean_execution_ms FROM "+qualified(project.Role(), "snapshot")+"() WHERE database_oid=$1 AND role_oid=$2 AND query_id=$3", claim.Database, claim.Role, claim.Query).Scan(&boot, &reset, &entry, &objective)
		if err != nil {
			return err
		}
		if len(claim.Generation) != 3 || !boot.Equal(claim.Generation[0]) || !reset.Equal(claim.Generation[1]) || !entry.Equal(claim.Generation[2]) || objective != windows.ObjectiveMS {
			return errLookupScope
		}
		return nil
	}
	if err := verify(ctx); err != nil {
		return true, err
	}
	h, err := m.MemoryHeadroom(ctx)
	if err != nil || h.ProjectRef != project.Ref() || h.MemoryAllowanceSlots(time.Now()) < 1 {
		result.DiagnosticReason = "fresh_project_memory_headroom_unavailable"
		return true, nil
	}
	before, err := measureManagedMemory(ctx, owner, fn, claim.Role, claim.Query, nil)
	if err != nil {
		result.DiagnosticReason = "matched_function_spill_evidence_unavailable"
		return true, nil
	}
	measured := memoryEvidence(fn, before, h)
	if !measured.Valid(time.Now()) {
		result.DiagnosticReason = "function_spill_or_memory_allowance_not_qualified"
		return true, nil
	}
	controls, err := observeManagedControls(ctx, owner, claim.Database, claim.Role, claim.Query, nil)
	if err != nil {
		result.DiagnosticReason = "protected_normal_traffic_unavailable"
		return true, nil
	}
	runID := fmt.Sprintf("managed-memory-%d-%d", claim.ID, claim.Attempt)
	evidence := IndexEvidence{RunID: runID, At: time.Now(), Decision: Investigate, TableOID: fn.Table.TableOID, Column: fn.ValueColumnNumber, Label: "lookup", Plan: PlanEvidence{Node: "Sort", ExaminedRows: before.SortRows, ReturnedRows: before.SortRows}, ExecutionMS: before.MeanMS, ManagedMemory: &measured}
	result.Evidence, result.ThreadID, result.ObservedLatency = &evidence, runID, windows
	if err := m.AdmitModel(ctx); err != nil {
		return true, err
	}
	proposal, err := m.Investigate(ctx, evidence)
	if err != nil {
		return true, err
	}
	result.Proposal = &proposal
	if proposal.Outcome == "no_change" && proposal.RunID == runID && proposal.TableOID == evidence.TableOID && proposal.Column == evidence.Column && proposal.JobID == 0 && len(proposal.Columns) == 0 && (proposal.Repair == "" || proposal.Repair == "memory") && len(proposal.Reason) > 0 && len(proposal.Reason) <= 1000 {
		result.Outcome, result.DiagnosticReason = "no_change", proposal.Reason
		return true, nil
	}
	if err := ValidateDiagnosticProposal(evidence, proposal, time.Now()); err != nil {
		return true, err
	}
	if proposal.Repair != "memory" || len(proposal.Columns) != 0 {
		return true, errors.New("memory proposal does not match selected function")
	}
	h, err = m.MemoryHeadroom(ctx)
	if err != nil || h.ProjectRef != project.Ref() || h.MemoryAllowanceSlots(time.Now()) < measured.ConcurrentBoostSlots {
		return true, supabaseproject.ErrMemoryHeadroomUnavailable
	}
	var fence pgx.Tx
	release := func() error {
		if fence == nil {
			return nil
		}
		err := releaseOwnedIndexEpoch(state, fence)
		fence = nil
		return err
	}
	defer release()
	change, err := ApplyManagedMemory(ctx, owner, fn, runID, h, ManagedMemoryGuards{CheckPermission: verify, RecordIntent: func(ctx context.Context, intent ManagedMemoryIntent) error {
		if err := recordOwnedProjectAction(ctx, state, project.ID(), epoch, claim, "memory", managedMemoryJournal{ManagedMemoryIntent: intent}); err != nil {
			return err
		}
		var err error
		fence, err = lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
		return err
	}, RecordApplied: func(ctx context.Context, c ManagedMemoryChange) error {
		if err := release(); err != nil {
			return err
		}
		if err := recordManagedMemoryApplied(ctx, state, project.ID(), epoch, claim, c); err != nil {
			return err
		}
		var err error
		fence, err = lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
		return err
	}})
	if err != nil {
		return true, err
	}
	if err := release(); err != nil {
		return true, err
	}
	report := ManagedMemoryReport{FunctionOID: fn.Target.FunctionOID, Measurement: "matched_read_only_function_experiment", Before: before, BeforeTraffic: maintenanceTraffic(controls), Evidence: measured, Outcome: "inconclusive", Reason: "matched function benefit and protected traffic were not fully established"}
	after, sampleErr := measureManagedMemory(ctx, owner, fn, claim.Role, claim.Query, &change)
	if sampleErr == nil {
		report.After = &after
	}
	afterControls, controlErr := observeManagedControls(ctx, owner, claim.Database, claim.Role, claim.Query, controls)
	report.AfterTraffic = maintenanceTraffic(afterControls)
	if sampleErr == nil && controlErr == nil && managedMemoryBenefit(before, after) {
		report.Outcome, report.Reason = "improved_tested_inputs", "the same function result used less temporary storage and ran faster; ordinary protected traffic stayed within its limits"
	}
	if errors.Is(controlErr, ErrManagedTrafficRegression) || sampleErr == nil && after.MeanMS > before.MeanMS*1.25 {
		report.Outcome, report.Reason = "regressed", "a measured function or protected normal-traffic query regressed"
	}
	identityErr := verifyManagedMemoryInstalled(ctx, owner, change)
	if errors.Is(identityErr, errManagedMemoryOwnerChanged) {
		if err := verify(ctx); err != nil {
			return true, err
		}
		report.Outcome, report.Reason = "inconclusive", "the owner changed the installed function after measurement; the owner change was preserved"
		result.ManagedMemory, result.ownedOperation, result.Outcome, result.DiagnosticReason = &report, "memory", report.Outcome, report.Reason
		return true, nil
	}
	if identityErr != nil {
		return true, identityErr
	}
	if report.Outcome != "improved_tested_inputs" {
		fence, err = lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
		if err != nil {
			return true, err
		}
		restoreErr := RestoreManagedMemory(ctx, owner, change, verify)
		if err := errors.Join(restoreErr, release()); err != nil {
			return true, err
		}
		report.Restored = true
		report.Reason += "; the exact original function definition was restored"
	}
	if err := verify(ctx); err != nil {
		return true, err
	}
	result.ManagedMemory, result.ownedOperation, result.Outcome, result.DiagnosticReason = &report, "memory", report.Outcome, report.Reason
	return true, nil
}

func recordManagedMemoryApplied(ctx context.Context, state *pgx.Conn, project string, epoch int64, claim investigationClaim, change ManagedMemoryChange) error {
	raw, err := json.Marshal(change)
	if err != nil {
		return err
	}
	tag, err := state.Exec(ctx, `WITH active AS MATERIALIZED(SELECT project_id FROM pooltune_worker.projects WHERE project_id=$1 AND monitor_epoch=$2 FOR UPDATE) UPDATE pooltune_worker.owned_project_actions a SET intent=jsonb_set(a.intent,'{applied}',$5::jsonb) FROM active p,pooltune_worker.investigations j WHERE a.project_id=p.project_id AND a.job_id=$3 AND a.operation='memory' AND a.outcome='pending' AND a.intent->>'run_id'=$6 AND NOT a.intent ? 'applied' AND j.id=a.job_id AND j.project_id=a.project_id AND j.attempts=$4 AND j.finished_at IS NULL AND j.lease_until>clock_timestamp()`, project, epoch, claim.ID, claim.Attempt, string(raw), change.Intent.RunID)
	if err == nil && tag.RowsAffected() != 1 {
		return errClaimLost
	}
	return err
}
