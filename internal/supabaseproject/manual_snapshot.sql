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
