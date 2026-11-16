package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/labdb"
	"pooltune/internal/supabaseproject"
)

// ManagedProjectIndex uses the owner-authorized credential of one connected
// project. Discovery determines the query and target for each investigation.
type ManagedProjectIndex struct {
	memoryRefreshedAt time.Time
	Cron              *ManagedProjectCron
	MemoryEnrollments []ManagedMemoryEnrollment
	MemoryHeadroom    func(context.Context) (supabaseproject.MemoryHeadroom, error)
	Capabilities      []string
	Config            *pgx.ConnConfig
	Investigate       IndexInvestigator
	AdmitModel        func(context.Context) error
	Paused            bool
}

func (m *ManagedProjectIndex) matches(project supabaseproject.Project) bool {
	return m != nil && m.Config != nil && m.Investigate != nil && m.AdmitModel != nil &&
		m.Config.Host == "db."+project.Ref()+".supabase.co" && m.Config.Port == 5432 && m.Config.Database == "postgres" &&
		m.Config.User != "" && m.Config.TLSConfig != nil && !m.Config.TLSConfig.InsecureSkipVerify &&
		m.Config.TLSConfig.ServerName == m.Config.Host && m.Config.TLSConfig.RootCAs != nil && len(m.Config.Fallbacks) == 0
}

func (m *ManagedProjectIndex) run(ctx context.Context, state, monitor *pgx.Conn, project supabaseproject.Project, epoch int64, claim investigationClaim, windows *ObservedLookupLatency, result *DispatchResult) (bool, error) {
	if !m.matches(project) || m.Paused {
		return false, nil
	}
	if handled, err := m.runMemory(ctx, state, monitor, project, epoch, claim, windows, result); handled || err != nil {
		return handled, err
	}
	observed, err := project.ReadDiscoveredIndexEvidence(ctx, monitor, claim.Role, claim.Query)
	if errors.Is(err, supabaseproject.ErrDiscoveredIndexEvidenceUnavailable) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	matches := func(e supabaseproject.DiscoveredIndexEvidence) bool {
		return e.DatabaseOID == claim.Database && e.RoleOID == claim.Role && e.QueryID == claim.Query && e.ObjectiveMS == windows.ObjectiveMS && len(claim.Generation) == 3 && e.Boot.Equal(claim.Generation[0]) && e.ResetAt.Equal(claim.Generation[1]) && e.EntrySince.Equal(claim.Generation[2])
	}
	if !matches(observed) {
		return true, errLookupScope
	}
	owner, err := pgx.ConnectConfig(ctx, m.Config.Copy())
	if err != nil {
		return true, err
	}
	defer labdb.Close(owner)
	if err := project.VerifyManagedRepair(ctx, owner); err != nil {
		return true, err
	}
	before, err := MeasureManagedIndexLookup(ctx, owner, observed, nil)
	if err != nil {
		result.DiagnosticReason = "managed_lookup_measurement_unavailable"
		return true, nil
	}
	if handled, err := m.runCleanupSettings(ctx, state, monitor, project, epoch, claim, windows, result, observed, owner, before); handled || err != nil {
		return handled, err
	}
	if handled, err := m.runMaintenance(ctx, state, monitor, project, epoch, claim, windows, result, observed, owner, before); handled || err != nil {
		return handled, err
	}
	if handled, err := m.runStatistics(ctx, state, monitor, project, epoch, claim, windows, result, observed, owner, before); handled || err != nil {
		return handled, err
	}
	if !slices.Contains(m.Capabilities, "index") {
		return false, nil
	}
	columns := observed.Columns
	if len(columns) == 1 {
		columns = nil
	}
	runID := fmt.Sprintf("managed-index-%d-%d", claim.ID, claim.Attempt)
	evidence := IndexEvidence{RunID: runID, At: time.Now().UTC(), Decision: Investigate, TableOID: observed.TableOID, Column: observed.Columns[0], Columns: columns, Existing: before.Catalog.Covering, Plan: before.Evidence, Label: before.Catalog.Keys[0].Name}
	result.Evidence = &evidence
	result.ThreadID = runID
	result.ObservedLatency = windows
	if evidence.Existing || before.Evidence.Node != "Seq Scan" || before.Evidence.ExaminedRows < 10000 || before.Evidence.ReturnedRows <= 0 || before.Evidence.ExaminedRows/before.Evidence.ReturnedRows < 10 {
		result.Outcome, result.DiagnosticReason = "no_change", "the measured sampled lookup does not qualify for an index"
		return true, nil
	}
	verify := func(ctx context.Context) error {
		if err := checkOwnedIndexClaim(ctx, state, project, epoch, claim); err != nil {
			return err
		}
		fresh, err := project.ReadDiscoveredIndexEvidence(ctx, monitor, claim.Role, claim.Query)
		if err != nil {
			return err
		}
		if !matches(fresh) || fresh.TableOID != observed.TableOID || fresh.Schema != observed.Schema || fresh.Relation != observed.Relation || fresh.Only != observed.Only || !slices.Equal(fresh.Columns, observed.Columns) {
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
		if errors.Is(err, ErrProjectModelBudget) {
			result.Outcome, result.DiagnosticReason = "diagnosis_unavailable", "daily_model_limit"
			return true, nil
		}
		return true, err
	}
	started := time.Now()
	proposal, err := m.Investigate(ctx, evidence)
	if err != nil {
		return true, err
	}
	report := IndexTrialReport{RunID: runID, JobID: claim.ID, Evidence: evidence, Proposal: proposal, Before: before.Sample, Measurement: "managed_sampled_input", ModelWallMS: elapsedMS(started)}
	result.Proposal = &proposal
	if proposal.Outcome == "no_change" && proposal.RunID == runID && proposal.TableOID == evidence.TableOID && proposal.Column == evidence.Column && slices.Equal(proposal.Columns, evidence.Columns) && len(proposal.Reason) > 0 && len(proposal.Reason) <= 1000 {
		result.Outcome, result.DiagnosticReason = "no_change", proposal.Reason
		return true, nil
	}
	if err := qualifyIndex(evidence, proposal, time.Now()); err != nil {
		result.Outcome, result.DiagnosticReason = "no_change", err.Error()
		return true, nil
	}
	var fence pgx.Tx
	var recorded bool
	guard := func(ctx context.Context, i ProjectIndexActionIntent) error {
		if i.Target != before.Catalog.Target || i.RunID != runID {
			return ErrProjectIndexTargetChanged
		}
		return verify(ctx)
	}
	started = time.Now()
	action, err := ApplyProjectIndex(ctx, m.Config, before.Catalog.Target, evidence, proposal, ProjectIndexActionGuards{
		CheckPermission: guard,
		CheckCapacity: func(ctx context.Context, _ ProjectIndexActionIntent) error {
			var available bool
			err := owner.QueryRow(ctx, `SELECT sum(numbackends)*3<current_setting('max_connections')::int*2 FROM pg_stat_database`).Scan(&available)
			if err == nil && !available {
				return errors.New("the database does not have connection headroom")
			}
			return err
		},
		RecordIntent: func(ctx context.Context, i ProjectIndexActionIntent) error {
			if err := guard(ctx, i); err != nil {
				return err
			}
			if err := recordOwnedIndexIntent(ctx, state, project.ID(), epoch, claim, i); err != nil {
				return err
			}
			recorded = true
			var err error
			fence, err = lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
			return err
		},
	})
	if fence != nil {
		err = errors.Join(err, releaseOwnedIndexEpoch(state, fence))
	}
	if err != nil {
		return true, err
	}
	if action.AlreadyCovered {
		if recorded {
			return true, errors.New("an equivalent index appeared after recording the action")
		}
		result.Outcome, result.DiagnosticReason = "no_change", "an equivalent index appeared before the action"
		return true, nil
	}
	report.BuildWallMS = elapsedMS(started)
	report.IndexBytes = action.IndexBytes
	report.IndexValid = true
	started = time.Now()
	after, err := MeasureManagedIndexLookup(ctx, owner, observed, before.Inputs)
	if err != nil {
		return true, err
	}
	report.VerifyWallMS = elapsedMS(started)
	report.After = after.Sample
	report.Outcome, report.Reason = "inconclusive", "the sampled input or ongoing protected traffic did not demonstrate improvement"
	afterControls, controlErr := observeManagedControls(ctx, owner, claim.Database, claim.Role, claim.Query, controls)
	if controlErr == nil || errors.Is(controlErr, ErrManagedTrafficRegression) {
		report.Before.HealthyMS, report.Before.WriteMS = controls.ReadMS, controls.WriteMS
		report.Before.HealthyReads, report.Before.Writes = int(controls.ReadCalls), int(controls.WriteCalls)
		report.After.HealthyMS, report.After.WriteMS = afterControls.ReadMS, afterControls.WriteMS
		report.After.HealthyReads, report.After.Writes = int(afterControls.ReadCalls), int(afterControls.WriteCalls)
		var exact bool
		var oid uint32
		var bytes int64
		err = owner.QueryRow(ctx, projectIndexReadbackSQL, observed.TableOID, observed.Schema, after.IndexName, observed.Columns).Scan(&oid, &bytes, &exact)
		if controlErr == nil && err == nil && exact && oid == action.IndexOID && before.Sample.PlanReturned == after.Sample.PlanReturned && after.Sample.ReadMS > 0 && after.Sample.ReadMS < before.Sample.ReadMS*.8 && after.Sample.Blocks < before.Sample.Blocks && afterControls.ReadMS <= controls.ReadMS*1.25 && afterControls.WriteMS <= controls.WriteMS*1.25 {
			report.Outcome, report.Reason = "improved_tested_inputs", "the sampled lookup improved and observed protected reads and writes stayed within their limits"
		}
	}
	if errors.Is(controlErr, ErrManagedTrafficRegression) {
		report.Outcome, report.Reason = "regressed", "a protected normal-traffic query regressed after the index"
	} else if before.Sample.PlanReturned == after.Sample.PlanReturned && before.Sample.ReadMS > 0 && after.Sample.ReadMS > before.Sample.ReadMS*1.25 {
		report.Outcome, report.Reason = "regressed", "the same sampled lookup became slower after the index"
	}
	if report.Outcome != "improved_tested_inputs" {
		intent, err := requestManagedIndexRollback(ctx, state, project.ID(), epoch, claim, action.IndexOID)
		if err != nil {
			return true, err
		}
		rollbackFence, err := lockOwnedIndexEpoch(ctx, state, project.ID(), epoch, claim)
		if err != nil {
			return true, err
		}
		rollbackErr := rollbackManagedIndex(ctx, owner, claim, intent)
		if err := errors.Join(rollbackErr, releaseOwnedIndexEpoch(state, rollbackFence)); err != nil {
			return true, err
		}
		report.Reverted, report.IndexValid = true, false
		report.Reason += "; the exact index created by this repair was removed"
	}
	if err := verify(ctx); err != nil {
		return true, err
	}
	result.Outcome, result.DiagnosticReason, result.Repair = report.Outcome, report.Reason, &report
	return true, nil
}

func (m *ManagedProjectIndex) recover(ctx context.Context, state *pgx.Conn, project supabaseproject.Project, claim investigationClaim, result *DispatchResult) (bool, error) {
	if !m.matches(project) || !claim.RecoveryOnly {
		return false, nil
	}
	if claim.MaintenanceOperation == "memory" {
		return m.recoverMemory(ctx, state, project, claim, result)
	}
	if claim.MaintenanceOperation != "" {
		return m.recoverMaintenance(ctx, state, project, claim, result)
	}
	var raw json.RawMessage
	if err := state.QueryRow(ctx, `SELECT intent FROM pooltune_worker.owned_index_actions WHERE job_id=$1 AND project_id=$2 AND outcome='pending'`, claim.ID, project.ID()).Scan(&raw); err != nil {
		return true, err
	}
	var intent ProjectIndexActionIntent
	if err := json.Unmarshal(raw, &intent); err != nil {
		return true, err
	}
	owner, err := pgx.ConnectConfig(ctx, m.Config.Copy())
	if err != nil {
		return true, err
	}
	defer labdb.Close(owner)
	if err := project.VerifyManagedRepair(ctx, owner); err != nil {
		return true, err
	}
	recovery, err := inspectManagedIndexIntent(ctx, owner, claim, intent)
	if err != nil {
		return true, err
	}
	recovery.intent = raw
	result.Outcome, result.DiagnosticReason, result.IndexRecovery = "repair_interrupted", recovery.Reason, &recovery
	return true, nil
}
