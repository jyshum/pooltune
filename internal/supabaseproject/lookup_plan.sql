BEGIN;
SET LOCAL search_path = pg_catalog, pg_temp;
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '10s';

-- POOLTUNE_ALLOW_LOOKUP

-- Validate the database credential's disclosure boundary as well as Go's
-- decoder. A raw EXPLAIN receipt must never cross this interface.
CREATE FUNCTION pooltune_monitor_role.lookup_plan_summary(raw jsonb)
RETURNS jsonb
LANGUAGE plpgsql IMMUTABLE STRICT SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $summary$
DECLARE
    keys constant text[] := ARRAY['node','estimated_rows','actual_rows','filtered_rows',
                                  'shared_hit_blocks','shared_read_blocks','execution_ms'];
    k text;
    n numeric;
BEGIN
    IF jsonb_typeof(raw)<>'object' OR NOT raw ?& keys OR raw-keys<>'{}'::jsonb
       OR jsonb_typeof(raw->'node') IS DISTINCT FROM 'string' OR raw->>'node' NOT IN ('Seq Scan','Index Scan','Index Only Scan') THEN
        RETURN NULL;
    END IF;
    FOREACH k IN ARRAY keys[2:7] LOOP
        IF jsonb_typeof(raw->k) IS DISTINCT FROM 'number' THEN
            RETURN NULL;
        END IF;
        n := (raw->>k)::numeric;
        IF n<0 OR n>9007199254740991 OR
           (k='execution_ms' AND n>750) OR (k<>'execution_ms' AND trunc(n)<>n) THEN
            RETURN NULL;
        END IF;
    END LOOP;
    RETURN jsonb_build_object('node',raw->>'node',
           'estimated_rows',(raw->>'estimated_rows')::numeric::bigint,
           'actual_rows',(raw->>'actual_rows')::numeric::bigint,
           'filtered_rows',(raw->>'filtered_rows')::numeric::bigint,
           'shared_hit_blocks',(raw->>'shared_hit_blocks')::numeric::bigint,
           'shared_read_blocks',(raw->>'shared_read_blocks')::numeric::bigint,
           'execution_ms',(raw->>'execution_ms')::numeric);
END
$summary$;

CREATE TABLE pooltune_monitor_role.lookup_plans (
    grant_id bigint PRIMARY KEY REFERENCES pooltune_monitor_role.lookup_grants(grant_id) ON DELETE CASCADE,
    captured_at timestamptz NOT NULL,
    boot timestamptz NOT NULL,
    reset_at timestamptz NOT NULL,
    entry_since timestamptz NOT NULL,
    catalog_stamp text NOT NULL,
    sample jsonb NOT NULL CHECK (octet_length(sample::text) <= 2048
        AND pooltune_monitor_role.lookup_plan_summary(sample) IS NOT NULL)
);
REVOKE ALL ON TABLE pooltune_monitor_role.lookup_plans FROM PUBLIC;

CREATE FUNCTION pooltune_monitor_role.lookup_plan_stamp(target oid)
RETURNS text
LANGUAGE sql STABLE SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
SET stats_fetch_consistency = none
AS $stamp$
    SELECT md5(concat_ws('|', pg_relation_filenode(c.oid)::text,
        extract(epoch FROM t.last_analyze)::text, extract(epoch FROM t.last_autoanalyze)::text,
        (SELECT string_agg(concat_ws(':', i.indexrelid, pg_relation_filenode(i.indexrelid),
                    i.indisvalid, i.indisready, i.indislive, pg_get_indexdef(i.indexrelid)),
                ',' ORDER BY i.indexrelid)
         FROM pg_index i WHERE i.indrelid=c.oid)))
    FROM pg_class c JOIN pg_stat_all_tables t ON t.relid=c.oid
    WHERE c.oid=target
$stamp$;

CREATE FUNCTION pooltune_monitor_role.capture_lookup_plan(app_role oid, statement_id bigint, sample_value integer)
RETURNS jsonb
LANGUAGE plpgsql SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
SET max_parallel_workers_per_gather = 0
AS $capture$
DECLARE
    approved record;
    stats_schema name;
    clocks record;
    relation text;
    statement text;
    estimated jsonb;
    measured jsonb;
    plan jsonb;
    stamp text;
BEGIN
    IF current_user<>session_user OR current_setting('transaction_read_only')<>'on'
       OR current_setting('transaction_isolation')<>'read committed'
       OR current_setting('statement_timeout')::interval NOT BETWEEN interval '1ms' AND interval '750ms'
       OR current_setting('transaction_timeout')::interval NOT BETWEEN interval '1ms' AND interval '2s'
       OR sample_value IS NULL THEN
        RAISE EXCEPTION 'capture needs an owner read-only session with 750ms statement and 2s transaction limits';
    END IF;

    SELECT g.grant_id,c.oid AS table_oid,n.nspname,c.relname,a.attname,a.attnum,r.rolname
      INTO STRICT approved
    FROM pooltune_monitor_role.lookup_grants g JOIN pg_class c ON c.oid=g.table_oid
    JOIN pg_namespace n ON n.oid=c.relnamespace
    JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=g.column_number
    JOIN pg_roles r ON r.oid=g.role_oid
    WHERE g.role_oid=app_role AND g.query_id=statement_id
      AND r.rolname<>session_user AND NOT (r.rolsuper OR r.rolbypassrls OR r.rolcreaterole OR r.rolreplication)
      AND pg_has_role(session_user,r.oid,'SET')
      AND a.atttypid='integer'::regtype AND NOT a.attisdropped;

    relation := format('%I.%I',approved.nspname,approved.relname);
    EXECUTE 'LOCK TABLE ONLY ' || relation || ' IN ACCESS SHARE MODE NOWAIT';
    IF to_regclass(relation)::oid IS DISTINCT FROM approved.table_oid OR NOT EXISTS (
      SELECT FROM pg_class c JOIN pg_attribute a ON a.attrelid=c.oid
      WHERE c.oid=approved.table_oid AND c.relkind='r' AND c.relpersistence='p'
        AND c.relam=(SELECT oid FROM pg_am WHERE amname='heap')
        AND NOT (c.relrowsecurity OR c.relispartition OR c.relhasrules)
        AND NOT EXISTS (SELECT FROM pg_inherits WHERE inhrelid=c.oid OR inhparent=c.oid)
        AND a.attnum=approved.attnum AND a.attname=approved.attname AND NOT a.attisdropped
        AND a.atttypid='integer'::regtype AND pg_total_relation_size(c.oid)<=16777216
        AND has_schema_privilege(app_role,c.relnamespace,'USAGE')
        AND has_column_privilege(app_role,c.oid,a.attnum,'SELECT')
        AND NOT EXISTS (
          SELECT FROM pg_index i JOIN pg_class ix ON ix.oid=i.indexrelid
          JOIN pg_am am ON am.oid=ix.relam
          WHERE i.indrelid=c.oid AND (i.indexprs IS NOT NULL OR i.indpred IS NOT NULL OR am.amname<>'btree'
            OR EXISTS (SELECT FROM unnest(i.indclass) AS k(opclass)
                       JOIN pg_opclass op ON op.oid=k.opclass WHERE NOT op.opcdefault OR op.opcnamespace<>'pg_catalog'::regnamespace))
        )
    ) THEN
        RAISE EXCEPTION 'capture target is outside the small ordinary lookup scope';
    END IF;

    SELECT n.nspname INTO STRICT stats_schema
    FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
    WHERE e.extname='pg_stat_statements';
    EXECUTE format(
      'SELECT pg_postmaster_start_time() AS boot,i.stats_reset,s.stats_since
       FROM %1$I.pg_stat_statements(false) s CROSS JOIN %1$I.pg_stat_statements_info i
       JOIN pg_database db ON db.datname=current_database()
       WHERE s.dbid=db.oid AND s.userid=$1 AND s.queryid=$2 AND s.toplevel',stats_schema)
      INTO STRICT clocks USING app_role,statement_id;
    stamp := pooltune_monitor_role.lookup_plan_stamp(approved.table_oid);

    statement := format('SELECT %1$I FROM ONLY %2$s WHERE %1$I OPERATOR(pg_catalog.=) $1',approved.attname,relation);
    EXECUTE format('SET LOCAL ROLE %I',approved.rolname);
    EXECUTE 'EXPLAIN (VERBOSE, FORMAT JSON) ' || statement INTO estimated USING sample_value;
    IF (estimated #>> '{0,Query Identifier}')::bigint IS DISTINCT FROM statement_id THEN
        RAISE EXCEPTION 'planned statement identity differs from the enrolled lookup';
    END IF;
    IF estimated #>> '{0,Plan,Schema}' IS DISTINCT FROM approved.nspname::text
       OR estimated #>> '{0,Plan,Relation Name}' IS DISTINCT FROM approved.relname::text
       OR estimated #> '{0,Plan}' ? 'Plans'
       OR estimated #>> '{0,Plan,Node Type}' NOT IN ('Seq Scan','Index Scan','Index Only Scan') THEN
        RAISE EXCEPTION 'planned scan shape is outside capture scope';
    END IF;
    EXECUTE 'EXPLAIN (ANALYZE, BUFFERS, VERBOSE, FORMAT JSON, TIMING OFF) ' || statement
      INTO measured USING sample_value;
    RESET ROLE;

    plan := measured #> '{0,Plan}';
    IF (measured #>> '{0,Query Identifier}')::bigint IS DISTINCT FROM statement_id
       OR plan->>'Schema' IS DISTINCT FROM approved.nspname::text
       OR plan->>'Relation Name' IS DISTINCT FROM approved.relname::text
       OR plan->>'Node Type' NOT IN ('Seq Scan','Index Scan','Index Only Scan')
       OR plan ? 'Plans' OR (plan->>'Actual Loops')::numeric IS DISTINCT FROM 1
       OR stamp IS DISTINCT FROM pooltune_monitor_role.lookup_plan_stamp(approved.table_oid) THEN
        RAISE EXCEPTION 'capture could not prove the exact enrolled single-scan plan';
    END IF;
    RETURN jsonb_build_object('grant_id',approved.grant_id,'captured_at',clock_timestamp(),
       'boot',clocks.boot,'reset_at',clocks.stats_reset,'entry_since',clocks.stats_since,'catalog_stamp',stamp,
       'sample',jsonb_build_object('node',plan->'Node Type','estimated_rows',plan->'Plan Rows',
           'actual_rows',plan->'Actual Rows','filtered_rows',COALESCE(plan->'Rows Removed by Filter','0'::jsonb),
           'shared_hit_blocks',plan->'Shared Hit Blocks','shared_read_blocks',plan->'Shared Read Blocks',
           'execution_ms',measured #> '{0,Execution Time}'));
END
$capture$;

CREATE FUNCTION pooltune_monitor_role.publish_lookup_plan(receipt jsonb)
RETURNS void
LANGUAGE sql SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $publish$
    INSERT INTO pooltune_monitor_role.lookup_plans AS p
        SELECT grant_id,captured_at,boot,reset_at,entry_since,catalog_stamp,sample
        FROM jsonb_to_record(receipt) AS r(
            grant_id bigint,captured_at timestamptz,boot timestamptz,reset_at timestamptz,
            entry_since timestamptz,catalog_stamp text,sample jsonb)
        WHERE captured_at BETWEEN clock_timestamp()-interval '2 minutes' AND clock_timestamp()
    ON CONFLICT (grant_id) DO UPDATE
        SET captured_at=excluded.captured_at,boot=excluded.boot,reset_at=excluded.reset_at,
            entry_since=excluded.entry_since,catalog_stamp=excluded.catalog_stamp,sample=excluded.sample
        WHERE excluded.captured_at>p.captured_at
$publish$;

CREATE FUNCTION pooltune_monitor_role.read_lookup_plan(app_role oid, statement_id bigint)
RETURNS TABLE (captured_at timestamptz, sample jsonb)
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $reader$
    SELECT p.captured_at,typed.sample
    FROM pooltune_monitor_role.read_lookup(app_role,statement_id) lookup
    JOIN pooltune_monitor_role.lookup_plans p USING(grant_id,boot,reset_at,entry_since)
    CROSS JOIN LATERAL (SELECT pooltune_monitor_role.lookup_plan_summary(p.sample) AS sample) typed
    WHERE typed.sample IS NOT NULL AND p.captured_at BETWEEN clock_timestamp()-interval '2 minutes' AND clock_timestamp()
      AND p.catalog_stamp=pooltune_monitor_role.lookup_plan_stamp(lookup.table_oid)
      AND has_function_privilege(session_user,'pooltune_monitor_role.read_lookup(oid,bigint)','EXECUTE')
$reader$;

REVOKE ALL ON FUNCTION pooltune_monitor_role.lookup_plan_summary(jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION pooltune_monitor_role.lookup_plan_stamp(oid) FROM PUBLIC;
REVOKE ALL ON FUNCTION pooltune_monitor_role.capture_lookup_plan(oid,bigint,integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION pooltune_monitor_role.publish_lookup_plan(jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION pooltune_monitor_role.read_lookup_plan(oid,bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION pooltune_monitor_role.read_lookup_plan(oid,bigint) TO pooltune_monitor_role;

DO $grants$
BEGIN
    IF EXISTS (
        SELECT FROM pg_class c, LATERAL aclexplode(c.relacl) a
        WHERE c.oid='pooltune_monitor_role.lookup_plans'::regclass AND a.grantee<>c.relowner
    ) OR EXISTS (
        SELECT FROM pg_proc p, LATERAL aclexplode(p.proacl) a
        WHERE p.oid IN ('pooltune_monitor_role.lookup_plan_stamp(oid)'::regprocedure,
                       'pooltune_monitor_role.lookup_plan_summary(jsonb)'::regprocedure,
                       'pooltune_monitor_role.capture_lookup_plan(oid,bigint,integer)'::regprocedure,
                       'pooltune_monitor_role.publish_lookup_plan(jsonb)'::regprocedure,
                       'pooltune_monitor_role.read_lookup_plan(oid,bigint)'::regprocedure)
          AND a.grantee<>p.proowner
          AND NOT (p.oid='pooltune_monitor_role.read_lookup_plan(oid,bigint)'::regprocedure
                   AND a.grantee='pooltune_monitor_role'::regrole)
    ) THEN
        RAISE EXCEPTION 'unexpected default privileges on lookup plan objects';
    END IF;
END
$grants$;
COMMIT;
