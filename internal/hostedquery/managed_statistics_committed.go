package hostedquery

import (
	"context"
	"errors"
	"pooltune/internal/supabaseproject"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// ManagedStatisticsIntent is durable before the native transaction commits.
// Catalog versions detect even same-value owner edits. Inputs are never stored.
type ManagedStatisticsIntent struct {
	Version              int
	RunID                string
	Target               ProjectIndexTarget
	Keys                 []ProjectIndexKey
	Variant              string
	Prior                ProjectStatisticsSetting
	PriorEffective       int32
	PriorColumnVersion   string
	NextTarget           int32
	ObjectName           string
	ObjectOID            uint32
	TrialTransactionID   string
	RestoreTransactionID string
	TrialVersion         string
	RestoredVersion      string
	OwnerOID             uint32
	Deadline             time.Time
	Phase                string
}

type managedStatisticsCommitGuards struct {
	Permission func(context.Context) error
	Capacity   func(context.Context) error
	Recheck    func(context.Context, pgx.Tx) error
	Record     func(context.Context, ManagedStatisticsIntent) error
	Save       func(context.Context, ManagedStatisticsIntent, ManagedStatisticsIntent) error
}

type managedStatisticsCommitResult struct {
	Intent                                                  ManagedStatisticsIntent
	Applied, Committed, Restored, Regenerated, OwnerChanged bool
	Outcome, Reason                                         string
	AnalyzeDuration                                         time.Duration
}

func statisticsColumnVersion(ctx context.Context, tx pgx.Tx, target ProjectIndexTarget, key ProjectIndexKey) (version string, err error) {
	err = tx.QueryRow(ctx, `SELECT xmin::text FROM pg_attribute WHERE attrelid=$1 AND attnum=$2 AND attname=$3 AND NOT attisdropped`, target.TableOID, key.Number, key.Name).Scan(&version)
	return version, err
}

func statisticsNativeTx(ctx context.Context, db *pgx.Conn, target ProjectIndexTarget) (pgx.Tx, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL search_path=pg_catalog;SET LOCAL statement_timeout='2s';SET LOCAL lock_timeout='150ms';SET LOCAL transaction_timeout='8s';SET LOCAL idle_in_transaction_session_timeout='3s';SET LOCAL maintenance_work_mem='16MB';SET LOCAL max_parallel_maintenance_workers=0`); err == nil {
		_, err = tx.Exec(ctx, "LOCK TABLE ONLY "+qualified(target.Schema, target.Relation)+" IN SHARE UPDATE EXCLUSIVE MODE NOWAIT")
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func rollbackStatisticsTx(tx pgx.Tx) {
	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = tx.Rollback(cleanup)
}

func lockManagedStatistics(ctx context.Context, db *pgx.Conn, table uint32) error {
	var pid uint32
	var locked bool
	if err := db.QueryRow(ctx, `SELECT pg_backend_pid(),pg_try_advisory_lock($1,$2)`, projectIndexLockNamespace, int32(table)).Scan(&pid, &locked); err != nil {
		return err
	}
	if pid != db.PgConn().PID() {
		return ErrProjectStatisticsTarget
	}
	if !locked {
		return errOwnedIndexActionActive
	}
	return nil
}

func applyManagedStatisticsCommitted(ctx context.Context, config *pgx.ConnConfig, initial ManagedStatisticsIntent, guards managedStatisticsCommitGuards, verify func(context.Context) (string, string, error)) (result managedStatisticsCommitResult, err error) {
	result.Intent = initial
	if !validManagedStatisticsIntent(initial, false) || guards.Permission == nil || guards.Capacity == nil || guards.Recheck == nil || guards.Record == nil || guards.Save == nil || verify == nil || config == nil {
		return result, errors.New("committed statistics requires an exact bounded intent and every guard")
	}
	db, err := supabaseproject.ConnectRepair(ctx, config)
	if err != nil {
		return result, err
	}
	defer closeVacuumConnection(db)
	if err := lockManagedStatistics(ctx, db, initial.Target.TableOID); err != nil {
		return result, err
	}
	tx, err := statisticsNativeTx(ctx, db, initial.Target)
	if err != nil {
		return result, err
	}
	defer rollbackStatisticsTx(tx)
	var owner uint32
	if err := tx.QueryRow(ctx, `SELECT current_user::regrole::oid`).Scan(&owner); err != nil {
		return result, err
	}
	initial.OwnerOID = owner
	for i, key := range initial.Keys {
		prior, err := readProjectStatisticsState(ctx, tx, initial.Target, key, true)
		if err != nil {
			return result, err
		}
		if i == 0 && initial.Variant == "column_target" {
			initial.Prior, initial.PriorEffective = prior.setting, prior.effective
			initial.PriorColumnVersion, err = statisticsColumnVersion(ctx, tx, initial.Target, key)
			if err != nil {
				return result, err
			}
			if !boundedProjectStatisticsTarget(prior.effective, initial.NextTarget) {
				return result, ErrProjectStatisticsTarget
			}
		}
	}
	if initial.Variant == "joint" {
		var absent bool
		if err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT FROM pg_statistic_ext WHERE stxnamespace=$1::regnamespace AND stxname=$2)`, initial.Target.Schema, initial.ObjectName).Scan(&absent); err != nil {
			return result, err
		}
		if !absent {
			return result, ErrProjectStatisticsTarget
		}
	}
	if err := guards.Recheck(ctx, tx); err != nil {
		return result, err
	}
	for _, gate := range []func(context.Context) error{guards.Permission, guards.Capacity} {
		if err := gate(ctx); err != nil {
			return result, err
		}
	}
	initial.Phase = "intent"
	if err := guards.Record(ctx, initial); err != nil {
		return result, err
	}
	result.Intent = initial
	if err := guards.Permission(ctx); err != nil {
		return result, err
	}
	if !time.Now().Before(initial.Deadline) {
		return result, context.DeadlineExceeded
	}
	relation := qualified(initial.Target.Schema, initial.Target.Relation)
	columns := pgx.Identifier{initial.Keys[0].Name}.Sanitize()
	if len(initial.Keys) == 2 {
		columns += "," + pgx.Identifier{initial.Keys[1].Name}.Sanitize()
	}
	statement := "CREATE STATISTICS " + qualified(initial.Target.Schema, initial.ObjectName) + " (mcv) ON " + columns + " FROM " + relation
	if initial.Variant == "column_target" {
		statement = "ALTER TABLE ONLY " + relation + " ALTER COLUMN " + columns + " SET STATISTICS " + strconv.FormatInt(int64(initial.NextTarget), 10)
	}
	result.Applied = true
	if _, err := tx.Exec(ctx, statement); err != nil {
		return result, err
	}
	started := time.Now()
	if _, err := tx.Exec(ctx, "ANALYZE (BUFFER_USAGE_LIMIT '256kB') "+relation+" ("+columns+")"); err != nil {
		return result, err
	}
	result.AnalyzeDuration = time.Since(started)
	ready := initial
	ready.Phase = "commit_ready"
	if err := tx.QueryRow(ctx, `SELECT pg_current_xact_id()::text`).Scan(&ready.TrialTransactionID); err != nil {
		return result, err
	}
	if initial.Variant == "column_target" {
		current, err := readProjectStatisticsState(ctx, tx, initial.Target, initial.Keys[0], true)
		if err != nil {
			return result, err
		}
		if current.setting != (ProjectStatisticsSetting{Value: initial.NextTarget}) {
			return result, ErrProjectStatisticsTarget
		}
		ready.TrialVersion, err = statisticsColumnVersion(ctx, tx, initial.Target, initial.Keys[0])
		if err != nil {
			return result, err
		}
	} else {
		if err := tx.QueryRow(ctx, `SELECT oid,xmin::text FROM pg_statistic_ext WHERE stxnamespace=$1::regnamespace AND stxname=$2 AND stxrelid=$3 AND stxowner=$4`, initial.Target.Schema, initial.ObjectName, initial.Target.TableOID, owner).Scan(&ready.ObjectOID, &ready.TrialVersion); err != nil {
			return result, err
		}
		var size int64
		if err := tx.QueryRow(ctx, `SELECT (pg_column_size(most_common_vals)+pg_column_size(most_common_freqs)+pg_column_size(most_common_base_freqs))::bigint FROM pg_stats_ext WHERE statistics_schemaname=$1 AND statistics_name=$2 AND schemaname=$1 AND tablename=$3 AND NOT inherited`, initial.Target.Schema, initial.ObjectName, initial.Target.Relation).Scan(&size); err != nil {
			return result, err
		}
		if size <= 0 || size > maxStatisticsBytes {
			return result, errors.New("joint statistics exceeded its storage budget")
		}
	}
	// Save uses a separate committed state connection while the epoch fence is
	// held. A native commit can never precede its exact recovery identity.
	if err := guards.Save(ctx, initial, ready); err != nil {
		return result, err
	}
	result.Intent = ready
	if err := guards.Permission(ctx); err != nil {
		return result, err
	}
	if !time.Now().Before(initial.Deadline) {
		return result, context.DeadlineExceeded
	}
	if err := tx.Commit(ctx); err != nil {
		return result, err
	}
	result.Committed = true
	verifyCtx, cancel := context.WithDeadline(ctx, initial.Deadline)
	result.Outcome, result.Reason, err = verify(verifyCtx)
	cancel()
	if err == nil && result.Outcome == "improved_tested_inputs" && time.Now().Before(initial.Deadline) {
		var matches bool
		matches, err = committedStatisticsStillMatches(ctx, db, result.Intent)
		if err == nil && matches && time.Now().Before(initial.Deadline) {
			return result, nil
		}
		result.Outcome, result.Reason = "inconclusive", "the committed statistics identity changed during verification"
	}
	if result.Outcome != "regressed" {
		result.Outcome = "inconclusive"
	}
	if err != nil || result.Reason == "" {
		result.Reason = "post-commit verification was incomplete"
	}
	cleanup, finish := context.WithTimeout(context.WithoutCancel(ctx), 6*time.Second)
	defer finish()
	restored, restoreErr := restoreManagedStatisticsCommitted(cleanup, db, result.Intent, guards.Permission, guards.Save)
	result.Intent = restored.Intent
	result.Restored = restored.Restored
	result.Regenerated = restored.Regenerated
	result.OwnerChanged = restored.OwnerChanged
	if restoreErr != nil {
		return result, restoreErr
	}
	if result.OwnerChanged {
		result.Reason += "; owner changes were preserved and automatic restoration was refused"
	} else if result.Restored && result.Regenerated {
		result.Reason += "; prior configuration was restored and statistics regenerated"
	} else if result.Restored {
		result.Reason += "; the recorded configuration is already restored"
	}
	return result, nil
}

func validManagedStatisticsIntent(i ManagedStatisticsIntent, receipt bool) bool {
	if i.Version != 1 || !managedStatisticsRunID.MatchString(i.RunID) || i.Target.DatabaseOID == 0 || i.Target.TableOID == 0 || i.Target.Schema == "" || i.Target.Relation == "" || i.Deadline.IsZero() || len(i.Keys) < 1 || len(i.Keys) > 2 {
		return false
	}
	columns := make([]int16, len(i.Keys))
	for n, k := range i.Keys {
		if k.Number <= 0 || k.Name == "" {
			return false
		}
		columns[n] = k.Number
	}
	if i.Variant == "column_target" {
		if len(i.Keys) != 1 || i.NextTarget < 1 || i.NextTarget > projectStatisticsMaxTarget {
			return false
		}
	} else if i.Variant == "joint" {
		if len(i.Keys) != 2 || !ValidIndexColumns(columns[0], columns) || i.ObjectName != managedJointStatisticsName(i.Target.TableOID, columns) {
			return false
		}
	} else {
		return false
	}
	if receipt && (i.OwnerOID == 0 || i.TrialVersion == "" || (i.Variant == "joint" && i.ObjectOID == 0) || (i.Variant == "column_target" && (!boundedProjectStatisticsTarget(i.PriorEffective, i.NextTarget) || i.PriorColumnVersion == ""))) {
		return false
	}
	return true
}

// restoreManagedStatisticsCommitted uses compare-and-set under a native relation
// lock. Regenerating statistics restores configuration, not the old random sample.
func restoreManagedStatisticsCommitted(ctx context.Context, db *pgx.Conn, intent ManagedStatisticsIntent, permission func(context.Context) error, save func(context.Context, ManagedStatisticsIntent, ManagedStatisticsIntent) error) (result managedStatisticsCommitResult, err error) {
	result.Intent = intent
	if !validManagedStatisticsIntent(intent, true) || permission == nil || save == nil {
		return result, errors.New("missing exact committed statistics restoration identity")
	}
	var sameTarget bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=$1 AND c.relname=$2 AND n.nspname=$3 AND c.relowner=$4)`, intent.Target.TableOID, intent.Target.Relation, intent.Target.Schema, intent.OwnerOID).Scan(&sameTarget); err != nil {
		return result, err
	}
	if !sameTarget {
		result.OwnerChanged = true
		return result, nil
	}
	tx, err := statisticsNativeTx(ctx, db, intent.Target)
	if err != nil {
		return result, err
	}
	defer rollbackStatisticsTx(tx)
	changed := func() (managedStatisticsCommitResult, error) { result.OwnerChanged = true; return result, nil }
	var owner uint32
	if err := tx.QueryRow(ctx, `SELECT current_user::regrole::oid`).Scan(&owner); err != nil {
		return result, err
	}
	if owner != intent.OwnerOID {
		return changed()
	}
	for _, key := range intent.Keys {
		if _, err := readProjectStatisticsState(ctx, tx, intent.Target, key, true); errors.Is(err, ErrProjectStatisticsTarget) {
			return changed()
		} else if err != nil {
			return result, err
		}
	}
	relation := qualified(intent.Target.Schema, intent.Target.Relation)
	columns := pgx.Identifier{intent.Keys[0].Name}.Sanitize()
	if len(intent.Keys) == 2 {
		columns += "," + pgx.Identifier{intent.Keys[1].Name}.Sanitize()
	}
	if intent.Variant == "column_target" {
		current, err := readProjectStatisticsState(ctx, tx, intent.Target, intent.Keys[0], true)
		if err != nil {
			return result, err
		}
		version, err := statisticsColumnVersion(ctx, tx, intent.Target, intent.Keys[0])
		if err != nil {
			return result, err
		}
		if current.setting == intent.Prior && intent.RestoredVersion != "" && version == intent.RestoredVersion {
			result.Restored, result.Regenerated = true, true
			return result, nil
		}
		if current.setting == intent.Prior && version == intent.PriorColumnVersion {
			result.Restored = true
			return result, nil
		}
		if current.setting != (ProjectStatisticsSetting{Value: intent.NextTarget}) || version != intent.TrialVersion {
			return changed()
		}
	} else {
		var exact bool
		sorted := []int16{intent.Keys[0].Number, intent.Keys[1].Number}
		slices.Sort(sorted)
		err := tx.QueryRow(ctx, `SELECT oid=$3 AND stxrelid=$4 AND stxowner=$5 AND xmin::text=$6 AND ARRAY(SELECT unnest(stxkeys))=$7::smallint[] AND stxkind=ARRAY['m']::"char"[] AND stxexprs IS NULL FROM pg_statistic_ext WHERE stxnamespace=$1::regnamespace AND stxname=$2`, intent.Target.Schema, intent.ObjectName, intent.ObjectOID, intent.Target.TableOID, intent.OwnerOID, intent.TrialVersion, sorted).Scan(&exact)
		if errors.Is(err, pgx.ErrNoRows) {
			var stillPresent bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_statistic_ext WHERE oid=$1)`, intent.ObjectOID).Scan(&stillPresent); err != nil {
				return result, err
			}
			if stillPresent {
				return changed()
			}
			if intent.Phase == "restore_ready" && intent.RestoreTransactionID != "" {
				var status *string
				if err := tx.QueryRow(ctx, `SELECT pg_xact_status($1::xid8)`, intent.RestoreTransactionID).Scan(&status); err != nil {
					return result, err
				}
				if status != nil && *status == "committed" {
					result.Restored, result.Regenerated = true, true
					return result, nil
				}
			}
			if intent.TrialTransactionID != "" {
				var status *string
				if err := tx.QueryRow(ctx, `SELECT pg_xact_status($1::xid8)`, intent.TrialTransactionID).Scan(&status); err != nil {
					return result, err
				}
				if status != nil && *status == "aborted" {
					result.Restored = true
					return result, nil
				}
			}
			return changed()
		}
		if err != nil {
			return result, err
		}
		if !exact {
			return changed()
		}
		// A table lock does not fence ALTER/DROP STATISTICS. Same-owner ALTER
		// takes the object's lock without changing its generation. Recheck the
		// identity under that lock before dropping by name. A replaced object
		// causes rollback, including any provisional ownership change.
		if _, err := tx.Exec(ctx, "ALTER STATISTICS "+qualified(intent.Target.Schema, intent.ObjectName)+" OWNER TO CURRENT_USER"); err != nil {
			return result, err
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_statistic_ext WHERE oid=$1 AND stxnamespace=$2::regnamespace AND stxname=$3 AND stxrelid=$4 AND stxowner=$5 AND xmin::text=$6 AND ARRAY(SELECT unnest(stxkeys))=$7::smallint[] AND stxkind=ARRAY['m']::"char"[] AND stxexprs IS NULL)`, intent.ObjectOID, intent.Target.Schema, intent.ObjectName, intent.Target.TableOID, intent.OwnerOID, intent.TrialVersion, sorted).Scan(&exact); err != nil {
			return result, err
		}
		if !exact {
			return changed()
		}
	}
	if err := permission(ctx); err != nil {
		return result, err
	}
	statement := "DROP STATISTICS " + qualified(intent.Target.Schema, intent.ObjectName)
	if intent.Variant == "column_target" {
		value := int64(intent.Prior.Value)
		if intent.Prior.UsesDefault {
			value = -1
		}
		statement = "ALTER TABLE ONLY " + relation + " ALTER COLUMN " + columns + " SET STATISTICS " + strconv.FormatInt(value, 10)
	}
	if _, err := tx.Exec(ctx, statement); err != nil {
		return result, err
	}
	if _, err := tx.Exec(ctx, "ANALYZE (BUFFER_USAGE_LIMIT '256kB') "+relation+" ("+columns+")"); err != nil {
		return result, err
	}
	ready := intent
	ready.Phase = "restore_ready"
	if err := tx.QueryRow(ctx, `SELECT pg_current_xact_id()::text`).Scan(&ready.RestoreTransactionID); err != nil {
		return result, err
	}
	if intent.Variant == "column_target" {
		ready.RestoredVersion, err = statisticsColumnVersion(ctx, tx, intent.Target, intent.Keys[0])
		if err != nil {
			return result, err
		}
	}
	if err := save(ctx, intent, ready); err != nil {
		return result, err
	}
	result.Intent = ready
	if err := permission(ctx); err != nil {
		return result, err
	}
	if err := tx.Commit(ctx); err != nil {
		return result, err
	}
	result.Restored, result.Regenerated = true, true
	return result, nil
}

func committedStatisticsStillMatches(ctx context.Context, db *pgx.Conn, i ManagedStatisticsIntent) (bool, error) {
	tx, err := statisticsNativeTx(ctx, db, i.Target)
	if err != nil {
		return false, err
	}
	defer rollbackStatisticsTx(tx)
	for _, key := range i.Keys {
		if _, err := readProjectStatisticsState(ctx, tx, i.Target, key, true); err != nil {
			return false, err
		}
	}
	if i.Variant == "column_target" {
		current, err := readProjectStatisticsState(ctx, tx, i.Target, i.Keys[0], true)
		if err != nil {
			return false, err
		}
		version, err := statisticsColumnVersion(ctx, tx, i.Target, i.Keys[0])
		return err == nil && current.setting == (ProjectStatisticsSetting{Value: i.NextTarget}) && version == i.TrialVersion, err
	}
	var exact bool
	sorted := []int16{i.Keys[0].Number, i.Keys[1].Number}
	slices.Sort(sorted)
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_statistic_ext WHERE oid=$1 AND stxnamespace=$2::regnamespace AND stxname=$3 AND stxrelid=$4 AND stxowner=$5 AND xmin::text=$6 AND ARRAY(SELECT unnest(stxkeys))=$7::smallint[] AND stxkind=ARRAY['m']::"char"[] AND stxexprs IS NULL)`, i.ObjectOID, i.Target.Schema, i.ObjectName, i.Target.TableOID, i.OwnerOID, i.TrialVersion, sorted).Scan(&exact)
	return exact, err
}
