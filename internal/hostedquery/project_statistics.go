package hostedquery

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const projectStatisticsMaxTarget = 200

var ErrProjectStatisticsTarget = errors.New("statistics adjustment requires the exact supported column and direct table owner")

type ProjectStatisticsRequest struct {
	Target     ProjectIndexTarget
	RunID      string
	ObservedAt time.Time
	Column     int16
	NextTarget int32
}

type ProjectStatisticsSetting struct {
	// UsesDefault preserves an unset target. Value is meaningful for an explicit target.
	UsesDefault bool
	Value       int32
}

type ProjectStatisticsIntent struct {
	ProjectStatisticsRequest
	Key            ProjectIndexKey
	Prior          ProjectStatisticsSetting
	PriorEffective int32
	RelationBytes  int64
}

type ProjectStatisticsGuards struct {
	// CheckPermission checks the live owner grant for this action. The caller
	// must coordinate schema DDL since PostgreSQL addresses ANALYZE by name.
	CheckPermission func(context.Context, ProjectStatisticsIntent) error
	CheckCapacity   func(context.Context, ProjectStatisticsIntent) error
	// RecordIntent succeeds only after saving the exact previous and next settings.
	RecordIntent func(context.Context, ProjectStatisticsIntent) error
}

type ProjectStatisticsResult struct {
	WriteAttempted  bool
	Confirmed       bool
	Restored        bool
	Prior, Current  ProjectStatisticsSetting
	AnalyzeDuration time.Duration
	phase           string
}

type projectStatisticsTrial struct {
	recheck func(context.Context, pgx.Tx, ProjectStatisticsIntent) error
	verify  func(context.Context, pgx.Tx, ProjectStatisticsIntent) (bool, error)
}

// ApplyProjectStatisticsTarget changes one already authorized integer column and
// runs column-scoped ANALYZE in the same transaction. Confirmed is a database
// readback, so callers still need workload observations to establish benefit.
// An error after WriteAttempted requires checking the recorded intent before retry.
func ApplyProjectStatisticsTarget(ctx context.Context, config *pgx.ConnConfig, request ProjectStatisticsRequest, guards ProjectStatisticsGuards) (result ProjectStatisticsResult, err error) {
	return applyProjectStatisticsTarget(ctx, config, request, guards, projectStatisticsTrial{})
}

func applyProjectStatisticsTarget(ctx context.Context, config *pgx.ConnConfig, request ProjectStatisticsRequest, guards ProjectStatisticsGuards,
	trial projectStatisticsTrial,
) (result ProjectStatisticsResult, err error) {
	result.phase = "prepare_write"
	if config == nil || guards.CheckPermission == nil || guards.CheckCapacity == nil || guards.RecordIntent == nil ||
		request.RunID == "" || len(request.RunID) > 128 || !freshStatisticsRequest(request, time.Now()) ||
		request.NextTarget < 1 || request.NextTarget > projectStatisticsMaxTarget {
		return result, errors.New("statistics adjustment needs a fresh bounded target, permissions, capacity and durable intent")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	db, err := pgx.ConnectConfig(ctx, config.Copy())
	if err != nil {
		return result, err
	}
	defer closeVacuumConnection(db)
	var pid, version int32
	if err := db.QueryRow(ctx, `SELECT pg_catalog.pg_backend_pid(), pg_catalog.current_setting('server_version_num')::int`).Scan(&pid, &version); err != nil {
		return result, err
	}
	if pid != int32(db.PgConn().PID()) || version < 170000 {
		return result, errors.New("statistics adjustment requires a native PostgreSQL 17 or later session")
	}
	catalog, err := ResolveProjectIndex(ctx, db, request.Target, request.Column, nil)
	if err != nil {
		return result, err
	}
	key := catalog.Keys[0]

	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return result, err
	}
	defer func() {
		cleanup, finish := context.WithTimeout(context.Background(), time.Second)
		defer finish()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, `SET LOCAL search_path=pg_catalog; SET LOCAL lock_timeout='150ms';
		SET LOCAL statement_timeout='2s'; SET LOCAL transaction_timeout='8s'; SET LOCAL idle_in_transaction_session_timeout='2s';
		SET LOCAL maintenance_work_mem='16MB'; SET LOCAL max_parallel_maintenance_workers=0`); err != nil {
		return result, err
	}
	if _, err := readProjectStatisticsState(ctx, tx, request.Target, key, false); err != nil {
		return result, err
	}
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.pg_try_advisory_xact_lock($1,$2)`, projectIndexLockNamespace, int32(request.Target.TableOID)).Scan(&locked); err != nil {
		return result, err
	}
	if !locked {
		return result, errors.New("another Pooltune action is active for this relation")
	}
	relation := qualified(request.Target.Schema, request.Target.Relation)
	if _, err := tx.Exec(ctx, "LOCK TABLE ONLY "+relation+" IN SHARE UPDATE EXCLUSIVE MODE NOWAIT"); err != nil {
		return result, err
	}
	prior, err := readProjectStatisticsState(ctx, tx, request.Target, key, true)
	if err != nil {
		return result, err
	}
	result.Prior = prior.setting
	if !boundedProjectStatisticsTarget(prior.effective, request.NextTarget) {
		return result, errors.New("requested statistics target is outside the bounded increase")
	}
	intent := ProjectStatisticsIntent{ProjectStatisticsRequest: request, Key: key, Prior: prior.setting,
		PriorEffective: prior.effective, RelationBytes: prior.relationBytes}
	result.phase = "check_permission_and_capacity"
	for _, guard := range []func(context.Context, ProjectStatisticsIntent) error{guards.CheckPermission, guards.CheckCapacity} {
		if err := guard(ctx, intent); err != nil {
			return result, err
		}
	}
	if trial.recheck != nil {
		result.phase = "recheck_measurements"
		if err := trial.recheck(ctx, tx, intent); err != nil {
			return result, err
		}
	}
	result.phase = "record_intent"
	if err := guards.RecordIntent(ctx, intent); err != nil {
		return result, err
	}
	result.phase = "permission_before_write"
	if err := guards.CheckPermission(ctx, intent); err != nil {
		return result, err
	}
	result.phase = "catalog_before_write"
	current, err := readProjectStatisticsState(ctx, tx, request.Target, key, true)
	if err != nil {
		return result, err
	}
	if current.setting != prior.setting || current.effective != prior.effective || current.relationBytes > prior.relationBytes ||
		!freshStatisticsRequest(request, time.Now()) || ctx.Err() != nil {
		return result, errors.New("statistics target or admitted capacity changed before execution")
	}

	column := pgx.Identifier{key.Name}.Sanitize()
	result.phase = "alter_target"
	result.WriteAttempted = true
	if _, err := tx.Exec(ctx, "ALTER TABLE ONLY "+relation+" ALTER COLUMN "+column+" SET STATISTICS "+strconv.FormatInt(int64(request.NextTarget), 10)); err != nil {
		return result, err
	}
	result.phase = "analyze_column"
	started := time.Now()
	if _, err := tx.Exec(ctx, `ANALYZE (BUFFER_USAGE_LIMIT '256kB') `+relation+" ("+column+")"); err != nil {
		return result, err
	}
	result.AnalyzeDuration = time.Since(started)
	result.phase = "catalog_after_write"
	current, err = readProjectStatisticsState(ctx, tx, request.Target, key, true)
	if err != nil {
		return result, err
	}
	var hasSample bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM pg_catalog.pg_stats WHERE schemaname=$1 AND tablename=$2
		AND attname=$3 AND NOT inherited)`, request.Target.Schema, request.Target.Relation, key.Name).Scan(&hasSample); err != nil {
		return result, err
	}
	if current.setting != (ProjectStatisticsSetting{Value: request.NextTarget}) || !hasSample {
		return result, errors.New("statistics adjustment did not pass exact catalog readback")
	}
	if trial.verify != nil {
		result.phase = "verify_measurements"
		retain, err := trial.verify(ctx, tx, intent)
		if err != nil || !retain {
			cleanup, finish := context.WithTimeout(context.Background(), time.Second)
			defer finish()
			if rollback := tx.Rollback(cleanup); rollback != nil {
				result.phase = "rollback"
				return result, errors.Join(err, rollback)
			}
			// Rolling back the action transaction also restores the planner's
			// prior sample. Another ANALYZE would create a different sample.
			result.Current, result.Confirmed, result.Restored = prior.setting, true, true
			return result, err
		}
	}
	result.phase = "commit"
	if err := tx.Commit(ctx); err != nil {
		return result, err
	}
	result.Current, result.Confirmed = current.setting, true
	return result, nil
}

func boundedProjectStatisticsTarget(prior, next int32) bool {
	return prior > 0 && next > prior && next <= projectStatisticsMaxTarget &&
		next <= prior*2 && next-prior <= 100
}

func freshStatisticsRequest(request ProjectStatisticsRequest, now time.Time) bool {
	return !request.ObservedAt.IsZero() && !request.ObservedAt.After(now) && now.Sub(request.ObservedAt) <= 30*time.Second
}

type projectStatisticsState struct {
	setting       ProjectStatisticsSetting
	effective     int32
	relationBytes int64
}

func readProjectStatisticsState(ctx context.Context, tx pgx.Tx, target ProjectIndexTarget, key ProjectIndexKey, locked bool) (projectStatisticsState, error) {
	var state projectStatisticsState
	var configured *int32
	err := tx.QueryRow(ctx, projectStatisticsStateSQL, target.DatabaseOID, target.TableOID, target.Schema, target.Relation,
		key.Number, key.Name, locked).Scan(&configured, &state.effective, &state.relationBytes)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (state.relationBytes <= 0 || state.relationBytes > projectIndexMaxRelationBytes)) {
		return state, ErrProjectStatisticsTarget
	}
	if err != nil {
		return state, err
	}
	if configured == nil {
		state.setting.UsesDefault = true
	} else {
		state.setting.Value = *configured
	}
	return state, nil
}

const projectStatisticsStateSQL = `
	SELECT a.attstattarget::integer, COALESCE(a.attstattarget::integer, pg_catalog.current_setting('default_statistics_target')::integer),
		pg_catalog.pg_total_relation_size(c.oid)
	FROM pg_catalog.pg_class c
	JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
	JOIN pg_catalog.pg_roles r ON r.oid=c.relowner AND r.rolname=current_user
	JOIN pg_catalog.pg_database d ON d.datname=pg_catalog.current_database()
	JOIN pg_catalog.pg_am am ON am.oid=c.relam AND am.amname='heap'
	JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum=$5 AND a.attname::text=$6
	WHERE d.oid=$1 AND c.oid=$2 AND n.nspname::text=$3 AND c.relname::text=$4
		AND current_user=session_user AND NOT pg_catalog.pg_is_in_recovery() AND pg_catalog.current_setting('transaction_read_only')='off'
		AND c.oid>=16384 AND c.relkind='r' AND c.relpersistence='p' AND NOT c.relispartition
		AND NOT pg_catalog.starts_with(n.nspname::text,'pg_') AND NOT pg_catalog.starts_with(n.nspname::text,'pt_monitor_')
		AND n.nspname<>'information_schema' AND pg_catalog.has_schema_privilege(n.oid,'USAGE')
		AND a.attnum>0 AND NOT a.attisdropped AND a.attgenerated='' AND a.atttypid IN ('int2'::regtype,'int4'::regtype,'int8'::regtype)
		AND pg_catalog.pg_relation_size(c.oid)>0
		AND NOT EXISTS (SELECT FROM pg_catalog.pg_inherits i WHERE i.inhrelid=c.oid OR i.inhparent=c.oid)
		AND NOT EXISTS (SELECT FROM pg_catalog.pg_depend x WHERE x.classid='pg_catalog.pg_class'::regclass AND x.objid=c.oid AND x.deptype='e')
		AND NOT EXISTS (SELECT FROM pg_catalog.pg_statistic_ext x WHERE x.stxrelid=c.oid AND x.stxexprs IS NOT NULL)
		AND NOT EXISTS (
			SELECT FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class x ON x.oid=i.indexrelid
			JOIN pg_catalog.pg_am m ON m.oid=x.relam JOIN pg_catalog.pg_proc p ON p.oid=m.amhandler
			WHERE i.indrelid=c.oid AND (i.indexprs IS NOT NULL OR i.indpred IS NOT NULL OR p.pronamespace<>'pg_catalog'::regnamespace)
		)
		AND (NOT $7::boolean OR EXISTS (
			SELECT FROM pg_catalog.pg_locks l WHERE l.pid=pg_catalog.pg_backend_pid() AND l.database=d.oid AND l.relation=c.oid
				AND l.mode='ShareUpdateExclusiveLock' AND l.granted
		))
`
