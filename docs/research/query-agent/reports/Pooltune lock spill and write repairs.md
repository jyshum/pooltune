# Pooltune lock spill and write repairs

**Pooltune should demote pool tuning as its headline and test concrete repairs for spills, maintenance backlog, and recurring lock contention.** Supabase already grows pools with demand and supplies compute-based defaults; PostgreSQL already provides automatic vacuuming, analysis, and checkpointing. The additional product is selecting the right intervention when those mechanisms are insufficient, executing it within the customer's permission, and verifying its effect. The strongest proposed additions are memory tuning for selected existing RPC functions and scoped table maintenance, with plain VACUUM and autovacuum-threshold changes separately authorized. Schedule-only staggering of explicitly enrolled jobs is another useful candidate. Checkpoint budgets and WAL compression deserve conditional pilots when the actual workload supports them. These are research proposals, not newly approved capabilities; hosted Supabase remains the target and live customer-transaction interruption remains outside V1.

Prepared November 11, 2026. This extends the earlier capabilities report without replacing its accepted three families: existing-pool tuning, eligible index changes, and targeted ANALYZE. No project access, hosted tests, benchmarks, application changes, or database mutations were performed. SQL below illustrates executor shapes; names and values are examples, not instructions to run on a real project. Provider/version behavior and privileges require explicit discovery before implementation.

## Native pooling changes the claim Pooltune should make

The objection to “pool wait → small ceiling increase” is correct. Supavisor transaction mode already creates backend connections when work needs them, up to the configured ceiling. PostgREST also has a dynamic pool. Supabase's current Management OpenAPI documents a nullable PostgREST `db_pool`: null means automatic configuration based on compute size. Pooltune cannot claim automatic pool growth or compute-aware default sizing as its invention. ([Supavisor FAQ](https://supabase.com/kb/troubleshooting/supavisor-faq-YyP5tI), [PostgREST pooling](https://docs.postgrest.org/en/latest/references/connection_pool.html), [Official Management OpenAPI](https://api.supabase.com/api/v1-json))

Three things differ: actual connections growing below a ceiling, a provider-selected default ceiling, and a workload-feedback experiment changing that ceiling. The reviewed evidence establishes the first two natively. It does not establish a hosted controller continuously optimizing the ceiling against queueing, CPU, throughput, and errors. That absence is not proof that Supabase has no internal optimization. A defensible product statement is: **find why work waits, apply the relevant eligible repair, and check that more useful work completes.**

| Incident | Native mechanism already exists | Remaining operational problem | Specific Pooltune intervention |
|---|---|---|---|
| Pool waiting | Dynamic pool growth and compute-based defaults | Distinguish undersized ceiling from slow queries or contention | Secondary bounded ceiling experiment only with headroom and restore support |
| Long lock holding | Lock management and deadlock victim selection | Repeated expensive work or overlapping jobs holds locks longer than necessary | Eligible FK/filter index; approved future job staggering |
| Temporary-file spills | Sort/hash memory management and planner | One expensive operation needs a better access path, estimates, or bounded memory | Existing index/ANALYZE; proposed exact-function `work_mem` |
| Dead-row backlog | Autovacuum and autoanalyze | Bulk-write backlog or a table's trigger mismatches its churn | Proposed one-table VACUUM; separate per-table threshold policy |
| Excess checkpoint work | Automatic checkpoints and write spreading | Current budget causes excessive checkpoint frequency for this workload | Conditional checkpoint-budget experiment |
| Excess WAL volume | WAL generation, durability, optional compression | Full-page-image traffic or avoidable index maintenance | Conditional scoped compression; existing index revert; future duplicate cleanup |

The native mechanisms are real, not reasons to dismiss the whole product. They instead raise its evidence standard. Supabase advisors and PostgreSQL's automatic maintenance already do useful work; Pooltune must demonstrate an intervention beyond repeating an advisory. ([Routine vacuuming](https://www.postgresql.org/docs/17/routine-vacuuming.html), [Supabase advisors](https://supabase.com/docs/guides/observability/advisors), [WAL configuration](https://www.postgresql.org/docs/17/wal-configuration.html))

Pool tuning also has a specific restoration gap. The OpenAPI GET can return `db_pool: null`, but PATCH specifies an integer from 0 to 1000, not null. Neither that schema nor the retrieved documentation establishes how to return to automatic mode. Restoring the old effective number is not restoring compute-based inheritance. Combined with upstream `db-pool` being non-reloadable, automatic pool changes need hosted application/draining and reset-to-default tests before eligibility. ([Management OpenAPI](https://api.supabase.com/api/v1-json), [PostgREST configuration](https://docs.postgrest.org/en/v14/references/configuration.html))

## Locks have preventable causes, but no universal unlock repair

A lock incident must distinguish an expensive holder, an idle transaction, queued DDL, inconsistent application lock order, advisory locks, and internal shared-memory contention. `pg_blocking_pids` is preferable to improvised lock joins because it includes conflicting holders and earlier queued waiters. `pg_locks` often represents blocked row work as a transaction-ID wait, while `LWLock` events concern different internal structures. Record PID plus backend start, sample repeatedly, and restrict query text. Query age is not exact lock-wait duration. ([Blocking functions](https://www.postgresql.org/docs/18/functions-info.html), [Lock view](https://www.postgresql.org/docs/18/view-pg-locks.html), [Wait events](https://www.postgresql.org/docs/18/monitoring-stats.html))

### An index can shorten future lock holding

**Evidence and eligibility.** A parent DELETE/key UPDATE can scan a large child table while enforcing a foreign key. PostgreSQL does not automatically index the referencing columns. Similarly, a selective UPDATE, DELETE, or SELECT FOR UPDATE can spend unnecessary time locating rows. Identify recurrent slow work in the blocking transaction, confirm the relevant constraint/predicate and real role-aware plan, and exclude an already useful index. An unindexed FK on a tiny table is not itself an incident. ([Foreign-key indexing](https://www.postgresql.org/docs/current/ddl-constraints.html))

**Action and authority.** This is an explicit use of the already accepted index family, not a new mutation category. A typed `add_lock_shortening_index(table_oid, constraint_oid, column_ids, schema_digest, evidence_id)` can generate:

```sql
CREATE INDEX CONCURRENTLY "pooltune_orders_customer_id"
ON "public"."orders" USING btree ("customer_id");
```

Use the existing narrow nonunique/plain-column eligibility and ownership checks. The top-level command needs a direct/session-affine autocommit connection, table ownership powers, applicable schema permission, and bounded runtime/lock budgets. Do not start a competing build in the middle of a lock convoy. Concurrent creation performs work and can leave an invalid object if interrupted. ([CREATE INDEX](https://www.postgresql.org/docs/17/sql-createindex.html))

**Verification and recovery.** Test representative FK/trigger or selective-row behavior on a fixture, then compare future operation duration, buffer work, blocked transaction-seconds, and completed transactions, with write/WAL/storage guardrails. Verify the exact valid index. Reconcile unknown outcomes or invalid artifacts before retrying; supported removal targets only the Pooltune-created index with dependency checks. The action does not unlock today's holder, shorten application idle time, or cure intrinsic contention on one hot row. Its value is lower future lock-held work.

### Staggering different approved jobs is a concrete repair

**Evidence and eligibility.** Join job run timing and `job_pid` with the blocking graph or resource peaks. Require repeated overlap of two different jobs, known commands/dependencies, and a user-declared permissible launch/completion window. pg_cron already serializes invocations of the same job, queueing a new invocation behind the active one. Same-job backlog therefore needs runtime/frequency analysis, not a claim that Pooltune supplies missing serialization. ([pg_cron behavior](https://github.com/citusdata/pg_cron))

**Action and application path.** Propose an independent `reschedule_registered_job` permission for individually enrolled jobs. Validate job ID, owner, database, command hash, schedule, active state, timezone, deadline and maximum delay. Use an actually authorized cron principal and the supported function, preserving all fields except schedule:

```sql
SELECT cron.alter_job(job_id := 42, schedule := '17 * * * *');
```

This changes future launch timing without deploying application code. The example minute has no universal significance. Job ownership/RLS access must be verified. Do not update cron tables directly or replace the job with unschedule-plus-create. ([Supabase Cron API](https://supabase.com/docs/guides/cron/quickstart), [Cron permissions](https://supabase.com/kb/troubleshooting/revoking-privileges-on-storage-pg_cron-vault-and-extension-objects-has-no-effect-916576))

**Verification and recovery.** Read back complete job identity and unchanged fields. Test running/queued behavior on the installed version, then observe enough scheduled runs to establish lower overlap while every required job still completes on time. Lower load because a job never ran is failure. Restore the exact old schedule only without external drift; never automatically “catch up” by executing the command. A schedule restoration does not undo missed deadlines or external effects already dispatched.

**Do not implement this by disabling jobs.** In pg_cron v1.6.4, changing active state can propagate into running-task cancellation, including worker termination or connection closure. A schedule-only edit avoids that explicit inactive-task path but still needs an in-flight survival test. Disabling or deleting jobs is outside this non-interrupting proposal. ([Task-state source](https://github.com/citusdata/pg_cron/blob/v1.6.4/src/task_states.c#L91-L108), [Running-task source](https://github.com/citusdata/pg_cron/blob/v1.6.4/src/pg_cron.c#L1674-L1784), [Cancellation check](https://github.com/citusdata/pg_cron/blob/v1.6.4/src/pg_cron.c#L2305-L2334))

### Timeouts and cancellation change failure semantics

Timeouts are implementable controls, but they are not underlying lock repairs. A scoped function `lock_timeout` rejects a waiting statement; `idle_in_transaction_session_timeout` deliberately terminates a future idle session. They need explicit failure-policy permission, application deadlines, retry knowledge, and route-aware application tests. Setting a timeout on Pooltune's connection does not affect the customer's sessions. Function `statement_timeout` behavior also depends on PostgREST's hoisting; direct SQL must not be assumed equivalent. These controls remain proposals outside the existing no-interruption scope. ([Client settings](https://www.postgresql.org/docs/current/runtime-config-client.html), [Supabase timeout routes](https://supabase.com/docs/guides/database/postgres/timeouts))

Cancellation needs precision even when excluded. Cancelling an active top-level statement normally aborts its transaction and releases ordinary transaction locks; an aborted-idle state does not prove those locks remain. Savepoint/subtransaction handling and session advisory locks are exceptions. A query cancel sent while an idle backend waits for the next client command is ignored in the audited PostgreSQL 17 path. Termination ends the session and can destroy work; neither action has a true undo. ([Signaling functions](https://www.postgresql.org/docs/18/functions-admin.html), [Idle cancellation source](https://github.com/postgres/postgres/blob/REL_17_STABLE/src/backend/tcop/postgres.c#L3422-L3433), [Transaction abort source](https://github.com/postgres/postgres/blob/REL_17_STABLE/src/backend/access/transam/xact.c#L2924-L2936), [Advisory lock semantics](https://www.postgresql.org/docs/18/explicit-locking.html))

Other real repairs include consistent lock order, smaller transaction boundaries, moving network calls outside transactions, and queue-specific `SKIP LOCKED`. They require application/RPC logic changes and correctness tests. They are valid future repository/deployment integrations, not automatic database-only V1 actions; adding `SKIP LOCKED` to arbitrary queries can silently omit business rows.

## Spills justify selected RPC memory experiments

A spill means an operation used temporary storage instead of retaining all intermediate data in memory. It is not automatically a defect. First establish a latency/resource problem at comparable completed workload. `pg_stat_statements.temp_blks_read` and `temp_blks_written` permit reset-aware per-statement deltas; multiply block counts by the actual `block_size` for approximate bytes processed, not peak disk space or required RAM. Database `temp_bytes` establishes overall work, not the responsible operator. Nested tracking is not automatically available and can double-count parent/function work if combined carelessly. ([Statement statistics](https://www.postgresql.org/docs/17/pgstatstatements.html), [Database statistics](https://www.postgresql.org/docs/17/monitoring-stats.html))

Where actual plans can be captured safely, external Sort or multi-batch Hash supports the diagnosis. Plain EXPLAIN estimates; EXPLAIN ANALYZE executes. `log_temp_files` can add evidence but records sizes at deletion time and is itself instrumentation requiring appropriate privilege and logging limits. Supabase documents a disabled default. ([Plan interpretation](https://www.postgresql.org/docs/17/using-explain.html), [PostgreSQL logging](https://www.postgresql.org/docs/17/runtime-config-logging.html), [Supabase log settings](https://supabase.com/docs/guides/database/postgres/postgres-log-config))

### Avoid unnecessary work before increasing memory

An eligible filter/order index can remove a large sort, especially for ORDER BY plus LIMIT, while targeted ANALYZE can fix estimates that selected an expensive plan. Both already fit accepted families. Match key direction, collation, null ordering, parameters and role/RLS context; broad reads can still favor a scan and sort. INCLUDE columns are not sorting keys and increase index size. Query rewrites such as fewer projected columns, earlier filters, or keyset pagination are real repairs but require separate application semantics/deployment scope. ([Indexes and ordering](https://www.postgresql.org/docs/17/indexes-ordering.html), [Index-only scans](https://www.postgresql.org/docs/17/indexes-index-only-scans.html))

If necessary work still spills, a new **selected RPC memory** category is technically credible. Start with an existing allowlisted PL/pgSQL function, or another function proven already non-inlined, with an understood internal operator or result-materialization spill. Do not wrap or rewrite its body. Inspect nested calls, dynamic SQL, recursion, and in-function SET statements; opaque behavior makes automatic eligibility weak. PL/pgSQL result materialization can itself spill, so not every internal spill is a visible Sort/Hash node. ([PL/pgSQL results](https://www.postgresql.org/docs/17/plpgsql-control-structures.html))

### The exact mutation is function-local work_mem

The executor owns or exercises owning-role authority for the exact overloaded function. Ordinary EXECUTE permission is insufficient. Bind the OID, input argument types, owner, definition digest, and full `pg_proc.proconfig` before mutation. In a short bounded transaction, generate one setting change:

```sql
ALTER FUNCTION public.refresh_customer_summary(uuid)
SET work_mem TO '8MB';
```

The value is illustrative, not a proposed universal default. Native function settings apply on entry and normally restore the previous setting on exit; body-level SET can complicate this. Preserve unrelated search path, security, timeout, and parallel properties. Record the change in exportable action/migration history so later function deployments do not silently erase it. ([ALTER FUNCTION](https://www.postgresql.org/docs/17/sql-alterfunction.html), [Function configuration](https://www.postgresql.org/docs/17/sql-createfunction.html), [Function catalog](https://www.postgresql.org/docs/17/catalog-pg-proc.html))

The setting affects **all callers of that function**, including other pools, direct SQL, and scheduled work. It does not make an outer PostgREST ORDER BY run inside the function's memory scope. PostgREST can hoist selected function settings to the whole transaction, but `work_mem` is not in its default hoist list. Do not silently enable hoisting: it broadens scope and could affect existing functions. Discover any existing hoist configuration and budget its actual effect. ([PostgREST transactions](https://docs.postgrest.org/en/stable/references/transactions.html), [Hoisted settings](https://docs.postgrest.org/en/stable/references/configuration.html))

Adding any `proconfig` SET can prevent scalar/set-returning SQL-function inlining in PostgreSQL 17. That can lose filter/limit pushdown and increase work. Starting with already non-inlined functions avoids introducing that specific change. A successful ALTER and catalog readback still require real application-path verification, including reused connections and relevant PostgREST cache refresh behavior. Do not modify a production function to print settings as a hidden diagnostic step. ([PostgreSQL optimizer source](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/backend/optimizer/util/clauses.c), [PostgREST cache](https://docs.postgrest.org/en/stable/references/schema_cache.html))

### Memory eligibility needs concurrency, not an arbitrary safe number

`work_mem` applies per operation, hash memory also multiplies by `hash_mem_multiplier`, and parallel workers multiply resource use. The proposed budget is a conservative bound on concurrent affected calls times simultaneous sort/hash/materialization demand and worker allowance, plus reserve for unrelated traffic and maintenance. Assess each database host/replica separately. Average free memory, temp-file size, or one pool ceiling does not establish this bound when other callers exist. Decline an automatic increase when concurrency or nested work cannot be bounded. ([PostgreSQL memory settings](https://www.postgresql.org/docs/17/runtime-config-resource.html))

**Verification.** Compare temporary blocks per completed call and actual execution/request duration under comparable inputs, rate, role, and deadlines. Guard completed throughput, errors, memory/swap/OOM indicators, CPU, disk, and other workload latency. No p95 may be invented from statement means. A single bounded experiment with a finite observation deadline is preferable to repeatedly doubling memory until the spill disappears.

**Restore and ambiguity.** On lost acknowledgment, inspect identity and current `proconfig` before retrying. Restore the exact previous explicit `work_mem`, or use `ALTER FUNCTION exact_signature RESET work_mem` if the key was absent. Do not RESET ALL or recreate the function. Check drift and repeat applicable application-path verification. Restoration governs later calls; it does not undo in-flight allocations or prior OOM harm. `temp_file_limit` is a containment policy that can cancel work, not a performance repair, and is outside this new memory proposal.

## Write pressure supports maintenance and conditional WAL repairs

Separate obsolete-row cleanup, dirty-page writes, WAL generation, and WAL retention. `pg_stat_wal` exposes `wal_bytes`, `wal_records`, `wal_fpi`, write/sync timing where enabled, and reset information. Table stats expose churn/HOT counts and maintenance history. PostgreSQL 17 uses `pg_stat_checkpointer`; PostgreSQL 15/16 expose checkpoint counters in `pg_stat_bgwriter`. Check version/columns and reset boundaries. WAL full-page-image counts are not exact image bytes, and high retained WAL can reflect a lagging replication consumer rather than excess generation. ([PostgreSQL 17 statistics](https://www.postgresql.org/docs/17/monitoring-stats.html), [PostgreSQL 16 statistics](https://www.postgresql.org/docs/16/monitoring-stats.html), [Replication-slot retention](https://www.postgresql.org/docs/17/view-pg-replication-slots.html))

### Plain targeted VACUUM can repair a real cleanup backlog

**Evidence and eligibility.** Require persistent estimated dead tuples or post-bulk-write cleanup need, normal maintenance not catching up, and sufficient I/O headroom. Check active vacuum/progress, workers, relation locks, old `backend_xmin`, prepared transactions, and slot `xmin`. Old snapshots can retain rows despite repeated VACUUM; a saturated disk can make added maintenance harmful. Exclude managed schemas, foreign tables, partition parents, and wraparound emergencies from the initial contract.

**Action and permission.** Propose a separate `vacuum_relation(relation_oid, fingerprint, evidence_id, budget_id)` grant for one ordinary application table. Use a dedicated direct/session connection with separately applied session lock/runtime/cost limits, then one top-level autocommit command:

```sql
VACUUM (SKIP_LOCKED, TRUNCATE FALSE, PARALLEL 0) "app"."events";
```

Plain VACUUM can coexist with ordinary reads/writes but consumes I/O. `TRUNCATE FALSE` avoids tail truncation requiring an exclusive table lock; `SKIP_LOCKED` does not guarantee no index waits. PostgreSQL 17 supports per-table `MAINTAIN`; PostgreSQL 15/16 ordinarily need ownership or the documented database-owner exception. Version-gate options and grants. A transaction-wrapped RPC is not a workaround because VACUUM cannot run inside a transaction block. ([PostgreSQL 17 VACUUM](https://www.postgresql.org/docs/17/sql-vacuum.html), [PostgreSQL 16 VACUUM](https://www.postgresql.org/docs/16/sql-vacuum.html), [PostgreSQL 15 VACUUM](https://www.postgresql.org/docs/15/sql-vacuum.html))

**Verification and recovery.** Capture notices/progress and maintenance evidence; skipped tables are not applied. Compare dead-row backlog, reusable-space/growth behavior, and workload outcomes under similar writes. Do not promise disk-file shrinkage, particularly with truncation disabled. A disconnected execution needs reconciliation, not an immediate repeat. This is non-reversible maintenance: it cannot restore removed obsolete versions or consumed I/O. Do not add ANALYZE implicitly unless both categories and a combined contract are granted. No VACUUM FULL, forced freeze, disabled page skipping, or hidden rewrite belongs here.

### Earlier autovacuum triggers can prevent recurring backlog

**Evidence and eligibility.** Show that cleanup repeatedly starts too late because the table's trigger exceeds its tolerable backlog, while workers have capacity. The usual threshold combines a base plus scale factor times estimated rows. Lowering it cannot fix pinned tuples or an already overwhelmed worker/disk subsystem. This is workload-specific adjustment to native automation, not replacement of autovacuum. ([Routine vacuuming](https://www.postgresql.org/docs/17/routine-vacuuming.html))

**Action.** Propose an independently authorized table-owner operation changing only approved threshold/scale-factor keys:

```sql
ALTER TABLE ONLY "app"."events" SET (
  autovacuum_vacuum_threshold = <approved integer>,
  autovacuum_vacuum_scale_factor = <approved fraction>
);
```

Angle-bracket values are deliberate placeholders. Execute in a short transaction with bounded lock wait; preserve table identity and exact `reloptions` key presence. Analyze thresholds need their own relevant grant. Keep cost-limit/delay tuning separate because per-table overrides interact with global worker cost balancing. Never disable autovacuum or freeze safeguards. ([Table parameters](https://www.postgresql.org/docs/17/sql-createtable.html), [ALTER TABLE](https://www.postgresql.org/docs/17/sql-altertable.html))

**Verification and restore.** Read back options, then observe several maintenance cycles for earlier cleanup, stable backlog, sustainable duration, and acceptable application/I/O cost. Restore prior explicit values; RESET only keys previously absent. Writing their old inherited number creates a new override and is not equivalent. Reconcile timeout ambiguity and preserve external edits. Reverting thresholds does not reverse maintenance already performed or immediately erase backlog.

### Checkpoint budgets and compression are feasible conditional pilots

**Checkpoint evidence.** Frequent checkpoints plus steady writes and full-page-image traffic justify investigation. Corroborate requested-checkpoint counters with reason logs; not every request means the WAL limit was reached. Distinguish timeout-driven frequency from volume-driven frequency and retained WAL caused by slots. Supabase exposes `max_wal_size` and `checkpoint_timeout` through its configuration API, and documents reloadable settings with a no-restart path. ([Postgres configuration API](https://supabase.com/docs/reference/api/v1-update-postgres-config), [Supabase configuration](https://supabase.com/docs/guides/database/custom-postgres-config))

A proposed `set_checkpoint_budget` adapter can issue one allowlisted change:

```text
PUT /v1/projects/{ref}/config/database/postgres
{"max_wal_size":"<approved value>","restart_database":false}
```

Require provider organization authority, current override/default provenance, reloadability, free-space/burst/retention budget, healthy consumers, and an acceptable recovery objective. More generous budgets can reduce repeated flush/full-page-image work but increase disk use and crash-recovery work. `max_wal_size` is a soft trigger, not a hard disk cap. Never force CHECKPOINT as routine diagnosis. ([WAL tradeoffs](https://www.postgresql.org/docs/17/wal-configuration.html))

Read back effective `pg_settings`, source, pending restart, and server start time across relevant instances. Compare completed writes, latency, WAL per comparable write, and I/O across multiple checkpoint cycles. Restore an existing override exactly; if originally absent, remove only the added provider override through the documented deletion route, not SQL RESET. Validate API merge/no-restart and deletion behavior on a disposable hosted project first. Recovery must preserve manual/default changes; restoring configuration does not instantly remove retained WAL.

**Compression evidence and action.** When full-page images contribute materially and CPU has headroom, a separately scoped `wal_compression` experiment is credible. It compresses images, not all WAL. For an authorized dedicated login writer, an illustrative action is `ALTER ROLE writer IN DATABASE postgres SET wal_compression = 'pglz'`, requiring role authority and parameter SET privilege. Codecs/build support must be discovered. This affects new logins; ordinary SET ROLE does not apply login defaults. Do not assume it tunes current PostgREST pooled traffic or kill connections to force application. ([WAL parameters](https://www.postgresql.org/docs/17/runtime-config-wal.html), [Role settings](https://www.postgresql.org/docs/17/sql-alterrole.html))

Verify effective settings on the real writer path, then WAL bytes per completed write, CPU, latency, and replay lag/CPU. Preserve durability controls such as `fsync`, `full_page_writes`, and `wal_level`. Restore the exact scoped prior setting or RESET that one absent key; existing sessions require route-aware convergence. Lack of a safe application path makes this a reviewable proposal, not an impossible optimization or ready automatic fix.

### Removing work is real, but broadens authority

An exact duplicate customer index can impose needless write/WAL/storage cost. A future explicit cleanup family could compare full keys, order, expressions, predicates, INCLUDE columns, opclasses, collation, storage details, validity and dependencies; retain a valid equivalent; exclude constraints, replica identity, partitions, extension objects, and ambiguous consumers. Low `idx_scan` or overlapping leading columns is insufficient. Execute only a checked `DROP INDEX CONCURRENTLY`, never CASCADE. ([Index catalog](https://www.postgresql.org/docs/17/catalog-pg-index.html), [Concurrent index removal](https://www.postgresql.org/docs/17/sql-dropindex.html))

Verify representative reads plus writes/WAL. Save a complete rebuild definition before removal. Recovery means rebuilding concurrently, which consumes time, space and I/O and can fail: it is not cheap instant revert. Removal of a regressing Pooltune-created index already fits accepted scope; customer-owned index removal remains new permission. Retaining an identical index also means duplicate removal alone does not restore HOT updates on indexed changing columns.

Lower fillfactor can improve future page space and HOT opportunities when updated columns permit it, but does not immediately repair packed existing pages. Observe through turnover, accepting extra storage/read cost; RESET cannot restore past layout. App batching, writer concurrency, and queue processing can directly flatten load but need application configuration/source/deployment integration and ordering/retry tests. These are credible later paths, not hidden additions to a maintenance command. ([HOT behavior](https://www.postgresql.org/docs/17/storage-hot.html))

## Expand V1 through mechanisms demonstrated on the pilot

The revised recommendation is to retain the accepted families, explicitly test lock-shortening indexes, and propose two new repair areas: selected existing-RPC memory and scoped table maintenance. Split one-off VACUUM from recurring autovacuum policy so the customer can authorize one without the other. Offer schedule-only staggering when a project actually has enrolled deferrable jobs. Treat checkpoint/WAL changes as conditional experiments before deciding their release status. Do not add every knob merely because SQL or an API exposes it.

The first authorized discovery pass on Jared's hosted project should identify actual Data API/RPC/direct routes, function identities and call paths, supported server/extension versions, writable scopes, cron jobs, and whether the incident classes exist. It should not invent a workload to justify the feature. Start with synthetic fixtures, then read-only observation, then one separately granted candidate. No such discovery or test has occurred in this research.

| Candidate | Required pilot evidence |
|---|---|
| Lock-shortening index | FK/selective-write fixture improves future hold time without claiming current unlock; hot-row case correctly rejected |
| Cron staggering | Different-job overlap reduced; same-job queue understood; active/queued runs survive schedule edit and obligations still complete |
| RPC work_mem | Internal versus outer spill distinguished; all-caller concurrency pressure tested; actual route setting and inlining behavior checked |
| Targeted VACUUM | Bulk-change backlog improves; pinned snapshots/slots and saturated disk correctly prevent ineffective work |
| Autovacuum thresholds | Several cycles stabilize backlog without new I/O/foreground regressions; inheritance restores exactly |
| Checkpoint/compression | Mechanism-specific benefit at comparable writes; disk/CPU/recovery/replay tradeoffs and setting application measured |
| Recovery | Crash after mutation, lost response, manual edit, grant revocation, stop, and failed restore produce correct visible state |

Reuse the prior durable-controller rules rather than adding another autonomous subsystem. Each new adapter needs exact identity/before-state, evidence, authorization, finite budget/deadline, observed application, and outcome checks. Full stop blocks fresh dispatch, including compensation; manual drift blocks blind restoration. Unknown external effects require inspection before retry. If restore fails, show that the active setting or artifact remains and needs attention. Non-reversible maintenance must be labeled before execution. Permission checks and argument limits stay outside the model.

The most useful next choices are which real project incident to target first; whether selected RPC memory and the two maintenance permissions belong in V1; whether a known cron job has a legitimate flexible window; and whether application telemetry provides enough concurrency and outcome evidence. Checkpoint/WAL scope should follow a detected mechanism, not precede it. Deployment, stack, and workflow settings remain unselected, and these research findings do not authorize implementation or live changes.
