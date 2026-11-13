# Ground Pooltune tools in measurable evidence

**Supabase exposes the observations needed to investigate the six accepted repairs, but through several distinct interfaces.** PostgreSQL statement counters identify expensive query patterns; catalogs describe indexes and table state; execution plans explain where work occurs; host metrics and logs help distinguish competing causes. The agent should receive bounded, structured evidence tools rather than arbitrary SQL access. Rust performs collection, arithmetic, permissions and controlled execution; the TypeScript/LangGraph investigator selects permitted investigations; Pooltune's PostgreSQL stores selected history. This preserves the [accepted six-repair plan](</Users/jshum/Desktop/code-folders/pooltune-learning/docs/product/query-performance-v1.md>). The largest unresolved input is representative query parameters and application-role context, which aggregate telemetry does not reconstruct.

Research checked November 12, 2026 against public API schemas and first-party source. No customer project, API response, SQL template or repair was tested. Schemas below are proposed contracts, not implemented endpoints.

## Three public interfaces have different guarantees

All paths below use `https://api.supabase.com`, Bearer authorization, and a project `ref` constrained to 20 lowercase letters. The saved [OpenAPI excerpt](</Users/jshum/Desktop/Tech Stuff/Resumes/softEng/research_notes/Pooltune Supabase evidence tool contracts/management-api-evidence.json>) records the retrieval date and full-spec SHA256 `91415a57fbfa38fbb5477422f1d6cf3049d856532c6c468cfb386dfc1e119e61`. ([Public specification](https://api.supabase.com/api/v1-json))

| Interface | Request and authority | Response contract and gaps |
|---|---|---|
| `POST /v1/projects/{ref}/database/query/read-only` | Body `{"query":"…","parameters":[…]}`; nonempty query required, parameters optional with unconstrained items. OAuth `database:read` / permission `database_read`; executes as `supabase_read_only_user`. Schema-qualified entities required. | Beta; success **201**, without a documented response-content schema. Failures 401/403/429/500. No declared timeout, cursor, row cap or SQLSTATE error envelope. ([Reference](https://supabase.com/docs/reference/api/v1-read-only-query)) |
| `GET /v1/projects/{ref}/analytics/endpoints/metrics` | OAuth `analytics:read` / `analytics_logs_read`; current scrape, no history/time-range parameters. | **200** text/plain or application/openmetrics-text. 400 includes inactive/unhealthy/unavailable metrics; also 401/403/429/500. ([Reference](https://supabase.com/docs/reference/api/v1-scrape-project-metrics)) |
| `GET /v1/projects/{ref}/analytics/endpoints/logs` | Same analytics authority; query parameters `sql`, `iso_timestamp_start`, `iso_timestamp_end`. Current contract requires ClickHouse SQL on unified logs, filtered by `source`. | **200** JSON `result` array with optional `error` string/object. Both timestamps omitted means last minute; supplied interval at most 24 hours, rounded to minutes. Failures include 401/402/403/429; no declared cursor. Old `/logs.all` returns 410. ([Reference](https://supabase.com/docs/reference/api/v1-get-project-logs)) |

**Reading a statistical view does not guarantee visibility of application query identities.** Qualify the HTTP identity before relying on it. A separately authorized native PostgreSQL connection is an alternative for evidence and is needed for operations requiring appropriate SQL authority/session behavior. An app publishable key is not generic administrative SQL access. No documented route above grants repair permission merely by existing. ([Connection methods](https://supabase.com/docs/guides/database/connecting-to-postgres))

Rust should own fixed parameterized queries, selected columns, object scope, deadlines and response budgets. The model selects an evidence request. It does not submit connection strings or arbitrary SQL. Public specifications leave actual byte limits, execution limits, cancellation behavior and numeric rate quotas unresolved; these require probes and configurable budgets.

## Statement counters identify costly work without copying rows

`pg_stat_statements` groups similar SQL statements. Discover its namespace, installed extension interface and visible fields rather than assuming `extensions` or a server-version-only schema. Identity is `(dbid, userid, queryid, toplevel)`, wrapped in Pooltune project and source-instance identity. `queryid` and 64-bit counters must survive JSON/TypeScript as decimal strings. Other users' text and identifiers require suitable monitoring privileges; hidden NULL identifiers must not be merged into one “query.” ([Supabase extension](https://supabase.com/docs/guides/database/extensions/pg_stat_statements), [PG17 reference](https://www.postgresql.org/docs/17/pgstatstatements.html))

| Fields | Units and useful interpretation | Important limit |
|---|---|---|
| `calls`, `total_exec_time` | Completed executions; cumulative milliseconds | Not all attempted requests, CPU time, or HTTP duration |
| `rows` | Returned/affected rows | Not rows examined |
| `shared_blks_hit/read/dirtied/written`, corresponding `local_blks_*` | Database blocks | Shared reads can still hit the OS cache; not guaranteed physical disk reads |
| `temp_blks_read`, `temp_blks_written` | Temporary blocks | Indicates intermediate work, not which plan node caused it |
| `wal_records`, `wal_fpi`, `wal_bytes` | Counts and bytes of write-log activity | Context for write overhead, not authority to tune WAL |
| `min/mean/max/stddev_exec_time` | Milliseconds over their accumulation epoch | Cannot reconstruct p95 or subtract maxima to obtain an interval maximum |
| Planning and I/O timing fields | Milliseconds when tracking is available | Disabled/uncertain coverage is unknown, not zero cost |

The execution counters update on successful completion; failures need separate error evidence. ([PG15 field definitions](https://www.postgresql.org/docs/15/pgstatstatements.html))

Interface 1.11 adds per-entry `stats_since`, `minmax_stats_since` and separates shared/local block timings; bundled PG15 uses 1.10, while PG18's 1.12 adds parallel-worker and WAL-buffer fields. Installed columns govern the adapter. `pg_stat_statements_info` supplies global `stats_reset` and `dealloc`. ([1.11 source](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/contrib/pg_stat_statements/pg_stat_statements--1.10--1.11.sql), [1.12 source](https://raw.githubusercontent.com/postgres/postgres/REL_18_STABLE/contrib/pg_stat_statements/pg_stat_statements--1.11--1.12.sql))

For consecutive valid samples, Rust computes:

```text
completed_calls = calls_B - calls_A
execution_ms = total_exec_time_B - total_exec_time_A
mean_execution_ms = execution_ms / completed_calls
calls_per_second = completed_calls / elapsed_seconds
shared_blocks_per_call = (delta_hits + delta_reads) / completed_calls
temp_bytes_written = delta_temp_blks_written * observed_block_size
```

Divide only when calls are positive. Reject or mark partial windows spanning resets, disappeared/recreated entries, decreasing counters or incomplete collection. Older interfaces cannot reliably expose every targeted reset when counters subsequently overtake the previous sample. A `dealloc` increase warns about coverage, not which query disappeared. Never reset customer counters to simplify comparisons. The implementation also shows that `showtext=false` avoids text loading but still iterates/materializes entries; SQL `LIMIT` bounds output, not all collection work. ([Extension implementation](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/contrib/pg_stat_statements/pg_stat_statements.c))

An illustrative, **unexecuted** interface-1.11 template follows. The namespace placeholder must be replaced using discovered, safely quoted metadata:

```sql
SELECT statement_timestamp() AS observed_at,
       s.dbid, s.userid, s.queryid::text, s.toplevel,
       s.stats_since, s.minmax_stats_since,
       s.calls::text, s.total_exec_time,
       s.rows::text, s.shared_blks_hit::text,
       s.shared_blks_read::text, s.temp_blks_written::text,
       s.wal_bytes::text
FROM "<extension_schema>".pg_stat_statements(false) AS s
WHERE s.dbid = (
  SELECT oid FROM pg_catalog.pg_database
  WHERE datname = current_database()
);
```

Read global epoch information around collection, record start/end times, and use short transactions. This is not an atomic snapshot of every changing counter. Routine polling omits SQL text; a separate investigation tool retrieves selected definitions. Default tracking omits function-internal statements, and query normalization does not preserve replay parameters. Query text can still retain sensitive literals. ([PG17 reference](https://www.postgresql.org/docs/17/pgstatstatements.html))

## Host metrics and logs distinguish alternative explanations

Supabase's pinned exporter sample establishes these metric names, with example labels, not their availability on a connected project. Its sample values are placeholders. Preserve actual labels and instance identity rather than combining unrelated services or replicas. ([Pinned metric exposition](https://github.com/supabase/supabase-grafana/blob/04469b485b758e3a28bdea5239f540a4c3604603/metrics.md))

| Source series | Type/unit and dimensions | Proposed derivation |
|---|---|---|
| `node_cpu_seconds_total` | Counter, seconds; `cpu`, `mode`, `service_type` | `100 × (1 − mean_cpu(Δidle/Δwall))`; non-idle includes iowait/steal, not query CPU |
| `node_memory_MemAvailable_bytes`, `node_memory_MemTotal_bytes` | Byte gauges; service/project labels | `100 × (1 − available/total)`; not PostgreSQL allocation or a work_mem budget |
| `node_disk_read_bytes_total`, `node_disk_written_bytes_total` | Byte counters; `device` | Delta/seconds; throughput alone does not prove saturation |
| `node_filesystem_avail_bytes`, `node_filesystem_size_bytes` | Byte gauges; `mountpoint`, device/filesystem dimensions | Capacity for the discovered data filesystem; avoid duplicate mount counting |
| `supavisor_connections_active` | Sample declares untyped; instantaneous connections; `db_name`, `mode`, `user` | Preserve pool dimensions; not query rate or physical backend count |

The pinned dashboard additionally references `pg_stat_database_num_backends`, `supavisor_pool_connections_checked_out` and `supavisor_pool_connections_idle`. Dashboard usage does not establish exporter TYPE or universal labels. The public PostgreSQL exporter service disables defaults and loads custom queries unavailable in the inspected tree, so generic exporter names must not be assumed. ([Pinned dashboard](https://github.com/supabase/supabase-grafana/blob/04469b485b758e3a28bdea5239f540a4c3604603/grafana/dashboard.json), [Exporter service](https://github.com/supabase/postgres/blob/16f89701ccd464d80204b7718a0914d04a67c666/ansible/files/postgres_exporter.service.j2))

A scrape is a snapshot. Historical rates require retained samples, reset checks and sufficient coverage. `unavailable`, `denied`, `stale`, `parse_failed` and `insufficient_samples` remain distinct from zero. A proposed collection interval is a budget decision, not an API guarantee. Connection metrics provide diagnosis only; pool resizing remains excluded.

For errors, current Studio source selects top-level `id`, `timestamp`, `source`, `severity_text`, `event_message`, and map `log_attributes`. PostgreSQL status is **`log_attributes['parsed.sql_state_code']`**; HTTP status is `log_attributes['response.status_code']`. SQLSTATE such as `40P01` remains a string. The source inventory also names parsed query ID, process ID, database and user fields, but individual events need not contain them. ([Pinned query source](https://github.com/supabase/supabase/blob/b6fdee5f5437e19b2d2b14c83e9d61eb4039b529/apps/studio/components/interfaces/UnifiedLogs/UnifiedLogs.queries.ts), [Field inventory](https://github.com/supabase/supabase/blob/b6fdee5f5437e19b2d2b14c83e9d61eb4039b529/packages/shared-data/log-constants.ts))

Compile a fixed ClickHouse query for `source='postgres_logs'`, selected severity/SQLSTATE fields, timestamp/id ordering and explicit LIMIT. Return counts and limited redacted examples. Validate body-level `error` even on HTTP 200; handle bounded subwindows, deduplication, late ingestion and missing retention. Empty logs do not prove no failures. Timestamp proximity alone does not map an error to a statement fingerprint.

Database context complements these sources: `pg_stat_database` exposes `numbackends`, transaction counts, deadlocks, temp files/bytes and its own reset epoch. `pg_stat_activity` supplies state, transaction/query start, wait classification and visible `query_id`; identify sessions by `(pid, backend_start)`. A current wait is a sample, not historical duration. Use capped `pg_blocking_pids` calls only when needed, since polling every backend adds lock-manager overhead. ([Database statistics](https://www.postgresql.org/docs/15/monitoring-stats.html#MONITORING-PG-STAT-DATABASE-VIEW), [Activity fields](https://www.postgresql.org/docs/17/monitoring-stats.html), [Blocking function](https://www.postgresql.org/docs/17/functions-info.html#FUNCTIONS-INFO-SESSION-TABLE))

## Catalogs and plans turn signals into repair hypotheses

An expensive query is an investigation candidate. The following targeted reads establish what exists and what PostgreSQL believes about it:

| Source | Exact selected fields | Interpretation |
|---|---|---|
| `pg_index` | `indexrelid`, `indrelid`, `indnkeyatts`, `indnatts`, `indkey`, `indclass`, `indcollation`, `indoption`, `indexprs`, `indpred`; uniqueness/primary/exclusion/replica flags; `indisvalid/ready/live` | Distinguish search keys, INCLUDE values, predicates, expressions, ordering and constraint purpose. Names alone cannot detect equivalence. ([Catalog](https://www.postgresql.org/docs/17/catalog-pg-index.html)) |
| `pg_class`, size functions | `oid`, `relowner`, `relkind`, `reltuples`, `relpages`, `relallvisible`, `reloptions`, `relrowsecurity`, `relforcerowsecurity`; table/index/total bytes | Row/page/visibility estimates versus actual storage measurements; partition descendants need explicit handling. ([Catalog](https://www.postgresql.org/docs/17/catalog-pg-class.html), [Size functions](https://www.postgresql.org/docs/17/functions-admin.html)) |
| `pg_attribute`, `pg_stats` | `attnum`, `atttypid`, `attstattarget`; `null_frac`, `avg_width`, `n_distinct`, frequencies, `correlation` | Per-column distribution/detail; correlation describes physical ordering, not relationships between columns. ([Attributes](https://www.postgresql.org/docs/17/catalog-pg-attribute.html), [Statistics](https://www.postgresql.org/docs/17/view-pg-stats.html)) |
| `pg_stats_ext` | `statistics_name`, `attnames`, `exprs`, `kinds`, `n_distinct`, `dependencies`, frequencies | Existing multicolumn information; owner-restricted visibility means absent output can be a permission gap. ([View](https://www.postgresql.org/docs/17/view-pg-stats-ext.html)) |
| Table/index statistics | `n_live_tup`, `n_dead_tup`, `n_mod_since_analyze`, modification counters, `last_analyze/autoanalyze/vacuum/autovacuum`; index `idx_scan`, `idx_tup_read/fetch`, block hits/reads | Trends need valid snapshots. Estimates and zero scans do not justify maintenance or deletion. ([Exact view source](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/backend/catalog/system_views.sql)) |

`EXPLAIN (FORMAT JSON)` provides estimates. Measured execution adds `ANALYZE`; `BUFFERS` exposes access work, while `TIMING OFF` reduces per-node timing overhead. `GENERIC_PLAN` cannot combine with ANALYZE. Planner cost is not milliseconds. ([EXPLAIN contract](https://www.postgresql.org/docs/17/sql-explain.html))

Exact useful JSON keys include `Node Type`, `Plan Rows`, `Actual Rows`, `Actual Loops`, `Rows Removed by Filter`, `Heap Fetches`, `Shared Read Blocks`, `Temp Written Blocks`, `Sort Method`, `Sort Space Type`, `Hash Batches`, `Planning Time` and `Execution Time`. Fields depend on node/options; missing is not zero. Rows/times can be per-loop averages, and parent-child totals overlap. ([PG17 implementation](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/backend/commands/explain.c), [Interpretation](https://www.postgresql.org/docs/17/using-explain.html))

HypoPG screens session-local hypothetical indexes through non-ANALYZE plans. Supabase's pinned advisor enumerates single-column candidates, so Pooltune must construct bounded compound-index candidates itself. Neither proves measured improvement. ([HypoPG](https://supabase.com/docs/guides/database/extensions/hypopg), [Pinned advisor](https://raw.githubusercontent.com/supabase/index_advisor/a6c959599357605cfdf262f96d30810b3201aa91/index_advisor--0.2.0--0.2.1.sql))

Measured replay needs an enrolled query, parameter types/representative values, effective role, claims, search path and planning settings. PostgREST establishes this context per request. Normalized text is not executable replay context; administrator execution is not representative. Even plan-only evaluation of arbitrary user functions is not a universal sandbox. ([PostgREST context](https://docs.postgrest.org/en/stable/references/transactions.html), [Function planning behavior](https://www.postgresql.org/docs/17/xfunc-volatility.html))

## Narrow contracts support exactly the six repairs

Every result should carry evidence ID, project/instance identity, source/version, collection interval, freshness, units, reset validation, completeness and missing-evidence reasons. Rust binds project authority to the investigation; the model cannot substitute another project or arbitrary role.

| Proposed tool | Bounded input | Structured output/effect |
|---|---|---|
| `get_query_performance_window` | Window, optional query keys, allowed ranking, limit | Validated per-query deltas and baseline comparison; no SQL text |
| `get_query_definition` | Exact key and evidence reference | Sanitized text, digest, parameter/context availability; disappearance or hidden identity is a blocker |
| `get_resource_context` / `get_database_pressure_context` | Window; optional bounded current blockers | Source-labeled host/database measures and separately timestamped waits |
| `get_error_context` | Bounded interval, permitted sources, row cap | SQLSTATE summaries, limited sanitized events, coverage |
| `inspect_relation` | Authorized OID, evidence sections, budget | Index definitions, sizes, distribution/maintenance summaries; no repair verdict |
| `explain_candidate` | Approved query/context, optional index specification, budget | Estimated plan and candidate usage; no persistent index |
| `test_candidate` | Qualified candidate, representative cases, explicit environment, budgets | Measured comparisons, result/role coverage, concurrent-write/resource effects, inconclusive reasons |
| `repair_progress` | Action and expected target/backend identity | Phase-specific progress plus final-state reconciliation; no cancellation |

Illustrative output with **fictional values**, not an observed project:

```json
{
  "status": "partial",
  "queryid": "7541123456789012345",
  "completedCalls": "1200",
  "executionMs": 360000,
  "meanExecutionMs": 300,
  "sharedBlocksPerCall": 2400,
  "tempBytesWritten": "0",
  "ioReadMs": null,
  "epochValidated": true,
  "missingEvidence": ["representative_parameters", "application_role_context"],
  "warnings": ["No p95 measurement", "I/O timing coverage unknown"]
}
```

| Accepted repair | Evidence needed beyond expensive-query detection |
|---|---|
| Nonunique index | Mismatched access path, representative plan/candidate benefit, acceptable neighboring reads/writes/storage |
| Targeted ANALYZE | Modification/history evidence and estimate error attributable to outdated information |
| Richer statistics | Fresh statistics still miss skew or same-table relationships; measured improvement from added detail |
| VACUUM / cleanup threshold | Cleanup-related access/backlog; distinguish late triggering from pinned rows or insufficient resources |
| Function-local work_mem | Exact enrolled function, internal sort/hash spill, necessary work and concurrent memory budget |
| Future Cron rescheduling | Enrolled job history, repeat interference evidence, owner-approved windows/deadlines |

Function evidence uses `pg_proc` identity/signature, owner and `proconfig`. Cron history exposes `jobid`, `runid`, `status`, `start_time`, `end_time` and related fields, subject to owner visibility. Progress views expose phase and phase-local counters; a missing progress row does not establish completion. ([Function catalog](https://www.postgresql.org/docs/17/catalog-pg-proc.html), [Pinned Cron schema](https://raw.githubusercontent.com/citusdata/pg_cron/5cedfa472ccc83567aa23ec645925ed8489a7797/pg_cron--1.2--1.3.sql), [Progress views](https://www.postgresql.org/docs/17/progress-reporting.html))

Catalog definitions, plans, logs and distribution values can contain customer literals. Default collection projects numeric metadata; selective definitions receive redaction and retention controls. This requires no copy of customer application tables. Read tools, experiments and separately authorized repairs remain distinct. Pool resizing, WAL tuning, index rebuilding, customer-index deletion, code/policy rewrites and job cancellation stay excluded.

## Qualification must close visibility and replay gaps

The minimum connection probe must establish the actual transport response/limits, SQL identity and grants, server and extension versions/namespaces/columns, visible cross-role query IDs, tracking settings, block size, and reset behavior. It must discover real metric names/types/labels and log field presence, then test bounded catalog, plan, HypoPG-session and progress access. Mutation authority is a separate per-repair qualification. All templates remain unexecuted until that work is authorized and performed.

The decisive remaining product input is **how an enrolled query obtains representative parameters and access-role context**. Without it, Pooltune can detect expensive work and produce useful hypotheses, but cannot honestly certify a candidate across real customers. Define that evidence contract next, then prove one index case from discovery through outcome and failure recovery before expanding qualification across the other five repairs.
