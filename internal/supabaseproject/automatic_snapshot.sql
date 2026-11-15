RETURN QUERY
            WITH db AS (
                SELECT d.oid FROM pg_database d WHERE d.datname = current_database()
            ), manual AS MATERIALIZED (
                SELECT e.slot, e.role_oid, e.query_id, e.mean_execution_ms
                FROM pooltune_monitor_role.enrolled_queries e
                JOIN pg_roles app ON app.oid = e.role_oid AND app.rolname <> session_user
            ), counters AS MATERIALIZED (
                SELECT s.userid, s.queryid, s.stats_since, s.calls, s.total_exec_time,
                       s.shared_blks_hit + s.shared_blks_read AS blocks, s.query
                FROM db
                JOIN %1$I.pg_stat_statements((SELECT count(*) < 20 FROM manual)) s ON s.dbid = db.oid
                WHERE s.toplevel AND s.queryid <> 0
            ), roles AS MATERIALIZED (
                SELECT r.oid, r.rolname, r.rolcanlogin,
                       (r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolbypassrls OR r.rolreplication
                        OR r.rolname ~ '^(pg_|supabase_|pt_monitor_|pooltune_)'
                        OR r.rolname IN ('postgres', 'authenticator', 'dashboard_user', 'service_role', 'pgbouncer', 'supavisor')) AS excluded
                FROM pg_roles r
            ), app_roles AS MATERIALIZED (
                SELECT r.oid
                FROM roles r
                WHERE NOT r.excluded
                  AND (r.rolcanlogin OR r.rolname IN ('anon', 'authenticated'))
                  AND NOT EXISTS (
                      SELECT FROM roles privileged
                      WHERE privileged.excluded AND pg_has_role(r.oid, privileged.oid, 'MEMBER')
                  )
            ), ranked AS (
                SELECT s.userid, s.queryid,
                       row_number() OVER (ORDER BY s.total_exec_time DESC, s.userid, s.queryid) AS impact_rank,
                       row_number() OVER (ORDER BY s.stats_since DESC, s.userid, s.queryid) AS newest_rank
                FROM counters s
                JOIN app_roles app ON app.oid = s.userid
                WHERE s.calls > 0
                  AND NOT EXISTS (SELECT FROM manual e WHERE e.role_oid = s.userid AND e.query_id = s.queryid)
                  -- Classify representative text inside PostgreSQL. It never leaves this reader.
                  AND octet_length(s.query) BETWEEN 1 AND 65536
                  AND s.query ~* $pattern$^[[:space:]]*(select|with|insert|update|delete|merge)\M$pattern$
                  AND s.query ~* $pattern$\m(from|insert|update|merge)\M$pattern$
                  AND s.query !~* $pattern$\m(from|join)\M[[:space:](]+(only[[:space:](]+)?"?(pg_[[:alnum:]_]+|information_schema|pt_monitor_[[:alnum:]_]+)\M$pattern$
                  AND s.query !~* $pattern$\m(pg_stat[[:alnum:]_]*|pg_locks|pg_is_in_recovery|pg_last_[[:alnum:]_]*|set_config)\M$pattern$
                  -- Every INTO must belong to an INSERT or MERGE. SELECT INTO is a utility command.
                  AND (s.query !~* $pattern$\minto\M$pattern$ OR (
                      s.query ~* $pattern$^[[:space:]]*(insert|merge|with)\M$pattern$
                      AND regexp_count(s.query, $pattern$\minto\M$pattern$, 1, 'i') =
                          regexp_count(s.query, $pattern$(^|[()])[[:space:]]*(insert|merge)[[:space:]]+into\M$pattern$, 1, 'i')
                  ))
            ), selected AS (
                SELECT e.slot, e.role_oid, e.query_id, e.mean_execution_ms FROM manual e
                UNION ALL
                (
                    -- stats_since is the entry's start time. Counter deltas establish actual activity.
                    SELECT 21::smallint, a.userid, a.queryid, 1000::double precision
                    FROM ranked a
                    ORDER BY least(a.impact_rank, a.newest_rank * 3), a.impact_rank
                    LIMIT (20 - (SELECT count(*) FROM manual))
                )
            )
            SELECT db.oid, e.role_oid, e.query_id, e.mean_execution_ms,
                   pg_postmaster_start_time(), info.stats_reset, s.stats_since,
                   s.calls, s.total_exec_time, s.blocks
            FROM db
            CROSS JOIN selected e
            CROSS JOIN %1$I.pg_stat_statements_info info
            LEFT JOIN counters s ON s.userid = e.role_oid AND s.queryid = e.query_id
            ORDER BY e.slot, e.role_oid, e.query_id;
