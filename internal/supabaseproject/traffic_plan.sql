BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '10s';

CREATE TABLE pooltune_monitor_role.traffic_plan_grants (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    role_oid oid NOT NULL,
    query_id bigint NOT NULL,
    context_table regclass NOT NULL,
    context_column smallint NOT NULL CHECK (context_column > 0),
    granted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (role_oid, query_id),
    FOREIGN KEY (role_oid, query_id)
        REFERENCES pooltune_monitor_role.enrolled_queries (role_oid, query_id) ON DELETE CASCADE
);
REVOKE ALL ON TABLE pooltune_monitor_role.traffic_plan_grants FROM PUBLIC;

CREATE FUNCTION pooltune_monitor_role.read_traffic_plan_grant(app_role oid, statement_id bigint)
RETURNS TABLE (grant_id bigint, login_name name, table_oid oid, column_number smallint,
               database_oid oid, objective_ms double precision, boot timestamptz,
               reset_at timestamptz, entry_since timestamptz, granted_at timestamptz)
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $reader$
    SELECT g.id, app.rolname, c.oid, a.attnum, s.database_oid, s.mean_execution_ms,
           s.boot, s.reset_at, s.entry_since, g.granted_at
    FROM pooltune_monitor_role.snapshot() s
    JOIN pooltune_monitor_role.traffic_plan_grants g USING (role_oid,query_id)
    JOIN pg_roles app ON app.oid = g.role_oid AND app.rolcanlogin
        AND NOT (app.rolsuper OR app.rolcreaterole OR app.rolreplication OR app.rolbypassrls)
        AND NOT pg_has_role(app.oid, 'pg_database_owner', 'SET')
        AND NOT EXISTS (SELECT FROM pg_auth_members m WHERE m.member = app.oid)
        AND (app.rolvaliduntil IS NULL OR app.rolvaliduntil > clock_timestamp())
    JOIN pg_class c ON c.oid = g.context_table AND c.relkind IN ('r', 'p', 'm')
    JOIN pg_namespace n ON n.oid = c.relnamespace AND NOT starts_with(n.nspname,'pg_')
        AND n.nspname <> 'information_schema' AND n.nspname <> 'pooltune_monitor_role'
    JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = g.context_column AND NOT a.attisdropped
    WHERE s.role_oid = app_role AND s.query_id = statement_id AND s.calls > 0
        AND has_schema_privilege(app.oid, n.oid, 'USAGE')
        AND has_column_privilege(app.oid, c.oid, a.attnum, 'SELECT');
$reader$;

REVOKE ALL ON FUNCTION pooltune_monitor_role.read_traffic_plan_grant(oid,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pooltune_monitor_role.read_traffic_plan_grant(oid,bigint) TO pooltune_monitor_role;

DO $grants$
BEGIN
    IF EXISTS (
        SELECT FROM pg_class c, LATERAL aclexplode(c.relacl) a
        WHERE c.oid = 'pooltune_monitor_role.traffic_plan_grants'::regclass AND a.grantee <> c.relowner
    ) OR EXISTS (
        SELECT FROM pg_class c, LATERAL aclexplode(c.relacl) a
        WHERE c.oid=pg_get_serial_sequence('pooltune_monitor_role.traffic_plan_grants','id')::regclass
          AND a.grantee <> c.relowner
    ) OR EXISTS (
        SELECT FROM pg_proc p, LATERAL aclexplode(p.proacl) a
        WHERE p.oid='pooltune_monitor_role.read_traffic_plan_grant(oid,bigint)'::regprocedure
            AND a.grantee NOT IN (p.proowner, 'pooltune_monitor_role'::regrole)
    ) THEN
        RAISE EXCEPTION 'unexpected default privileges on Pooltune traffic-plan objects';
    END IF;
END
$grants$;
COMMIT;
