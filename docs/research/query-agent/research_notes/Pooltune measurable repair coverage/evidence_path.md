# Supabase observation-to-index-repair evidence path

## Does the firsthand account establish a solvable repair?

### Takeaway
It provides unusually concrete, technically plausible evidence of an index repair. It does not establish that Pooltune already reproduces the result or that the reported speedup generalizes to another project.

### Cited Findings
- The author reports a quota-check query filtering `metric_name` and `state`, sorting `breach_detected_at DESC`, and taking `LIMIT 1`. The old index contains `(metric_name, breach_detected_at)`; the replacement adds `state` between them. The published plans show a parallel sequential scan taking **605.274 ms**, with **232,178 shared blocks read**, versus an index scan taking **0.048 ms**, with **3 shared buffer hits**. Both return zero rows. The author reports a separate page improving from 30–40 seconds to about two seconds, then about 300 ms after further work. — [Firsthand account](https://adityajindal.dev/writing/postgres-index-win)
- PostgreSQL documents multicolumn B-tree indexes as efficient when leading columns have equality conditions. B-tree ordering can also serve `ORDER BY ... LIMIT`; backward scans are supported, so descending declaration is not universally mandatory for descending results. — [Multicolumn indexes](https://www.postgresql.org/docs/17/indexes-multicolumn.html), [Ordering](https://www.postgresql.org/docs/17/indexes-ordering.html)

### Inferences
- Correct the shorthand “wrong index order”: the old index is **missing the state column**, not merely an incorrect permutation of the final three columns.
- This is a strong reference case for “repeated selective query does excessive work because the available index does not match its predicates.” It is one instance of a general repair class, not a one-query product.
- The plans support substantial work reduction. They are not a controlled latency benchmark: the before result has substantial reads and the after result only cache hits. Cache state, concurrency and repeated-run distributions are not controlled. Do not promise a 12,000× speedup from these two samples.

### Gaps
- The account does not publish a full reproducible database, complete SQL projection/schema, PostgreSQL configuration, sampled workload, all parameters/roles, index creation overhead, or write-regression measurements. Its plan excerpt omits some per-node execution details. Its reported page-level improvement and causality cannot be independently verified from the excerpt.
- This is PostgreSQL on RDS, not a demonstrated Supabase run. The mechanism transfers; project-specific eligibility still needs proving.

## What can an automatic Supabase collector actually observe?

### Takeaway
Existing SQL statistics can discover expensive query shapes without instrumenting the application. They do not supply every concrete request needed to reproduce those queries, nor prove page-level improvement.

### Cited Findings
- Supabase documents enabling `pg_stat_statements` and querying calls, mean/max/total execution duration and variability to find costly statements. Its example combines minimum call volume and execution-time contribution rather than ranking only call frequency. — [Supabase query statistics](https://supabase.com/docs/guides/database/extensions/pg_stat_statements)
- PostgreSQL tracks `(dbid, userid, queryid, toplevel)`, cumulative calls/time, returned-or-affected rows, buffer/temp-block counters, WAL bytes, and statement start timestamps. Text normally replaces constants with placeholders. Other users' text/query IDs require `pg_read_all_stats` or superuser privileges. Tracking defaults to top-level statements; nested statements require `track=all`. Entries can be evicted; `pg_stat_statements_info` reports deallocations/global reset. Execution counters update on successful completion. — [PostgreSQL statistics reference](https://www.postgresql.org/docs/17/pgstatstatements.html)
- Supabase's read-only Management API is `POST /v1/projects/{ref}/database/query/read-only`, runs as `supabase_read_only_user`, requires database-read scope/permission, and is labelled beta. It requires schema-qualified entity references. The endpoint does not establish owner-level mutation permissions. — [API reference](https://supabase.com/docs/reference/api/v1-read-only-query)
- Client trace propagation can correlate app requests with Supabase logs, but it requires active application spans and a configured tracing provider. The SDK alone does not provide a ready-made measurement of an entire page interaction. — [Client tracing](https://supabase.com/docs/guides/observability/client-side-tracing)

### Inferences
- Save periodic snapshots and compare **valid windows**, not lifetime leaderboard positions. Proposed calculations: `delta total_exec_time / delta calls` for average duration; time contribution to shortlist; buffer reads per call to identify costly access; temporary blocks per call to flag spills. Reject discontinuous/reset/recreated-entry windows and preserve collection gaps. Available columns differ with extension/server version.
- Sum top-level contribution separately from nested diagnostics to avoid treating an outer RPC and its inner statements as independent workload totals.
- Execution time is **not measured CPU time**. Neither `rows` nor a zero-row result tells us rows examined. Means/maxima/standard deviations do not reconstruct p95 or app latency. Failed/time-out requests need complementary error evidence.
- Plain-English collector output can be: “This query ran repeatedly, became slower, and reads many database pages per call.” The collector cannot yet conclude “it needs this index.”

### Gaps
- No actual Supabase project was inspected. Need probe extension availability/version, actual tracking settings, the collector's view access, and its ability to see the relevant roles' SQL. A documentation endpoint's existence does not prove all desired views/grants are usable through that identity.
- Normalized statements omit concrete parameter values and tenant/user identity. There may be a generic-plan candidate screen without values, but skew-sensitive verification needs representative request samples, fixtures, or suitable logged parameters. A service-role replay is not automatically representative of an authenticated customer.

## How would it investigate, choose, apply and verify an index?

### Takeaway
There is a concrete evidence chain using supported PostgreSQL and Supabase tools. Pooltune must implement the decision-making, context preservation, candidate generation and measurement; calling the native advisor alone is insufficient for this exact composite-index example.

### Cited Findings
- `EXPLAIN ANALYZE` executes the statement and reveals actual rows, execution time and per-node behavior. `BUFFERS` attributes access work; rejected-row counts reveal filtering; sort/hash nodes report useful execution details. Node times/row counts can be per-loop averages and must not be blindly summed. A sequential scan can legitimately beat an index. — [Reading measured plans](https://www.postgresql.org/docs/17/using-explain.html)
- Plain `EXPLAIN` supplies estimates in planner cost units, not measured milliseconds. PostgreSQL supports JSON output and generic parameter plans; this does not turn an estimate into a runtime benchmark. — [EXPLAIN reference](https://www.postgresql.org/docs/17/sql-explain.html)
- Supabase supports `.explain()` on Data API queries, RPCs and writes. It is disabled by default; production use requires protecting plan access. `analyze` executes the request, including writes. — [Supabase query plans](https://supabase.com/docs/guides/api/query-plans)
- PostgREST impersonates a request role, applies a search path, and exposes JWT claims and request settings within its transaction. — [PostgREST request context](https://docs.postgrest.org/en/stable/references/transactions.html)
- Supabase exposes `index_advisor` through SQL and Studio, accepting generic parameters and returning estimated before/after costs plus index statements. — [Index advisor](https://supabase.com/docs/guides/database/extensions/index_advisor)
- **Critical limitation found in official code:** the inspected `index_advisor` 0.2.1 upgrade definition forces generic plans and enumerates hypothetical **single-column** indexes for supported permanent table/materialized-view columns. It does not enumerate the three-column candidate from the firsthand case. Installed extension behavior must be version-checked. — [Official implementation](https://raw.githubusercontent.com/supabase/index_advisor/main/index_advisor--0.2.0--0.2.1.sql)
- Supabase supports HypoPG for hypothetical indexes; they are session-local, so candidate creation and planning must share the connection. The upstream extension uses them only with non-ANALYZE EXPLAIN. It can screen whether the planner would use a proposed index without building the real structure. — [Supabase HypoPG](https://supabase.com/docs/guides/database/extensions/hypopg), [HypoPG usage](https://hypopg.readthedocs.io/en/rel1_stable/usage.html)
- `CREATE INDEX CONCURRENTLY` avoids locking out normal writes during the build, but does extra work, can wait, cannot run within a transaction block, and can leave an invalid index after failure that still imposes update overhead. Indexes can themselves worsen performance. — [CREATE INDEX](https://www.postgresql.org/docs/17/sql-createindex.html)
- PostgreSQL's CREATE INDEX implementation checks table ownership through `RangeVarCallbackOwnsRelation`, then checks schema CREATE rights. Ownership rights can be inherited through an owning role; ordinary `SELECT` or PostgreSQL 17 `MAINTAIN` does not grant CREATE INDEX. — [Command dispatch](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/backend/tcop/utility.c), [Ownership check](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/backend/commands/tablecmds.c), [Schema check](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/backend/commands/indexcmds.c), [Privilege definitions](https://www.postgresql.org/docs/17/ddl-priv.html)

### Inferences
Proposed end-to-end playbook, not completed implementation:

1. **Notice the change.** From valid snapshot windows, identify a recurrent query whose per-call duration/access work or total contribution is significant. Also inspect other high-impact queries so the visibly slow screen is not automatically blamed.
2. **Acquire enough context.** Collect exact query shape, current indexes, object definitions and suitable representative values. Preserve runtime role, tenant claims, settings and prepared-plan behavior. If these are missing, the finding can remain a candidate rather than an eligible automatic change.
3. **Confirm the mechanism.** Use an estimated plan first. When allowed and appropriately bounded, get a measured plan for representative read-only cases. Look for waste compatible with a missing access path, not “all scans are bad.” Distinguish a database read from a function with side effects.
4. **Generate a small candidate set.** For this case, use equality predicates and ordering to propose a multicolumn B-tree. Compare existing definitions; do not duplicate them or remove customer indexes. Native advisor output can contribute candidates but is not complete. This can be structured reasoning rather than brute-force query rewriting.
5. **Screen candidates cheaply.** Use HypoPG to reject candidates the planner would not use and compare implications for important neighboring queries. Estimate space. Generic-plan gains alone do not prove benefit for skewed parameters or write overhead.
6. **Benchmark the shortlist where appropriate.** A representative test database can measure real indexes, parameter/tenant variants, write costs, warm/cold cache and concurrent work. Thousands of arbitrary SQL mutations are unnecessary for this index family. Exact clone/replay requirements are covered by the separate evaluation research.
7. **Apply one eligible change.** Use an authorized maintenance connection that supports top-level concurrent index creation, with resource/time budgets and validity checks. A read-only API credential cannot perform this step. Failed-build cleanup and repeated-run behavior are part of the operation contract.
8. **Verify on real workload.** Compare valid before/after windows: target duration and access work per call; successful throughput; write latency/errors; disk/index footprint; other important queries. Use comparable parameter/workload mixes. Record attribution limitations when traffic or releases change simultaneously. Dropping only the newly created index is the supported revert; it does not undo the build's spent resources.

This workflow is broader than one incident but narrower than arbitrary optimization: repeated filters, joins and ordered lookups can share an evidence-to-index playbook while retaining different test cases. The harness can choose from a bounded repair vocabulary, which gives an agent meaningful diagnosis and experimentation work without unrestricted mutation.

### Gaps
- Need demonstrate full loop on controlled Supabase cases before saying the agent solves this class reliably. Documentation confirms ingredients; neither the firsthand article nor API reference proves agent accuracy.
- Need explicit product choice about required parameter samples/app tracing. With database-only statistics, Pooltune can honestly report database-query improvements. Claims that a particular page or customer experience improved require measurements of that experience.
- Nested SQL inside functions may be hidden under top-level tracking. Investigation must detect incomplete visibility rather than promise automatic access to every internal operation.
