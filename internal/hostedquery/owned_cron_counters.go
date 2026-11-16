package hostedquery

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

const ownedCronCounterView = "statement_counters"

func ownedCronHealthyQuery(schema string) string {
	return "SELECT id FROM ONLY " + qualified(schema, "healthy") + " WHERE id=$1"
}

func ownedCronWriteQuery(schema string) string {
	return "UPDATE ONLY " + qualified(schema, "healthy") + " SET padding=CASE padding WHEN 'a' THEN 'b' ELSE 'a' END WHERE id=$1 RETURNING id"
}

// OwnedCronCountersSQL installs a view over the caller's existing counters. It
// needs no extension schema grant and runs with the caller's base privileges.
// It returns three named lanes and never returns statement text.
func OwnedCronCountersSQL(installation string) (string, error) {
	project, err := supabaseproject.New(OwnedIndexRookeryRef, installation)
	if err != nil {
		return "", errors.New("Cron counters require one owned Rookery installation")
	}
	role := project.OwnedRepairProbeRole("cron")
	view := qualified(ownedCronRuntimePrefix+installation, ownedCronCounterView)
	target := ownedCronQuery(role)
	write := ownedCronWriteQuery(role)
	literal := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	return strings.NewReplacer(
		"@ROLE@", role, "@VIEW@", view, "@TARGET@", literal(target),
		"@NORMAL_TARGET@", literal(strings.Replace(target, "VALUES (1)", "VALUES ($1)", 1)),
		"@HEALTHY@", literal(ownedCronHealthyQuery(role)), "@WRITE@", literal(write),
		"@NORMAL_WRITE@", literal(strings.Replace(write, "WHEN 'a' THEN 'b' ELSE 'a'", "WHEN $2 THEN $3 ELSE $4", 1)),
	).Replace(`BEGIN;
SET LOCAL search_path=pg_catalog,pg_temp;
DO $counters$
DECLARE stats_schema name;
BEGIN
    SELECT n.nspname INTO STRICT stats_schema FROM pg_catalog.pg_extension e
      JOIN pg_catalog.pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pg_stat_statements';
    IF NOT pg_catalog.has_table_privilege('@ROLE@', pg_catalog.format('%I.pg_stat_statements',stats_schema),'SELECT')
       OR NOT pg_catalog.has_table_privilege('@ROLE@', pg_catalog.format('%I.pg_stat_statements_info',stats_schema),'SELECT') THEN
        RAISE EXCEPTION 'existing fixture counter SELECT privileges are unavailable';
    END IF;
    EXECUTE pg_catalog.format($view_sql$
      CREATE VIEW @VIEW@ WITH (security_invoker=true,security_barrier=true) AS
      SELECT f.lane,s.queryid,s.dbid,s.userid,pg_catalog.pg_postmaster_start_time() AS boot,
             i.stats_reset AS reset_at,s.stats_since AS entry_since,s.calls,
             s.total_exec_time AS execution_ms,s.shared_blks_hit+s.shared_blks_read AS blocks
      FROM %1$I.pg_stat_statements s CROSS JOIN %1$I.pg_stat_statements_info i
      CROSS JOIN LATERAL (SELECT CASE
          WHEN s.query IN (@TARGET@,@NORMAL_TARGET@) THEN 'target'
          WHEN s.query=@HEALTHY@ THEN 'healthy'
          WHEN s.query IN (@WRITE@,@NORMAL_WRITE@) THEN 'write'
      END AS lane) f
      WHERE CURRENT_USER=SESSION_USER AND SESSION_USER='@ROLE@'
        AND s.userid=(SELECT oid FROM pg_catalog.pg_roles WHERE rolname=SESSION_USER)
        AND current_database()='postgres' AND s.dbid=(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database())
        AND s.toplevel AND s.queryid IS NOT NULL AND f.lane IS NOT NULL
    $view_sql$,stats_schema);
END
$counters$;
REVOKE ALL ON @VIEW@ FROM PUBLIC;
GRANT SELECT ON @VIEW@ TO @ROLE@;
COMMIT;`), nil
}

func validOwnedCronCounterView(req OwnedCronRequest) bool {
	return req.Runtime.CounterViewOID != 0 && len(req.Runtime.CounterViewDigest) == 64
}

const ownedCronCounterCatalogSQL = `SELECT EXISTS(
		SELECT FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		JOIN pg_catalog.pg_rewrite r ON r.ev_class=c.oid AND r.rulename='_RETURN'
		WHERE c.oid=$1 AND n.nspname=$2 AND c.relname='statement_counters' AND c.relkind='v'
		  AND c.relowner=$3 AND n.nspowner=$3 AND c.relowner='postgres'::regrole::oid
		  AND c.reloptions @> ARRAY['security_invoker=true','security_barrier=true']
		  AND encode(sha256(convert_to(r.ev_action::text,'UTF8')),'hex')=$4
		  AND current_user=session_user AND pg_catalog.has_table_privilege(current_user,c.oid,'SELECT')
		  AND NOT EXISTS(SELECT FROM pg_catalog.aclexplode(COALESCE(c.relacl,pg_catalog.acldefault('r',c.relowner))) a
		    WHERE a.grantee<>c.relowner AND (a.grantee<>current_user::regrole::oid OR a.privilege_type<>'SELECT' OR a.is_grantable))
	)`

func ownedCronCounterCatalogArgs(req OwnedCronRequest) []any {
	return []any{req.Runtime.CounterViewOID, req.Runtime.Schema, req.Runtime.OwnerOID, req.Runtime.CounterViewDigest}
}

func checkOwnedCronCounterView(ctx context.Context, db trialReader, req OwnedCronRequest) error {
	if !validOwnedCronRuntime(req) || !validOwnedCronCounterView(req) {
		return errors.New("exact owned Cron counter view is unavailable")
	}
	return scanOwnedCronCounterView(db.QueryRow(ctx, ownedCronCounterCatalogSQL, ownedCronCounterCatalogArgs(req)...))
}

func scanOwnedCronCounterView(row pgx.Row) error {
	var ready bool
	err := row.Scan(&ready)
	if err != nil || !ready {
		return errors.New("exact owned Cron counter view is unavailable")
	}
	return nil
}

func ownedCronCounterSQL(req OwnedCronRequest) string {
	return `SELECT EXISTS(SELECT FROM ` + qualified(req.Runtime.Schema, ownedCronCounterView) + `
		WHERE lane=$1 AND dbid=$2 AND userid=$3 AND queryid=$4 AND boot=$5 AND reset_at=$6 AND entry_since=$7)`
}

func ownedCronCounterArgs(lane string, c Counter) []any {
	return []any{lane, c.Database, c.Role, c.Query, c.Boot, c.Reset, c.Entry}
}

func currentOwnedCronCounters(ctx context.Context, db trialReader, req OwnedCronRequest, lane string, counter Counter) (bool, error) {
	var match bool
	err := db.QueryRow(ctx, ownedCronCounterSQL(req), ownedCronCounterArgs(lane, counter)...).Scan(&match)
	return match, err
}
