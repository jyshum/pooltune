# Representative index validation without copying customer databases

Research date: 2026-11-12. Source inspection and design proposals only. No credentials, customer connection, experiments, configuration changes, installation, or index creation. Six approved repair families remain unchanged. All numerical budgets below are tunable proposals, not benchmarked defaults.

## 1. What counts as a representative experiment?

### Takeaway

A query fingerprint identifies a family, not the parameters or security context needed to reproduce its performance. Qualify an explicit small set of important cases against the existing customer database. State what the cases cover and do not claim universal workload proof.

### Cited Findings

- Prepared statements can use custom plans based on parameter values or generic plans independent of them. Parameter types can be explicit or inferred. With default auto mode PostgreSQL initially tries five custom executions before comparing a generic plan's estimated cost. These are session-scoped objects. [PG17 PREPARE reference](https://www.postgresql.org/docs/17/sql-prepare.html)
- `pg_prepared_statements` exposes `parameter_types`, `generic_plans` and `custom_plans` only for the current session. A monitoring connection cannot use it as a history of another application's prepared plans. [Prepared statement view](https://www.postgresql.org/docs/17/view-pg-prepared-statements.html)
- PostgREST establishes transaction-scoped request settings and impersonated roles; generated SQL uses prepared statements. Headers and JWT claims can affect execution context. [PostgREST transactions](https://docs.postgrest.org/en/stable/references/transactions.html)
- PostgreSQL estimates selectivity using sampled statistics. Limited-column estimates may not model correlated columns well. [Planner statistics](https://www.postgresql.org/docs/17/planner-stats.html)
- LIMIT/OFFSET values can change the chosen plan, and deterministic paging requires a unique ordering. [LIMIT/OFFSET reference](https://www.postgresql.org/docs/17/queries-limit.html)
- Partial index eligibility depends on proving its predicate during planning; a generic parameterized predicate need not imply the index's predicate. [Partial indexes](https://www.postgresql.org/docs/17/indexes-partial.html)
- EXPLAIN VERBOSE prints the computed query identifier. Actual `explain.c` emits `Query Identifier` as signed int64 when verbose is enabled, queryId is nonzero, and compute_query_id is not regression mode, matching the BIGINT representation in pg_stat_statements. [EXPLAIN implementation](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/backend/commands/explain.c)
- pg_stat_statements normalizes constants, combines multiple executions, and its identity additionally includes database, user and top-level status. Hash stability has limits. [Query statistics](https://www.postgresql.org/docs/17/pgstatstatements.html)

### Inferences

Recommended minimal case dimensions for an enrolled query family:

| Dimension | Cases to consider | Why it matters |
|---|---|---|
| Tenant size | A typical small tenant and a large tenant, if the app is multi-tenant | A lookup scanning ten records can mask a tenant scanning millions |
| Selectivity | Common/broad value and rare/selective value | The same index need not be best for both |
| Time filter | Short recent window and longer historical window | Changes matches and potentially partitions, cache posture or sort work |
| Pagination | Typical first page plus representative later page/cursor | More skipped work or a different predicate can alter the plan |
| Empty/boundary input | Legitimate empty result and null/boundary where supported | Catches assumptions hidden by a single happy-path case |
| Types/context | Correct parameter types, effective role, claims, search_path, relevant settings | Tests the actual intended query rather than a plausible-looking substitute |

This is a selection guide, not an instruction to build the Cartesian product of every dimension. For a single-tenant app, tenant-size cases do not apply. Begin with 4–6 relevant cases, capture why each was selected, and retain unknown/uncovered dimensions explicitly. A large tenant is not necessarily a frequent tenant: table distribution and request distribution are different evidence.

Sources of case definitions, in preference order: existing developer-supplied examples or authorized test requests; approved metadata or bounded in-database selection under an enrolled rule; otherwise ask for a small set through the dashboard. No new application SDK is a prerequisite. Merely connecting Supabase can discover expensive families, but does not automatically reconstruct every request or grant authority to impersonate end users.

Use two valid experiment paths, without conflating them:

1. **Enrolled HTTP request:** issue the real Data API request with an authorized test identity and its established role/claim context, requesting a plan instead of rows. This can measure the real generated SQL without first reconstructing raw SQL. Preserve projection, filter, ordering, pagination, count/representation preferences, and RPC arguments. A generic monitoring role with BYPASSRLS is not a representative end-user test identity.
2. **Enrolled SQL statement:** where exact SQL, types and authorized session context are available, run controlled EXPLAIN in a dedicated session. This enables same-session HypoPG preflight. If identity/context equivalence is incomplete, label it an approximation rather than an exact replay.

Query Identifier equality is useful linkage to a monitored family, not proof of equivalent claims, parameters, data, generic/custom cache state or service latency. Lack of a matching query ID likewise requires investigation, not automatic rewriting until IDs happen to match. Never bypass RLS to obtain a faster benchmark.

Inspect both generic and custom estimated plans when mode is relevant. `EXPLAIN (GENERIC_PLAN)` supports estimates, not actual execution. Actual testing may use explicitly scoped plan_cache_mode and PREPARE/EXECUTE in the controlled SQL session, but those are experiments, not proof of the application's current pooled-session mode. Do not infer PostgREST's preparation history from a freshly opened collector session.

### Gaps

The selected customer's schemas, RPC dependencies, real request shapes, representative parameter types, workload skew and settings are unknown. Statistics alone cannot supply a complete request replay corpus, end-user context, or reliable traffic weights. A case set may improve confidence without proving absence of every possible regression.

## 2. Can we prove an index helps before creating it?

### Takeaway

HypoPG estimates usefulness without building an index. It cannot measure the candidate's real runtime. A no-copy workflow must distinguish preflight estimates from an authorized real index build and subsequent measured verification on the customer's existing database.

### Cited Findings

- Hypothetical indexes live in private connection memory, are not ordinary catalog indexes, and only affect EXPLAIN without ANALYZE. [HypoPG usage reference](https://hypopg.readthedocs.io/en/rel1_stable/usage.html)
- Supabase provides HypoPG as an available extension; availability is not evidence that every project already has it enabled or that every SQL transport preserves its session. [Supabase HypoPG documentation](https://supabase.com/docs/guides/database/extensions/hypopg)
- EXPLAIN ANALYZE executes its statement, discards normal SELECT output, and retains side effects. `TIMING OFF` avoids per-node timing overhead while total execution time is still measured. `SETTINGS` reports nondefault planning-related settings, not the complete session configuration. [EXPLAIN reference](https://www.postgresql.org/docs/17/sql-explain.html)
- EXPLAIN timing excludes client network delivery, and measurement itself adds overhead. Toy-sized tables cannot predict large-table performance. [Using EXPLAIN, caveats](https://www.postgresql.org/docs/17/using-explain.html)
- Supabase's Data API `.explain()` returns a plan, supports JSON and ANALYZE, and is disabled by default. Production exposure requires protection; configuring a new pre-request hook must account for an existing hook. [Supabase plan reference](https://supabase.com/docs/guides/api/query-plans)
- CREATE INDEX CONCURRENTLY performs multiple phases and scans, can leave invalid indexes on failure, cannot run in a transaction block, and can affect write work. Concurrent does not mean free or without waits. [CREATE INDEX reference](https://www.postgresql.org/docs/17/sql-createindex.html)
- READ ONLY transactions block specified SQL writes but are not a guarantee against all disk activity. Functions have declared volatility contracts; VOLATILE functions can modify data and function annotations can be wrong. [SET TRANSACTION](https://www.postgresql.org/docs/17/sql-set-transaction.html), [Function volatility](https://www.postgresql.org/docs/17/xfunc-volatility.html)
- Supabase branches are separate environments with their own credentials/configuration, not the live database instance. [Branching reference](https://supabase.com/docs/guides/deployment/branching)

### Inferences

Practical lifecycle proposal:

1. Capture observed workload baseline and a small number of representative measured read-only cases, if they are qualified.
2. Produce bounded eligible index candidates. If exact SQL/context and session continuity are available, compare hypothetical estimated plans and size estimates. Reject obviously unsuitable candidates. If HTTP-only context is available, do not pretend a HypoPG index in a different SQL session can influence a PostgREST request.
3. Select one candidate under an explicit customer policy for controlled production changes: size/workload limits, permitted tables/index forms, build window and recovery authority. **At this point actual candidate performance is still unproven.**
4. Create the actual index through the guarded executor, track progress and catalog validity, reconcile uncertain responses rather than blind retry.
5. Run the same qualified cases again, then observe ordinary workload windows and nearby queries plus write/resource context. Tag whether each result is estimate, controlled measurement or ordinary workload observation.
6. Keep, remove Pooltune's own eligible index under its rollback policy, or mark inconclusive. An invalid index also needs explicit recovery, not a success label.

If product policy requires measured benefit before any production mutation, we need a separately approved isolated test environment with representative data. That is an optional stronger testing path with cost and data-handling implications, not a mandatory default or a free exact clone. Even a copy differs in cache state, concurrency and timing. Do not provision or copy anything automatically under current scope.

The required data boundary: the representative application dataset stays in the customer's Supabase environment. EXPLAIN runs there and returns plans/numbers, not selected application rows. Parameters and plan expressions can still be sensitive: “no dataset replication” is not “zero data leaves.”

Start the executable subset with enrolled SELECT cases over reviewed ordinary relations and supported expressions. A leading SELECT keyword is not sufficient: it can invoke user functions, operators, views/RLS expressions, foreign access or remote side effects. Plain estimated EXPLAIN can also evaluate expressions during planning. Inspect dependencies and reject uncertain cases; explicitly enroll RPCs and dependencies rather than execute arbitrary logged SQL. A read-only transaction and rollback are additional controls, not a universal external-side-effect sandbox.

Cache posture must be recorded, not claimed controlled. Repeated runs warm caches; index building itself can change cache contents. Record first/subsequent runs and shared reads/hits, randomize case order, compare similar conditions and observe subsequent real workload. Do not flush production caches, restart the database, or use `DISCARD ALL` as if it clears shared/OS caches. No-copy pre/post comparison has time/load/data-change confounding, so count insufficient evidence honestly.

Suggested initial experiment budget, explicitly unvalidated: at most 3 hypothetical candidate definitions; 6 cases; generic/custom estimates only where applicable; at most one real candidate build at a time per project. For controlled execution, at most 3 repetitions per case before and after, no more than 36 executions total, with an independent aggregate duration/resource cap that can stop earlier. Use case-specific statement/lock limits, a quiet-window gate and stop on saturation. Budget exhaustion produces an incomplete case set, not assumed success. Long queries may need different approved budgets, not silently longer timeouts.

### Gaps

No test identity, Supabase project, authorized setting changes or known representative inputs were supplied. Data API plan exposure may be disabled. SQL transport privileges and same-session semantics must be tested before promising HypoPG. No acceptable fixed performance threshold, sample count, production write-overhead margin or automatic rollback threshold has yet been validated.

## 3. How do we qualify partial coverage and demonstrate V1 honestly?

### Takeaway

Eligibility should advance in explicit stages. It is useful for Pooltune to monitor many queries while only repairing a smaller qualified subset. Low setup does not require pretending every discovered query is automatically replayable.

### Cited Findings

The underlying sources establish distinct capabilities: normalized historical counters, parameterized prepared execution, estimated hypothetical plans, and measured real execution. None provides a universal guarantee that a candidate improves every production request. [pg_stat_statements](https://www.postgresql.org/docs/17/pgstatstatements.html), [PREPARE](https://www.postgresql.org/docs/17/sql-prepare.html), [HypoPG](https://hypopg.readthedocs.io/en/rel1_stable/usage.html), [EXPLAIN](https://www.postgresql.org/docs/17/sql-explain.html)

### Inferences

Proposed qualification states:

| State | Required evidence | Allowed conclusion |
|---|---|---|
| Monitor-only | Visible stable query family and valid intervals | This family is expensive or regressing |
| Estimable | Available exact/enrolled statement or request context; usable planning path | Planner predicts this access path/candidate may help |
| Controlled-test-ready | Authorized role/context, reviewed side effects, representative case set, limits | We may measure these particular cases |
| Automatic-repair-qualified | All prior required evidence plus permitted candidate/build policy, privileges, budget/headroom, rollback/reconciliation and verification plan | We may attempt a bounded index change, not claim improvement yet |
| Verified / regressed / inconclusive | Valid post-change evidence against defined acceptance criteria | Actual observed outcome within stated coverage |

“Estimable” is not always dependent on HypoPG: ordinary plans are useful even when hypothetical indexes are unavailable. Likewise, HTTP controlled tests can be ready without reconstructing SQL, while hypothetical comparison remains unavailable. Track individual capability flags alongside the stages, avoiding a falsely linear capability model.

Minimal retained case descriptor proposal:

```text
case_id, query_family_key, statement_or_request_template_digest,
test_identity_ref, parameter_type_list, parameter_set_ref,
case_labels [large_tenant, broad_filter, later_page], selection_reason,
context_digest, schema_version, source_observation_time,
plan_mode [observed, forced_custom, forced_generic, unknown],
execution_allowed, coverage_gaps, expiry
```

The model receives references and coarse case labels, not JWTs, access tokens or unrestricted raw values. Test identities are supplied/authorized rather than minted to impersonate arbitrary users. Store only narrowly needed parameters in a restricted, encrypted short-retention execution store if retention is explicitly allowed; otherwise use ephemeral references or approved in-database selection. Redact literals from returned plan conditions before logs/model storage. A case ID does not magically eliminate the need for a secure backing source. Do not export raw SELECT results merely to validate an index.

Proposed first demonstration on a future authorized test project:

1. Enroll one important SELECT family with a correct request/role context and 4–6 cases covering the relevant skew. Keep application data in that customer's project.
2. Show the detector ranks the family using real valid intervals; record capability/visibility gaps.
3. Link a measured baseline plan to the family when Query Identifier is available, while independently checking context and case identity.
4. Demonstrate a predicted candidate where supported. Clearly display predicted cost separately from milliseconds.
5. Under a pre-approved bounded build policy, create one nonunique index, recover correctly from simulated uncertain worker state, and verify valid catalog state before use.
6. Repeat cases and inspect buffers, rows/loops, temporary work and elapsed times; then compare ordinary workload means with sufficient completed calls. Inspect nearby enrolled queries and write/resource counters.
7. Require a predeclared meaningful improvement threshold and no breached regression/side-effect limits. Set the concrete thresholds from the baseline noise and query goals before deployment. No adequate samples means inconclusive.
8. Prove negative behavior too: wrong role, missing parameters, unsafe RPC, changed schema, saturation and expired evidence prevent automatic execution. Demonstrate rollback applies only to the index Pooltune created.

For an index addition we are not rewriting query semantics, but an app that relies on unspecified row order can still observe changed order. Flag missing deterministic ordering where relevant, without expanding V1 into application-query rewrites. The demonstration measures a qualified subset; its report should list uncovered tenants, filters, modes, and related query families.

### Gaps

This is a proposed validation contract, not an implemented harness. Product approval of controlled builds before measured proof must be explicit in the final technical specification. No service can infer all application correctness requirements or guarantee performance for all parameter combinations solely from PostgreSQL's aggregate telemetry.
