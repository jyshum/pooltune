package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

var managedMaintenanceRunID = regexp.MustCompile(`^managed-maintenance-([1-9][0-9]*)-[1-9][0-9]*$`)

func (m *ManagedProjectIndex) recoverMaintenance(ctx context.Context, state *pgx.Conn, project supabaseproject.Project, claim investigationClaim, result *DispatchResult) (bool, error) {
	var raw json.RawMessage
	if err := state.QueryRow(ctx, `SELECT intent FROM pooltune_worker.owned_project_actions WHERE project_id=$1 AND job_id=$2 AND operation=$3 AND outcome='pending'`, project.ID(), claim.ID, claim.MaintenanceOperation).Scan(&raw); err != nil {
		return true, err
	}
	if claim.MaintenanceOperation == "statistics" {
		var version struct{ Version int }
		if err := json.Unmarshal(raw, &version); err != nil {
			return true, err
		}
		if version.Version == 1 {
			return m.recoverCommittedStatistics(ctx, state, project, claim, raw, result)
		}
	}
	var intent ProjectMaintenanceIntent
	if err := json.Unmarshal(raw, &intent); err != nil {
		return true, err
	}
	if claim.MaintenanceOperation != "statistics" && string(intent.Operation) != claim.MaintenanceOperation {
		return true, ErrProjectMaintenanceTarget
	}
	owner, err := supabaseproject.ConnectRepair(ctx, m.Config)
	if err != nil {
		return true, err
	}
	defer closeVacuumConnection(owner)
	if err := project.VerifyManagedRepair(ctx, owner); err != nil {
		return true, err
	}
	var report ManagedMaintenanceReport
	if claim.MaintenanceOperation == "statistics" {
		report, err = inspectManagedStatisticsIntent(ctx, owner, claim, raw)
	} else {
		report, err = inspectManagedMaintenanceIntent(ctx, owner, claim, intent)
	}
	if err != nil {
		return true, err
	}
	result.ownedOperation = claim.MaintenanceOperation
	result.managedRecoveryIntent = raw
	result.Outcome, result.DiagnosticReason, result.ManagedMaintenance = report.Outcome, report.Reason, &report
	return true, nil
}

// inspectManagedMaintenanceIntent observes an interrupted action. It never
// retries ANALYZE or VACUUM and never turns a maintenance counter into a benefit.
func inspectManagedMaintenanceIntent(ctx context.Context, db *pgx.Conn, claim investigationClaim, intent ProjectMaintenanceIntent) (report ManagedMaintenanceReport, err error) {
	report.Operation, report.Measurement, report.Outcome = intent.Operation, "interrupted_maintenance_catalog_observation", "repair_interrupted"
	parts := managedMaintenanceRunID.FindStringSubmatch(intent.RunID)
	if len(parts) != 2 || parts[1] != fmt.Sprint(claim.ID) || intent.Target.DatabaseOID != claim.Database || intent.Target.TableOID == 0 || intent.Target.Schema == "" || intent.Target.Relation == "" || intent.ObservedAt.IsZero() || intent.RelationBytes <= 0 || intent.RelationBytes > projectMaintenanceMaxRelationBytes ||
		(intent.Operation != ProjectAnalyze && intent.Operation != ProjectVacuum) || (intent.Operation == ProjectAnalyze && (len(intent.Columns) != 1 || intent.Columns[0] <= 0)) || (intent.Operation == ProjectVacuum && len(intent.Columns) != 0) {
		return report, errors.New("invalid recorded managed maintenance identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var pid, database uint32
	if err := db.QueryRow(ctx, `SELECT pg_backend_pid(),oid FROM pg_database WHERE datname=current_database()`).Scan(&pid, &database); err != nil {
		return report, err
	}
	if pid != db.PgConn().PID() || database != claim.Database {
		return report, ErrProjectMaintenanceTarget
	}
	var locked bool
	if err := db.QueryRow(ctx, `SELECT pg_try_advisory_lock($1,$2)`, projectIndexLockNamespace, int32(intent.Target.TableOID)).Scan(&locked); err != nil {
		return report, err
	}
	if !locked {
		return report, errOwnedIndexActionActive
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var released bool
		unlockErr := db.QueryRow(cleanup, `SELECT pg_advisory_unlock($1,$2)`, projectIndexLockNamespace, int32(intent.Target.TableOID)).Scan(&released)
		if unlockErr != nil || !released {
			closeVacuumConnection(db)
			err = errors.Join(err, errors.New("maintenance recovery lock release failed"), unlockErr)
		}
	}()
	if _, _, err := readProjectMaintenanceTarget(ctx, db, intent.ProjectMaintenanceRequest); errors.Is(err, ErrProjectMaintenanceTarget) {
		report.RecoveryStatus, report.Reason = "target_changed", "The recorded relation or its maintenance authority changed. The interrupted command was not repeated and has no verified benefit."
		return report, nil
	} else if err != nil {
		return report, err
	}
	observation, err := observeProjectMaintenance(ctx, db, intent.ProjectMaintenanceRequest)
	if err != nil {
		return report, err
	}
	report.RecoveryObservation = &observation
	report.RecoveryStatus, report.Reason = "completion_unconfirmed", "The interrupted maintenance command has no confirmed completion or verified benefit. It was not repeated."
	if !observation.LastRun.Before(intent.ObservedAt) && !observation.LastRun.After(observation.At) {
		report.RecoveryStatus, report.Reason = "maintenance_observed", "PostgreSQL records maintenance after the saved intent. This does not establish which actor ran it or whether performance improved. The command was not repeated."
	}
	return report, nil
}
