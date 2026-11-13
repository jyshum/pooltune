# Function memory Cron and query trial limits

Research: November 12, 2026. PostgreSQL 17, hosted Supabase, official documentation/source review only. No database calls or experiments. Numeric proposals below are initial qualification policies, not accepted settings or universally safe defaults.

## How can function memory tuning remain useful and bounded?

### Takeaway

Function-scoped memory tuning can repair measured spills, but `work_mem` is not a total function-memory ceiling. Eligibility depends on concurrent demand as well as a faster single test.

### Cited Findings

- `work_mem` applies to individual sort/hash operations, with multiple operations and sessions consuming memory simultaneously. Hash operations use `work_mem × hash_mem_multiplier`; PostgreSQL's defaults are 4 MB and 2.0, respectively, not necessarily Supabase's effective values. Parallel query workers have individual limits. `temp_file_limit` caps temporary-file space per process, excludes explicit temporary tables, and requires appropriate setting privileges. These controls do not reserve a fixed share of database RAM. [PG resource settings](https://www.postgresql.org/docs/17/runtime-config-resource.html)
- A function's `SET` setting applies on entry and the previous value is restored on exit. A plain `SET` inside its body can override that behavior. `ALTER FUNCTION ... SET work_mem` changes the enrolled function; save its previous setting, including whether it was absent, for recovery. [CREATE FUNCTION](https://www.postgresql.org/docs/17/sql-createfunction.html), [ALTER FUNCTION](https://www.postgresql.org/docs/17/sql-alterfunction.html)
- `auto_explain.log_nested_statements` exposes statements inside functions, but requires privileged setup and is off by default. Analysis instrumentation has overhead; sampling and disabling node timing can reduce it. `pg_stat_statements` can expose temp-block and execution totals, but these are not a complete per-invocation memory trace. [auto_explain](https://www.postgresql.org/docs/17/auto-explain.html), [statement statistics](https://www.postgresql.org/docs/17/pgstatstatements.html)

### Inferences

- Proposed eligibility: known function definition, relevant inputs, internal spill evidence, a credible concurrency ceiling and measured resource headroom. The agent can cap its own trials, but cannot cap ordinary app invocations by changing function configuration.
- An illustrative screening calculation is concurrent invocations × concurrently active memory-consuming operations × participating processes × proposed `work_mem`, with hash multipliers and additional overhead. This is a conservative estimate to qualify, not a guaranteed allocator bound. Twenty concurrent calls with two sorts at 16 MB already imply about 640 MB before other memory.
- Start with the smallest candidate supported by the spill evidence, one trial at a time. Increase only within a customer-specific ceiling; verify temp work, duration, errors and whole-workload health. Test multiple input sizes rather than only the smallest customer.
- If concurrency cannot be bounded credibly, continue diagnosis and recommend the candidate, but defer persistent memory increases. A general-purpose proxy or app concurrency rewrite would expand the accepted scope.
- Restore the prior setting on regression; do not claim this immediately reclaims memory from invocations already running. Recheck definition/settings before restoration so an intervening customer deployment is not overwritten.

### Gaps

Supabase availability of nested-plan capture, effective settings, monitoring freshness and the function's concurrent demand need project-specific qualification. A top-level function plan alone is insufficient evidence for its internal sort/hash behavior. Exact propagation to cached function execution deserves a version-specific integration test.

## What makes automatic Cron rescheduling eligible?

### Takeaway

Moving an enrolled job can reduce repeated contention without rewriting it. The difficult boundary is preserving the job's business timing and already-pending executions.

### Cited Findings

- `cron.alter_job` accepts schedule changes. pg_cron runs only one instance of the same job at a time; a second due execution waits. Its scheduler timezone defaults to GMT and is configurable. These are extension semantics, not a guarantee that all jobs finish by a deadline. [pg_cron documentation](https://github.com/citusdata/pg_cron)
- Supabase records job/run information in `cron.job` and `cron.job_run_details`, and recommends at most eight concurrent jobs with each lasting at most ten minutes. Those are provider recommendations, not enforced limits or permission to change global settings. [Supabase Cron](https://supabase.com/docs/guides/cron)
- Upstream scheduler state retains `pendingRunCount`; task refresh reuses existing job-ID state while reloading schedule metadata. Therefore a schedule edit must not be represented as flushing already queued executions. Code has special behavior for interval schedules and clock changes. Verify against the deployed extension version. [task-state source](https://raw.githubusercontent.com/citusdata/pg_cron/main/src/task_states.c), [scheduler source](https://raw.githubusercontent.com/citusdata/pg_cron/main/src/pg_cron.c)

### Inferences

- Proposed initial envelope: schedule-only changes to one enrolled job at a time. Preserve command, identity and intended execution count; require owner-approved earliest/latest times, maximum delay, timezone and job dependencies. Example: move a nightly report from 01:00 to 01:30 only if it still finishes before the agreed 02:00 deadline.
- Select a quiet transition away from both old/new due times, inspect recent runs and actual duration, preview future occurrences and daylight-saving boundaries, and journal old/new schedules. After changing, verify the next actual runs and important foreground queries. Timing correlation alone does not establish benefit.
- If the queue is ambiguous, the job is already backlogged, or business deadlines are unknown, defer the schedule change. Do not cancel an active customer job or silently skip work to make the latency graph improve.
- Recovery means restoring a suitable future schedule, not undoing completed work. Do not blindly restore an old expression if doing so creates another run or misses a deadline.

### Gaps

There is no verified SQL-visible, durable pending-run counter in this review. Job history alone cannot prove the absence of queued execution. Initial qualification should use simple, slow-frequency jobs with clear business windows; second-based or deadline-critical jobs need additional evidence.

## What can controlled query trials actually guarantee?

### Takeaway

A trial is real database work. Enforce time, concurrency and eligible effects; treat resource headroom and before/after comparability as measured conditions rather than guaranteed quotas.

### Cited Findings

- `EXPLAIN ANALYZE` executes its statement, including side effects. `TIMING OFF` avoids repeated node-clock overhead while retaining whole-statement timing. `MEMORY` in PG17 describes planning memory, not total execution memory. [EXPLAIN](https://www.postgresql.org/docs/17/sql-explain.html)
- `statement_timeout` aborts overlong statements; `lock_timeout` applies separately to lock acquisition attempts. Read-only transactions prohibit many mutations but are not a complete side-effect sandbox. Volatile functions can modify state, while immutable functions can be evaluated during planning. [timeouts](https://www.postgresql.org/docs/17/runtime-config-client.html), [read-only semantics](https://www.postgresql.org/docs/17/sql-set-transaction.html), [volatility](https://www.postgresql.org/docs/17/xfunc-volatility.html)
- Roles and RLS change visible data and query execution. An administrator trial can bypass customer policies. Cancellation success means a signal was sent, not proof that all work stopped or that a preceding action did not commit. [RLS](https://www.postgresql.org/docs/17/ddl-rowsecurity.html), [cancellation](https://www.postgresql.org/docs/17/functions-admin.html)

### Inferences

- Proposed policy: reuse existing evidence; otherwise one heavy trial per project, with the actual role/settings/inputs, a server deadline fitted to the query's observed runtime, shorter lock-wait budget, permitted temporary-space ceiling, and capped returned evidence. Longer reports need a larger preauthorized window, not perpetual automatic timeout escalation.
- Inspect function calls and effects, not merely the `SELECT` prefix. Do not claim `ROLLBACK` undoes external effects. Return plans rather than full result sets where sufficient; truncation must be explicit. Adding `LIMIT` changes the workload and cannot prove the original query's speed.
- Cancel only Pooltune-owned work with verified session identity, then reconcile before retry. Falling headroom can trigger stopping, but sampling delay prevents a promise of instantaneous protection. An interrupted trial produces incomplete evidence, not a slow completed execution.

### Gaps

Concrete timeout, byte, concurrency and latency thresholds require workload calibration. This review establishes no universal safe CPU percentage, global query-memory cap, or guaranteed zero-impact trial.
