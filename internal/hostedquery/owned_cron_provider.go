package hostedquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

type OwnedCronRuntime struct {
	Schema            string
	FunctionOID       uint32
	OwnerOID          uint32
	DefinitionDigest  string
	CounterViewOID    uint32
	CounterViewDigest string
}

const ownedCronRuntimeName = "scheduler_status"
const ownedCronRuntimePrefix = "pt_cron_guard_"

// OwnedCronFixtureRoutinesSQL renders the existing bounded invoker routines for
// the marked disposable hosted fixture. Installation stays with its operator.
func OwnedCronFixtureRoutinesSQL(installation string, expires time.Time) (string, error) {
	project, err := supabaseproject.New(OwnedIndexRookeryRef, installation)
	if err != nil || expires.IsZero() {
		return "", errors.New("Cron routines require one owned Rookery installation and expiration")
	}
	return strings.Join(cronRoutineCommands(project.OwnedRepairProbeRole("cron"), expires), ";\n") + ";\n", nil
}

// OwnedCronProviderSQL creates the approved fixed, temporary read for one owned
// Rookery fixture. The fixture operator installs it and removes it after the test.
func OwnedCronProviderSQL(installation string, expires time.Time) (string, error) {
	project, err := supabaseproject.New(OwnedIndexRookeryRef, installation)
	if err != nil || expires.IsZero() {
		return "", errors.New("Cron provider requires one owned Rookery installation and expiration")
	}
	role := pgx.Identifier{project.OwnedRepairProbeRole("cron")}.Sanitize()
	schema := pgx.Identifier{ownedCronRuntimePrefix + installation}.Sanitize()
	function := qualified(ownedCronRuntimePrefix+installation, ownedCronRuntimeName) + "()"
	return fmt.Sprintf(`BEGIN;
CREATE SCHEMA %[1]s;
REVOKE ALL ON SCHEMA %[1]s FROM PUBLIC;
CREATE FUNCTION %[2]s RETURNS TABLE (sampled_at timestamptz, launcher_pid integer,
    launcher_started_at timestamptz, postmaster_started_at timestamptz, ready boolean)
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog
    AS $pooltune$%[3]s$pooltune$;
REVOKE ALL ON FUNCTION %[2]s FROM PUBLIC;
GRANT USAGE ON SCHEMA %[1]s TO %[4]s;
GRANT EXECUTE ON FUNCTION %[2]s TO %[4]s;
COMMIT;`, schema, function, ownedCronRuntimeBody(project.OwnedRepairProbeRole("cron"), expires), role), nil
}

func ownedCronRuntimeBody(role string, expires time.Time) string {
	return fmt.Sprintf(`
BEGIN
    IF session_user <> '%s' OR pg_catalog.clock_timestamp() >= '%s'::timestamptz THEN
        RETURN;
    END IF;
    PERFORM pg_catalog.pg_stat_clear_snapshot();
    RETURN QUERY
        SELECT pg_catalog.clock_timestamp(), a.pid, a.backend_start, pg_catalog.pg_postmaster_start_time(),
            COALESCE(pg_catalog.current_setting('cron.timezone',true) IN ('UTC','GMT')
                AND pg_catalog.current_setting('cron.log_run',true)='on'
                AND pg_catalog.current_setting('cron.use_background_workers',true)='off'
                AND pg_catalog.current_setting('cron.database_name',true)=pg_catalog.current_database(),false)
        FROM pg_catalog.pg_stat_activity a
        WHERE a.backend_type='pg_cron launcher' AND a.backend_start IS NOT NULL;
END
`, role, expires.UTC().Format(time.RFC3339Nano))
}

func validOwnedCronRuntime(req OwnedCronRequest) bool {
	prefix := "supabase/" + OwnedIndexRookeryRef + "/"
	installation, match := strings.CutPrefix(req.Target.Project, prefix)
	project, err := supabaseproject.New(OwnedIndexRookeryRef, installation)
	return match && err == nil && project.ID() == req.Target.Project && !req.Expires.IsZero() &&
		req.Runtime.Schema == ownedCronRuntimePrefix+installation && req.Runtime.FunctionOID != 0 &&
		req.Runtime.OwnerOID != 0 && len(req.Runtime.DefinitionDigest) == 64
}

const ownedCronRuntimeIsPinned = `EXISTS (
		SELECT FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
		JOIN pg_catalog.pg_roles owner ON owner.oid=p.proowner
		WHERE p.oid=$1 AND n.nspname=$2 AND p.proname='scheduler_status' AND p.pronargs=0
		  AND owner.oid=$3 AND owner.rolname='postgres' AND n.nspowner=owner.oid
		  AND current_user=session_user AND current_user=$4::name AND p.prosecdef AND p.provolatile='v'
		  AND p.prorettype='pg_catalog.record'::regtype AND p.proretset AND p.prokind='f'
		  AND p.proconfig=ARRAY['search_path=pg_catalog'] AND p.prosrc=$5
		  AND pg_catalog.encode(pg_catalog.sha256(pg_catalog.convert_to(pg_catalog.pg_get_functiondef(p.oid),'UTF8')),'hex')=$6
		  AND pg_catalog.has_schema_privilege(current_user,n.oid,'USAGE')
		  AND NOT pg_catalog.has_schema_privilege(current_user,n.oid,'CREATE')
		  AND pg_catalog.has_function_privilege(current_user,p.oid,'EXECUTE')
		  AND NOT EXISTS(SELECT FROM pg_catalog.aclexplode(COALESCE(n.nspacl,pg_catalog.acldefault('n',n.nspowner))) a
		      WHERE a.grantee<>n.nspowner AND (a.grantee<>current_user::regrole::oid OR a.privilege_type<>'USAGE' OR a.is_grantable))
		  AND NOT EXISTS(SELECT FROM pg_catalog.aclexplode(COALESCE(p.proacl,pg_catalog.acldefault('f',p.proowner))) a
		      WHERE a.grantee<>p.proowner AND (a.grantee<>current_user::regrole::oid OR a.privilege_type<>'EXECUTE' OR a.is_grantable))
	)`

func ownedCronRuntimeArgs(req OwnedCronRequest) []any {
	role := "pt_hosted_cr_" + strings.TrimPrefix(req.Runtime.Schema, ownedCronRuntimePrefix)
	return []any{req.Runtime.FunctionOID, req.Runtime.Schema, req.Runtime.OwnerOID, role,
		ownedCronRuntimeBody(role, req.Expires), req.Runtime.DefinitionDigest}
}

func checkOwnedCronRuntime(ctx context.Context, db trialReader, req OwnedCronRequest) error {
	if !validOwnedCronRuntime(req) {
		return errors.New("exact temporary Cron scheduler reader is unavailable")
	}
	var valid bool
	err := db.QueryRow(ctx, "SELECT "+ownedCronRuntimeIsPinned, ownedCronRuntimeArgs(req)...).Scan(&valid)
	if err != nil || !valid {
		return errors.New("exact temporary Cron scheduler reader is unavailable")
	}
	return nil
}

func readOwnedCronRuntime(ctx context.Context, db trialReader, req OwnedCronRequest) (time.Time, CronGeneration, error) {
	if !validOwnedCronRuntime(req) {
		return time.Time{}, CronGeneration{}, errors.New("exact temporary Cron scheduler reader is unavailable")
	}
	// The fixed reader is invoked only after its body and privileges have been
	// checked in this same statement, including during the native transaction.
	stmt := `SELECT CASE WHEN ` + ownedCronRuntimeIsPinned + ` THEN (
		SELECT jsonb_build_object('at',sampled_at,'generation',jsonb_build_object('pid',launcher_pid,
			'started',launcher_started_at,'postmaster',postmaster_started_at),'ready',ready,'launchers',count(*) OVER ()) FROM ` +
		qualified(req.Runtime.Schema, ownedCronRuntimeName) + `() LIMIT 1) END`
	var raw []byte
	var proof struct {
		At         time.Time      `json:"at"`
		Generation CronGeneration `json:"generation"`
		Ready      bool           `json:"ready"`
		Launchers  int64          `json:"launchers"`
	}
	err := db.QueryRow(ctx, stmt, ownedCronRuntimeArgs(req)...).Scan(&raw)
	if err != nil || json.Unmarshal(raw, &proof) != nil || !proof.Ready || proof.Launchers != 1 || proof.At.IsZero() ||
		proof.Generation.PID <= 0 || proof.Generation.Started.IsZero() || proof.Generation.Postmaster.IsZero() {
		return time.Time{}, CronGeneration{}, errors.New("owned Cron scheduler clock and provider runtime are unavailable")
	}
	return proof.At, proof.Generation, nil
}

func newOwnedCronProvider(req OwnedCronRequest) projectCronProvider {
	if req.Runtime == (OwnedCronRuntime{}) {
		return projectCronProvider{checkProjectCronProvider, readCronClock}
	}
	return projectCronProvider{
		check: func(ctx context.Context, db trialReader) error {
			if err := checkProjectCronProviderCatalog(ctx, db); err != nil {
				return err
			}
			var scoped bool
			if err := db.QueryRow(ctx, `SELECT pg_catalog.row_security_active('cron.job') AND pg_catalog.row_security_active('cron.job_run_details')`).Scan(&scoped); err != nil || !scoped {
				return errors.New("owned Cron job and run history require active row security")
			}
			return checkOwnedCronRuntime(ctx, db, req)
		},
		clock: func(ctx context.Context, db trialReader) (time.Time, CronGeneration, error) {
			return readOwnedCronRuntime(ctx, db, req)
		},
	}
}
