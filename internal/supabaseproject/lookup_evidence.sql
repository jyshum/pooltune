BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '10s';

CREATE TABLE pooltune_monitor_role.lookup_grants (
    grant_id bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    role_oid oid NOT NULL,
    query_id bigint NOT NULL,
    table_oid oid NOT NULL,
    column_number smallint NOT NULL CHECK (column_number > 0),
    PRIMARY KEY (role_oid, query_id),
    FOREIGN KEY (role_oid, query_id)
        REFERENCES pooltune_monitor_role.enrolled_queries (role_oid, query_id) ON DELETE CASCADE
);
REVOKE ALL ON TABLE pooltune_monitor_role.lookup_grants FROM PUBLIC;

DO $install$
DECLARE
    stats_schema name;
BEGIN
    SELECT n.nspname INTO STRICT stats_schema
    FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
    WHERE e.extname='pg_stat_statements';

    EXECUTE format($definition$
        CREATE FUNCTION pooltune_monitor_role.allow_lookup(app_role oid, statement_id bigint, relation regclass, lookup_column name)
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
                     format('SELECT %%1$I FROM %%2$I.%%3$I WHERE %%1$I OPERATOR(pg_catalog.=) $1', a.attname, n.nspname, c.relname)
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

CREATE FUNCTION pooltune_monitor_role.read_lookup(app_role oid, statement_id bigint)
RETURNS TABLE (
    grant_id bigint, database_oid oid, table_oid oid, column_number smallint,
    objective_ms double precision, boot timestamptz, reset_at timestamptz, entry_since timestamptz,
    table_pages integer, estimated_table_rows double precision, leading_btree boolean,
    modified_since_analyze bigint, estimated_dead_rows bigint
)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $reader$
BEGIN
    IF session_user <> 'pooltune_monitor_role'
       OR NOT has_function_privilege(session_user, 'pooltune_monitor_role.snapshot()', 'EXECUTE') THEN
        RAISE EXCEPTION 'Pooltune monitor access was revoked' USING ERRCODE = '42501';
    END IF;

    RETURN QUERY
    SELECT g.grant_id, s.database_oid, c.oid, a.attnum, s.mean_execution_ms, s.boot, s.reset_at, s.entry_since,
           c.relpages, CASE WHEN c.reltuples >= 0 THEN c.reltuples::double precision END,
           EXISTS (
               SELECT FROM pg_index i
               JOIN pg_class ix ON ix.oid=i.indexrelid
               JOIN pg_am am ON am.oid=ix.relam AND am.amname='btree'
               JOIN pg_opclass op ON op.oid=i.indclass[0] AND op.opcdefault AND op.opcintype='integer'::regtype
               WHERE i.indrelid=c.oid AND i.indkey[0]=a.attnum AND i.indisvalid
                 AND i.indisready AND i.indislive AND i.indpred IS NULL
           ),
           t.n_mod_since_analyze, t.n_dead_tup
    FROM pooltune_monitor_role.lookup_grants g
    JOIN pg_class c ON c.oid=g.table_oid AND c.relkind='r' AND c.relpersistence='p'
         AND c.relam=(SELECT oid FROM pg_am WHERE amname='heap')
         AND NOT (c.relrowsecurity OR c.relispartition OR c.relhasrules)
    JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=g.column_number
         AND NOT a.attisdropped AND a.atttypid='integer'::regtype
    JOIN pg_stat_all_tables t ON t.relid=c.oid
    JOIN pooltune_monitor_role.snapshot() s ON s.role_oid=g.role_oid AND s.query_id=g.query_id
    WHERE g.role_oid=app_role AND g.query_id=statement_id AND s.entry_since IS NOT NULL
      AND NOT EXISTS (SELECT FROM pg_inherits WHERE inhrelid=c.oid OR inhparent=c.oid)
      AND has_schema_privilege(app_role,c.relnamespace,'USAGE')
      AND has_column_privilege(app_role,c.oid,a.attnum,'SELECT');
END
$reader$;

REVOKE ALL ON FUNCTION pooltune_monitor_role.allow_lookup(oid,bigint,regclass,name) FROM PUBLIC;
REVOKE ALL ON FUNCTION pooltune_monitor_role.read_lookup(oid,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pooltune_monitor_role.read_lookup(oid,bigint) TO pooltune_monitor_role;

DO $grants$
BEGIN
    IF EXISTS (
        SELECT FROM pg_class c, LATERAL aclexplode(c.relacl) a
        WHERE c.oid='pooltune_monitor_role.lookup_grants'::regclass AND a.grantee<>c.relowner
    ) OR EXISTS (
        SELECT FROM pg_class c, LATERAL aclexplode(c.relacl) a
        WHERE c.oid=pg_get_serial_sequence('pooltune_monitor_role.lookup_grants','grant_id')::regclass
          AND a.grantee<>c.relowner
    ) OR EXISTS (
        SELECT FROM pg_proc p, LATERAL aclexplode(p.proacl) a
        WHERE p.oid='pooltune_monitor_role.allow_lookup(oid,bigint,regclass,name)'::regprocedure
          AND a.grantee<>p.proowner
    ) OR EXISTS (
        SELECT FROM pg_proc p, LATERAL aclexplode(p.proacl) a
        WHERE p.oid='pooltune_monitor_role.read_lookup(oid,bigint)'::regprocedure
          AND a.grantee NOT IN (p.proowner,'pooltune_monitor_role'::regrole)
    ) THEN
        RAISE EXCEPTION 'unexpected default privileges on lookup diagnostic objects';
    END IF;
END
$grants$;
COMMIT;
