package hostedquery

import (
	"context"
	"errors"
	"fmt"
	"math"
	"pooltune/internal/supabaseproject"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type ProjectCleanupSettingsObservation struct {
	Target                                  ProjectIndexTarget
	At, Boot, StatsReset, LastAutovacuum    time.Time
	ScaleOption                             string
	ScaleFactor, EstimatedRows              float64
	BaseThreshold, MaximumThreshold         int64
	DeadTuples, Cleanups, RelationBytes     int64
	AutomaticCleanups, Updated, Deleted     int64
	AutovacuumNaptimeSeconds                int64
	AutovacuumEnabled, OnlyDeadTupleTrigger bool
}

type ProjectCleanupSettingsRequest struct {
	RunID          string
	Earlier, Later ProjectCleanupSettingsObservation
}

type ProjectCleanupSettingsIntent struct {
	Target                    ProjectIndexTarget
	RunID                     string
	BeforeOption, AfterOption string // Empty means the table inherits the default.
	Restoring                 bool
}

type ProjectCleanupSettingsGuards struct {
	// The caller keeps the exact grant and namespace binding fenced during this action.
	CheckPermission func(context.Context, ProjectCleanupSettingsIntent) error
	CheckCapacity   func(context.Context, int64, ProjectCleanupSettingsIntent) error
	// RecordIntent persists the exact one-option change before it reaches PostgreSQL.
	RecordIntent         func(context.Context, ProjectCleanupSettingsIntent) error
	recordObservedIntent func(context.Context, ProjectCleanupSettingsIntent, ProjectCleanupSettingsObservation) error
	recordLockedIntent   func(context.Context, ProjectCleanupSettingsIntent, ProjectCleanupSettingsObservation, pgx.Tx) error
}

type ProjectCleanupSettingsResult struct {
	Intent                    ProjectCleanupSettingsIntent
	Before, After             ProjectCleanupSettingsObservation
	WriteAttempted, Confirmed bool
}

var ErrProjectCleanupSettingsChanged = errors.New("cleanup option changed since the recorded action")

// ReadProjectCleanupSettings captures the exact table's current trigger settings
// and estimated dead tuple backlog using a connection authenticated as its owner.
func ReadProjectCleanupSettings(ctx context.Context, db *pgx.Conn, target ProjectIndexTarget) (ProjectCleanupSettingsObservation, error) {
	if db == nil {
		return ProjectCleanupSettingsObservation{}, ErrProjectMaintenanceTarget
	}
	return readProjectCleanupSettings(ctx, db, target)
}

// ApplyProjectCleanupSettings lowers the update/delete trigger through one
// table option. PostgreSQL readback confirms the setting. Verify later cleanup
// and protected traffic separately before reporting an improvement.
func ApplyProjectCleanupSettings(ctx context.Context, config *pgx.ConnConfig, request ProjectCleanupSettingsRequest, guards ProjectCleanupSettingsGuards) (ProjectCleanupSettingsResult, error) {
	return updateProjectCleanupSettings(ctx, config, request.Later.Target, request.RunID, guards, func(now ProjectCleanupSettingsObservation) (ProjectCleanupSettingsIntent, error) {
		return planProjectCleanupSettings(request, now)
	})
}

var errCleanupSettingsEvidence = errors.New("cleanup needs fresh increasing backlog with a stable trigger and counter generation")
var errCleanupSettingsThreshold = errors.New("measured backlog does not justify lowering this cleanup trigger")

func planProjectCleanupSettings(request ProjectCleanupSettingsRequest, now ProjectCleanupSettingsObservation) (ProjectCleanupSettingsIntent, error) {
	later, earlier := request.Later, request.Earlier
	span := later.At.Sub(earlier.At)
	if span < time.Second || span > 30*time.Second || now.At.Before(later.At) || now.At.Sub(later.At) > 30*time.Second {
		return ProjectCleanupSettingsIntent{}, fmt.Errorf("%w: observation interval", errCleanupSettingsEvidence)
	}
	if !matchingCleanupSettings(earlier, later) || !matchingCleanupSettings(later, now) || !now.AutovacuumEnabled {
		return ProjectCleanupSettingsIntent{}, fmt.Errorf("%w: settings or generation changed", errCleanupSettingsEvidence)
	}
	if earlier.DeadTuples >= later.DeadTuples || later.DeadTuples > now.DeadTuples {
		return ProjectCleanupSettingsIntent{}, fmt.Errorf("%w: backlog stopped increasing", errCleanupSettingsEvidence)
	}

	next := math.Round(math.Max(.05, now.ScaleFactor-math.Min(.05, now.ScaleFactor/4))*1e6) / 1e6
	if now.EstimatedRows <= 0 || next >= now.ScaleFactor || !nonnegativeFinite(now.EstimatedRows) || !validCleanupOption(now.ScaleOption, true) ||
		cleanupThreshold(now, next) >= float64(earlier.DeadTuples) || float64(now.DeadTuples) >= cleanupThreshold(now, now.ScaleFactor) {
		return ProjectCleanupSettingsIntent{}, errCleanupSettingsThreshold
	}
	return ProjectCleanupSettingsIntent{Target: now.Target, RunID: request.RunID,
		BeforeOption: now.ScaleOption, AfterOption: strconv.FormatFloat(next, 'f', -1, 64)}, nil
}

// RestoreProjectCleanupSettings compares the live option with the recorded
// value and restores only that option. A different live value is preserved.
func RestoreProjectCleanupSettings(ctx context.Context, config *pgx.ConnConfig, original ProjectCleanupSettingsIntent, guards ProjectCleanupSettingsGuards) (ProjectCleanupSettingsResult, error) {
	if original.Restoring || !validCleanupOption(original.AfterOption, false) || !validCleanupOption(original.BeforeOption, true) ||
		original.BeforeOption == original.AfterOption {
		return ProjectCleanupSettingsResult{}, errors.New("restore needs the original recorded cleanup change")
	}
	return updateProjectCleanupSettings(ctx, config, original.Target, original.RunID, guards, func(now ProjectCleanupSettingsObservation) (ProjectCleanupSettingsIntent, error) {
		if now.ScaleOption != original.AfterOption {
			return ProjectCleanupSettingsIntent{}, ErrProjectCleanupSettingsChanged
		}
		return ProjectCleanupSettingsIntent{Target: original.Target, RunID: original.RunID,
			BeforeOption: original.AfterOption, AfterOption: original.BeforeOption, Restoring: true}, nil
	})
}

func updateProjectCleanupSettings(ctx context.Context, config *pgx.ConnConfig, target ProjectIndexTarget, runID string, guards ProjectCleanupSettingsGuards,
	plan func(ProjectCleanupSettingsObservation) (ProjectCleanupSettingsIntent, error)) (result ProjectCleanupSettingsResult, err error) {
	if config == nil || runID == "" || len(runID) > 128 || guards.CheckPermission == nil || guards.CheckCapacity == nil || (guards.RecordIntent == nil && guards.recordObservedIntent == nil && guards.recordLockedIntent == nil) {
		return result, errors.New("cleanup setting needs an exact request, permission, capacity and durable intent")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	db, err := supabaseproject.ConnectRepair(ctx, config)
	if err != nil {
		return result, err
	}
	defer closeVacuumConnection(db)
	if err := configureProjectMaintenance(ctx, db); err != nil {
		return result, err
	}
	if _, err := db.Exec(ctx, `SET application_name='pooltune bounded cleanup trigger'`); err != nil {
		return result, err
	}
	before, err := readProjectCleanupSettings(ctx, db, target)
	if err != nil {
		return result, err
	}
	intent, err := plan(before)
	if err != nil {
		return result, err
	}
	if err := guards.CheckPermission(ctx, intent); err != nil {
		return result, err
	}
	var acquired bool
	if err := db.QueryRow(ctx, `SELECT pg_catalog.pg_try_advisory_lock($1,$2)`, projectIndexLockNamespace, int32(target.TableOID)).Scan(&acquired); err != nil {
		return result, err
	}
	if !acquired {
		return result, errors.New("another Pooltune action is active for this relation")
	}
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return result, err
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL stats_fetch_consistency=none; SET LOCAL idle_in_transaction_session_timeout='8s'`); err != nil {
		return result, err
	}
	relation := qualified(target.Schema, target.Relation)
	if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+relation+" IN SHARE UPDATE EXCLUSIVE MODE NOWAIT"); err != nil {
		return result, err
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM pg_catalog.pg_locks WHERE pid=pg_catalog.pg_backend_pid()
		AND database=$1 AND relation=$2 AND mode='ShareUpdateExclusiveLock' AND granted)`, target.DatabaseOID, target.TableOID).Scan(&acquired); err != nil {
		return result, err
	}
	if !acquired {
		return result, ErrProjectMaintenanceTarget
	}
	recheck := func() error {
		result.Before, err = readProjectCleanupSettings(ctx, tx, target)
		if err != nil {
			return err
		}
		latest, err := plan(result.Before)
		if err == nil && latest != intent {
			return ErrProjectCleanupSettingsChanged
		}
		return err
	}
	if err := recheck(); err != nil {
		return result, err
	}
	if err := guards.CheckCapacity(ctx, result.Before.RelationBytes, intent); err != nil {
		return result, err
	}
	result.Intent = intent
	if guards.recordLockedIntent != nil {
		if err := guards.recordLockedIntent(ctx, intent, result.Before, tx); err != nil {
			return result, err
		}
	} else if guards.recordObservedIntent != nil {
		if err := guards.recordObservedIntent(ctx, intent, result.Before); err != nil {
			return result, err
		}
	} else if err := guards.RecordIntent(ctx, intent); err != nil {
		return result, err
	}
	if err := guards.CheckPermission(ctx, intent); err != nil {
		return result, err
	}
	if err := recheck(); err != nil {
		return result, err
	}
	statement := `ALTER TABLE ONLY ` + relation + ` RESET (autovacuum_vacuum_scale_factor)`
	if intent.AfterOption != "" {
		// Both the generated setting and a journal-sourced previous value are decimal literals.
		if !validCleanupOption(intent.AfterOption, false) {
			return result, errors.New("invalid cleanup scale factor")
		}
		statement = `ALTER TABLE ONLY ` + relation + ` SET (autovacuum_vacuum_scale_factor='` + intent.AfterOption + `')`
	}
	result.WriteAttempted = true
	if _, err := tx.Exec(ctx, statement); err != nil {
		return result, err
	}
	result.After, err = readProjectCleanupSettings(ctx, tx, target)
	if err != nil {
		return result, err
	}
	if result.After.ScaleOption != intent.AfterOption {
		return result, errors.New("cleanup setting readback did not match the exact planned change")
	}
	if err := tx.Commit(ctx); err != nil {
		return result, err
	}
	result.Confirmed = true
	return result, nil
}

func validCleanupOption(option string, allowInherited bool) bool {
	if option == "" {
		return allowInherited
	}
	value, err := strconv.ParseFloat(option, 64)
	return err == nil && len(option) <= 32 && nonnegativeFinite(value) && value <= 100
}

func matchingCleanupSettings(a, b ProjectCleanupSettingsObservation) bool {
	return a.Target == b.Target && !a.Boot.IsZero() && a.Boot.Equal(b.Boot) && a.StatsReset.Equal(b.StatsReset) &&
		a.ScaleOption == b.ScaleOption && a.ScaleFactor == b.ScaleFactor &&
		a.EstimatedRows == b.EstimatedRows && a.BaseThreshold == b.BaseThreshold && a.MaximumThreshold == b.MaximumThreshold &&
		a.AutovacuumEnabled == b.AutovacuumEnabled && a.Cleanups == b.Cleanups
}

func cleanupThreshold(o ProjectCleanupSettingsObservation, factor float64) float64 {
	threshold := float64(o.BaseThreshold) + o.EstimatedRows*factor
	if o.MaximumThreshold >= 0 {
		return min(threshold, float64(o.MaximumThreshold))
	}
	return threshold
}

func readProjectCleanupSettings(ctx context.Context, session projectMaintenanceSession, target ProjectIndexTarget) (ProjectCleanupSettingsObservation, error) {
	o := ProjectCleanupSettingsObservation{Target: target}
	err := session.QueryRow(ctx, supabaseproject.RepairQuery(ctx, session, `SELECT pg_catalog.statement_timestamp(), pg_catalog.pg_postmaster_start_time(), COALESCE(sd.stats_reset, 'epoch'::timestamptz),
		COALESCE(opt->>'autovacuum_vacuum_scale_factor',''),
		COALESCE(NULLIF((opt->>'autovacuum_vacuum_scale_factor')::double precision,-1), pg_catalog.current_setting('autovacuum_vacuum_scale_factor')::double precision),
		COALESCE(NULLIF((opt->>'autovacuum_vacuum_threshold')::bigint,-1), pg_catalog.current_setting('autovacuum_vacuum_threshold')::bigint),
		COALESCE((opt->>'autovacuum_vacuum_max_threshold')::bigint, pg_catalog.current_setting('autovacuum_vacuum_max_threshold',true)::bigint,-1),
		c.reltuples::double precision, s.n_dead_tup, s.autovacuum_count+s.vacuum_count, pg_catalog.pg_total_relation_size(c.oid),
		s.autovacuum_count, coalesce(s.last_autovacuum,'epoch'::timestamptz), s.n_tup_upd, s.n_tup_del,
		(SELECT setting::bigint FROM pg_catalog.pg_settings WHERE name='autovacuum_naptime' AND unit='s'),
		pg_catalog.current_setting('autovacuum')='on' AND pg_catalog.current_setting('track_counts')='on' AND COALESCE((opt->>'autovacuum_enabled')::boolean,true),
		COALESCE((opt->>'autovacuum_vacuum_insert_threshold')::int, pg_catalog.current_setting('autovacuum_vacuum_insert_threshold')::int)=-1
		  AND pg_catalog.age(c.relfrozenxid) < 1000000 AND pg_catalog.mxid_age(c.relminmxid) < 1000000
	FROM pg_catalog.pg_class c
	JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
	JOIN pg_catalog.pg_am am ON am.oid=c.relam AND am.amname='heap'
	JOIN pg_catalog.pg_database d ON d.datname=pg_catalog.current_database()
	JOIN pg_catalog.pg_stat_database sd ON sd.datid=d.oid
	JOIN pg_catalog.pg_roles actor ON actor.rolname=current_user AND actor.oid=c.relowner
	JOIN pg_catalog.pg_stat_all_tables s ON s.relid=c.oid
	CROSS JOIN LATERAL (SELECT pg_catalog.jsonb_object_agg(option_name,option_value) AS opt FROM pg_catalog.pg_options_to_table(c.reloptions)) options
	WHERE d.oid=$1 AND c.oid=$2 AND n.nspname::text=$3 AND c.relname::text=$4
		AND current_user=session_user AND NOT pg_catalog.pg_is_in_recovery()
		AND c.oid>=16384 AND c.relkind='r' AND NOT c.relispartition AND c.relpersistence IN ('p','u')
		AND pg_catalog.left(n.nspname::text,3)<>'pg_' AND n.nspname<>'information_schema'
		AND pg_catalog.has_schema_privilege(n.oid,'USAGE')
		AND NOT EXISTS (SELECT FROM pg_catalog.pg_inherits i WHERE i.inhrelid=c.oid OR i.inhparent=c.oid)
		AND NOT EXISTS (SELECT FROM pg_catalog.pg_depend x WHERE x.classid='pg_catalog.pg_class'::regclass AND x.objid=c.oid AND x.deptype='e')
	`), target.DatabaseOID, target.TableOID, target.Schema, target.Relation).Scan(&o.At, &o.Boot, &o.StatsReset, &o.ScaleOption, &o.ScaleFactor,
		&o.BaseThreshold, &o.MaximumThreshold, &o.EstimatedRows, &o.DeadTuples, &o.Cleanups, &o.RelationBytes,
		&o.AutomaticCleanups, &o.LastAutovacuum, &o.Updated, &o.Deleted, &o.AutovacuumNaptimeSeconds, &o.AutovacuumEnabled, &o.OnlyDeadTupleTrigger)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (o.RelationBytes < 0 || o.RelationBytes > projectMaintenanceMaxRelationBytes) {
		return ProjectCleanupSettingsObservation{}, errors.New("cleanup setting needs the exact owner and an eligible ordinary table")
	}
	return o, err
}
