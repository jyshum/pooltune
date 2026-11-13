# Additional planner repairs for Pooltune V1

## Which additions most directly optimize a hosted Supabase database?

### Takeaway

Proposal: add two concrete operations under **planner statistics repair**: selected extended statistics, and bounded column statistics targets. Keep parameter-sensitive RPC plan repair as a narrower pilot. JIT and parallel changes deserve experiments, not a broad automatic knob tuner. These recommendations are research proposals, not approved implementation or authorization to access projects. Sources accessed November 11, 2026; PostgreSQL 17 is the reference implementation, not an assertion about a customer's installed major version.

### Cited Findings

- PostgreSQL recommends better statistics over bluntly suppressing planner methods. Its `plan_cache_mode` supports `auto`, `force_custom_plan`, and `force_generic_plan`; the choice is considered when a cached plan executes. [Query planning configuration](https://www.postgresql.org/docs/17/runtime-config-query.html)
- Extended statistics are native PostgreSQL objects. Supported kinds are `dependencies`, `mcv`, and `ndistinct`; collecting their data requires `ANALYZE`. Their creation requires ownership of the underlying table. They currently do not improve table-join selectivity estimates. [CREATE STATISTICS](https://www.postgresql.org/docs/17/sql-createstatistics.html)
- Supabase provides customer database roles, but a role's actual privileges still matter. A monitoring connection is not automatically an owner-capable mutation connection. [Supabase roles](https://supabase.com/docs/guides/database/postgres/roles)

### Inferences

- Proposed priority: **V1 core** extended statistics and column targets; **opt-in V1 pilot** function custom plans; **later experiment** function JIT-off and parallel limits.
- This supports the headline “an agent that optimizes your database”: the agent repairs how PostgreSQL chooses work, rather than only moving a workload onto larger compute.
- Keep one typed operation per repair, plus a common lifecycle: observe, explain the cause, estimate cost, compare old state, apply bounded change, observe real traffic, keep or recover. Open source can expose all preconditions, SQL templates, test fixtures, and audit events. It does not require opening up arbitrary generated SQL execution.

### Gaps

- No hosted project was accessed. Actual PostgreSQL version, role ownership, effective settings, managed PostgREST version, and workload evidence must be probed during authorized onboarding.
- No evidence supports a promise that every slow query benefits from these changes. The action catalog needs explicit “not eligible” outcomes.

## Can the agent repair correlated-column estimates with CREATE STATISTICS?

### Takeaway

Yes. This is a strong V1 addition when a query's predicates or grouping use columns from the same table and a measured estimate error causes a worse plan. It is a different repair from merely rerunning `ANALYZE`.

### Cited Findings

- Dependencies apply to simple column-to-constant equality and constant `IN` conditions. They do not cover ranges, `LIKE`, column-to-column comparison, or column-to-expression comparison. They assume compatible predicates, so contradictory combinations can still be misestimated. `ndistinct` addresses numbers of groups; MCV records common joint values. [Planner statistics](https://www.postgresql.org/docs/17/planner-stats.html)
- PostgreSQL's worked example improves a two-column estimate from 1 row to the actual 100 rows using dependency statistics; separate examples demonstrate grouping and joint MCV use. This demonstrates estimate repair, not a guaranteed latency gain. [Multivariate examples](https://www.postgresql.org/docs/17/multivariate-statistics-examples.html)
- `pg_stats.correlation` measures physical row order versus one column's logical order. It does **not** measure dependence between two columns. [pg_stats](https://www.postgresql.org/docs/17/view-pg-stats.html)
- Creation acquires `ShareUpdateExclusiveLock`, conflicts with concurrent statistics maintenance/DDL, and does not interfere with ordinary currently executing queries. [PG17 statistics command source](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/backend/commands/statscmds.c)
- `IF NOT EXISTS` compares only the object name, not its definition. Expression statistics can also provide estimates without maintaining an expression index. [CREATE STATISTICS](https://www.postgresql.org/docs/17/sql-createstatistics.html)
- Only the statistics owner, its schema owner, or a superuser can drop the object. [DROP STATISTICS](https://www.postgresql.org/docs/17/sql-dropstatistics.html)

### Inferences

**Proposed typed executor:** `create_extended_statistics(relation_oid, column_attnums, kinds, expected_catalog_hash, action_id, budgets)`. V1 restricts to two or three plain columns on one ordinary application table. Resolve and quote identifiers from catalogs, not model text. Do not blanket-create every combination or every kind.

**Evidence:** find a repeated expensive statement; identify the first materially wrong row estimate inside its plan. Compare estimates for individual filters versus their conjunction, or estimated versus actual group count. Rule out stale statistics first. Use plans in the real application role, RLS context and parameter classes. Require representative actual evidence from an authorized safe execution or captured plan; column names and `pg_stats.correlation` are not sufficient. A join node's bad estimate alone is not eligibility: the repair must concern a supported same-table restriction or grouping beneath it.

**Preflight:** snapshot table OID/name/owner, column identity/type, statistics definitions/owners, server version, table size, current maintenance and lock state. Require table-owner authority, schema creation privileges where applicable, and a valid `ANALYZE` path. Check existing objects for semantic duplication. Use one operation per relation, short lock timeout, bounded statement time, and an I/O budget. A maximum elapsed-time cap limits runtime, not total I/O; cancel only Pooltune's own work if necessary. No customer sessions are interrupted.

**Example action, illustrative names only:**

```sql
CREATE STATISTICS public.pooltune_orders_tenant_status (mcv)
ON tenant_id, status FROM public.orders;
ANALYZE public.orders;
```

Use `dependencies` for demonstrated dependent compatible equalities; use `mcv` for skewed joint values; use `ndistinct` for misestimated multi-column group counts. A `tenant_id,status` object is useful only if this particular workload and distribution justify it. Generic parameterized plans can limit value-specific benefits, so test the same preparation behavior as the app.

**Application path:** these are catalog changes consumed by subsequent planning across Data API, RPC, and direct SQL. No application deployment is inherently needed. Statistics updates trigger re-planning of affected prepared statements; measure the consequent planning work, too. [PREPARE](https://www.postgresql.org/docs/17/sql-prepare.html)

**Verification:** confirm the intended statistics were populated, compare actual plan shape and row estimates, then compare execution time, buffer use, temporary I/O, and application latency for comparable traffic. Check other high-volume query fingerprints touching this table. Better estimates with unchanged latency are not proof of an optimization win.

**Recovery:** compare the created object's identity/definition/ownership against the action record, then drop only that object. If state changed, stop automatic recovery. Dropping it removes the extra planner input; it does not restore ordinary statistics refreshed by `ANALYZE`, previously cached plans, elapsed I/O, or previously completed requests. Do not modify system statistics catalogs directly.

### Gaps

- No safe universal correlation threshold or latency percentage was established. These are policy values to validate in the pilot, not database facts.
- Expression statistics are promising, but arbitrary expressions can add maintenance work and require expression safety analysis. Propose simple-column support first; an explicitly allowlisted expression subset can follow.

## Can the agent improve skewed-column statistics without adding an index?

### Takeaway

Yes. Raise one column's statistics target when fresh statistics still produce poor estimates for an irregular distribution. This spends sampling, storage and planning effort to improve decisions; it is not a free improvement.

### Cited Findings

- The target controls detail in common-value lists and histograms. The documented default is 100; a larger target can improve irregular-distribution estimates and increases collection/storage effort. `pg_stats` only exposes readable tables and can contain actual data values. [Planner statistics](https://www.postgresql.org/docs/17/planner-stats.html)
- `ALTER TABLE ... ALTER COLUMN ... SET STATISTICS` accepts 0–10000 and takes `SHARE UPDATE EXCLUSIVE`. `DEFAULT` restores inherited behavior; older `-1` syntax is described as obsolete by the retrieved version. Table ownership is required for ordinary table alteration. [ALTER TABLE](https://www.postgresql.org/docs/17/sql-altertable.html)
- Retrieved PG17 catalog documentation/source represents inherited `attstattarget` as NULL. Do not assume the older negative sentinel across releases. [pg_attribute](https://www.postgresql.org/docs/17/catalog-pg-attribute.html), [PG17 catalog source](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/include/catalog/pg_attribute.h)
- `ANALYZE table(column)` can restrict which columns receive statistics. `SKIP_LOCKED` can still encounter blocking while opening indexes or children; `BUFFER_USAGE_LIMIT` limits its buffer strategy, not total scanned bytes. PG17 maintenance privileges and ownership rules must be checked against the deployed version. [ANALYZE](https://www.postgresql.org/docs/17/sql-analyze.html)

### Inferences

**Proposed typed executor:** `set_column_statistics_target(relation_oid, attnum, old_target_state, new_target, expected_catalog_hash, budgets)` followed by bounded `ANALYZE` of the target column. When an existing multicolumn statistics object needs refreshing, explicitly include all relevant columns or analyze the whole table; do not assume a one-column analyze refreshed it.

**Evidence:** repeated estimate error on a single column despite recent sufficient maintenance; skew or coarse histogram plausible; a controlled higher-target comparison improves the relevant estimates and yields a better measured plan. Do not raise the target merely because a table is large. Do not export customer common-value arrays to the model when summary metadata suffices.

**Preflight:** capture raw catalog value and normalized state `{inherited|explicit,value}`, effective default, column/table identity, ownership and existing stats objects. Bound increases, for example an experimental 100→250 step, without claiming 250 is generally optimal. The same budget and lock rules as extended statistics apply. Record recurring future `ANALYZE` cost as part of the product's tradeoff explanation.

```sql
ALTER TABLE public.orders ALTER COLUMN customer_id SET STATISTICS 250;
ANALYZE public.orders (customer_id);
```

**Application path and measurement:** no app deploy inherently needed. Compare different parameter classes, execution plus planning time, plan stability, estimate quality, and later maintenance duration. Do not simultaneously add an index and raise the target when attributing the improvement.

**Recovery:** restore the exact prior explicit target, or the deployed version's inherited/default syntax. Then optionally run a budgeted `ANALYZE` to rebuild using restored policy. That produces a new sample; it cannot reconstruct the old statistics or guarantee the old plan. Never describe this as exact performance rollback.

### Gaps

- Hosted Supabase defaults may differ from upstream. Read effective values before suggesting an increase.
- A higher target cannot fix arbitrary expression estimates, cross-column dependence, missing access paths, or an already optimal plan.

## Is function-scoped custom planning a real RPC repair?

### Takeaway

Yes, for a narrow case: existing PL/pgSQL RPCs whose internal parameterized statements choose a harmful generic cached plan. Propose an opt-in V1 pilot, with explicit planning-cost and parameter-diversity checks.

### Cited Findings

- PL/pgSQL prepares internal statements through SPI and can retain generic plans. Dynamic `EXECUTE` replans each execution already. [PL/pgSQL implementation](https://www.postgresql.org/docs/17/plpgsql-implementation.html)
- PostgreSQL's normal heuristic starts with five custom executions, compares estimated costs, then may choose a generic plan. `EXPLAIN EXECUTE` distinguishes generic parameter placeholders from custom values; prepared statements are session-local. [PREPARE](https://www.postgresql.org/docs/17/sql-prepare.html)
- A function configuration setting applies on entry and restores the previous setting on exit. [CREATE FUNCTION](https://www.postgresql.org/docs/17/sql-createfunction.html)
- Function alteration requires ownership and identifies overloads by input argument types. `SET` and individual `RESET` are supported. [ALTER FUNCTION](https://www.postgresql.org/docs/17/sql-alterfunction.html)
- PG17 inlining checks reject SQL functions with a non-null configuration array, so attaching a setting can remove an optimization. [PG17 optimizer source](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/backend/optimizer/util/clauses.c)
- PostgREST's documented default hoisted settings exclude `plan_cache_mode`. Native function settings and transaction-wide hoisting are different mechanisms. [PostgREST configuration](https://docs.postgrest.org/en/stable/references/configuration.html)

### Inferences

**Proposed typed executor:** `set_rpc_plan_mode(function_oid, identity_args, expected_function_hash, old_config_entry, mode=force_custom_plan, budgets)`.

**Eligibility:** one existing allowlisted PL/pgSQL function; identifiable internal parameter-sensitive statement; materially different common/rare parameter distributions; a warm-session generic-versus-custom comparison demonstrates total benefit. A high max/mean duration ratio alone is insufficient. Exclude dynamic `EXECUTE` already replanning, SQL functions whose inlining would change, and functions that override the same setting internally. No function source rewrite.

**Preflight and action:** snapshot exact signature, owner, body hash, language, security/RLS context and full `proconfig`; check that the target setting is supported and user-settable in this server. Compare-and-set only this field, preserve every unrelated entry. The example targets an existing function, not permission to create one:

```sql
ALTER FUNCTION public.search_orders(uuid, text)
SET plan_cache_mode TO force_custom_plan;
```

**Application path:** the function entry setting precedes internal SPI planning/execution, so it can affect those statements. It is too late to change the outer PostgREST statement's already selected plan. Do not present this as a generic fix for all parameterized Data API queries. An agent's private `SET LOCAL` only changes its own test transaction. No app deployment is inherently necessary for the native function setting, but test the real API route after applying it. Do not assume managed hoisting support or silently add that configuration.

**Measurement:** warm the relevant connection path past the initial custom executions; test common, rare, empty-result and authorized tenant parameter classes with comparable load. Compare planning plus execution cost, throughput, CPU, error rate and endpoint tail latency. Limit diagnostic calls, concurrency and runtime. Never auto-run arbitrary `EXPLAIN ANALYZE`: it executes the query and may invoke functions with side effects. Only approved read-safe fixtures or an isolated environment support execution-based experiments.

**Recovery:** if previously absent, `ALTER FUNCTION ... RESET plan_cache_mode`; otherwise restore that exact prior entry. Recheck function identity/body/owner and expected current config before recovery. Do not `RESET ALL`, terminate app sessions, or claim to undo requests already executed. Observe subsequent real requests after recovery.

### Gaps

- An external monitoring session cannot inspect every live application's session-local prepared plans. Internal statement proof may require an authorized reproduction or existing nested-statement instrumentation.
- A direct test that manually prepares extracted inner SQL is only a diagnostic surrogate. The function's actual role, RLS, parameter binding and warmed SPI behavior must corroborate it.

## Should JIT or parallel settings become headline V1 actions?

### Takeaway

Keep these as later bounded experiments. They can help specific queries but have weaker general eligibility than better statistics or proven generic-plan repair.

### Cited Findings

- JIT compilation can cost more than it saves for short queries. Cost thresholds are evaluated at plan time; `jit=off` affects both planning and execution. `EXPLAIN` can report JIT compilation time. [JIT decisions](https://www.postgresql.org/docs/17/jit-decision.html)
- Parallel query can multiply resources across workers and increase total system resource use under load. [Resource configuration](https://www.postgresql.org/docs/17/runtime-config-resource.html)
- PostgreSQL cannot establish arbitrary user function parallel safety automatically. User functions default unsafe; incorrect markings can cause errors or wrong answers. [Parallel safety](https://www.postgresql.org/docs/17/parallel-safety.html)

### Inferences

- A later `ALTER FUNCTION ... SET jit TO off` experiment is reasonable only when internal query JIT overhead is measured, the function scope applies, and mixed parameter/load tests show a net win. Restore only the prior `jit` entry. Function settings do not cover JIT work already performed by the outer caller.
- A later `ALTER FUNCTION ... SET max_parallel_workers_per_gather TO 0` experiment could reduce contention for an internal parallel plan, but must account for cached plan behavior and demonstrate better workload throughput rather than one query's faster/slower isolated execution. Restore the prior entry after comparison. No global worker increase, no customer connection restart, no automatic `PARALLEL SAFE` relabeling.
- Avoid making global `enable_seqscan`, join-method toggles, random cost constants, or disabling all PostgREST prepared statements standard V1 repairs. They obscure root causes and affect unrelated requests.

### Gaps

- There is no inspected customer plan showing JIT or parallel overhead here. These are mechanisms worth testing later, not current diagnosed problems.
- Hosted runtime restrictions and the exact cached-plan effect need a pilot before either operation becomes an advertised supported repair.
