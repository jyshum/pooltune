# Pooltune deeper lock repairs and pooling differentiation

November 11, 2026. Research only. No live project access or changes. This evaluates proposed scope expansions without approving them. Existing V1 excludes interruption of live customer transactions, including explicit cancellation/termination. All numerical examples below illustrate executable shapes, not recommended universal thresholds.

## Does pool-wait tuning duplicate Supabase's built-in pooling?

### Takeaway

The user is right to challenge differentiation: both Supavisor and PostgREST already grow their active connection pools with demand, and hosted Supabase already supplies a compute-based default PostgREST ceiling. Pooltune's possible additional value is evidence-based adjustment of that ceiling and diagnosis of why it is reached, not creating connection pooling or automatic connection growth.

### Cited Findings

- Supavisor transaction mode creates more backend connections only when needed for pending work, up to the configured ceiling. It reuses them and closes unused connections after five minutes. Session mode allocates a direct backend per client up to the ceiling. Pools are per user/database/mode, and PostgREST has its own internal pool. [Supavisor FAQ](https://supabase.com/kb/troubleshooting/supavisor-faq-YyP5tI)
- PostgREST's connection pool is dynamic; it opens connections as needed up to `db-pool` and retires idle connections. Increasing capacity is one possible remedy, alongside reducing request count and query execution time. [PostgREST connection pool](https://docs.postgrest.org/en/latest/references/connection_pool.html)
- The current official Management OpenAPI GET response defines nullable `db_pool` with description: “If `null`, the value is automatically configured based on compute size.” It similarly describes default acquisition timeout 10, lifetime 1800, and max idle time 30. The PATCH input schema defines `db_pool` as integer 0..1000, not nullable. This was read directly from `PostgrestConfigWithJWTSecretResponse_Output`, `V1PostgrestConfigResponse_Output`, and `V1UpdatePostgrestConfigBody`. [Official Supabase Management OpenAPI](https://api.supabase.com/api/v1-json)
- Upstream PostgREST `db-pool` is non-reloadable; its `db-pool-automatic-recovery` setting concerns reconnecting after failure, not adaptive ceiling optimization. [PostgREST configuration](https://docs.postgrest.org/en/v14/references/configuration.html)

### Inferences

- There are three different mechanisms: demand-based actual connections below a ceiling; compute-based default ceiling; workload/outcome-based changes to the ceiling. Supabase demonstrably provides the first two. No reviewed primary source establishes a hosted feedback controller that continuously adjusts ceilings based on database CPU, wait queues, throughput and error tradeoffs. Absence of such evidence does not prove no internal optimizations exist.
- A defensible claim is: “Pooltune finds whether waiting comes from insufficient permitted concurrency, slow queries, or contention, then applies and verifies the appropriate supported change.” “We automatically scale your pool” is too broad and potentially duplicates existing functionality.
- Before editing an auto-configured project, preserve both mode (`automatic` versus overridden) and effective runtime ceiling. Reverting to the old numeric effective ceiling is not equivalent to restoring automatic compute-based configuration. The PATCH schema does not establish that `null` or `0` restores auto mode; validate separately.
- A pool-wait card needs queue/acquisition evidence plus healthy DB headroom. Recommending a larger ceiling merely because clients wait can worsen a lock convoy. Pool ceiling tuning remains a useful secondary tool, but a thin standalone differentiator.

### Gaps

- Exact compute-to-PostgREST-ceiling formula and reset-to-auto API semantics were not established from the retrieved official specification. Safe hosted rollout/drain behavior still needs disposable-project tests.
- No live measurements establish Pooltune could outperform Supabase's defaults. That is an experiment to run, not a claim to sell as proven.

## How should Pooltune distinguish lock causes and immediate relief from prevention?

### Takeaway

“Locks” is too broad to be a single diagnose-only bucket. Some cases have concrete repairs within the existing index family, some can be prevented with narrow schedule or timeout-policy changes, and some need application changes or explicit interruption authority. The diagnostic layer must make that distinction before choosing a repair.

### Cited Findings

- `pg_blocking_pids(pid)` reports both conflicting lock holders and earlier waiters whose requested lock creates a soft block. Parallel workers map to client-visible PIDs; duplicates are possible. Prepared-transaction blockers can appear as PID 0. Excessively frequent calls incur lock-manager overhead. [PostgreSQL information functions](https://www.postgresql.org/docs/18/functions-info.html)
- `pg_locks` describes heavyweight objects and requested modes. Ordinary row locks generally live on disk; a blocked row operation commonly appears as waiting for a transaction ID. Correct conflict/queue joins are difficult, so PostgreSQL recommends `pg_blocking_pids`. [PostgreSQL pg_locks](https://www.postgresql.org/docs/18/view-pg-locks.html)
- Wait type `Lock` includes relation, transactionid, tuple, advisory and other heavyweight waits. `LWLock` instead protects shared-memory data structures, with events such as BufferContent, BufferMapping and LockManager. A generic “unlock this backend” algorithm is not a remedy for all LWLock contention. [PostgreSQL monitoring](https://www.postgresql.org/docs/18/monitoring-stats.html)
- Foreign keys do not automatically create an index on referencing columns. Deleting a referenced row or changing its referenced key requires finding matching referencing rows, so an index can avoid expensive child-table scans. [PostgreSQL constraints](https://www.postgresql.org/docs/current/ddl-constraints.html)
- PostgreSQL detects a deadlock and aborts one transaction. Preventing recurrence usually requires consistent acquisition order across application transactions and retry handling. Session-level advisory locks survive transaction rollback; transaction-level advisory locks release at transaction end. [PostgreSQL explicit locking](https://www.postgresql.org/docs/18/explicit-locking.html)
- `lock_timeout` aborts a statement after an individual lock-acquisition wait; `statement_timeout` limits the statement as a whole. `idle_in_transaction_session_timeout` terminates a session idle inside a transaction, releasing its transaction resources. These are different semantics, not three names for a performance limit. [PostgreSQL client settings](https://www.postgresql.org/docs/current/runtime-config-client.html)
- `pg_cancel_backend` cancels the current query; `pg_terminate_backend` terminates the session. Signaling requires allowed role membership, `pg_signal_backend`, or appropriate elevated privilege, and non-superusers cannot signal superuser backends. A boolean success may mean only that a signal was sent; termination with a positive timeout can wait for completion. [PostgreSQL signaling functions](https://www.postgresql.org/docs/18/functions-admin.html)
- PostgreSQL 17 source ignores a query-cancel request while waiting for the client to send its next command. Therefore cancelling an idle-in-transaction backend is not a reliable way to release its locks. [PostgreSQL postgres.c source](https://github.com/postgres/postgres/blob/REL_17_STABLE/src/backend/tcop/postgres.c#L3422-L3433)
- Top-level transaction abort invokes lock resource release in PostgreSQL 17. A backend may subsequently display `idle in transaction (aborted)` while awaiting ROLLBACK, but it is incorrect to infer that all its original ordinary transaction locks necessarily remain. Savepoint/subtransaction errors and session-level advisory locks require separate treatment. [PostgreSQL xact.c abort source](https://github.com/postgres/postgres/blob/REL_17_STABLE/src/backend/access/transam/xact.c#L2924-L2936)

### Inferences

Proposed bounded evidence query, run read-only with adequate monitoring privileges, an execution timeout and restricted output:

```sql
SELECT pid, backend_start, datid, usesysid, application_name,
       state, xact_start, query_start, state_change,
       wait_event_type, wait_event, pg_blocking_pids(pid) AS blockers
FROM pg_stat_activity
WHERE datname = current_database()
  AND pid <> pg_backend_pid()
  AND (wait_event_type = 'Lock' OR state LIKE 'idle in transaction%');
```

Build a bounded graph from these edges, then fetch blocker identity/state and relevant `pg_locks` records. Distinguish true roots from intermediate waiters, including a pending ACCESS EXCLUSIVE migration that is queued behind a reader but blocks later readers. Repeated snapshots establish duration; query age alone is not exact lock-wait duration. Record backend start as well as PID to detect reuse. Do not assume a missing SQL text means no query or low severity; it can be a privilege limit.

Suggested classification:

| Observed cause | Immediate resolution without interruption | Concrete prevention/repair |
|---|---|---|
| Child-table scan during FK checks or cascade work | Wait for current holder; an index build does not retroactively accelerate the running query | Eligible nonunique child FK index after plan/workload validation |
| Slow selective UPDATE/DELETE or SELECT FOR UPDATE | Same limitation for current statement | Eligible predicate index; shorten future lock-held execution |
| Idle open transaction holding rows | Application voluntarily commits/rolls back | Fix client transaction lifecycle; separately proposed idle timeout for future sessions |
| Pending blocking DDL or maintenance | Wait; do not submit another competing action | Bounded lock wait, quiet-window scheduling, concurrent variants where equivalent |
| Two independent cron jobs collide | Let current jobs finish | Stagger future schedule within declared business deadlines |
| Opposite lock order/deadlock | PostgreSQL already aborts a victim | App/RPC transaction-order patch and correct retry semantics |
| Session advisory lock leak | Owning session unlocks or ends | Transaction-scoped advisory lock design where semantically appropriate |
| LWLock / hot page / internal contention | Not equivalent to a heavyweight blocker chain | Reduce repeated work or concurrency; physical/data-layout/app repair based on named event |

Cancellation versus termination, if ever authorized as a later scope expansion: cancel of an active top-level statement normally raises an error and aborts that transaction, releasing its ordinary transaction locks; cancellation inside a subtransaction may leave outer transaction locks. Session advisory locks persist. Cancel of an idle transaction is ignored in the documented source path. Termination rolls back the session's active transaction and ends session locks, but can destroy work and trigger retries or application errors. Neither operation has an “undo” that resurrects the transaction. Verify blocker disappearance and waiting workload recovery, never merely the signaling boolean. Existing V1 does not authorize either operation.

### Gaps

- Application semantics, deadlines and retry behavior cannot be inferred safely from a query fingerprint alone. A hot shared row may represent an essential serialization constraint.
- Per-statement trigger/FK execution detail can be absent from default pg_stat_statements settings. Reproduce suspicious FK operations on a disposable fixture with representative data rather than running destructive EXPLAIN ANALYZE in production.

## Which narrow automatic lock repairs are practical candidates, and what are their execution contracts?

### Takeaway

Explicitly include lock-shortening FK/predicate indexes in existing V1. A schedule-only change to approved cron jobs is the strongest additional non-interrupting candidate. Scoped timeout policies are technically executable but change failure behavior and require explicit approval of that expanded product scope. Deactivating cron jobs is not reliably non-interrupting.

### Cited Findings

- Supabase's `cron.alter_job(job_id bigint, schedule text default null, command text default null, database text default null, username text default null, active boolean default null)` supports schedule-only and activation changes. Use cron functions, not direct updates to managed tables. [Supabase Cron quickstart](https://supabase.com/docs/guides/cron/quickstart); [Supabase Cron debugging](https://supabase.com/kb/troubleshooting/pgcron-debugging-guide-n1KTaz)
- pg_cron executes different jobs concurrently, but queues another invocation of the same job behind its active invocation. Job visibility/modification is subject to ownership/RLS. [pg_cron repository](https://github.com/citusdata/pg_cron)
- Crucial source detail: pg_cron v1.6.4 RefreshTaskHash sets task `isActive` from job `active`; running states check `jobCanceled`, and the background-worker path terminates the worker. The libpq path transitions to error and closes the connection. Therefore `active := false` can cancel a running job, not merely suppress future launches. [pg_cron task-state source](https://github.com/citusdata/pg_cron/blob/v1.6.4/src/task_states.c#L91-L108); [pg_cron execution source](https://github.com/citusdata/pg_cron/blob/v1.6.4/src/pg_cron.c#L1674-L1784); [jobCanceled implementation](https://github.com/citusdata/pg_cron/blob/v1.6.4/src/pg_cron.c#L2305-L2334)
- ALTER ROLE defaults normally affect new logins, not already-open sessions or ordinary SET ROLE. Self-settings are allowed; modifying other roles' defaults requires appropriate elevated role administration rights (PG18 documents CREATEROLE plus ADMIN OPTION for eligible non-superuser roles). [PostgreSQL ALTER ROLE](https://www.postgresql.org/docs/18/sql-alterrole.html)
- PostgREST explicitly applies impersonated role settings per request and can hoist selected function settings before the main query. `statement_timeout` is in the default hoisted list; `lock_timeout` is not listed there by default. [PostgREST transactions](https://docs.postgrest.org/en/v14/references/transactions.html); [PostgREST hoisted settings](https://docs.postgrest.org/en/v14/references/configuration.html#db-hoisted-tx-settings)
- Supabase documents role timeout changes followed by `NOTIFY pgrst, 'reload config'` for Client API requests. Its session SET guidance requires a direct/session connection, not separate calls through transaction pooling. [Supabase timeouts](https://supabase.com/docs/guides/database/postgres/timeouts)
- ALTER FUNCTION SET/RESET changes a configuration option without rewriting its body, and requires function ownership. SET affects the current session; SET LOCAL only its transaction. [PostgreSQL ALTER FUNCTION](https://www.postgresql.org/docs/18/sql-alterfunction.html); [PostgreSQL SET](https://www.postgresql.org/docs/18/sql-set.html)

### Inferences

All following executor signatures are proposals. All require durable before-state, identity/hash checks, evidence IDs, exact authorization, bounded execution, drift-aware restore and actual outcome measurements.

#### A. FK and predicate indexes: accepted family, explicit lock use case

**Typed action:** `add_lock_shortening_index(table_oid, constraint_oid?, key_attnums, expected_schema_hash, evidence_id, resource_budget)`.

**Eligibility:** Recurrent long blocking transaction includes a parent DELETE/key UPDATE with an unindexed child FK, or a selective row-locking operation scanning excessive rows. Require candidate index usefulness for the real key/role/plan and no suitable existing index. An unindexed FK advisory alone is not sufficient for a tiny/static child table. A composite FK candidate must cover all relevant equality keys; do not blindly assume every leading-column ordering rule from simplified guidance.

**Executor example:** `CREATE INDEX CONCURRENTLY "pooltune_orders_customer_id" ON "public"."orders" USING btree ("customer_id");` on an owned, ordinary table. Requires table ownership powers, schema CREATE, session-affine direct/session transport, and a top-level command outside an explicit transaction. Apply timeout settings separately on that connection. Refuse unique, expression, partial, partitioned-parent and custom operator cases until independently supported. Check for conflicting maintenance before dispatch; a lock incident is often a poor time to build.

**Expected effect:** Less work in future FK checks or row selection, potentially shorter transaction hold times. It will not release current locks or fix application time spent idle after the query.

**Verification:** Valid index; representative query/trigger plan improvement in disposable test; production post-change duration and buffer work, blocked transaction-seconds and completed transaction rate, with write/WAL/storage cost guards. Avoid counting mere drop in lock waits caused by fewer successful requests as improvement.

**Rollback:** Drop only this provenance-verified index concurrently, with dependency checks. Invalid leftovers require reconciliation/cleanup. Reverting the index does not recreate a past workload or undo already-completed application transactions.

#### B. Stagger approved cron jobs: strongest additional candidate

**Typed action:** `reschedule_job(job_id, expected_command_hash, expected_schedule, new_schedule, permitted_time_window, deadline, expected_owner, evidence_id)`.

**Eligibility:** Two different jobs repeatedly overlap and block one another or foreground work, as shown by `cron.job_run_details.job_pid`, run timing and blocker graphs. User declares acceptable scheduling window, maximum delay, timezone and task dependencies. Same-job repeat backlog needs frequency/runtime analysis because pg_cron already serializes each individual job. Exclude financial deadlines, externally time-sensitive jobs, unknown side effects and uncontrolled job chains unless their semantics are supplied.

**Executor example:** `SELECT cron.alter_job(job_id := 42, schedule := '17 * * * *');`. Preserve `command`, `database`, `username` and `active`. Use credentials actually authorized to manage that job; do not equate generic DB read access or ownership-role membership with guaranteed pg_cron RLS visibility. Can execute in an ordinary transaction over a direct connection, with bounded own statement timeout. Read job identity/config immediately before and after. Do not issue UNSCHEDULE+SCHEDULE or `active=false` as an implementation shortcut.

**Expected effect:** Reduce overlap of future launches; not resolve a current blocker. Already queued runs and scheduler reload timing require version-specific tests. A schedule change retaining active true avoids the explicit inactive-task termination path identified in source, but must still pass an in-flight survival test before production eligibility.

**Verification:** Wait through enough affected schedules. Confirm intended runs still occur, finish before required deadlines and have lower overlap/blocking/foreground impact. Failure to run is not successful load reduction. Track backlog, not just lower peak CPU.

**Rollback:** Restore the exact old schedule if no intervening edit. Restoring the schedule does not replay missed executions or undo external side effects; never automatically catch up by running the job command. A rare/unmeasured workload can remain unverified.

#### C. Scoped lock/time budgets: real executable proposal, requires expanded authority

**Typed action:** `set_workload_timeout(target: approved_rpc|dedicated_login_role, target_identity, parameter: lock_timeout|statement_timeout|idle_in_transaction_session_timeout, expected_previous_override, timeout_ms, route, evidence_id, failure_semantics_grant)`.

**Examples:**

```sql
ALTER FUNCTION public.refresh_customer_summary(uuid) SET lock_timeout TO '750ms';
ALTER FUNCTION public.refresh_customer_summary(uuid) SET statement_timeout TO '5s';
ALTER ROLE app_worker IN DATABASE postgres SET idle_in_transaction_session_timeout TO '60s';
```

The first changes future function-internal lock waits. The second's end-to-end timer is route-specific: for PostgREST use its documented hoisting and schema/config refresh behavior; do not assume changing a function SET clause retroactively starts/restarts a statement timer for arbitrary direct SQL callers. Function identity includes argument types, ownership and definition/proconfig fingerprints. Preserve every unrelated setting and never CREATE OR REPLACE the whole function just to change a timeout.

Role defaults ordinarily apply to new login sessions. This does not configure a currently stuck backend. PostgREST role-setting application is a separate documented mechanism and requires a reload; test the actual call path. `SET lock_timeout` on Pooltune's own connection does nothing to a customer's other sessions. Role-level policy on broad shared `authenticated`, `postgres` or platform roles has excessive blast radius for an automatic narrow action; favor a known RPC or dedicated application role.

**Eligibility:** Known service objective and maximum acceptable wait/runtime, correct rollback/retry behavior, bounded external side effects, sufficient history to avoid terminating legitimate long work, and an explicitly authorized failure-policy change. `lock_timeout` rejects a waiter; it does not eliminate the holder. Idle timeout intentionally terminates future idle transactions. Thus these are not loopholes around the current no-interruption constraint: enabling them as customer-workload repairs requires a conscious scope revision.

**Verification:** Check stored override and effective settings through the actual route, then controlled lock fixture in a disposable project; observe production timeout/error rates, retry volume, completion rate and lock-held duration. Restoring previous SET or RESET removes the new override for future invocations/new sessions according to route. It cannot undo a cancellation, restore a terminated transaction, or repair a side effect outside transaction rollback.

#### D. Safer maintenance orchestration: essential internal behavior, optional later customer-action family

For Pooltune's own DDL, bound lock acquisition, use supported concurrent variants, schedule against measured low-contention windows, and defer rather than pile into an existing wait queue. This is already executor safety, not a new customer fix. For customer cron DDL, a schedule-only action can be in family B. Rewriting the command from a blocking operation to a concurrent one requires command-specific semantic/transaction validation and separate authorization; no general regex rewrite.

App/RPC repairs such as consistent row-lock ordering, splitting long transactions, releasing connections before external I/O, and introducing queue-specific `SKIP LOCKED` are plausible concrete code patches, not intrinsically impossible. They change logic and retry/fairness semantics. A database-only V1 can provide a precise patch and a reproduction, while a later repo-connected agent can implement and test it under an explicitly broader scope. SKIP LOCKED must never be added blindly to general business queries because omitted locked rows change results.

### Gaps

- Hosted pg_cron extension version and mode must be discovered. The source conclusion is specifically verified against v1.6.4, which Supabase's current debugging documentation identifies; don't silently assume all future versions behave identically.
- Schedule-only edits need tests for running jobs, queued repeats, time zones and reload races. A snapshot saying no job runs now is not an atomic guarantee that deactivate will not cancel a run starting a moment later.
- Timeout values, worker identities and customer semantics are not provided. These are candidate capabilities, not authorized changes to Jared's projects.
