DO $install$
DECLARE
    stats_schema name;
BEGIN
    SELECT n.nspname INTO STRICT stats_schema
    FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
    WHERE e.extname='pg_stat_statements';

    EXECUTE format($definition$
        CREATE OR REPLACE FUNCTION pooltune_monitor_role.allow_lookup(app_role oid, statement_id bigint, relation regclass, lookup_column name)
        RETURNS void
        LANGUAGE plpgsql SECURITY INVOKER
        SET search_path = pg_catalog, pg_temp
        AS $grant$
        BEGIN
            INSERT INTO pooltune_monitor_role.lookup_grants (role_oid, query_id, table_oid, column_number)
            SELECT app_role, statement_id, c.oid, a.attnum
            FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
            JOIN pg_attribute a ON a.attrelid=c.oid AND a.attname=lookup_column
                AND a.attnum>0 AND NOT a.attisdropped AND a.atttypid='integer'::regtype
            WHERE c.oid=relation AND n.nspname NOT IN ('information_schema','pooltune_monitor_role')
              AND NOT starts_with(n.nspname,'pg_')
              AND c.relkind='r' AND c.relpersistence='p' AND c.relam=(SELECT oid FROM pg_am WHERE amname='heap')
              AND NOT (c.relrowsecurity OR c.relispartition OR c.relhasrules)
              AND NOT EXISTS (SELECT FROM pg_inherits WHERE inhrelid=c.oid OR inhparent=c.oid)
              AND EXISTS (
                 SELECT FROM %1$I.pg_stat_statements(true) s JOIN pg_database db ON db.oid=s.dbid
                 WHERE db.datname=current_database() AND s.userid=app_role AND s.queryid=statement_id AND s.toplevel
                   AND s.query IN (
                     format('SELECT %%1$I FROM %%2$I.%%3$I WHERE %%1$I = $1', a.attname, n.nspname, c.relname),
                     format('SELECT %%1$I FROM %%2$I.%%3$I WHERE %%1$I = $1;', a.attname, n.nspname, c.relname),
                     format('SELECT %%1$I FROM %%2$I.%%3$I WHERE %%1$I OPERATOR(pg_catalog.=) $1', a.attname, n.nspname, c.relname),
                     format('SELECT %%1$I FROM ONLY %%2$I.%%3$I WHERE %%1$I OPERATOR(pg_catalog.=) $1', a.attname, n.nspname, c.relname),
                     format('SELECT %%1$I FROM ONLY %%2$I.%%3$I WHERE %%1$I OPERATOR(pg_catalog.=) $1;', a.attname, n.nspname, c.relname)
                   )
              );
            IF NOT FOUND THEN
                RAISE EXCEPTION 'no matching monitored integer equality lookup';
            END IF;
        END
        $grant$;
    $definition$, stats_schema);
END
$install$;
