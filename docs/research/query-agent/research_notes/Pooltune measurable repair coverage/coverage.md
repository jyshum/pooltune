# Concrete repair coverage under the query-performance niche

Research checked November 12, 2026. No live project was accessed or tested. PostgreSQL 17 is the documentation reference; installed version, extension support and authority must be discovered. Proposed autonomy classifications below are product inferences, not already-qualified implementation capabilities.

## Can one query-performance niche contain several measurable repairs?

### Takeaway
Yes. “Important queries became expensive as data or workload changed” contains several distinguishable root causes, including unsuitable indexes, stale statistics, inadequately detailed statistics, temporary-work spills, and cleanup-related heap reads. One successful index case is a strong first end-to-end benchmark; it is not evidence that every index suggestion or all eight families are ready for autonomous release.

### Cited Findings
- PostgreSQL statement telemetry records calls, execution time, returned/affected rows, shared-buffer activity, temporary blocks, and WAL contributions for normalized statements. It does not expose a complete latency distribution or application intent. [Statement statistics](https://www.postgresql.org/docs/17/pgstatstatements.html)
- Actual execution plans show where scanning, sorting, joining and filtering happen. Estimated costs are abstract units, not milliseconds; actual-versus-estimated row discrepancies require execution evidence, normally EXPLAIN ANALYZE. ANALYZE executes the statement. [EXPLAIN](https://www.postgresql.org/docs/17/using-explain.html)
- Supabase's index advisor accepts SQL, can handle generic parameters, and returns candidate index statements and before/after estimated costs. It is a candidate generator, not proof of measured improvement. [Supabase index advisor](https://supabase.com/docs/guides/database/extensions/index_advisor)

### Inferences
- Watch query shapes over time, rank changes in per-call time and total workload contribution, then investigate a bounded shortlist. A frequently executed moderately expensive query can matter more than one rare slow report.
- Read two reset-aware snapshots: interval mean execution time is change in total execution time divided by change in calls. Do not compare lifetime means across different histories or convert means into p95. Pair counters with errors/timeouts, completed work, roles, representative parameters and app timings where available.
- Many queries map to a small number of repair mechanisms. This creates breadth without an arbitrary “fix all databases” tool. The agent chooses which supported hypothesis to test; deterministic checks enforce target identity, permissions and limits.
- The first release should demonstrate several causes and negative controls, rather than have eight equally enabled families. Suggested core: index candidates plus targeted statistics repair; qualify narrow maintenance and RPC-memory cases as additional contracts. Cron, pool settings, WAL settings and application/policy patches retain their conditional lanes.

### Gaps
- Telemetry alone does not prove the most important user journey or give all parameter values. No hosted probe confirms the exact account's telemetry visibility, ownership or maintenance permissions.
- No fixture has yet demonstrated Pooltune diagnosing these causes independently, making the change, surviving interruption and verifying live results.

## Which concrete problems map to evidence, repair and verification?

### Takeaway
The following are separate measurable scenarios, not just different SQL commands. Each proposed automatic action requires a supported mechanism, sufficient evidence, permission and a finite experiment budget. A successful SQL command is not the success criterion.

### Cited Findings

**1. A selective lookup or ordered list examines too much data.** Existing indexes may not match the actual predicates/order. Indexes support multiple keys, expressions, INCLUDE and partial predicates, with different applicability; concurrent builds permit ordinary writes but add work and have failure caveats. [CREATE INDEX](https://www.postgresql.org/docs/17/sql-createindex.html)

**2. A bulk import changes distribution and the old plan becomes poor.** Table counters expose estimated modified rows since analysis and last manual/automatic analysis timestamps. ANALYZE samples data and refreshes planner statistics. [Table statistics](https://www.postgresql.org/docs/17/monitoring-stats.html), [ANALYZE](https://www.postgresql.org/docs/17/sql-analyze.html)

**3. Fresh statistics still miss linked columns or uneven customers.** Higher column statistics targets provide more distribution detail at added collection/planning cost. Extended statistics record joint common values, dependencies or group counts; creation must be followed by ANALYZE, and PG17 does not use them for join selectivity estimates. [Planner statistics](https://www.postgresql.org/docs/17/planner-stats.html), [CREATE STATISTICS](https://www.postgresql.org/docs/17/sql-createstatistics.html)

**4. Necessary sort/hash work spills to temporary disk.** work_mem applies per operation, with separate simultaneous operations and sessions multiplying demand; hash memory uses hash_mem_multiplier. Function configuration can apply on entry and restore on exit. [Resource settings](https://www.postgresql.org/docs/17/runtime-config-resource.html), [Function settings](https://www.postgresql.org/docs/17/sql-createfunction.html)

**5. A covered query still fetches many table pages after changes.** Index-only scans avoid table fetches only when visibility information permits it. Recently changed pages can require table checks despite a covering index. VACUUM maintains the visibility map. [Index-only scans](https://www.postgresql.org/docs/17/indexes-index-only-scans.html), [Routine vacuuming](https://www.postgresql.org/docs/17/routine-vacuuming.html)

**6. Repeated updates leave maintenance behind.** Native autovacuum dynamically responds to table changes and is sufficient for many installations. Plain VACUUM makes obsolete space reusable and can consume significant I/O; it does not generally shrink the file. Selected options include TRUNCATE FALSE and SKIP_LOCKED, with caveats. [Routine vacuuming](https://www.postgresql.org/docs/17/routine-vacuuming.html), [VACUUM](https://www.postgresql.org/docs/17/sql-vacuum.html)

**7. Separate scheduled jobs interfere with foreground queries.** Supabase stores schedules in cron.job and run history/status in cron.job_run_details. pg_cron already serializes repeated invocations of the same job, but permits different jobs concurrently and supplies cron.alter_job. [Supabase Cron](https://supabase.com/docs/guides/cron), [pg_cron](https://github.com/citusdata/pg_cron)

**8. Authorization logic repeats avoidable work.** Supabase documents measuring plans with application role/JWT claims and optimizing correctly written policies. Policy filter indexes and query/policy transformations can help, but testing with privileged roles can miss the actual workload. Disabling RLS for comparison belongs only in nonproduction. [RLS performance](https://supabase.com/docs/guides/database/postgres/row-level-security-performance)

### Inferences

| Scenario | Proposed evidence and exclusions | Narrow repair | Outcome and tradeoff tests | Proposed lane |
|---|---|---|---|---|
| 1. Missing/mismatched access path | Interval query cost increase; representative plan scans many rows or sorts unnecessarily; actual predicates/role and existing indexes checked. A sequential scan alone is not a defect. | Create one tested nonunique index candidate, concurrently where supported. Preserve customer indexes. | Repeated duration, buffers and returned results across common/rare inputs; representative writes, WAL and storage; invalid build cleanup. | Core conditional automation. |
| 2. Stale distribution after import | n_mod_since_analyze, analysis history, workload transition, and meaningful estimated/actual row error. Exclude already-fresh statistics and an inherently expensive full-data query. | ANALYZE one eligible table/columns. | Better relevant execution and estimates; important neighboring queries; analysis overhead. No exact old-statistics undo. | Core conditional automation with maintenance opt-in. |
| 3. Correlation/skew beyond current statistics | Persistent row/group estimate error after freshness confirmed; evidence on same-table combinations or skewed values; test parameter groups. | Create one suitable extended statistics object plus ANALYZE, or raise one column target and refresh. | Actual execution, estimates, planning time and analysis cost; unaffected queries and uncommon values. More accurate estimates alone are insufficient. | Individually qualified statistics contracts. |
| 4. Scoped RPC spill | temp_blks per call identifies candidate; actual internal sort/hash/materialization evidence and latency impact. Rule out avoidable scan/sort and outer PostgREST work. Need concurrent callers/nested work bounded. | Small function-local work_mem change for an already non-inlined enrolled function; do not silently rewrite it. | Less temp work and better duration at comparable completed load; memory/CPU/errors and other queries under concurrency. Function setting affects all callers. | Conditional advanced repair, not global memory autotuning. |
| 5. Visibility-induced heap reads | Actual Index Only Scan with excessive Heap Fetches, workload slowdown and recent churn; index already covers query. Old snapshots or continuously hot pages may make benefit unavailable/short-lived. | Bounded plain VACUUM of one eligible table. | Lower heap fetches/buffer work and improved query duration; maintenance I/O and concurrent workload unchanged within budget. | Narrow maintenance contract. |
| 6. Recurring late cleanup | n_dead_tup trend, churn and last_autovacuum/history; distinguish late trigger from workers/disk overload or rows pinned by snapshots. Dead-row count alone is insufficient. | Targeted cleanup when justified; table-specific threshold adjustment only if trigger timing is cause. | Several cycles of stable backlog, sustainable maintenance, relevant read/write durations and disk activity; no claim of file shrink. | Opt-in maintenance contract. |
| 7. Different-job overlap | Run timestamps/status joined to resource/wait/query degradation; repeatable overlap; exclude same-job serial queue and unrelated current blocker. | Shift future schedule within owner-approved windows. | Both jobs still complete on time; less foreground delay/errors; compare multiple windows. Never disable/cancel active jobs as a hidden schedule repair. | Conditional scheduling, requires business deadlines. |
| 8. Repeated policy/query work | Representative authenticated plan and application tests isolate policy cost or repeated query work. | Prepare exact limited policy/RPC/app-query patch; an ordinary index on policy filter can use index lane. | Same permitted/denied rows across roles/tenants plus latency, parameter variation and changed-query workload effects. | Reviewed migration/code lane until separately qualified. |

The verification columns are proposed engineering requirements derived from the documented mechanisms, not guarantees made by PostgreSQL or Supabase.

### Gaps
- A clone may differ in data distribution, settings, cache, statistics and concurrency. A passing candidate experiment is necessary evidence, not conclusive production benefit.
- In-function work_mem needs actual ALTER authority, preservation of existing settings and testing that adding a function setting does not change inlining/pushdown behavior. Existing catalog documents this issue; this research did not independently inspect optimizer source again.
- Current production blockage is not repaired by creating an index for future operations. Killing transactions remains outside V1. There is no universal “fix locks” action.

## What remains of the other families, and how should V1 breadth be evaluated?

### Takeaway
Pool, checkpoint/WAL and index rebuild controls are real, but add distinct preconditions and tradeoffs. They should earn automatic release status through demonstrated problem cases, rather than turn the eight-family catalog into eight unconditional promises.

### Cited Findings
- Supavisor dynamically adds server connections up to its ceiling. Pools are per user/database/mode, so aggregate demand matters. [Supavisor FAQ](https://supabase.com/kb/troubleshooting/supavisor-faq-YyP5tI)
- Supabase's Postgres configuration API exposes max_wal_size and checkpoint_timeout. Frequent checkpoints can increase writes and full-page-image logging; larger intervals/budgets trade disk and recovery work for reduced checkpoint overhead. [Supabase configuration API](https://supabase.com/docs/reference/api/v1-update-postgres-config), [WAL configuration](https://www.postgresql.org/docs/17/wal-configuration.html)
- PG17 telemetry supplies pg_stat_wal and pg_stat_checkpointer; installed-version differences must be handled. REINDEX CONCURRENTLY can replace bloated indexes while allowing ordinary writes, but has multiple phases and can leave temporary objects on failure. [Statistics views](https://www.postgresql.org/docs/17/monitoring-stats.html), [REINDEX](https://www.postgresql.org/docs/17/sql-reindex.html)

### Inferences
- **Pool family:** require measured acquisition failure/waits, the correct ceiling being reached, database headroom and exclusion of slow-query/lock causes. The outcome is fewer acquisition failures without reduced completed throughput or database overload. This is exceptional capacity configuration, not the primary query repair.
- **WAL/checkpoint family:** combine requested checkpoint rate, reason logs, WAL bytes/full-page-image rate and disk evidence; exclude replication retention as the cause. A small max_wal_size adjustment can be evaluated across checkpoint cycles, with storage, recovery, replay and write-latency budgets. This is a conditional later pilot.
- **Index rebuild:** require physical index evidence and a meaningful performance/storage objective. Dead rows or zero index scans do not prove a useful rebuild or safe removal. A rebuild is not the same contract as adding an index, and shrinking an index alone does not establish query improvement.
- **Release breadth test:** first prove diagnosis-to-outcome on several known fixtures, including wrong-index, stale-stats, correlated-stats, eligible-spill and visibility-maintenance cases. Add healthy fast scans, hot-row blocking, unavoidable large aggregation, unsupported permissions and misrepresentative parameters as negative cases. Track useful repair completion and harmful/unnecessary mutations, not just tool-call success.
- Keep the product promise narrow by outcome: maintaining chosen important query workloads. Keep the investigation broad enough to distinguish its supported causes and decline unsupported ones. This is more defensible than restricting it to one exact query or enabling every documented knob.

### Gaps
- No controlled evaluation currently proves the model-plus-harness chooses correctly among these competing repairs. Capability breadth and acceptable regression thresholds remain a design/evaluation decision, not a finding established by API documentation.
