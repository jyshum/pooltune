BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;

DO $install$
DECLARE stats_schema name;
BEGIN
    SELECT n.nspname INTO STRICT stats_schema
    FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
    WHERE e.extname='pg_stat_statements';

    EXECUTE format($definition$
        CREATE FUNCTION pooltune_monitor_role.read_discovered_index(app_role oid, statement_id bigint)
        RETURNS TABLE (
            database_oid oid, role_oid oid, query_id bigint, table_oid oid, schema_name name, relation_name name,
            only_table boolean, column_numbers smallint[], objective_ms double precision,
            boot timestamptz, reset_at timestamptz, entry_since timestamptz, plan_node text,
            table_pages integer, estimated_table_rows double precision,
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
            predicate text;
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
                AND NOT EXISTS (
                    SELECT FROM pooltune_monitor_role.enrolled_queries e
                    WHERE e.role_oid=app_role AND e.query_id=statement_id
                )
            ), identified AS (
                SELECT o.*, regexp_match(s.query, $shape$
                    ^ [[:space:]]* SELECT [[:space:]]+ ("[a-z_][a-z0-9_]*"|[a-z_][a-z0-9_]*)
                    [[:space:]]+ FROM [[:space:]]+ (ONLY[[:space:]]+)?
                    ("[a-z_][a-z0-9_]*"|[a-z_][a-z0-9_]*) \. ("[a-z_][a-z0-9_]*"|[a-z_][a-z0-9_]*)
                    [[:space:]]+ WHERE [[:space:]]+ \1 [[:space:]]* (?:=|OPERATOR\(pg_catalog\.=\)) [[:space:]]* \$1
                    (?: [[:space:]]+ AND [[:space:]]+ ("[a-z_][a-z0-9_]*"|[a-z_][a-z0-9_]*)
                        [[:space:]]* (?:=|OPERATOR\(pg_catalog\.=\)) [[:space:]]* \$2 )?
                    [[:space:]]* ;? [[:space:]]* $
                    $shape$, 'ix') AS names
                FROM observed o JOIN %1$I.pg_stat_statements(true) s
                  ON s.dbid=o.database_oid AND s.userid=o.role_oid AND s.queryid=o.query_id
                 AND s.stats_since=o.entry_since AND s.toplevel
                WHERE octet_length(s.query) BETWEEN 1 AND 512
            ), parsed AS (
                SELECT i.*,
                       (parse_ident(i.names[3]))[1] AS schema_name,
                       (parse_ident(i.names[4]))[1] AS relation_name,
                       (parse_ident(i.names[1]))[1] AS first_name,
                       (parse_ident(i.names[5]))[1] AS second_name
                FROM identified i WHERE i.names IS NOT NULL
            )
            SELECT i.database_oid, i.mean_execution_ms, i.boot, i.reset_at, i.entry_since,
                   n.nspname, c.relname, c.oid, i.names[2] IS NOT NULL AS only_table, i.first_name, i.second_name
              INTO STRICT candidate
            FROM parsed i JOIN pg_namespace n ON n.nspname=i.schema_name
            JOIN pg_class c ON c.relnamespace=n.oid AND c.relname=i.relation_name
            WHERE n.nspname NOT IN ('information_schema','pooltune_monitor_role')
              AND NOT starts_with(n.nspname,'pg_') AND NOT starts_with(n.nspname,'pt_monitor_')
              AND i.first_name IS DISTINCT FROM i.second_name
              AND c.relkind='r' AND c.relpersistence='p' AND has_schema_privilege(app_role,n.oid,'USAGE');

            relation := format('%%I.%%I',candidate.nspname,candidate.relname);
            BEGIN
                EXECUTE 'LOCK TABLE ONLY ' || relation || ' IN ACCESS SHARE MODE NOWAIT';
                FOR check_step IN 1..2 LOOP
                    IF to_regclass(relation)::oid IS DISTINCT FROM candidate.oid THEN
                        RAISE no_data_found;
                    END IF;
                    SELECT a.attnum AS first_number, b.attnum AS second_number, c.relpages,
                           CASE WHEN c.reltuples>=0 THEN c.reltuples::double precision END AS reltuples,
                           t.n_mod_since_analyze, t.n_dead_tup
                      INTO STRICT matched
                    FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
                    JOIN pg_attribute a ON a.attrelid=c.oid AND a.attname=candidate.first_name
                    LEFT JOIN pg_attribute b ON b.attrelid=c.oid AND b.attname=candidate.second_name
                    JOIN pg_stat_all_tables t ON t.relid=c.oid
                    WHERE c.oid=candidate.oid AND n.nspname=candidate.nspname AND c.relname=candidate.relname
                      AND c.relkind='r' AND c.relpersistence='p' AND c.relam=(SELECT oid FROM pg_am WHERE amname='heap')
                      AND NOT (c.relrowsecurity OR c.relispartition OR c.relhasrules)
                      AND NOT EXISTS (SELECT FROM pg_inherits WHERE inhrelid=c.oid OR inhparent=c.oid)
                      AND a.attnum>0 AND NOT a.attisdropped AND a.atttypid='integer'::regtype
                      AND has_schema_privilege(app_role,n.oid,'USAGE')
                      AND has_column_privilege(app_role,c.oid,a.attnum,'SELECT')
                      AND (candidate.second_name IS NULL OR
                          (b.attnum>0 AND NOT b.attisdropped AND b.atttypid='integer'::regtype
                           AND has_column_privilege(app_role,c.oid,b.attnum,'SELECT')))
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
                        -- Rebuild only built-in integer predicates. Do not execute observed text or sample values.
                        predicate := format('%%I OPERATOR(pg_catalog.=) $1',candidate.first_name);
                        IF matched.second_number IS NOT NULL THEN
                            predicate := predicate || format(' AND %%I OPERATOR(pg_catalog.=) $2',candidate.second_name);
                        END IF;
                        EXECUTE format('EXPLAIN (ANALYZE FALSE, VERBOSE, GENERIC_PLAN, FORMAT JSON) SELECT %%I FROM %%s%%s WHERE %%s',
                            candidate.first_name,CASE WHEN candidate.only_table THEN 'ONLY ' ELSE '' END,relation,predicate)
                            INTO explained;
                        scan := explained #> '{0,Plan}';
                        IF (explained #>> '{0,Query Identifier}')::bigint IS DISTINCT FROM statement_id
                           OR scan->>'Schema' IS DISTINCT FROM candidate.nspname::text
                           OR scan->>'Relation Name' IS DISTINCT FROM candidate.relname::text
                           OR COALESCE(scan->>'Node Type','') NOT IN ('Seq Scan','Index Scan','Index Only Scan') OR scan ? 'Plans' THEN
                            RAISE no_data_found;
                        END IF;
                    END IF;
                END LOOP;

                -- Release the planning locks before returning the bounded catalog evidence.
                RAISE SQLSTATE 'PT001';
            EXCEPTION
                WHEN SQLSTATE 'PT001' THEN NULL;
                WHEN no_data_found OR lock_not_available OR undefined_table OR wrong_object_type OR insufficient_privilege THEN RETURN;
            END;

            RETURN QUERY
            SELECT candidate.database_oid, app_role, statement_id, candidate.oid, candidate.nspname,
                   candidate.relname, candidate.only_table, array_remove(ARRAY[matched.first_number,matched.second_number],NULL),
                   candidate.mean_execution_ms, candidate.boot, candidate.reset_at, candidate.entry_since, scan->>'Node Type',
                   matched.relpages, matched.reltuples, matched.n_mod_since_analyze, matched.n_dead_tup;
        EXCEPTION
            WHEN no_data_found OR too_many_rows THEN RETURN;
        END
        $reader$;
    $definition$, stats_schema);
END
$install$;

REVOKE ALL ON FUNCTION pooltune_monitor_role.read_discovered_index(oid,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pooltune_monitor_role.read_discovered_index(oid,bigint) TO pooltune_monitor_role;

DO $grants$
BEGIN
    IF EXISTS (
        SELECT FROM pg_proc p, LATERAL aclexplode(p.proacl) a
        WHERE p.oid='pooltune_monitor_role.read_discovered_index(oid,bigint)'::regprocedure
          AND a.grantee NOT IN (p.proowner,'pooltune_monitor_role'::regrole)
    ) THEN
        RAISE EXCEPTION 'unexpected default privileges on Pooltune discovery evidence';
    END IF;
END
$grants$;
COMMIT;
