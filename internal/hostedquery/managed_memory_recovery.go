package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

var managedMemoryRunID = regexp.MustCompile(`^managed-memory-([1-9][0-9]*)-[1-9][0-9]*$`)

func (m *ManagedProjectIndex) recoverMemory(ctx context.Context, state *pgx.Conn, project supabaseproject.Project, claim investigationClaim, result *DispatchResult) (bool, error) {
	var raw json.RawMessage
	if err := state.QueryRow(ctx, `SELECT intent FROM pooltune_worker.owned_project_actions WHERE project_id=$1 AND job_id=$2 AND operation='memory' AND outcome='pending'`, project.ID(), claim.ID).Scan(&raw); err != nil {
		return true, err
	}
	var journal managedMemoryJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		return true, err
	}
	parts := managedMemoryRunID.FindStringSubmatch(journal.RunID)
	if len(parts) != 2 || parts[1] != fmt.Sprint(claim.ID) || journal.Function.Target.Project != project.ID() || journal.Function.Target.DatabaseOID != claim.Database || journal.PolicySchema != managedMemorySchema(project.ID()) {
		return true, errors.New("invalid managed memory recovery identity")
	}
	owner, err := pgx.ConnectConfig(ctx, m.Config.Copy())
	if err != nil {
		return true, err
	}
	defer closeVacuumConnection(owner)
	if err := project.VerifyManagedRepair(ctx, owner); err != nil {
		return true, err
	}
	var epoch int64
	if err := state.QueryRow(ctx, `SELECT monitor_epoch FROM pooltune_worker.projects WHERE project_id=$1`, project.ID()).Scan(&epoch); err != nil {
		return true, err
	}
	fence, err := lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
	if err != nil {
		return true, err
	}
	defer releaseOwnedIndexEpoch(state, fence)
	report, err := reconcileManagedMemory(ctx, owner, journal, func(ctx context.Context) error { return checkOwnedIndexClaim(ctx, state, project, epoch, claim) })
	if err != nil {
		return true, err
	}
	result.Outcome, result.DiagnosticReason, result.ManagedMemory = "repair_interrupted", report.Reason, &report
	result.ownedOperation, result.managedRecoveryIntent = "memory", raw
	return true, nil
}

// Reconciliation never reapplies extra memory. It restores only a committed
// exact generation; an unacknowledged rewrite expires and is reported honestly.
func reconcileManagedMemory(ctx context.Context, owner *pgx.Conn, j managedMemoryJournal, guard func(context.Context) error) (ManagedMemoryReport, error) {
	report := ManagedMemoryReport{FunctionOID: j.Function.Target.FunctionOID, Measurement: "interrupted_function_catalog_observation", Outcome: "repair_interrupted"}
	var definition string
	if err := owner.QueryRow(ctx, `SELECT pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE p.oid=$1 AND n.nspname=$2 AND p.proname=$3 AND p.proowner=current_user::regrole::oid AND current_user=session_user`, j.Function.Target.FunctionOID, j.Function.Target.Schema, j.Function.Target.Function).Scan(&definition); errors.Is(err, pgx.ErrNoRows) {
		report.RecoveryStatus, report.Reason = "target_changed", "The selected function or owner changed. No definition was overwritten."
		return report, nil
	} else if err != nil {
		return report, err
	}
	if definition == j.Function.OriginalDefinition {
		report.RecoveryStatus, report.Reason, report.Restored = "original_definition_present", "The original function definition is present. No performance benefit is claimed.", true
		return report, nil
	}
	if j.Applied == nil {
		report.RecoveryStatus, report.Reason = "applied_generation_unconfirmed", "The interrupted change has no recorded committed generation. Extra memory expires automatically; no function definition was overwritten."
		return report, nil
	}
	if j.Applied.Intent != j.ManagedMemoryIntent {
		return report, errors.New("recorded memory acknowledgement does not match original intent")
	}
	var unchanged bool
	if err := owner.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_proc p WHERE p.oid=$1 AND p.xmin::text=$2 AND md5(to_jsonb(p)::text)=$3)`, j.Function.Target.FunctionOID, j.Applied.AppliedGeneration, j.Applied.AppliedDigest).Scan(&unchanged); err != nil {
		return report, err
	}
	if !unchanged {
		report.RecoveryStatus, report.Reason = "target_changed", "The function changed after the repair. The owner's newer definition and settings were preserved."
		return report, nil
	}
	if err := RestoreManagedMemory(ctx, owner, *j.Applied, guard); err != nil {
		return report, err
	}
	report.RecoveryStatus, report.Reason, report.Restored = "original_definition_restored", "The interrupted repair's exact function change was restored. No performance benefit is claimed.", true
	return report, nil
}
