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
        CREATE OR REPLACE FUNCTION pooltune_monitor_role.snapshot()
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
                -- POOLTUNE_EXTRA_GUARD
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

            -- POOLTUNE_READER
        END
        $reader$;
    $definition$, stats_schema);
END
$install$;
