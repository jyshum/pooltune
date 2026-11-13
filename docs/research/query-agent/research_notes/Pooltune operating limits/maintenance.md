# Index and maintenance operating limits

Research date: November 12, 2026. PostgreSQL 17 and hosted Supabase documentation; no live probes. Policies below are recommendations, not accepted numeric defaults.

## How can the agent build an index without overwhelming the database?

### Takeaway

Concurrent building preserves ordinary writes, but still spends real CPU, I/O and storage. Give a justified build enough time to finish, with independent limits on lock waiting and observed application harm; do not impose the same tiny timeout used for telemetry.

### Cited Findings

- `CREATE INDEX CONCURRENTLY` scans twice and waits for relevant transactions. CPU/I/O overhead can slow other work. Only one concurrent build per table is allowed; it cannot run inside a transaction block. A failed build can leave an invalid index that still incurs update overhead. Concurrent creation on a partitioned parent is unsupported. `IF NOT EXISTS` checks a name, not equivalent definition. — [CREATE INDEX](https://www.postgresql.org/docs/17/sql-createindex.html)
- `maintenance_work_mem` limits maintenance workspace; `max_parallel_maintenance_workers` bounds eligible worker use. Parallel utility workers share the maintenance memory limit but may increase CPU/I/O. Neither setting is a whole-process memory quota or a reserved CPU percentage. — [Resource controls](https://www.postgresql.org/docs/17/runtime-config-resource.html)
- `statement_timeout` bounds statement duration; `lock_timeout` bounds each individual lock-acquisition wait. They should be scoped to Pooltune sessions rather than applied to every application session. Neither is a guarantee of zero customer blocking. — [Timeout semantics](https://www.postgresql.org/docs/17/runtime-config-client.html)
- `pg_stat_progress_create_index` reports build, validation and waiting phases, including old-snapshot waits. Catalog `pg_index.indisvalid` distinguishes usable indexes. — [Progress](https://www.postgresql.org/docs/17/progress-reporting.html), [Index catalog](https://www.postgresql.org/docs/17/catalog-pg-index.html)
- `DROP INDEX CONCURRENTLY` avoids locking out ordinary application access but has ownership, transaction and partition restrictions. — [DROP INDEX](https://www.postgresql.org/docs/17/sql-dropindex.html)
- Supabase documents direct or session-mode connections for persistent session settings and longer administration tasks. Dashboard/client limits must not determine all maintenance budgets. — [Supabase timeouts](https://supabase.com/docs/guides/database/postgres/timeouts)

### Inferences

- Proposed automatic eligibility: enrolled table, justified candidate, no equivalent suitable index, qualified command form, sufficient measured disk headroom, acceptable workload conditions and a long-enough permitted window. Keep one substantial Pooltune repair per project initially; this is our coordination policy, not PostgreSQL's per-project constraint.
- Preflight storage must account for final index, temporary build workspace, WAL and ongoing customer growth. Forecasts are estimates, so retain headroom and monitor; do not promise “reserve 5% CPU” or an exact byte ceiling for permanent index growth.
- Evaluate candidates through existing evidence and estimated plans before physical builds. Build a ranked candidate rather than physically trying dozens. If the build is progressing without unacceptable impact, allow it to continue within its budget. If blocked by a customer's transaction, defer or cancel our own build; do not kill theirs.
- Persist action identity and target definition before starting. After lost connectivity, check whether the build is still running or already valid before retrying. Inspect and remove only a qualifying Pooltune-created invalid index under the cleanup grant. Cleanup itself needs time/resources; exhaustion of the investigation budget must not silently abandon it.

### Gaps

No documented pause/resume command for these index builds was found. Aborting and starting again is not resuming. Exact disk/time/headroom thresholds and permitted Supabase session settings require project qualification. Parallel-worker/memory controls do not establish a strict total impact cap.

## How much work do statistics repairs require?

### Takeaway

Refreshing stale statistics and collecting richer statistics are real database work, but usually much narrower than repeatedly executing the slow query. More detailed statistics should be targeted at the demonstrated estimate error.

### Cited Findings

- `ANALYZE table(columns)` targets selected columns; large tables are sampled. The highest selected statistics target determines sample size, with larger targets increasing time and space. `SKIP_LOCKED` can skip an initially locked relation but does not guarantee avoiding every later wait. PG17 ordinarily allows table `MAINTAIN` for analysis. — [ANALYZE](https://www.postgresql.org/docs/17/sql-analyze.html)
- `CREATE STATISTICS` needs table ownership; subsequent `ANALYZE` populates it. PG17 extended statistics do not solve selectivity estimation for joins across tables. — [CREATE STATISTICS](https://www.postgresql.org/docs/17/sql-createstatistics.html)
- `ALTER COLUMN SET STATISTICS` changes future collection and acquires `SHARE UPDATE EXCLUSIVE`; its documented range is 0–10000, not a recommended operating range. — [ALTER TABLE](https://www.postgresql.org/docs/17/sql-altertable.html)
- `BUFFER_USAGE_LIMIT` bounds the buffer access strategy, reducing cache disruption. Cost-based delay applies to VACUUM and ANALYZE and sleeps after accumulated estimated I/O work. It is not a strict I/O bandwidth limiter. — [Resource controls](https://www.postgresql.org/docs/17/runtime-config-resource.html)

### Inferences

- Refresh automatically when current evidence supports stale summaries causing relevant bad estimates; avoid a blanket daily refresh of every table. Reuse observed estimate errors and modification history. Already-current accurate statistics justify no change.
- For richer statistics, select relevant columns/statistic kinds, record prior targets, set bounded collection detail, analyze, and compare representative query behavior. Account for ongoing future analysis cost, not just today's command.
- Removing our statistics object or restoring a target is possible recovery, but cannot recreate the exact old sampled statistics. Do not advertise exact undo for ANALYZE. If the estimate error lies outside the supported statistic mechanism, report that rather than endlessly increasing targets.

### Gaps

No universal safe target, analysis timeout or sample-memory budget follows from the docs. Access privileges and actual sample costs need testing. Exact cost-based throttling behavior should be verified with the deployed PG build.

## How can cleanup remain useful without interfering with the app?

### Takeaway

Vacuum can make dead space reusable and improve visibility information, while threshold tuning can start future cleanup sooner. Both need evidence that cleanup is the relevant cause rather than a blocked cleanup horizon or unrelated bottleneck.

### Cited Findings

- Plain VACUUM normally allows reads/writes, can generate substantial I/O, and cannot run inside a transaction block. `TRUNCATE false` avoids the exclusive-lock attempt to return empty tail pages to the OS; `PARALLEL 0` disables parallel index cleanup. `SKIP_LOCKED` is not comprehensive protection from later waits. — [VACUUM](https://www.postgresql.org/docs/17/sql-vacuum.html)
- Vacuum updates visibility information supporting index-only scans. Old transactions and replication slots can obstruct necessary cleanup. Standard vacuum makes space reusable and is not equivalent to a table rewrite. — [Routine vacuuming](https://www.postgresql.org/docs/17/routine-vacuuming.html)
- Per-table threshold and scale-factor overrides determine when autovacuum becomes eligible; they do not reserve a worker or promise immediate completion. Changing autovacuum storage parameters takes `SHARE UPDATE EXCLUSIVE`. — [Autovacuum settings](https://www.postgresql.org/docs/17/runtime-config-autovacuum.html), [ALTER TABLE](https://www.postgresql.org/docs/17/sql-altertable.html)
- `pg_cancel_backend` sends a query-cancel signal subject to privileges; a true return means the signal was sent, not verified completion. — [Server signaling](https://www.postgresql.org/docs/17/functions-admin.html)

### Inferences

- Proposed default recipe: explicitly targeted plain vacuum, bounded workspace, buffer strategy, cost-based delay, no tail truncation, and limited parallelism. These preserve useful cleanup while reducing interference, without promising no impact.
- If autovacuum is already fixing the table, observe instead of duplicating it. If long transactions retain old rows, repeated vacuum is not the solution; explain the blocker without terminating customer work.
- Tune only enrolled table thresholds when late triggering is demonstrated. Save old settings and verify future cleanup keeps pace without hurting foreground work. Undo settings if adverse; completed physical cleanup is not rolled back.
- Cancellation tools must identify our own backend and action, cancel, then confirm stopped. Partial completed vacuum work may remain; a later vacuum is a new pass, not an exact checkpoint resume. A recovered Go worker must reconcile database state before dispatching another action.

### Gaps

No universal CPU/I/O fraction or completion time is guaranteed. Postgres/Supabase metrics are observation, not resource reservation. Unknown resource evidence should defer resource-sensitive work or require a separately qualified conservative path, not silently count as spare capacity.
