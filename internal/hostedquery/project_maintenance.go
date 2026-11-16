package hostedquery

import (
	"context"
	"errors"
	"pooltune/internal/supabaseproject"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type ProjectMaintenanceOperation string

const (
	ProjectAnalyze                     ProjectMaintenanceOperation = "analyze"
	ProjectVacuum                      ProjectMaintenanceOperation = "vacuum"
	projectMaintenanceMaxRelationBytes                             = 24 << 20
)

type ProjectMaintenanceRequest struct {
	Target     ProjectIndexTarget
	RunID      string
	Operation  ProjectMaintenanceOperation
	ObservedAt time.Time
	// Columns contains exact attribute numbers for ANALYZE. An empty list
	// selects the eligible table as a whole.
	Columns []int16
}

type ProjectMaintenanceIntent struct {
	ProjectMaintenanceRequest
	RelationBytes int64
}

type ProjectMaintenanceGuards struct {
	// CheckPermission checks the live, operation-specific owner grant. The
	// caller must coordinate schema renames and drops through the action's
	// return. PostgreSQL addresses both maintenance commands by schema name.
	CheckPermission func(context.Context, ProjectMaintenanceIntent) error
	CheckCapacity   func(context.Context, ProjectMaintenanceIntent) error
	// RecordIntent succeeds only after durably recording the exact operation.
	RecordIntent func(context.Context, ProjectMaintenanceIntent) error
}

type ProjectMaintenanceObservation struct {
	At, LastRun       time.Time
	Count, DeadTuples int64
}

type ProjectMaintenanceResult struct {
	WriteAttempted bool
	Confirmed      bool
	Before, After  ProjectMaintenanceObservation
}

type projectMaintenanceSession interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

var ErrProjectMaintenanceTarget = errors.New("maintenance needs the exact eligible relation and its owner or direct MAINTAIN grant")

// ApplyProjectMaintenance consumes an already investigated, bound action from
// the caller. Confirmed reports PostgreSQL's maintenance counters. These counters
// measure the operation itself. Verification of query benefit is separate.
// Errors after WriteAttempted require inspecting the durable intent before retry.
func ApplyProjectMaintenance(ctx context.Context, config *pgx.ConnConfig, request ProjectMaintenanceRequest, guards ProjectMaintenanceGuards) (result ProjectMaintenanceResult, err error) {
	if config == nil || guards.CheckPermission == nil || guards.CheckCapacity == nil || guards.RecordIntent == nil ||
		!validProjectMaintenance(request, time.Now()) {
		return result, errors.New("maintenance needs a fresh exact request, permissions, capacity and durable intent")
	}
	request.Columns = slices.Clone(request.Columns)
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	actionConfig := config.Copy()
	previousNoticeHandler := actionConfig.OnNotice
	var warned bool
	actionConfig.OnNotice = func(conn *pgconn.PgConn, notice *pgconn.Notice) {
		if notice.Severity == "WARNING" || notice.SeverityUnlocalized == "WARNING" {
			warned = true
		}
		if previousNoticeHandler != nil {
			previousNoticeHandler(conn, notice)
		}
	}
	db, err := supabaseproject.ConnectRepair(ctx, actionConfig)
	if err != nil {
		return result, err
	}
	defer closeVacuumConnection(db)
	if err := configureProjectMaintenance(ctx, db); err != nil {
		return result, err
	}
	bytes, initialColumns, err := readProjectMaintenanceTarget(ctx, db, request)
	if err != nil {
		return result, err
	}
	intent := ProjectMaintenanceIntent{ProjectMaintenanceRequest: request, RelationBytes: bytes}
	check := func(guard func(context.Context, ProjectMaintenanceIntent) error) error {
		current := intent
		current.Columns = slices.Clone(intent.Columns)
		return guard(ctx, current)
	}
	if err := check(guards.CheckPermission); err != nil {
		return result, err
	}
	var acquired bool
	// Share the table lock key with the index action. Closing db releases it.
	if err := db.QueryRow(ctx, `SELECT pg_catalog.pg_try_advisory_lock($1,$2)`, projectIndexLockNamespace, int32(request.Target.TableOID)).Scan(&acquired); err != nil {
		return result, err
	}
	if !acquired {
		return result, errors.New("another Pooltune action is active for this relation")
	}

	lockDB, lockMode := db, "ShareUpdateExclusiveLock"
	lockSQL := " IN SHARE UPDATE EXCLUSIVE MODE NOWAIT"
	if request.Operation == ProjectVacuum {
		// VACUUM starts its own transactions. This pin protects the heap's OID
		// while allowing both its maintenance lock and ordinary app writes.
		lockDB, err = supabaseproject.ConnectRepair(ctx, config)
		if err != nil {
			return result, err
		}
		defer closeVacuumConnection(lockDB)
		lockMode, lockSQL = "AccessShareLock", " IN ACCESS SHARE MODE NOWAIT"
	}
	lockTx, err := lockDB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return result, err
	}
	defer func() {
		cleanup, finish := context.WithTimeout(context.Background(), time.Second)
		defer finish()
		_ = lockTx.Rollback(cleanup)
	}()
	if _, err := lockTx.Exec(ctx, `SET LOCAL statement_timeout='750ms'; SET LOCAL idle_in_transaction_session_timeout='12s'`); err != nil {
		return result, err
	}
	relation := qualified(request.Target.Schema, request.Target.Relation)
	if _, err := lockTx.Exec(ctx, "LOCK TABLE ONLY "+relation+lockSQL); err != nil {
		return result, err
	}
	var session projectMaintenanceSession = db
	if request.Operation == ProjectAnalyze {
		session = lockTx
		if _, err := lockTx.Exec(ctx, `SET LOCAL statement_timeout='3s'`); err != nil {
			return result, err
		}
	}
	checkPin := func() error {
		var pinned bool
		err := session.QueryRow(ctx, `SELECT EXISTS (SELECT FROM pg_catalog.pg_locks
			WHERE pid=$1 AND database=$2 AND relation=$3 AND mode=$4 AND granted)`,
			int32(lockDB.PgConn().PID()), request.Target.DatabaseOID, request.Target.TableOID, lockMode).Scan(&pinned)
		if err == nil && !pinned {
			return ErrProjectMaintenanceTarget
		}
		return err
	}
	if err := checkPin(); err != nil {
		return result, err
	}
	bytes, columns, err := readProjectMaintenanceTarget(ctx, session, request)
	if err != nil {
		return result, err
	}
	if !slices.Equal(initialColumns, columns) {
		return result, ErrProjectMaintenanceTarget
	}
	intent.RelationBytes = bytes
	for _, guard := range []func(context.Context, ProjectMaintenanceIntent) error{guards.CheckCapacity, guards.RecordIntent, guards.CheckPermission} {
		if err := check(guard); err != nil {
			return result, err
		}
	}
	if err := checkPin(); err != nil {
		return result, err
	}
	if _, current, err := readProjectMaintenanceTarget(ctx, session, request); err != nil || !slices.Equal(current, columns) {
		if err != nil {
			return result, err
		}
		return result, ErrProjectMaintenanceTarget
	}
	result.Before, err = observeProjectMaintenance(ctx, session, request)
	if err != nil {
		return result, err
	}
	if !validProjectMaintenance(request, time.Now()) || ctx.Err() != nil {
		return result, errors.New("maintenance evidence expired before execution")
	}
	statement := `VACUUM (PROCESS_TOAST FALSE, TRUNCATE FALSE, PARALLEL 0, SKIP_DATABASE_STATS TRUE, BUFFER_USAGE_LIMIT '256kB') ` + relation
	if request.Operation == ProjectAnalyze {
		statement = `ANALYZE (BUFFER_USAGE_LIMIT '256kB') ` + relation
		if len(request.Columns) > 0 {
			names := make([]string, len(columns))
			for i, column := range columns {
				names[i] = pgx.Identifier{column}.Sanitize()
			}
			statement += " (" + strings.Join(names, ", ") + ")"
		}
	}
	warned = false
	result.WriteAttempted = true
	if _, err := session.Exec(ctx, statement); err != nil {
		return result, err
	}
	if warned {
		return result, errors.New("PostgreSQL issued a warning during maintenance; action requires readback")
	}
	if request.Operation == ProjectAnalyze {
		if err := lockTx.Commit(ctx); err != nil {
			return result, err
		}
	}
	if _, err := db.Exec(ctx, `SELECT pg_catalog.pg_stat_force_next_flush()`); err != nil {
		return result, err
	}
	if _, current, err := readProjectMaintenanceTarget(ctx, db, request); err != nil || !slices.Equal(current, columns) {
		if err != nil {
			return result, err
		}
		return result, ErrProjectMaintenanceTarget
	}
	result.After, err = observeProjectMaintenance(ctx, db, request)
	if err != nil {
		return result, err
	}
	if result.After.Count != result.Before.Count+1 || result.After.LastRun.Before(result.Before.At) || result.After.LastRun.After(result.After.At) {
		return result, errors.New("PostgreSQL did not confirm exactly one maintenance operation")
	}
	result.Confirmed = true
	return result, nil
}

func validProjectMaintenance(r ProjectMaintenanceRequest, now time.Time) bool {
	if r.Target.TableOID == 0 || r.Target.DatabaseOID == 0 || r.Target.Schema == "" || r.Target.Relation == "" ||
		r.RunID == "" || len(r.RunID) > 128 || r.ObservedAt.IsZero() || r.ObservedAt.After(now) || now.Sub(r.ObservedAt) > 30*time.Second {
		return false
	}
	if r.Operation == ProjectVacuum {
		return len(r.Columns) == 0
	}
	if r.Operation != ProjectAnalyze || len(r.Columns) > 4 {
		return false
	}
	for i, column := range r.Columns {
		if column <= 0 || slices.Contains(r.Columns[:i], column) {
			return false
		}
	}
	return true
}

func configureProjectMaintenance(ctx context.Context, db *pgx.Conn) error {
	var pid int32
	var supported bool
	if err := db.QueryRow(ctx, `SELECT pg_catalog.pg_backend_pid(),
		pg_catalog.current_setting('server_version_num')::int >= 170000 AND pg_catalog.current_setting('track_counts')='on'
		AND pg_catalog.has_function_privilege('pg_catalog.pg_stat_force_next_flush()', 'EXECUTE')`).Scan(&pid, &supported); err != nil {
		return err
	}
	if !supported || pid != int32(db.PgConn().PID()) {
		return errors.New("maintenance requires a native PostgreSQL 17+ session with statistics readback")
	}
	_, err := db.Exec(ctx, `SET search_path=pg_catalog; SET client_min_messages=warning; SET lock_timeout='250ms'; SET statement_timeout='3s';
		SET maintenance_work_mem='16MB'; SET max_parallel_maintenance_workers=0;
		SET vacuum_cost_delay='2ms'; SET vacuum_cost_limit=200; SET default_statistics_target=100`)
	return err
}

func readProjectMaintenanceTarget(ctx context.Context, session projectMaintenanceSession, r ProjectMaintenanceRequest) (int64, []string, error) {
	var bytes int64
	var columns []string
	err := session.QueryRow(ctx, supabaseproject.RepairQuery(ctx, session, projectMaintenanceTargetSQL), r.Target.DatabaseOID, r.Target.TableOID, r.Target.Schema, r.Target.Relation,
		string(r.Operation), r.Columns).Scan(&bytes, &columns)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, ErrProjectMaintenanceTarget
	}
	if err != nil {
		return 0, nil, err
	}
	if bytes < 0 || bytes > projectMaintenanceMaxRelationBytes || r.Operation == ProjectAnalyze &&
		(len(columns) == 0 || len(columns) > 64 || len(r.Columns) > 0 && len(columns) != len(r.Columns)) {
		return 0, nil, ErrProjectMaintenanceTarget
	}
	return bytes, columns, nil
}

const projectMaintenanceTargetSQL = `
	WITH target AS (
		SELECT c.oid, c.relacl, c.relowner, r.oid AS actor, r.rolsuper
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		JOIN pg_catalog.pg_am am ON am.oid=c.relam AND am.amname='heap'
		JOIN pg_catalog.pg_database d ON d.datname=pg_catalog.current_database()
		JOIN pg_catalog.pg_roles r ON r.rolname=current_user
		WHERE d.oid=$1 AND c.oid=$2 AND n.nspname::text=$3 AND c.relname::text=$4
			AND current_user=session_user AND NOT pg_catalog.pg_is_in_recovery()
			AND pg_catalog.current_setting('transaction_read_only')='off'
			AND c.oid >= 16384 AND c.relkind='r' AND NOT c.relispartition AND c.relpersistence IN ('p','u')
			AND pg_catalog.left(n.nspname::text, 3) <> 'pg_' AND n.nspname <> 'information_schema'
			AND pg_catalog.has_schema_privilege(n.oid,'USAGE')
			AND NOT EXISTS (SELECT FROM pg_catalog.pg_inherits i WHERE i.inhrelid=c.oid OR i.inhparent=c.oid)
			AND NOT EXISTS (SELECT FROM pg_catalog.pg_depend x WHERE x.classid='pg_catalog.pg_class'::regclass AND x.objid=c.oid AND x.deptype='e')
	), columns AS (
		SELECT a.attnum, a.attname, a.atttypid, a.attstattarget
		FROM target t JOIN pg_catalog.pg_attribute a ON a.attrelid=t.oid
		WHERE $5='analyze' AND a.attnum>0 AND NOT a.attisdropped
			AND (COALESCE(pg_catalog.cardinality($6::smallint[]),0)=0 OR a.attnum=ANY($6))
	)
	SELECT pg_catalog.pg_total_relation_size(t.oid), ARRAY(SELECT attname::text FROM columns ORDER BY attnum)
	FROM target t
	WHERE (t.relowner=t.actor OR NOT t.rolsuper AND EXISTS (
		SELECT FROM pg_catalog.aclexplode(t.relacl) a WHERE a.grantee=t.actor AND a.privilege_type='MAINTAIN'
	)) AND pg_catalog.has_table_privilege(t.oid,'MAINTAIN')
	AND NOT EXISTS (
		SELECT FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class x ON x.oid=i.indexrelid
		JOIN pg_catalog.pg_am m ON m.oid=x.relam
		JOIN pg_catalog.pg_proc handler ON handler.oid=m.amhandler
		WHERE i.indrelid=t.oid AND (handler.pronamespace <> 'pg_catalog'::regnamespace OR ($5='analyze' AND i.indexprs IS NOT NULL))
	)
	AND ($5 <> 'analyze' OR (
		NOT EXISTS (SELECT FROM pg_catalog.pg_statistic_ext s WHERE s.stxrelid=t.oid AND s.stxexprs IS NOT NULL)
		AND NOT EXISTS (SELECT FROM columns WHERE attstattarget=0 OR attstattarget>1000
			OR atttypid NOT IN ('int2'::regtype,'int4'::regtype,'int8'::regtype,'float4'::regtype,'float8'::regtype,
			'bool'::regtype,'numeric'::regtype,'text'::regtype,'varchar'::regtype,'uuid'::regtype,'date'::regtype,
			'timestamp'::regtype,'timestamptz'::regtype,'json'::regtype,'jsonb'::regtype,'bytea'::regtype))
	))
`

func observeProjectMaintenance(ctx context.Context, session projectMaintenanceSession, r ProjectMaintenanceRequest) (ProjectMaintenanceObservation, error) {
	var observation ProjectMaintenanceObservation
	var lastRun *time.Time
	err := session.QueryRow(ctx, `SELECT pg_catalog.statement_timestamp(),
		CASE WHEN $2='analyze' THEN last_analyze ELSE last_vacuum END,
		CASE WHEN $2='analyze' THEN analyze_count ELSE vacuum_count END, n_dead_tup
		FROM pg_catalog.pg_stat_all_tables WHERE relid=$1`, r.Target.TableOID, string(r.Operation)).Scan(
		&observation.At, &lastRun, &observation.Count, &observation.DeadTuples)
	if lastRun != nil {
		observation.LastRun = *lastRun
	}
	return observation, err
}
