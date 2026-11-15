BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '10s';

CREATE ROLE pooltune_monitor_role NOLOGIN NOINHERIT NOSUPERUSER NOCREATEROLE NOCREATEDB NOBYPASSRLS NOREPLICATION CONNECTION LIMIT 2;
GRANT CONNECT ON DATABASE postgres TO pooltune_monitor_role;
CREATE SCHEMA pooltune_monitor_role;
REVOKE ALL ON SCHEMA pooltune_monitor_role FROM PUBLIC;

CREATE TABLE pooltune_monitor_role.enrolled_queries (
    slot smallint PRIMARY KEY CHECK (slot BETWEEN 1 AND 20),
    role_oid oid NOT NULL CHECK (role_oid <> 0),
    query_id bigint NOT NULL CHECK (query_id <> 0),
    mean_execution_ms double precision NOT NULL
        CHECK (mean_execution_ms > 0 AND mean_execution_ms < 'Infinity'::double precision),
    UNIQUE (role_oid, query_id)
);
REVOKE ALL ON TABLE pooltune_monitor_role.enrolled_queries FROM PUBLIC;

DO $install$
DECLARE
    stats_schema name;
BEGIN
    IF NOT pg_has_role(current_user, 'pg_read_all_stats', 'USAGE') THEN
        RAISE EXCEPTION 'the setup role must be able to see cross-role query IDs';
    END IF;

    SELECT n.nspname INTO STRICT stats_schema
    FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
    WHERE e.extname = 'pg_stat_statements';

    EXECUTE format($definition$
        CREATE FUNCTION pooltune_monitor_role.snapshot()
        RETURNS TABLE (
            database_oid oid, role_oid oid, query_id bigint, mean_execution_ms double precision,
            boot timestamptz, reset_at timestamptz, entry_since timestamptz,
            calls bigint, execution_ms double precision, blocks bigint
        )
        LANGUAGE plpgsql VOLATILE SECURITY DEFINER
        SET search_path = pg_catalog, pg_temp
        AS $reader$
        BEGIN
            IF session_user <> 'pooltune_monitor_role'
                OR current_setting('transaction_isolation') <> 'read committed'
                OR NOT EXISTS (
                SELECT FROM pg_roles r
                WHERE r.rolname = session_user AND r.rolcanlogin AND NOT r.rolinherit
                    AND NOT (r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolbypassrls OR r.rolreplication)
                    AND r.rolconnlimit BETWEEN 1 AND 2
                    AND (r.rolvaliduntil IS NULL OR r.rolvaliduntil > clock_timestamp())
                    AND NOT EXISTS (SELECT FROM pg_auth_members m WHERE m.member = r.oid)
            ) OR NOT EXISTS (SELECT FROM pg_stat_ssl WHERE pid = pg_backend_pid() AND ssl) THEN
                RAISE EXCEPTION 'Pooltune monitor identity or transport was revoked or changed' USING ERRCODE = '42501';
            END IF;

            RETURN QUERY
            SELECT db.oid, e.role_oid, e.query_id, e.mean_execution_ms,
                   pg_postmaster_start_time(), info.stats_reset, s.stats_since,
                   s.calls, s.total_exec_time, s.shared_blks_hit + s.shared_blks_read
            FROM pooltune_monitor_role.enrolled_queries e
            JOIN pg_roles app ON app.oid = e.role_oid AND app.rolname <> session_user
            CROSS JOIN pg_database db
            CROSS JOIN %1$I.pg_stat_statements_info info
            LEFT JOIN %1$I.pg_stat_statements(false) s
                ON s.dbid = db.oid AND s.userid = e.role_oid AND s.queryid = e.query_id AND s.toplevel
            WHERE db.datname = current_database()
            ORDER BY e.slot;
        END
        $reader$;
    $definition$, stats_schema);
END
$install$;

REVOKE ALL ON FUNCTION pooltune_monitor_role.snapshot() FROM PUBLIC;
GRANT USAGE ON SCHEMA pooltune_monitor_role TO pooltune_monitor_role;
GRANT EXECUTE ON FUNCTION pooltune_monitor_role.snapshot() TO pooltune_monitor_role;

DO $grants$
BEGIN
    -- Refuse project-wide default privileges which would expose these objects.
    IF EXISTS (
        SELECT FROM pg_namespace n, LATERAL aclexplode(n.nspacl) a
        WHERE n.nspname = 'pooltune_monitor_role'
          AND a.grantee NOT IN (n.nspowner, 'pooltune_monitor_role'::regrole)
    ) OR EXISTS (
        SELECT FROM pg_class c, LATERAL aclexplode(c.relacl) a
        WHERE c.oid = 'pooltune_monitor_role.enrolled_queries'::regclass
          AND a.grantee <> c.relowner
    ) OR EXISTS (
        SELECT FROM pg_proc p, LATERAL aclexplode(p.proacl) a
        WHERE p.oid = 'pooltune_monitor_role.snapshot()'::regprocedure
          AND a.grantee NOT IN (p.proowner, 'pooltune_monitor_role'::regrole)
    ) THEN
        RAISE EXCEPTION 'unexpected default privileges on Pooltune monitor objects';
    END IF;
END
$grants$;
COMMIT;
