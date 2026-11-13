# Query telemetry contracts for Pooltune

Research date: 2026-11-12. Documentation/source inspection only. SQL and schemas below are unexecuted design templates, not tested integrations. No customer access or changes occurred. Proposed tools support the existing six repairs; they do not expand repair authority.

## 1. Which query measurements actually exist?

### Takeaway

Use PostgreSQL's numeric statement counters for discovery and regression detection. They identify expensive query families, not the cause of slowness or proof that an index will help. The collector must discover extension capabilities before selecting a SQL template.

### Cited Findings

- Supabase documents `pg_stat_statements` and shows installing it in `extensions`; do not assume that namespace or installation state for every customer. [Supabase extension reference](https://supabase.com/docs/guides/database/extensions/pg_stat_statements)
- Statement identity is `(dbid, userid, queryid, toplevel)`. Other roles' query identifiers and text require superuser or `pg_read_all_stats` privileges. Query hashes have limited stability; normalized text can retain literal values. Default tracking covers top-level statements, not function-internal statements. Planning tracking is off by default and enabling it can impose contention overhead. [PG17 extension reference](https://www.postgresql.org/docs/17/pgstatstatements.html)
- Execution counters include `calls`, `total_exec_time` in milliseconds, `rows` retrieved/affected, shared/local block hits/reads/dirties/writes, temporary block reads/writes, and WAL bytes/records. Planning and execution update at successful completion of their respective phases; a failed execution is not an execution-count error event. `pg_stat_statements_info` exposes global `stats_reset` and `dealloc`, the number of entry-deallocation events. [PG15 reference](https://www.postgresql.org/docs/15/pgstatstatements.html)
- PG15's bundled extension version is 1.10. PG17's 1.11 SQL interface adds `stats_since`, `minmax_stats_since`, separates shared/local block timing, and adds the `minmax_only` reset argument. Query output shape follows the installed extension interface, not only server major version. [PG15 control source](https://raw.githubusercontent.com/postgres/postgres/REL_15_STABLE/contrib/pg_stat_statements/pg_stat_statements.control), [1.11 SQL definition](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/contrib/pg_stat_statements/pg_stat_statements--1.10--1.11.sql)
- PG18's bundled default is 1.12. Its SQL definition adds `wal_buffers_full`, `parallel_workers_to_launch`, and `parallel_workers_launched`. These are optional discovery fields, not V1 prerequisites. [PG18 control](https://raw.githubusercontent.com/postgres/postgres/REL_18_STABLE/contrib/pg_stat_statements/pg_stat_statements.control), [1.12 definition](https://raw.githubusercontent.com/postgres/postgres/REL_18_STABLE/contrib/pg_stat_statements/pg_stat_statements--1.11--1.12.sql)
- The actual C implementation accepts `showtext=false`, avoids query-text loading in that branch, and iterates/materializes the statistics hash table. Its reset logic can remove selected entries without necessarily changing the global reset epoch. Thus SQL `LIMIT` bounds returned data, not extension iteration work. [Extension implementation: pg_stat_statements_internal and entry_reset](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/contrib/pg_stat_statements/pg_stat_statements.c)
- `track_io_timing` controls timing measurements and itself has instrumentation overhead. It can be changed per session with sufficient privileges. Therefore the collector's current setting is not proof of complete timing coverage across all historical application sessions. [Runtime statistics settings](https://www.postgresql.org/docs/17/runtime-config-statistics.html)

### Inferences

Recommended capability probe: `server_version_num`, extension namespace/version, actual view columns/function signature, `compute_query_id`, `pg_stat_statements.track`, `track_planning`, `track_io_timing`, `block_size`, available permissions, and source-instance identity. Values unavailable through permissions should remain unavailable. Crucially, read-only SQL transport does not imply cross-role fingerprint visibility. If `queryid` is NULL for application roles, stable per-query monitoring is unavailable even if numeric rows can be read. Do not merge these rows under a NULL identity or claim fingerprint monitoring works. Verify an authorized monitoring-role path separately.

Preserve queryid and bigint counters as decimal strings through JSON/TypeScript. Do not round 64-bit identities through JavaScript Number. Include Pooltune project and instance identity outside the PostgreSQL tuple to avoid crossing customers/restores/replicas.

Important interpretations:

| Field | Use | Do not claim |
|---|---|---|
| `calls`, `total_exec_time` | Frequency and aggregate execution burden | All attempted requests, CPU time, or end-to-end HTTP latency |
| `rows` | Returned/affected rows | Rows examined or filtered out |
| shared blocks hit/read | PostgreSQL buffer work | Guaranteed physical disk I/O; OS caching can intervene |
| temp blocks written | Disk-backed intermediate work signal | Exact offending plan node or necessary memory setting |
| WAL bytes | Write side-effect context | Autonomous WAL tuning authority |
| mean/min/max/stddev | Distribution summaries over their accumulation epoch | p95/p99 or a new-window maximum obtained by subtraction |

Proposed unexecuted PG17/ext1.11 polling template, after discovering and safely quoting the extension namespace. `"<extension_schema>"` is a replacement placeholder, not a real namespace. Run under the actually authorized collector role; a generic read-only role is not assumed sufficient:

```sql
SELECT statement_timestamp() AS observed_at,
       s.dbid, s.userid, s.queryid::text, s.toplevel,
       s.stats_since, s.minmax_stats_since,
       s.calls::text, s.total_exec_time,
       s.rows::text, s.shared_blks_hit::text,
       s.shared_blks_read::text, s.shared_blks_dirtied::text,
       s.shared_blks_written::text,
       s.temp_blks_read::text, s.temp_blks_written::text,
       s.wal_bytes::text
FROM "<extension_schema>".pg_stat_statements(false) AS s
WHERE s.dbid = (SELECT oid FROM pg_database WHERE datname = current_database());
```

Read the same discovered namespace's `pg_stat_statements_info` before and after the scan to catch a concurrent global reset. Timestamp collection start/end locally as well. This is not a transactional snapshot of all counters. Never reset customer counters merely to calculate intervals.

Derive over valid consecutive samples of the same identity and epoch:

```text
completed_calls = calls_B - calls_A
execution_ms = total_exec_time_B - total_exec_time_A
mean_execution_ms = execution_ms / completed_calls  [only if calls > 0]
calls_per_second = completed_calls / elapsed_seconds
shared_blocks_per_call = (delta_hits + delta_reads) / completed_calls
temp_bytes_written = delta_temp_blks_written * observed_block_size
wal_bytes_per_call = delta_wal_bytes / completed_calls
```

If an epoch changes, an entry disappears/reappears, counters decrease, or collection is incomplete, mark the interval invalid or partial and establish another baseline. New entries with a known stats_since inside the interval can be represented as partial intervals, explicitly. PG15/ext1.10 cannot reliably detect every targeted reset or eviction/recreation: counters may grow past the old value before the next poll. A global dealloc increase is a coverage warning, not proof that a particular query was evicted. For ext1.11, retain per-entry stats_since; keep the separate minmax epoch for extrema.

Polling design proposal: collect bounded numeric history without query text; rank by interval execution burden and worsening per-call cost. Fetch representative text only for selected investigations through a different permissioned tool and sanitize it before storage/model use. Cumulative top-N alone will favor historically busy queries and can miss new regressions. If full numeric scans exceed budget, disclose partial coverage and use an enrolled-query set plus periodic discovery. Filtering an SRF result is not a server-side index lookup.

### Gaps

No real Supabase project was inspected. Installed extension version, configured tracking, row visibility, timings coverage, overhead, and collection cadence remain unverified. Query counters contain neither a complete error stream nor parameter distributions, histograms, CPU attribution, or a plan history. A collector observing zero timing with disabled/uncertain coverage must expose null/unknown, not “zero I/O latency.”

## 2. What context can distinguish a bad index from blocking or general load?

### Takeaway

Fetch database-wide context and a bounded snapshot of currently relevant sessions. These can eliminate misleading hypotheses, but polling cannot reconstruct every short-lived wait or error.

### Cited Findings

- `pg_stat_database` exposes committed/rolled-back transactions, cache blocks, tuple operations, temporary files/bytes, deadlocks, session counters and a reset timestamp. `numbackends` is current rather than cumulative. [PG15 database-statistics reference](https://www.postgresql.org/docs/15/monitoring-stats.html#MONITORING-PG-STAT-DATABASE-VIEW)
- `pg_stat_activity` supplies `pid`, `leader_pid`, `backend_start`, `xact_start`, `query_start`, `state_change`, `state`, `wait_event_type`, `wait_event`, `query_id`, `backend_xmin`, and `backend_type`. `query_start` refers to the last query when not active. General stats may lag and are transaction-cached; activity snapshots also persist within a transaction. Other users' fields can be hidden without monitoring privileges. [PG17 monitoring reference](https://www.postgresql.org/docs/17/monitoring-stats.html)
- The view definitions provide exact aliases and underlying functions, useful for capability checks. Session fatal/abandoned/killed counters and database deadlocks are aggregate context, not query-tagged timeout counts. [System view source](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/backend/catalog/system_views.sql)
- `pg_blocking_pids(pid)` covers hard and queued soft blockers; prepared transactions can appear as PID 0; parallel execution can produce duplicate PIDs. Frequent calls briefly lock shared lock-manager state and impose overhead. [System information functions](https://www.postgresql.org/docs/17/functions-info.html#FUNCTIONS-INFO-SESSION-TABLE)
- PostgREST transactions establish request settings, including JWT claims, and generated queries use prepared statements. [PostgREST transaction reference](https://docs.postgrest.org/en/stable/references/transactions.html)
- RLS can restrict visible rows according to role; table owners ordinarily bypass it. [PostgreSQL RLS reference](https://www.postgresql.org/docs/17/ddl-rowsecurity.html)

### Inferences

Recommended numeric database context: `xact_commit`, `xact_rollback`, `deadlocks`, `temp_files`, `temp_bytes`, `blks_hit`, `blks_read`, `tup_inserted/updated/deleted`, `sessions_fatal/abandoned/killed`, `stats_reset`; current `numbackends` separately. Interpret intervals against its own reset epoch, not pg_stat_statements' epoch. Rollbacks include deliberate application rollbacks; they are not an error-rate denominator. `pg_stat_database_conflicts` is standby recovery conflict context, not ordinary primary lock-timeout history.

Recommended active-session context: omit `query`, client addresses and arbitrary application_name strings by default. Return authorized identity, state, wait classification, query_id when visible, transaction/query ages, and capped blocker edges. Use `(pid, backend_start)` for session identity because PIDs are recycled. Query age applies only to active queries; idle transaction age comes from xact_start. A wait observed once is not a wait-duration measurement. Track repeated observations, explicitly sampled, if needed. Do not call pg_blocking_pids for every backend on every routine poll.

Use short collection transactions or deliberately clear the statistics snapshot between reads. Do not keep a monitoring connection's transaction open across successive scheduled samples. Snapshot timestamps and cross-view timing need explicit output so the investigator does not assume perfect simultaneity.

For index experiments, a privileged administrator's execution can differ from the customer's PostgREST request. Require an enrolled representative query, parameter types and representative values kept on the execution side where feasible, effective role, approved request-claim context, search_path and relevant plan settings. A pg_stat_statements queryid is not an API route or end-user identity. Many customers/end-users can share the authenticated DB role. Top-level RPC text alone cannot explain an internal sort or scan. If nested tracking is absent, expose that limitation rather than inventing internal attribution. This source review does not authorize turning on track=all globally.

### Gaps

No verified mapping from arbitrary PostgREST request to an exact statement identity, full application error rate, or representative parameter distribution exists in these views alone. Missing context can block index validation even when numeric monitoring works. Polling activity is incomplete for short lock incidents. Resource CPU/disk saturation requires the adjacent Supabase Metrics API research rather than pretending these SQL counters provide host metrics.

## 3. What narrow tools should the investigator receive?

### Takeaway

Give the model curated windows, source coverage and context. Rust collects raw counters and performs validated arithmetic; LangGraph should not subtract ad hoc untrusted counters itself. These are proposed contracts, not implemented APIs.

### Cited Findings

The source mechanisms above support numeric polling, selective SQL-text retrieval, database aggregates, and live blocker lookup. They do not establish permission to mutate application objects. [Supabase extension reference](https://supabase.com/docs/guides/database/extensions/pg_stat_statements), [PostgreSQL session functions](https://www.postgresql.org/docs/17/functions-info.html)

### Inferences

All tool calls derive authorized project_id from the signed investigation context. The model cannot submit a free-form connection string, SQL query, another customer's project, arbitrary role, or unbounded time range. A source envelope common to outputs:

```typescript
type EvidenceEnvelope = {
  observedAt: string; collectedFrom: string; collectedUntil: string;
  sourceInstanceId: string; evidenceId: string;
  status: 'complete' | 'partial' | 'unavailable';
  serverVersionNum: number; extensionVersion: string | null;
  capabilities: Record<string, boolean | 'unknown'>;
  warnings: string[];
};
type QueryKey = { dbid: string; userid: string; queryid: string; toplevel: boolean };
```

1. `get_query_performance_window({windowId, queryKeys?, rankBy, limit})`

Allowed rankBy: `execution_ms`, `mean_execution_ms`, `temp_bytes_written`, `baseline_regression`. Bounded limit enforced in Rust. Reads validated stored windows, optionally requests a fresh collection when allowed. Outputs keys, sample range, calls, per-call metrics, baseline comparison, coverage and epochs. No SQL text.

Illustrative output fragment, fictional values:

```json
{
  "observedAt":"2026-11-12T20:05:00Z",
  "status":"complete",
  "window":{"start":"2026-11-12T20:00:00Z","end":"2026-11-12T20:05:00Z"},
  "globalResetAt":"2026-11-10T08:00:00Z",
  "deallocationEventsDelta":"0",
  "query":{"dbid":"5","userid":"16389","queryid":"7541123456789012345","toplevel":true},
  "entryStatsSince":"2026-11-10T08:12:00Z",
  "completedCalls":"1200","executionMs":360000,"meanExecutionMs":300,
  "sharedBlocksPerCall":2400,"tempBytesWritten":"0",
  "ioReadMs":null,
  "capabilities":{"perEntryEpoch":true,"nestedStatements":false,"ioTimingCoverage":"unknown"},
  "warnings":["No p95 or error-rate measurement","I/O timing coverage unknown"]
}
```

2. `get_query_definition({queryKey, evidenceId})`

Fresh exact identity lookup; return sanitized representative SQL, a definition digest, retrieval timestamp, semantic-context status, redaction summary, and parameter metadata if explicitly enrolled. Fail closed if permissions hide the identity or the entry disappeared. Mark raw constants/literal reconstruction unavailable; do not guess values. Never promise normalized SQL is anonymous. This separates sensitive evidence retrieval from routine monitoring.

3. `get_database_pressure_context({windowId, queryKey?, includeCurrentBlockers})`

Return database interval aggregates plus optional current sessions and blocker edges with separate observation times. Enforce PID/session count and sampling budget. Example: `{status:'partial', databaseDeadlocksDelta:'2', currentBlockedSessions:3, blockers:[{pid:123, backendStart:'...', blockedBy:[456]}], warnings:['Live sample cannot explain every earlier wait','Some query_id fields hidden']}`. Absence of blockers now does not rule out blocking during the previous interval.

Tool results should contain the exact source version, units, null semantics, collection duration, reset/epoch validation, freshness deadline and diagnostic warnings. Keep baseline arithmetic outside the model, but let the investigator request permitted supplemental evidence. No changes to the six repair families are implied by these read-only tools.

### Gaps

Permission setup on real Supabase plans, actual collection overhead, statement limits, supported SQL transport, retry semantics and safe timeouts require integration tests. The root researcher covers those alongside plans/catalogs and Metrics API. Tool schemas are design proposals; no deployment or capability verification against a customer project has occurred.
