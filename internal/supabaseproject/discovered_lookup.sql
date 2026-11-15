BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;

DO $install$
DECLARE stats_schema name;
BEGIN
    SELECT n.nspname INTO STRICT stats_schema
    FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
    WHERE e.extname='pg_stat_statements';

    EXECUTE format($definition$
        CREATE FUNCTION pooltune_monitor_role.read_discovered_lookup(app_role oid, statement_id bigint)
        RETURNS TABLE (
            grant_id bigint, database_oid oid, table_oid oid, column_number smallint,
            objective_ms double precision, boot timestamptz, reset_at timestamptz, entry_since timestamptz,
            table_pages integer, estimated_table_rows double precision, leading_btree boolean,
            modified_since_analyze bigint, estimated_dead_rows bigint
        )
        LANGUAGE plpgsql VOLATILE SECURITY DEFINER
        SET search_path = pg_catalog, pg_temp
        SET lock_timeout = '50ms'
        SET max_parallel_workers_per_gather = 0
        SET enable_bitmapscan = off
        SET constraint_exclusion = off
        AS $reader$
        DECLARE
            candidate record;
            matched record;
            relation text;
            explained jsonb;
            scan jsonb;
        BEGIN
            IF session_user <> 'pooltune_monitor_role'
               OR NOT has_function_privilege(session_user, 'pooltune_monitor_role.snapshot()', 'EXECUTE') THEN
                RAISE EXCEPTION 'Pooltune monitor access was revoked' USING ERRCODE = '42501';
            END IF;
            IF current_setting('transaction_read_only') <> 'on'
               OR current_setting('statement_timeout')::interval NOT BETWEEN interval '1ms' AND interval '2s' THEN
                RETURN;
            END IF;

            WITH observed AS MATERIALIZED (
                SELECT s.* FROM pooltune_monitor_role.snapshot() s
                WHERE s.role_oid=app_role AND s.query_id=statement_id AND s.entry_since IS NOT NULL
            ), identified AS (
                SELECT o.*, regexp_match(s.query,
                    $shape$^SELECT ([a-z_][a-z0-9_]*) FROM (ONLY )?([a-z_][a-z0-9_]*)\.([a-z_][a-z0-9_]*) WHERE \1 (?:=|OPERATOR\(pg_catalog\.=\)) \$1;?$$shape$) AS names
                FROM observed o JOIN %1$I.pg_stat_statements(true) s
                  ON s.dbid=o.database_oid AND s.userid=o.role_oid AND s.queryid=o.query_id
                 AND s.stats_since=o.entry_since AND s.toplevel
            )
            SELECT i.database_oid, i.mean_execution_ms, i.boot, i.reset_at, i.entry_since,
                   n.nspname, c.relname, c.oid AS oid, a.attname, a.attnum, i.names[2] IS NOT NULL AS only_table
              INTO candidate
            FROM identified i JOIN pg_namespace n ON n.nspname=i.names[3]
            JOIN pg_class c ON c.relnamespace=n.oid AND c.relname=i.names[4]
            JOIN pg_attribute a ON a.attrelid=c.oid AND a.attname=i.names[1] AND a.attnum>0 AND NOT a.attisdropped
            WHERE c.relkind='r' AND c.relpersistence='p' AND a.atttypid='integer'::regtype
              AND n.nspname NOT IN ('information_schema','pooltune_monitor_role')
              AND NOT starts_with(n.nspname,'pg_') AND NOT starts_with(n.nspname,'pt_monitor_')
              AND has_schema_privilege(app_role,n.oid,'USAGE')
              AND has_column_privilege(app_role,c.oid,a.attnum,'SELECT');
            IF NOT FOUND THEN RETURN; END IF;

            relation := format('%%I.%%I',candidate.nspname,candidate.relname);
            BEGIN
                EXECUTE 'LOCK TABLE ONLY ' || relation || ' IN ACCESS SHARE MODE NOWAIT';
                FOR check_step IN 1..2 LOOP
                    IF to_regclass(relation)::oid IS DISTINCT FROM candidate.oid THEN
                        RAISE no_data_found;
                    END IF;
                    SELECT c.relpages, CASE WHEN c.reltuples>=0 THEN c.reltuples::double precision END AS reltuples,
                           EXISTS (
                               SELECT FROM pg_index i JOIN pg_class ix ON ix.oid=i.indexrelid
                               JOIN pg_am am ON am.oid=ix.relam AND am.amname='btree'
                               JOIN pg_opclass op ON op.oid=i.indclass[0] AND op.opcdefault AND op.opcintype='integer'::regtype
                               WHERE i.indrelid=c.oid AND i.indkey[0]=a.attnum AND i.indisvalid AND i.indisready AND i.indislive
                           ) AS leading_btree, t.n_mod_since_analyze, t.n_dead_tup
                      INTO STRICT matched
                    FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
                    JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=candidate.attnum AND a.attname=candidate.attname
                    JOIN pg_stat_all_tables t ON t.relid=c.oid
                    WHERE c.oid=candidate.oid AND n.nspname=candidate.nspname AND c.relname=candidate.relname
                      AND c.relkind='r' AND c.relpersistence='p' AND c.relam=(SELECT oid FROM pg_am WHERE amname='heap')
                      AND NOT (c.relrowsecurity OR c.relispartition OR c.relhasrules)
                      AND NOT EXISTS (SELECT FROM pg_inherits WHERE inhrelid=c.oid OR inhparent=c.oid)
                      AND NOT a.attisdropped AND a.atttypid='integer'::regtype
                      AND has_schema_privilege(app_role,n.oid,'USAGE')
                      AND has_column_privilege(app_role,c.oid,a.attnum,'SELECT')
                      AND NOT EXISTS (
                          SELECT FROM pg_index i JOIN pg_class ix ON ix.oid=i.indexrelid
                          JOIN pg_am am ON am.oid=ix.relam
                          WHERE i.indrelid=c.oid AND (
                              i.indexprs IS NOT NULL OR i.indpred IS NOT NULL OR am.amname<>'btree'
                              OR EXISTS (SELECT FROM unnest(i.indclass) AS k(opclass)
                                         JOIN pg_opclass op ON op.oid=k.opclass
                                         WHERE NOT op.opcdefault OR op.opcnamespace<>'pg_catalog'::regnamespace)
                          )
                      );
                    IF check_step = 1 THEN
                        -- Plan only the fixed integer lookup, with no observed text or input values.
                        EXECUTE format('EXPLAIN (ANALYZE FALSE, VERBOSE, GENERIC_PLAN, FORMAT JSON) '
                            'SELECT %%1$I FROM %%2$s%%3$s WHERE %%1$I OPERATOR(pg_catalog.=) $1',
                            candidate.attname,CASE WHEN candidate.only_table THEN 'ONLY ' ELSE '' END,relation) INTO explained;
                        scan := explained #> '{0,Plan}';
                        IF (explained #>> '{0,Query Identifier}')::bigint IS DISTINCT FROM statement_id
                           OR scan->>'Schema' IS DISTINCT FROM candidate.nspname::text
                           OR scan->>'Relation Name' IS DISTINCT FROM candidate.relname::text
                           OR COALESCE(scan->>'Node Type','') NOT IN ('Seq Scan','Index Scan','Index Only Scan') OR scan ? 'Plans' THEN
                            RAISE no_data_found;
                        END IF;
                    END IF;
                END LOOP;

                -- Roll back the local subtransaction to release planner locks before returning evidence.
                RAISE SQLSTATE 'PT001';
            EXCEPTION
                WHEN SQLSTATE 'PT001' THEN NULL;
                WHEN no_data_found OR lock_not_available OR undefined_table OR wrong_object_type OR insufficient_privilege THEN RETURN;
            END;

            RETURN QUERY
            SELECT 1::bigint, candidate.database_oid, candidate.oid, candidate.attnum, candidate.mean_execution_ms,
                   candidate.boot, candidate.reset_at, candidate.entry_since, matched.relpages, matched.reltuples,
                   matched.leading_btree, matched.n_mod_since_analyze, matched.n_dead_tup;
        END
        $reader$;
    $definition$, stats_schema);
END
$install$;

REVOKE ALL ON FUNCTION pooltune_monitor_role.read_discovered_lookup(oid,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pooltune_monitor_role.read_discovered_lookup(oid,bigint) TO pooltune_monitor_role;

DO $grants$
BEGIN
    IF EXISTS (
        SELECT FROM pg_proc p, LATERAL aclexplode(p.proacl) a
        WHERE p.oid='pooltune_monitor_role.read_discovered_lookup(oid,bigint)'::regprocedure
          AND a.grantee NOT IN (p.proowner,'pooltune_monitor_role'::regrole)
    ) THEN
        RAISE EXCEPTION 'unexpected default privileges on Pooltune discovery evidence';
    END IF;
END
$grants$;
COMMIT;
