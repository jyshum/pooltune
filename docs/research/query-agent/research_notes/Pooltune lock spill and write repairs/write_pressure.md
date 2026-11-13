# Concrete write-pressure repairs for hosted Supabase

Research date: November 11, 2026. These are new proposals, not accepted scope or permission for live access. Existing accepted actions remain pool tuning, narrowly eligible index creation/removal and targeted ANALYZE. No database changes or benchmarks were performed.

## Which write-pressure causes are actually distinguishable and repairable?

### Takeaway

There are concrete repairs beyond diagnosis. The best new V1 candidate is scoped table maintenance for demonstrated vacuum lag, with targeted VACUUM and per-table autovacuum tuning as separate permissions. Checkpoint tuning, WAL compression, exact duplicate-index cleanup and background-job rescheduling are feasible conditional pilots, not inherently unavailable features.

### Cited Findings

- Supabase disk saturation includes both throughput and IOPS limits, and depletion can interfere with autovacuum and backups. CPU readings may include I/O waiting. [High Disk I/O](https://supabase.com/kb/troubleshooting/exhaust-disk-io)
- PostgreSQL already automatically vacuums and analyzes tables. Vacuum eligibility depends on a base threshold plus a scale factor times estimated tuples. Per-table overrides are available. Too many large eligible tables can occupy all workers, and old row versions must remain while potentially visible to transactions. [Routine vacuuming](https://www.postgresql.org/docs/17/routine-vacuuming.html)
- `pg_stat_wal` supplies `wal_records`, `wal_fpi`, `wal_bytes`, write/sync counters, timing when enabled and a reset marker. Table statistics include estimated dead/live tuples, insert/update/delete/HOT counts and maintenance timestamps. PostgreSQL 17 has `pg_stat_checkpointer`; PostgreSQL 16 includes checkpoint fields in `pg_stat_bgwriter`. [PG17 statistics](https://www.postgresql.org/docs/17/monitoring-stats.html), [PG16 statistics](https://www.postgresql.org/docs/16/monitoring-stats.html)
- `pg_stat_statements` reports statement WAL bytes/full-page images and dirtied/written blocks. These help attribute work to query fingerprints, subject to configuration and reset/entry-lifecycle limits. [pg_stat_statements](https://www.postgresql.org/docs/17/pgstatstatements.html)
- Replication-slot `xmin` can prevent tuple removal. `restart_lsn` can retain WAL; lowering retention enough can leave a slot unusable. [Replication slots](https://www.postgresql.org/docs/17/view-pg-replication-slots.html)

### Inferences

Do not classify every high-I/O incident as “too many writes.” First collect reset-aware deltas over a complete interval, plus resource and application outcomes. Read-only diagnostic tools should expose fixed summaries rather than unrestricted SQL or customer row contents.

| Observed pattern | Confirming investigation | Concrete repair candidate |
|---|---|---|
| Many updated/deleted row versions accumulate | Estimated dead tuples trend, last maintenance, autovacuum progress and workers, old snapshots/prepared transactions/slot xmin, table growth | Targeted bounded plain VACUUM; tune table thresholds if repeatedly too late |
| High WAL generation with frequent checkpoints and many full-page images | WAL/checkpointer deltas, checkpoint reason logs, current configuration, comparable write mix | Raise a bounded checkpoint WAL budget or timeout; separately trial WAL compression |
| Write regression after an added index | Same write mix, statement WAL/duration deltas, index creation timeline, HOT changes | Remove precisely the Pooltune-created index under existing supported revert contract |
| Exact redundant customer index on frequently written table | Full catalog equivalence and dependency checks, retained equivalent index, read/replication requirements | New opt-in redundant-index cleanup contract |
| Many non-HOT updates despite updates avoiding indexed columns | Updated-column evidence, low HOT ratio, page-space evidence, growth pattern | Fillfactor trial for future pages; not immediate repair of packed existing pages |
| Repeated coincident cron writes | Job identity/owner, schedule, runtime history, query evidence, permitted completion window | Schedule-only staggering of specifically authorized background jobs |
| High WAL retention but ordinary WAL generation | Slot consumer lag and retention, archiver state, disk use | Fix consumer externally; do not drop or invalidate replication slots automatically |
| Legitimate sustained writes exceed capacity | Throughput, durability obligations, actual disk limits | App batching/queue control if explicitly integrated, or capacity recommendation |

WAL generation is different from retained WAL on disk, and both differ from dirty data-page writes. High `wal_fpi` counts indicate an investigation direction, not a byte percentage: compressed images and omitted page holes mean count times page size is not exact WAL attribution. Checkpoints already run automatically and spread writes; Pooltune would adapt workload-specific settings, not introduce checkpointing.

Minimal useful collector:

```sql
SELECT now(), current_setting('server_version_num');
SELECT wal_records, wal_fpi, wal_bytes, wal_buffers_full,
       wal_write, wal_sync, wal_write_time, wal_sync_time, stats_reset
FROM pg_stat_wal;

SELECT relid, schemaname, relname, n_tup_ins, n_tup_upd, n_tup_del,
       n_tup_hot_upd, n_live_tup, n_dead_tup, n_mod_since_analyze,
       last_vacuum, last_autovacuum, vacuum_count, autovacuum_count
FROM pg_stat_user_tables WHERE relid = $1;

SELECT c.oid, c.relowner, c.relkind, c.reloptions, c.reltuples,
       c.relfrozenxid, pg_total_relation_size(c.oid)
FROM pg_class AS c WHERE c.oid = $1;
```

The executor resolves and quotes catalog identities; `$1` is a bound OID, not model-supplied SQL. Add known version-specific checkpoint queries: on PG17 use `num_timed`, `num_requested`, `write_time`, `sync_time`, `buffers_written`, `stats_reset` from `pg_stat_checkpointer`; on PG15/16 use corresponding `checkpoints_timed`, `checkpoints_req`, `checkpoint_write_time`, `checkpoint_sync_time`, `buffers_checkpoint`, `stats_reset` from `pg_stat_bgwriter`. `pg_stat_io` exists from PG16, and does not replace WAL-specific views. Discover columns rather than pretending one query fits every version. [PG16 release notes](https://www.postgresql.org/docs/16/release-16.html)

### Gaps

- Resource telemetry alone does not identify business-safe work to defer or eliminate. Job and query intent must be supplied or observed through an authorized integration.
- No personal hosted project was inspected. Whether any of these incident classes occur in Jared's workloads remains unknown.

## How can scoped table maintenance become a real autonomous repair?

### Takeaway

Add a proposed `table maintenance` family with independent grants for one-table plain VACUUM and table-level autovacuum thresholds. This repairs a specific backlog or recurring threshold mismatch. It does not reduce all write traffic, shrink disk bills immediately, or defeat snapshots that retain dead rows.

### Cited Findings

- Plain VACUUM reclaims space for reuse and can coexist with normal reads/writes, but adds substantial I/O. `TRUNCATE FALSE` avoids the tail-truncation operation that needs `ACCESS EXCLUSIVE`; `SKIP_LOCKED` does not guarantee no waits on indexes. It cannot run inside a transaction block. PG17 permits per-table `MAINTAIN`; database owners have additional rights. [PG17 VACUUM](https://www.postgresql.org/docs/17/sql-vacuum.html)
- PostgreSQL 15 and 16 ordinarily require table ownership or superuser status, with database-owner permission for non-shared tables. No PG17 `MAINTAIN` grant should be assumed on those versions. [PG15 VACUUM](https://www.postgresql.org/docs/15/sql-vacuum.html), [PG16 VACUUM](https://www.postgresql.org/docs/16/sql-vacuum.html)
- Changing table storage parameters requires ownership and uses `SHARE UPDATE EXCLUSIVE` for autovacuum/fillfactor. `SET` changes do not immediately rewrite existing contents. `RESET` restores inherited/default behavior. [ALTER TABLE](https://www.postgresql.org/docs/17/sql-altertable.html)
- Per-table vacuum/analyze threshold, scale-factor and vacuum-cost options exist. [Table storage parameters](https://www.postgresql.org/docs/17/sql-createtable.html)
- Workers with table-specific vacuum cost delay/limit settings are excluded from global worker cost balancing. Repeated commands conflicting with autovacuum's lock can prevent completion. [Routine vacuuming](https://www.postgresql.org/docs/17/routine-vacuuming.html)

### Inferences

**Action A: `vacuum_relation(relation_id, evidence_id, budget_id)`**

Eligible only when there is persistent estimated dead-tuple accumulation or post-bulk-write cleanup need, normal maintenance has not caught up, tuple retention is not known to block useful cleanup, and the affected user-owned table plus its indexes fit a measured work budget. Exclude provider-managed schemas, partitioned parents, foreign tables and wraparound emergencies from the first contract; individually enrolled ordinary leaf tables can be supported. Check `pg_stat_progress_vacuum`, current relation locks, active autovacuum, old `backend_xmin`, prepared transactions and slot retention. Do not start competing maintenance on the same relation.

Execute on a dedicated direct/session administrative connection, each command in autocommit, with short lock wait, maximum runtime and connection-local cost settings. Illustrative shape, with values supplied by a tested budget policy:

```sql
SET application_name = 'pooltune-maintenance';
SET lock_timeout = '<approved lock budget>';
SET statement_timeout = '<approved runtime budget>';
SET vacuum_cost_delay = '<tested delay>';
SET vacuum_cost_limit = '<tested cost limit>';
VACUUM (SKIP_LOCKED, TRUNCATE FALSE, PARALLEL 0) "app"."events";
```

Those examples are adapter-generated statements, not a runnable universal configuration. PG16+ can additionally set a bounded `BUFFER_USAGE_LIMIT` after capability discovery. Do not force `INDEX_CLEANUP OFF` merely to make the operation finish quickly; that changes whether accumulated index debris is cleaned. Do not run `VACUUM FULL`, `FREEZE` or disable page skipping under this contract.

If statistics are also stale and the customer granted both actions, `VACUUM (ANALYZE, SKIP_LOCKED, TRUNCATE FALSE, PARALLEL 0)` is an explicit combined contract. Otherwise leave ANALYZE separate to preserve diagnosis and outcome attribution.

PG17 example onboarding grant is `GRANT MAINTAIN ON TABLE "app"."events" TO pooltune_maintainer;`, plus schema access as needed. MAINTAIN itself allows more than VACUUM, so the typed executor must still prohibit unrelated operations. PG15/16 need an owner-authorized execution route. A database function is not a simple SECURITY DEFINER workaround because VACUUM cannot execute inside its transaction. Do not transfer ownership automatically just to make it work.

Capture command status/notices, progress, before/after maintenance counters, estimated dead tuples, table growth and subsequent workload performance. A skipped relation is `not_applied`; a successful command without workload benefit is not `improved`. Normal VACUUM with truncation disabled will generally leave allocated file size unchanged. Success is reclaimed reusable space/backlog reduction and improved or stable workload under similar write volume. There is no meaningful undo for removed obsolete row versions or consumed I/O. Stop may cancel only Pooltune-owned work through a separately tested cancellation contract; it cannot restore earlier physical state.

**Action B: `set_relation_autovacuum_policy(relation_id, policy_id, expected_reloptions)`**

Eligible when repeated observation shows the table passes a tolerable dead-row budget well before its current trigger, and workers have capacity to run earlier. If it already crosses the trigger but workers are busy, or an old snapshot pins rows, lowering thresholds alone is not the causal repair. The trigger is `B + S × estimated_table_rows`; choose a target from observed churn and cleanup time, not a universal percentage. First contract can change only `autovacuum_vacuum_threshold` and `autovacuum_vacuum_scale_factor`, with analyze equivalents separately permitted when appropriate.

```sql
BEGIN;
SET LOCAL lock_timeout = '<approved lock budget>';
SET LOCAL statement_timeout = '<short metadata budget>';
ALTER TABLE ONLY "app"."events" SET (
  autovacuum_vacuum_threshold = <approved integer>,
  autovacuum_vacuum_scale_factor = <approved fraction>
);
COMMIT;
```

Store the exact old key/value presence in `pg_class.reloptions`, the inherited global values, table identity and policy version. Read back `reloptions`. Restore each previous explicit value using SET; restore previously absent keys using `ALTER TABLE ONLY ... RESET (...)`. Writing the formerly inherited numeric value creates a new override and is not equivalent. Never `RESET ALL`, overwrite unrelated reloptions, disable autovacuum, or alter freeze safeguards. Reverting configuration cannot reverse vacuums already performed.

Per-table cost limit/delay is a subsequent resource-policy contract rather than an incidental extra argument: overriding these bypasses worker balancing, so per-table values need a project-wide budget. Start with earlier triggers and existing cost behavior. Confirm cleanup happens sooner over several cycles, dead-row accumulation stabilizes, autovacuum duration fits the available interval, and application p95/errors/throughput plus I/O remain acceptable. A metadata readback is not enough.

### Gaps

- Numeric limits, table-size eligibility, workload windows and headroom thresholds require controlled calibration and hosted tests.
- Supabase roles/grants must be inspected on the enrolled version. SQL privileges cannot be inferred from possession of an API key.

## What additional repairs are feasible, and which should enter a conditional pilot?

### Takeaway

Avoid blanket rejection. Checkpoint budgets and WAL compression are documented controls. Exact duplicate-index cleanup and schedule-only job staggering can remove avoidable work. Each needs an explicit narrower contract and a test demonstrating its mechanism before automatic rollout.

### Cited Findings

- Supabase's Management API exposes `max_wal_size`, `checkpoint_timeout` and `restart_database` at `PUT /v1/projects/{ref}/config/database/postgres`. [Update Postgres config](https://supabase.com/docs/reference/api/v1-update-postgres-config)
- Supabase documents these settings as reloadable, role-level `wal_compression`, and CLI removal of individual overrides. CLI v2 distinguishes reloadable settings from restart settings. Custom overrides supersede provider defaults, and SQL RESET does not remove system-level provider overrides. Organization Owner/Administrator access is required for system customization. [Customizing Postgres config](https://supabase.com/docs/guides/database/custom-postgres-config)
- PostgreSQL checkpoints flush dirty pages; increasing checkpoint frequency can increase full-page-image WAL and I/O while reducing crash-recovery work. PostgreSQL triggers checkpoints by timeout or WAL limit and already throttles writes. [WAL configuration](https://www.postgresql.org/docs/17/wal-configuration.html)
- `wal_compression` compresses full-page images, not every WAL record. It trades CPU for reduced WAL volume. Supported codecs depend on build, and changing it requires superuser or appropriate SET privilege. [WAL parameters](https://www.postgresql.org/docs/17/runtime-config-wal.html)
- HOT updates avoid new ordinary index entries when updated columns are not indexed and space exists on the old page. PG17 excludes summarizing indexes from the updated-column restriction; PG15 documents the stricter rule. Lower fillfactor increases space availability. [PG17 HOT](https://www.postgresql.org/docs/17/storage-hot.html), [PG15 HOT](https://www.postgresql.org/docs/15/storage-hot.html)
- `DROP INDEX CONCURRENTLY` requires ownership, cannot run inside a transaction, does not accept CASCADE, and cannot drop partitioned indexes or indexes supporting primary-key/unique constraints. [DROP INDEX](https://www.postgresql.org/docs/17/sql-dropindex.html)
- `pg_index` records key/included columns, predicates, expressions, operator classes, collation, validity and replica-identity flags used to distinguish indexes. [pg_index](https://www.postgresql.org/docs/17/catalog-pg-index.html)
- `cron.alter_job` permits schedule and active-state updates; `cron.unschedule` removes jobs. Supabase permissions are owner-sensitive; `postgres` is not a superuser and cannot simply remove another role's job. [pg_cron](https://github.com/citusdata/pg_cron), [Supabase cron permissions](https://supabase.com/kb/troubleshooting/revoking-privileges-on-storage-pg_cron-vault-and-extension-objects-has-no-effect-916576)
- Role configuration defaults apply on new logins; `SET ROLE` does not reload them. [ALTER ROLE](https://www.postgresql.org/docs/17/sql-alterrole.html)
- The parallel primary-source audit of pg_cron v1.6.4 found inactive jobs propagated into task state, cancellation checks during running states, background-worker termination and libpq connection cleanup. This is source evidence, not a hosted execution test. [Task active-state refresh](https://github.com/citusdata/pg_cron/blob/v1.6.4/src/task_states.c#L91-L108), [Cancellation check](https://github.com/citusdata/pg_cron/blob/v1.6.4/src/pg_cron.c#L2305-L2334), [Running-task handling](https://github.com/citusdata/pg_cron/blob/v1.6.4/src/pg_cron.c#L1674-L1784)

### Inferences

**Conditional pilot C: `set_checkpoint_budget`**

Mechanism: if volume-triggered checkpoints occur too often under a steady write mix, modestly raise `max_wal_size`; if time-triggered checkpoints are demonstrably too frequent, separately consider `checkpoint_timeout`. Use checkpoint reason logs to corroborate counters because requested checkpoints can have causes other than hitting the WAL limit. More generous intervals can reduce repeated flushing/full-page images, but increase storage demand and potential recovery work. This is a real optimization, not necessarily a capacity fix.

Request only a single allowlisted setting with explicit no-restart behavior, then verify `pg_settings.setting`, `context`, `source`, `pending_restart`, and `pg_postmaster_start_time()` on primary and relevant replicas. Current documented request shape:

```text
PUT /v1/projects/{ref}/config/database/postgres
{"max_wal_size":"<bounded value>","restart_database":false}
```

Equivalent documented CLI shape: `supabase postgres-config update --config max_wal_size=<bounded value> --remote=<project-ref> --experimental --no-restart`. Never use `--replace-existing-overrides` for this action. Validate merge behavior and API default behavior in a disposable hosted project before production use.

Eligibility needs: recorded current override versus provider default, confirmed reloadability, finite approved storage budget, free-disk margin that accounts for WAL bursts and retention, acceptable recovery target, healthy archive/replication consumers, and application outcome signals. `max_wal_size` is a soft trigger, not a hard disk cap. A budget must include observed retention and burst rates rather than assume the configured value caps disk use. Do not force CHECKPOINT as a diagnostic action.

If the original override existed, restore that exact value. If it was absent, delete only the added override using the provider's documented deletion path, e.g. `supabase postgres-config delete --config max_wal_size --remote=<project-ref> --experimental --no-restart`; verify inherited active state. SQL `RESET` is not equivalent. Guard against intervening human/config/compute changes. Reversion does not undo disk expansion or make retained WAL instantly vanish. Observe across multiple checkpoint cycles and compare latency, completed writes, WAL per comparable write and I/O. No safety claim about recovery time without measuring or explicitly accepting that tradeoff.

**Conditional pilot D: `set_writer_wal_compression`**

Mechanism: significant full-page-image traffic plus CPU headroom. Use a supported codec and the real login writer role or explicitly integrated transaction, not an assumed `authenticated` role. For a dedicated writer login, a possible contract is `ALTER ROLE writer IN DATABASE postgres SET wal_compression = 'pglz';`, subject to target-role authority and parameter SET privilege. Record `pg_db_role_setting`/`pg_roles` configuration and exact scope; restore the former explicit value or RESET that one setting if absent.

This affects new login sessions, not arbitrary existing pooled backends. `SET ROLE authenticated` is insufficient. If the relevant connection pool's lifecycle cannot safely apply and verify the value, leave the action in pending/unsupported state; do not kill sessions to make it apply. In an opt-in app transaction integration, `SET LOCAL wal_compression = ...` can provide narrower duration if permissions allow. Compare CPU, write latency, WAL bytes per comparable completed write, and replay CPU/lag. It is a poor choice when CPU is already saturated or WAL consists mostly of non-image records. Preserve `fsync`, `full_page_writes`, `wal_level` and durability semantics; do not disable them as an optimization.

**Conditional pilot E: `remove_exact_redundant_index`**

Removing a newly introduced Pooltune index that regressed writes already fits the accepted revert intent. Removing a preexisting customer index is new consent. The first new contract should require an exact surviving equivalent ordinary non-unique index on the same table, not mere low `idx_scan` usage or overlapping leading columns. Compare access method, full key order, expressions, predicates, included attributes, sort/null options, collation, opclasses, relevant storage options and validity/readiness. Exclude uniqueness/exclusion constraints, primary keys, dependency-backed indexes, replica identity, partitioned/attached indexes, extension-owned indexes, and ambiguous consumers. Inspect `pg_constraint`, `pg_depend`, `pg_index`, replication/publication requirements and application query context. Do not use CASCADE or change replica identity to make removal possible.

Save a complete rebuild definition, relation identity, owner, storage placement and metadata. Execute exactly `DROP INDEX CONCURRENTLY "app"."redundant_idx";` through a dedicated autocommit route with finite lock/runtime budgets. Read back catalog state; preserve the surviving index. Validate representative reads/plans as well as write duration, WAL and HOT behavior. Restoration means `CREATE INDEX CONCURRENTLY` from the saved definition, which takes time, I/O and space and can fail. This is rebuild compensation, not instant rollback. A long quiet window with zero scans does not prove a nonduplicate index is unnecessary. Removing a duplicate does not restore HOT if the retained identical index still references the updated columns, though it reduces duplicate maintenance.

**Conditional pilot F: `reschedule_registered_job`**

Limit to individually enrolled pg_cron jobs with a declared permissible completion window and known business meaning. Read job ID, username, database, command hash, current schedule, active state and recent runs. Show simultaneous jobs produce avoidable peaks or repeated work; similar SQL alone does not establish duplicate business intent. Stagger execution within the granted window using only `SELECT cron.alter_job(job_id := <id>, schedule := '<validated schedule>');`, preserving command, owner, database and active state. Read back full job identity and expected unchanged fields. Restore the old schedule only if no external drift.

CRITICAL: do not present `active := false` or `cron.unschedule` as harmless future-only suspension. The parallel source audit found cancellation behavior for running jobs in pg_cron v1.6.4. Disabling/removing a running job may interrupt work, which conflicts with the existing V1 no-session-rescue boundary. Schedule-only updates still require tests for queued/running job semantics on the enrolled extension version. A job that invokes HTTP work can already have dispatched an external effect; DB state cannot undo that. Existing/completed job effects are never reversed by rescheduling. Registering a deferrable job is a small explicit product integration, not per-request ranking across the app.

**Fillfactor and batching: real, but not immediate V1 incident controls**

`ALTER TABLE ONLY "app"."events" SET (fillfactor = <bounded percentage>);` is a supported owner action. It can help future page layout when frequent updates avoid indexed columns and page fullness is the limiting factor. It does not make room in every existing packed page immediately. Do not smuggle `VACUUM FULL`, CLUSTER or a table rewrite into the action to force the benefit. Observe through table turnover, HOT ratio, WAL/write latency and increased storage/read cost; RESET restores setting inheritance, not past page layout. A focused background optimization pilot is plausible, but the delayed effect weakens the incident-repair promise.

Application batching, worker concurrency and idempotent queue processing can directly flatten write load. An agent can make these changes only through an explicit application configuration/source/deployment integration with correctness checks for transaction boundaries, retry semantics, ordering and deadlines. There is no database setting that universally merges application writes without changing behavior. Keep this an honest integration opportunity rather than saying the agent can never fix it or promising database magic.

**Recommended addition and testing order:** propose table maintenance as the first new family, with a one-table post-bulk-update VACUUM experiment and a recurring autovacuum-threshold experiment. In parallel select whichever write-pressure mechanism is actually present in Jared's personal hosted project for one additional conditional pilot. Do not enable every setting merely because it exists.

Product-tier recommendation: table maintenance is the proposed actual V1 expansion. Checkpoint tuning and WAL compression are conditional experiments before deciding whether they belong in V1. Customer-owned duplicate-index removal and cron scheduling are explicit later proposals with usable contracts, not implicitly approved V1 actions. Existing Pooltune-index revert remains within accepted scope. Fillfactor and app batching stay longer-horizon integrations unless pilot evidence justifies a narrower release.

| Test | Required proof |
|---|---|
| High-churn table below an overly large autovacuum threshold | Earlier cleanup limits dead-row accumulation without excessive I/O |
| Snapshot or slot pins tuples | Agent detects retention and does not repeat ineffective VACUUM |
| Autovacuum already working effectively | No unnecessary maintenance |
| Saturated write device | Agent avoids competing maintenance that worsens service |
| Checkpoint-heavy controlled workload | Higher bounded WAL budget improves relevant outcome; disk and recovery tradeoffs recorded |
| Same write workload with/without compression | WAL reduction and CPU/replay cost measured; actual session setting verified |
| Exact duplicate versus merely overlapping index | Only true eligible duplicate removed; read/write correctness maintained |
| Active and queued cron job during schedule change | No undocumented cancellation, duplicate or missed obligation; extension-version behavior established |
| Worker crash after mutation / human drift | State reconciled; no duplicate vacuum loop or blind override restoration |

### Gaps

- These are source-grounded contracts and hypotheses, not tested adapters. PostgreSQL correctness does not establish Supabase account permissions, runtime propagation or useful workload benefit.
- Managed-service API support is established for checkpoint settings; default propagation and deletion route behavior still need hosted tests.
- Safe pg_cron schedule-change semantics and authority on actual project versions remain a launch gate. Disabling and deleting jobs are excluded from the narrow proposal until cancellation effects and authorization are explicitly resolved.
- Source docs describe configuration primitives, not an endorsed autonomous Pooltune policy. Eligibility, bounds and release order above are engineering proposals.
