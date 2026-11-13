# Implementable spill repairs for hosted Supabase

Checked November 11, 2026, America/Vancouver. Research only. No live project access or mutation. New memory-tuning actions below are **proposed**, not accepted; index changes, targeted ANALYZE, and existing-pool tuning retain their prior accepted status.

## 1. Can the agent establish a real spill and choose a repair?

### Takeaway

Yes. Sort/hash spilling can be diagnosed and sometimes removed with an eligible index or better statistics. A narrowly scoped new action, increasing memory for one existing RPC function, is technically feasible when the spill is inside that function and the system has a defensible memory/concurrency budget.

### Cited Findings

- `pg_stat_statements.temp_blks_read` and `temp_blks_written` count temporary blocks per normalized statement. `temp_blk_read_time`/`temp_blk_write_time` use milliseconds and depend on I/O timing. Compare counter deltas within the same `(dbid, userid, queryid, toplevel)` and reset epoch. `track=all` adds nested function statements; default upstream tracking is top-level. Do not double-count parent and nested statements. [PostgreSQL query statistics](https://www.postgresql.org/docs/17/pgstatstatements.html).
- `pg_stat_database.temp_files`/`temp_bytes` include temporary files regardless of logging settings. They establish database-level work, not which plan operator caused it. `stats_reset` is a comparison boundary. [PostgreSQL cumulative statistics](https://www.postgresql.org/docs/17/monitoring-stats.html).
- `log_temp_files` logs file sizes in bytes on file deletion; `0` logs all, positive values set a threshold, and `-1` disables. It requires superuser or appropriate parameter SET privilege. [PostgreSQL logging](https://www.postgresql.org/docs/17/runtime-config-logging.html). Supabase documents `-1` as its default and permits CLI/SQL configuration. Its prose describes creation, but upstream PostgreSQL is precise about the actual deletion-time emission. [Supabase log settings](https://supabase.com/docs/guides/database/postgres/postgres-log-config).
- An execution plan with on-disk Sort reports disk use. Hash `Batches > 1` entails disk use. Plain EXPLAIN only estimates; `EXPLAIN ANALYZE` executes the statement and its side effects. Incremental sorts can reduce spill risk. [PostgreSQL EXPLAIN](https://www.postgresql.org/docs/17/using-explain.html).
- A matching B-tree can satisfy `ORDER BY` without a separate sort; matching `ORDER BY ... LIMIT` can avoid processing the entire candidate set. It can be worse when most rows must be visited. [Indexes and ordering](https://www.postgresql.org/docs/17/indexes-ordering.html).
- Covering indexes can remove heap reads when required columns are available and visibility permits. INCLUDE columns do not become ordering/search keys and add index size. [Index-only scans](https://www.postgresql.org/docs/17/indexes-index-only-scans.html).
- ANALYZE updates planner statistics, which can change the selected plan. Autovacuum normally performs automatic analysis already. This supports a targeted intervention when estimates are demonstrably stale, not blanket periodic ANALYZE as a novel feature. [ANALYZE](https://www.postgresql.org/docs/17/sql-analyze.html).

### Inferences

**Proposed diagnosis sequence:**

1. Detect a sustained rise in temporary bytes plus latency/failures at comparable completed workload. Spill alone is not automatically an incident.
2. Rank statement fingerprints by interval temporary bytes and execution time. Compute approximate temp bytes as `Δtemp_blks_written × current_setting('block_size')::integer`, and normalize by `Δcalls`. Keep read and written blocks separate; do not interpret this as peak disk space or exact RAM needed.
3. Capture plan shape, parameters/parameter classes, app role/RLS context, estimated cardinalities, table changes, settings, and known nested calls. Identify whether the costly operator is inside the RPC or in PostgREST's outer query.
4. Prefer avoiding work: eligible filter/order index; refresh stale statistics if there is supporting evidence; consider a bounded memory change only when substantial necessary work remains and spare memory exists.
5. Use already-recorded actual plans or a controlled test project to validate external Sort or multi-batch Hash. Plain EXPLAIN is a candidate generator, not proof that a spill happened. Diagnostic execution in production requires its own bounded permission; do not run arbitrary functions simply because their outer statement is SELECT.

**Specific repair mapping:**

| Evidence | Implementable repair | Boundary |
|---|---|---|
| Filter plus ORDER BY/LIMIT sorts many rows; no matching index | Existing index action creates eligible composite B-tree using filter prefix and ordering keys | Demonstrate plan uses it; match direction/null/collation; count write/storage cost |
| Bad estimates after data changes lead to oversized join/sort input | Existing targeted ANALYZE | Refresh may improve plan; not a guaranteed spill cure and no true statistics undo |
| Necessary internal RPC sort spills; memory headroom and concurrency bound exist | Proposed function `work_mem` increase | Exact allowlisted function, all its callers included in budget |
| Necessary internal hash spills while sorts do not | Proposed later function `hash_mem_multiplier` change | Hash-only benefit; still multiplied by work_mem and concurrency |
| Whole table API request sorts outside any function | Eligible ordering index, or bounded role/request memory experiment only with separate broader grant | Function SET cannot fix the outer operation by itself |
| Oversized intermediate/result set caused by query shape | App/query rewrite in separately approved PR/deployment workflow | E.g. project fewer columns, push filters earlier, use keyset pagination; preserve semantics |

The last row is a real repair path, but repository/deployment access is additional product scope. It should not be advertised as an automatic database-only fix.

### Gaps

- Temporary block counters do not identify a unique operator or peak memory requirement. Parameter distributions, nested plans, and outer-query sorts may remain unknown.
- No universal Supabase work_mem default was established for every compute/version. Upstream PostgreSQL 17 uses 4MB, but read actual settings and override provenance instead of applying that assumption to hosted projects.
- No direct per-RPC peak-concurrency guarantee is supplied by pg_stat_statements. A pool ceiling bounds a service, not necessarily every direct caller or multiple pools; absent a valid conservative bound, decline automatic memory increases.

## 2. Which memory scopes actually affect Supabase application traffic?

### Takeaway

Use a persistent function configuration for a narrow RPC repair. An agent's own SET LOCAL does not affect app traffic. Role defaults and PostgREST function hoisting are distinct mechanisms with broader effects and reload requirements.

### Cited Findings

- `SET` affects only the current session; `SET LOCAL` lasts for the current transaction. [SET](https://www.postgresql.org/docs/17/sql-set.html).
- A function SET option takes effect on entry and restores the prior value on exit. An ordinary SET inside the body can override that restoration, so inspect the body and nested callees. [Function settings](https://www.postgresql.org/docs/17/sql-createfunction.html).
- `ALTER FUNCTION schema.name(input_argument_types) SET work_mem TO '8MB'` is valid. The executor must own the function or exercise owning-role authority. `RESET work_mem` removes its function-local override; existing explicit values must be restored explicitly. Input types distinguish overloads; argument names and OUT-only arguments do not identify the function. [ALTER FUNCTION](https://www.postgresql.org/docs/17/sql-alterfunction.html).
- Function identity, owner, language, security/volatility/parallel attributes, and local configuration are exposed through `pg_proc`; `proconfig` stores local settings. [pg_proc](https://www.postgresql.org/docs/17/catalog-pg-proc.html).
- PostgreSQL 17 source rejects both scalar and set-returning SQL-function inlining when `proconfig` is non-null. Adding a SET can therefore change planner behavior even if the new memory value is beneficial. This is verified against current versioned source, not only the older wiki. [PG17 optimizer source](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/backend/optimizer/util/clauses.c).
- PL/pgSQL RETURN NEXT/RETURN QUERY currently materializes the result and may spill to disk; work_mem controls when that storage starts. This means not every function spill is a Sort/Hash node. [PL/pgSQL result handling](https://www.postgresql.org/docs/17/plpgsql-control-structures.html).
- PostgREST explicitly applies impersonated-role settings as transaction settings, unlike raw PostgreSQL SET ROLE. It can hoist allowed function settings to the whole transaction, overriding connection/impersonated-role values. [PostgREST transactions](https://docs.postgrest.org/en/stable/references/transactions.html).
- `db-hoisted-tx-settings` is reloadable, supports in-database configuration, and **does not include work_mem by default**. Its documented defaults are `statement_timeout, plan_filter.statement_cost_limit, default_transaction_isolation`. Upstream examples permit adding work_mem. Configuration reload can use `NOTIFY pgrst, 'reload config'`. In PostgREST 16, configuring via authenticator ALTER ROLE is described as legacy and requiring superuser; managed Supabase capabilities must be checked separately. [PostgREST configuration](https://docs.postgrest.org/en/stable/references/configuration.html).
- `NOTIFY pgrst, 'reload schema'` refreshes schema metadata; configuration also reloads if in-database configuration is enabled. [PostgREST schema cache](https://docs.postgrest.org/en/stable/references/schema_cache.html).
- PostgreSQL ALTER ROLE defaults ordinarily apply to new logins; SET ROLE alone does not apply them. Existing ORM sessions therefore retain previous values until replaced or changed. [ALTER ROLE](https://www.postgresql.org/docs/17/sql-alterrole.html), [SET ROLE](https://www.postgresql.org/docs/17/sql-set-role.html).
- Supabase documents per-function REST behavior for statement_timeout and `NOTIFY pgrst, 'reload config'` after API-role timeout changes. That supports the hosted role-setting route, but is not a specific guarantee that an arbitrary newer hoist setting is changeable on every hosted version. [Supabase timeout scopes](https://supabase.com/docs/guides/database/postgres/timeouts).
- Supabase already exposes global work_mem customization via SQL/CLI without a restart requirement. That is a broad existing control, not a safe per-query automatic feature. [Supabase custom configuration](https://supabase.com/docs/guides/database/custom-postgres-config).

### Inferences

**Recommended initial new category: “Tune memory for selected RPC functions.”**

- First version changes only `work_mem` for an exact allowlisted existing PL/pgSQL function, or another function proven already non-inlined, with a known internal spill and inspected body. Do not rewrite the body or create wrappers as part of this action.
- Existing callers keep the same API. There is no required application deployment for a native function setting. Changes affect subsequent invocations through all calling paths, not just one HTTP route. There is no per-user canary: one catalog change is shared.
- Require no unreviewed in-function `SET`, dynamic SQL, opaque nested calls, recursion, or role/security changes that undermine the memory model. More complex functions can remain review-required until evaluated.
- Do not initially add work_mem to PostgREST's hoist list automatically. Native function scope is sufficient for internal operators; hoisting expands the budget to the outer request and may change existing functions across the service. If hoisting already exists, account for whole-transaction scope explicitly.
- A SQL function losing inlining can prevent filter/limit pushdown and generate more work. Restricting the initial action to already non-inlined functions avoids introducing that particular regression; still test result materialization and outer query behavior.
- Function-level `hash_mem_multiplier` is technically expressible by the same mechanism. Consider it a later parameter within the explicit category after separate tests. Change one knob at a time.
- A specialized role dedicated to one controlled job could support `ALTER ROLE ... IN DATABASE ... SET work_mem`, but shared `authenticated`, `anon`, or `authenticator` roles are broad. Do not call those query-specific fixes.

### Gaps

- Exact hosted PostgREST version, active hoist list, permitted configuration route, propagation/cache behavior, and effective app-call settings must be verified on Jared's disposable project before enabling this executor. A successful catalog readback alone is insufficient.
- An executor cannot grant itself ALTER FUNCTION authority. Ownership delegation must be established during authorized setup; ordinary execute or monitoring privileges are insufficient.

## 3. What is the executable action and recovery contract?

### Takeaway

A bounded function-memory experiment can be implemented without arbitrary mutation SQL. It needs a verified function identity, explicit state comparison, a concurrency-aware memory budget, app-path validation, and exact configuration restoration.

### Cited Findings

- Upstream PostgreSQL 17 defaults work_mem to 4MB and hash_mem_multiplier to 2.0. work_mem is per sort/hash-like operation, not per connection; hash limits multiply it. Parallel workers each consume resources, so four workers plus leader can multiply resource use fivefold. `temp_file_limit` cancels transactions exceeding a per-process temporary-space cap and excludes explicit temporary tables. It is containment, not a speedup. [PostgreSQL resources](https://www.postgresql.org/docs/17/runtime-config-resource.html).
- Supabase already configures connection defaults by compute tier and provides memory observability. Its compute page does not establish a safe per-function memory ceiling. [Supabase compute](https://supabase.com/docs/guides/platform/compute-and-disk), [Supabase performance tuning](https://supabase.com/docs/guides/platform/performance).

### Inferences

**Proposed typed operation** (design, not live instructions):

```text
set_function_work_mem {
  project_ref, database_oid,
  function_oid, schema, name, input_type_oids,
  expected_definition_digest, expected_owner_oid,
  expected_proconfig, expected_effective_work_mem,
  new_work_mem_kib,
  evidence_ids, memory_budget_id,
  permission_grant_id, expires_at,
  observation_policy_id, idempotency_key
}
```

The policy executor resolves catalog names/types itself, allowlists the parameter, formats SQL identifiers/literals safely, and rejects arbitrary SQL. The model cannot broaden object scope or raise budget caps.

**Preflight:**

1. Match permission grant, allowlisted function OID/signature, owner, definition hash, all existing function settings, current effective settings for the real app caller, and absence of another active experiment/deployment. Capture unrelated `search_path`, timeout, security and parallel properties so they cannot be accidentally altered.
2. Attribute the spill to an internal operator/result materialization; use stable parameter classes and recent comparison windows. Require meaningful calls and observed latency impact. Check if index/ANALYZE already solves the same cause with less retained memory.
3. Budget worst plausible incremental work memory: `max concurrent affected calls × (worker allowance + leader) × (simultaneous sort allowance × Δwork_mem + simultaneous hash allowance × Δ(work_mem × hash_mem_multiplier))`, plus materialization and safety reserve. This is a conservative eligibility model, not a PostgreSQL allocation guarantee. Count other pools, background jobs, direct callers, autovacuum and competing traffic. Sum relevant PostgREST instances/pools per database host; calculate each read replica's budget separately rather than treating primary headroom as replica headroom. An average free-memory sample is insufficient.
4. Reject unavailable concurrency bounds, unknown nested plans, parameter growth outside tested limits, current swap/OOM pressure, unmodeled caller paths, or a budget that is only safe at average load. A narrower endpoint allowlist cannot reduce the effect of a shared function definition.
5. A proposed pilot policy might allow one doubling, e.g. 4MB to 8MB, only when the model passes. Those numbers are experiment defaults to validate, not universal safe values. Never calculate required RAM directly from temp-file byte size.

**Apply:**

- Persist intent and exact old state before mutation. Acquire a Pooltune per-function advisory lock for agent coordination, then re-read state. That lock does not prevent customer DDL; a detected external edit stops the experiment.
- In a short transaction with bounded lock/statement timeout, execute only the generated `ALTER FUNCTION ... SET work_mem TO ...`. A short retry after lock failure is permitted only if state still matches. Capture post-change proconfig and compare all untouched fields.
- Refresh PostgREST schema metadata where applicable and verify a controlled real RPC invocation sees the intended scope, using a disposable pilot fixture or already authorized instrumentation. Do not modify an arbitrary production function to print current_setting as a hidden verification step.
- Validate requests across reused connections and new connections. A catalog value is not proof that all cached/managed request behavior has propagated. No application code deployment is inherently needed for this narrow action.

**Measure:**

- Primary: lower temporary blocks written per completed call and better execution/request latency at comparable call rate, input/row-count distribution, role/RLS context and deadlines.
- Guardrails: completed throughput, error/timeout rates, memory available/swap/OOM indicators, CPU, disk, and concurrent workload latency. Fewer spills while completing fewer requests is not a success. Do not claim p95 from pg_stat_statements means.
- If traffic is insufficient or parameter mix changed, classify inconclusive and apply the pre-agreed experiment-expiry restoration policy. One active change; cooldown before any further increase. No automatic stair-step to maximum memory.

**Recovery:**

- Network timeout/crash after ALTER: inspect the function. If current state equals intended state, resume verification; if it equals old state, retry only under unchanged eligibility; if neither, mark conflict and stop.
- Restore only the altered key. If old `proconfig` contained work_mem, restore that exact value. If absent, use `ALTER FUNCTION exact_signature RESET work_mem`; do not set the historical effective numeric value, because that would create a new override. Never RESET ALL or replace the whole function to undo one GUC.
- Before restoring, compare identity, definition and target value with Pooltune's last-known state. Preserve customer changes; mismatch stops blind restoration. Re-run applicable metadata reload and app-path verification.
- Restoration governs later calls. It does not undo memory already allocated by in-flight work, completed requests, or any OOM harm. Full stop blocks new experiments; cancellation/termination is outside this action and remains unapproved.
- Persist the change in an exportable migration/action history so application deployments that recreate the function do not silently erase the tuning. Such deployment drift invalidates the outcome window and triggers re-evaluation, not automatic reapplication.

**Minimum pilot evaluations before auto-enabling:**

- Internal PL/pgSQL sort that spills at baseline, improves with bounded memory, and restores cleanly when original override is absent.
- Existing explicit override restored exactly, preserving search_path and other properties.
- Shared SQL RPC where SET would disable inlining: executor rejects it until separately supported.
- Outer PostgREST sort remains unaffected: never claim success from inner-only configuration.
- Hash spill with no sort spill; confirm work_mem/multiplier math and avoid simultaneous knob changes.
- Concurrent callers plus parallel plans exceed budget: no action.
- Disconnect after commit, insufficient traffic, catalog replacement, manual edit, stale cached plan/config, and post-change memory regression: deterministic recovery paths.

### Gaps

- Exact peak operator overlap is hard to bound from plain plans; finite pilot allowlists and conservative concurrency ceilings are necessary. If those prevent all useful cases in the target projects, retain a reviewable proposed SQL change until better evidence exists rather than pretending autonomy is safe.
- Tuning function memory does not solve every spill. Query shape, result size, poor estimates, unavoidable datasets, and hardware constraints can require other actions. This limitation should narrow eligibility, not erase the feasible repair category.
