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
)

var managedStatisticsRunID = regexp.MustCompile(`^managed-statistics-([1-9][0-9]*)-[1-9][0-9]*$`)

func managedJointStatisticsName(table uint32, columns []int16) string {
	return fmt.Sprintf("pooltune_mcv_%d_%d_%d", table, columns[0], columns[1])
}

func inspectManagedStatisticsIntent(ctx context.Context, db *pgx.Conn, claim investigationClaim, raw json.RawMessage) (report ManagedMaintenanceReport, err error) {
	report.Operation, report.Measurement, report.Outcome = "statistics", "interrupted_statistics_catalog_observation", "repair_interrupted"
	var column ProjectStatisticsIntent
	var joint OwnedExtendedStatisticsIntent
	if json.Unmarshal(raw, &column) != nil || json.Unmarshal(raw, &joint) != nil {
		return report, errors.New("invalid statistics recovery intent")
	}
	target, runID, bytes := column.Target, column.RunID, column.RelationBytes
	isColumn := column.Column > 0
	columns := []int16{column.Column}
	if !isColumn {
		target, runID, bytes = joint.Target, joint.RunID, joint.RelationBytes
		columns = joint.ColumnNumbers
	}
	parts := managedStatisticsRunID.FindStringSubmatch(runID)
	if len(parts) != 2 || parts[1] != fmt.Sprint(claim.ID) || target.DatabaseOID != claim.Database || target.TableOID == 0 || target.Schema == "" || target.Relation == "" || bytes <= 0 || bytes > projectIndexMaxRelationBytes {
		return report, errors.New("invalid recorded statistics identity")
	}
	if isColumn {
		if column.Key.Number != column.Column || column.Key.Name == "" || !boundedProjectStatisticsTarget(column.PriorEffective, column.NextTarget) {
			return report, errors.New("invalid recorded column statistics settings")
		}
	} else if len(columns) != 2 || !ValidIndexColumns(columns[0], columns) || joint.ObjectName != managedJointStatisticsName(target.TableOID, columns) {
		return report, errors.New("invalid recorded joint statistics keys")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var pid, database uint32
	if err := db.QueryRow(ctx, `SELECT pg_backend_pid(),oid FROM pg_database WHERE datname=current_database()`).Scan(&pid, &database); err != nil {
		return report, err
	}
	if pid != db.PgConn().PID() || database != claim.Database {
		return report, ErrProjectStatisticsTarget
	}
	var locked bool
	if err := db.QueryRow(ctx, `SELECT pg_try_advisory_lock($1,$2)`, projectIndexLockNamespace, int32(target.TableOID)).Scan(&locked); err != nil {
		return report, err
	}
	if !locked {
		return report, errOwnedIndexActionActive
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var released bool
		unlockErr := db.QueryRow(cleanup, `SELECT pg_advisory_unlock($1,$2)`, projectIndexLockNamespace, int32(target.TableOID)).Scan(&released)
		if unlockErr != nil || !released {
			closeVacuumConnection(db)
			err = errors.Join(err, errors.New("statistics recovery lock release failed"), unlockErr)
		}
	}()
	tx, err := db.Begin(ctx)
	if err != nil {
		return report, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err = errors.Join(err, tx.Rollback(cleanup))
	}()
	changed := func() (ManagedMaintenanceReport, error) {
		report.RecoveryStatus, report.Reason = "target_changed", "The recorded statistics target or owner changed. The interrupted trial was not repeated and has no verified benefit."
		return report, nil
	}
	for _, number := range columns {
		var name string
		if err := tx.QueryRow(ctx, `SELECT attname FROM pg_attribute WHERE attrelid=$1 AND attnum=$2 AND NOT attisdropped`, target.TableOID, number).Scan(&name); errors.Is(err, pgx.ErrNoRows) {
			return changed()
		} else if err != nil {
			return report, err
		}
		if isColumn && name != column.Key.Name {
			return changed()
		}
		current, err := readProjectStatisticsState(ctx, tx, target, ProjectIndexKey{Number: number, Name: name}, false)
		if errors.Is(err, ErrProjectStatisticsTarget) {
			return changed()
		}
		if err != nil {
			return report, err
		}
		if isColumn {
			switch current.setting {
			case column.Prior:
				report.RecoveryStatus = "prior_statistics_setting"
			case ProjectStatisticsSetting{Value: column.NextTarget}:
				report.RecoveryStatus = "trial_statistics_setting"
			default:
				report.RecoveryStatus = "statistics_setting_changed"
			}
			report.Reason = "The current column setting was read after interruption. This does not prove which planner sample survived or any performance benefit. The trial was not repeated."
			return report, nil
		}
	}
	var exact bool
	sorted := slices.Clone(columns)
	slices.Sort(sorted)
	err = tx.QueryRow(ctx, `SELECT stxrelid=$3 AND stxowner=current_user::regrole::oid AND ARRAY(SELECT unnest(stxkeys))=$4::smallint[] AND stxkind=ARRAY['m']::"char"[] AND stxexprs IS NULL FROM pg_statistic_ext WHERE stxnamespace=$1::regnamespace AND stxname=$2`, target.Schema, joint.ObjectName, target.TableOID, sorted).Scan(&exact)
	if errors.Is(err, pgx.ErrNoRows) {
		report.RecoveryStatus = "statistics_object_absent"
		err = nil
	} else if err == nil && exact {
		report.RecoveryStatus = "matching_statistics_object_present"
	} else if err == nil {
		report.RecoveryStatus = "statistics_object_changed"
	}
	if err != nil {
		return report, err
	}
	report.Reason = "The recorded statistics object's current catalog state was observed. The interrupted trial was not repeated and has no verified performance result."
	return report, nil
}
