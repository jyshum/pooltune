package hostedquery

import (
	"context"
	"errors"
	"slices"
	"time"

	"pooltune/internal/supabaseproject"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ProjectRepairAccessScope contains the owner's selected capabilities and
// exact database targets. A preflight result alone never authorizes an action.
type ProjectRepairAccessScope struct {
	Project      string                       `json:"project"`
	Capabilities []string                     `json:"capabilities"`
	Table        *ProjectIndexTarget          `json:"table,omitempty"`
	Function     *ProjectFunctionMemoryTarget `json:"function,omitempty"`
	Cron         *ProjectCronTarget           `json:"cron,omitempty"`
	CronWindow   *ProjectCronWindowPlan       `json:"cron_window,omitempty"`
}

type ProjectRepairAccessReport struct {
	Project    string                `json:"project"`
	Operations []ProjectRepairAccess `json:"operations"`
}

type ProjectRepairAccess struct {
	Operation   string `json:"operation"`
	Capability  string `json:"capability"`
	Requirement string `json:"database_requirement"`
	Status      string `json:"status"`
}

const (
	repairAccessGranted      = "granted"
	repairAccessMissing      = "database_permission_missing"
	repairAccessTargetNeeded = "target_required"
	repairAccessChanged      = "target_changed"
)

var projectRepairAccessOperations = [...]ProjectRepairAccess{
	{Operation: "index", Capability: "index", Requirement: "Table-owner login"},
	{Operation: "analyze", Capability: "analyze", Requirement: "Table owner or direct MAINTAIN on this table"},
	{Operation: "joint_statistics", Capability: "statistics", Requirement: "Table-owner login"},
	{Operation: "column_statistics", Capability: "statistics", Requirement: "Table-owner login"},
	{Operation: "vacuum", Capability: "vacuum", Requirement: "Table owner or direct MAINTAIN on this table"},
	{Operation: "cleanup_trigger", Capability: "vacuum", Requirement: "Table-owner login"},
	{Operation: "function_memory", Capability: "memory", Requirement: "Enrolled function-owner login"},
	{Operation: "cron", Capability: "cron", Requirement: "Enrolled job-owner login, cron.alter_job, and approved schedule window"},
}

// ReadProjectRepairAccess checks live native permissions for a caller-verified
// connection. It issues catalog reads only. The dispatch fence, evidence,
// capacity, and action-time checks must still run before a database change.
func ReadProjectRepairAccess(ctx context.Context, db *pgx.Conn, project supabaseproject.Project, scope ProjectRepairAccessScope) (ProjectRepairAccessReport, error) {
	if db == nil || scope.Project != project.ID() {
		return ProjectRepairAccessReport{}, errors.New("repair scope must match the connected project")
	}
	for _, capability := range scope.Capabilities {
		if !slices.ContainsFunc(projectRepairAccessOperations[:], func(op ProjectRepairAccess) bool { return op.Capability == capability }) {
			return ProjectRepairAccessReport{}, errors.New("unknown repair capability")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var pid int32
	var native bool
	err := db.QueryRow(ctx, `SELECT pg_catalog.pg_backend_pid(), current_user=session_user
		AND NOT pg_catalog.pg_is_in_recovery() AND pg_catalog.current_setting('transaction_read_only')='off'`).Scan(&pid, &native)
	if err != nil || !native || pid != int32(db.PgConn().PID()) {
		return ProjectRepairAccessReport{}, errors.New("repair access requires a native, writable PostgreSQL session using its login role")
	}
	if err := project.VerifyRepairInstallation(ctx, db); err != nil {
		return ProjectRepairAccessReport{}, err
	}

	var tableStatus string
	var tableOwner, maintainer bool
	if scope.Table != nil && slices.ContainsFunc(scope.Capabilities, func(capability string) bool {
		return capability == "index" || capability == "analyze" || capability == "statistics" || capability == "vacuum"
	}) {
		tableOwner, maintainer, err = readRepairTableAccess(ctx, db, *scope.Table)
		if errors.Is(err, pgx.ErrNoRows) {
			tableStatus = repairAccessChanged
			err = nil
		} else if err != nil {
			return ProjectRepairAccessReport{}, err
		}
	}

	report := ProjectRepairAccessReport{Project: project.ID(), Operations: make([]ProjectRepairAccess, 0, len(projectRepairAccessOperations))}
	for _, operation := range projectRepairAccessOperations {
		switch {
		case !slices.Contains(scope.Capabilities, operation.Capability):
			operation.Status = "not_selected"
		case operation.Operation == "function_memory":
			operation.Status, err = readRepairFunctionAccess(ctx, db, project, scope.Function)
		case operation.Operation == "cron":
			operation.Status, err = readRepairCronAccess(ctx, db, project, scope.Cron, scope.CronWindow)
		case scope.Table == nil:
			operation.Status = repairAccessTargetNeeded
		case tableStatus != "":
			operation.Status = tableStatus
		case tableOwner || maintainer && (operation.Operation == "analyze" || operation.Operation == "vacuum"):
			operation.Status = repairAccessGranted
		default:
			operation.Status = repairAccessMissing
		}
		if err != nil {
			return ProjectRepairAccessReport{}, err
		}
		report.Operations = append(report.Operations, operation)
	}
	return report, nil
}

func readRepairTableAccess(ctx context.Context, db *pgx.Conn, table ProjectIndexTarget) (owner, maintain bool, err error) {
	err = db.QueryRow(ctx, `SELECT c.relowner=r.oid AND pg_catalog.has_schema_privilege(n.oid,'USAGE'),
			NOT r.rolsuper AND pg_catalog.current_setting('server_version_num')::int>=170000
			AND pg_catalog.has_schema_privilege(n.oid,'USAGE') AND EXISTS (
				SELECT FROM pg_catalog.aclexplode(c.relacl) a WHERE a.grantee=r.oid AND a.privilege_type='MAINTAIN'
			)
		FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		JOIN pg_catalog.pg_database d ON d.datname=pg_catalog.current_database()
		JOIN pg_catalog.pg_roles r ON r.rolname=current_user
		WHERE d.oid=$1 AND c.oid=$2 AND n.nspname::text=$3 AND c.relname::text=$4
		  AND c.oid>=16384 AND c.relkind='r' AND NOT c.relispartition AND c.relpersistence IN ('p','u')
		  AND pg_catalog.left(n.nspname::text,3)<>'pg_' AND n.nspname<>'information_schema'
		  AND NOT EXISTS (SELECT FROM pg_catalog.pg_inherits i WHERE i.inhrelid=c.oid OR i.inhparent=c.oid)
		  AND NOT EXISTS (SELECT FROM pg_catalog.pg_depend x WHERE x.classid='pg_catalog.pg_class'::regclass AND x.objid=c.oid AND x.deptype='e')`,
		table.DatabaseOID, table.TableOID, table.Schema, table.Relation).Scan(&owner, &maintain)
	return owner, maintain, err
}

func readRepairFunctionAccess(ctx context.Context, db *pgx.Conn, project supabaseproject.Project, target *ProjectFunctionMemoryTarget) (string, error) {
	if target == nil {
		return repairAccessTargetNeeded, nil
	}
	if target.Project != project.ID() {
		return repairAccessChanged, nil
	}
	_, err := ReadProjectFunctionMemory(ctx, db, *target)
	return nativeRepairAccessStatus(err)
}

func readRepairCronAccess(ctx context.Context, db *pgx.Conn, project supabaseproject.Project, target *ProjectCronTarget, window *ProjectCronWindowPlan) (string, error) {
	if target == nil || window == nil {
		return repairAccessTargetNeeded, nil
	}
	if target.Project != project.ID() || !validProjectCronTarget(*target) {
		return repairAccessChanged, nil
	}
	original, _, err := window.schedules()
	if err != nil {
		return "cron_window_mismatch", nil
	}
	var mayAlter bool
	err = db.QueryRow(ctx, `SELECT COALESCE(pg_catalog.has_function_privilege(current_user,
		pg_catalog.to_regprocedure('cron.alter_job(bigint,text,text,text,text,boolean)'),'EXECUTE'),false)`).Scan(&mayAlter)
	if err != nil {
		return nativeRepairAccessStatus(err)
	}
	if !mayAlter {
		return repairAccessMissing, nil
	}
	snapshot, _, err := readProjectCronPair(ctx, db, *target)
	if err != nil {
		return nativeRepairAccessStatus(err)
	}
	if snapshot.Schedule != original {
		return "cron_window_mismatch", nil
	}
	return repairAccessGranted, nil
}

func nativeRepairAccessStatus(err error) (string, error) {
	if err == nil {
		return repairAccessGranted, nil
	}
	var pgErr *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &pgErr) && slices.Contains([]string{"42501", "3F000", "42P01", "42883"}, pgErr.Code) {
		return repairAccessMissing, nil
	}
	return "", err
}
