# Candidate experiments and agent evaluation for Pooltune

## Can the agent validate a repair without trying thousands of rewritten queries?

### Takeaway
A missing-index repair has a concrete, testable evidence chain. A good first approach is a few evidence-driven candidate indexes or statistics repairs, followed by representative execution tests; broad automatic SQL rewriting is a different and harder capability. These are proposed engineering methods, not tests already performed.

### Cited Findings
- Supabase provides HypoPG, whose hypothetical indexes let PostgreSQL estimate whether it would choose an index without building it. They are connection-local, so creation and EXPLAIN must use the same database connection; the Supabase example groups them in one query because pooling may change connections. This is a planner experiment, not an executed performance result. [Supabase HypoPG](https://supabase.com/docs/guides/database/extensions/hypopg)
- HypoPG explicitly supports hypothetical indexes only for EXPLAIN without ANALYZE. An EXPLAIN ANALYZE uses real objects, not hypothetical indexes. [HypoPG maintainer documentation](https://github.com/HypoPG/hypopg)
- Supabase's current index_advisor documentation states it recommends only single-column B-tree indexes. Its output is useful candidate generation, not a comprehensive search over compound indexes or proof of runtime improvement. [index_advisor](https://supabase.com/docs/guides/database/extensions/index_advisor)
- EXPLAIN shows planned scans, joins, and estimated row counts. PostgreSQL planner costs are arbitrary units, not milliseconds. Returning and transmitting data can materially affect actual elapsed time without appearing in planner cost. Small changes in filters or LIMIT can change the selected plan. [Using EXPLAIN](https://www.postgresql.org/docs/17/using-explain.html)
- EXPLAIN ANALYZE executes the statement and adds profiling overhead. BUFFERS supplies information about shared/local/temporary block work; SQL documentation also supports machine-readable JSON output. Plain EXPLAIN and actual execution therefore answer different questions. [EXPLAIN](https://www.postgresql.org/docs/17/sql-explain.html)
- A prepared query can use a parameter-specific custom plan or a shared generic plan. Testing a convenient literal once does not necessarily reproduce the plan used by an application across executions. [PREPARE](https://www.postgresql.org/docs/17/sql-prepare.html)
- Supabase roles can bypass row-level security. A privileged administrative execution is therefore not an adequate substitute for testing the application's real role and user context. [Row Level Security](https://supabase.com/docs/guides/database/postgres/row-level-security)
- Indexes accelerate locating matching records, but introduce overhead and must be used selectively. [PostgreSQL indexes](https://www.postgresql.org/docs/17/indexes-intro.html)

### Inferences
- Recommended ladder: (1) rank suspicious query patterns using observed interval workload; (2) inspect plan, existing indexes, data distribution and application role; (3) propose a small number of plausible repairs; (4) use hypothetical plans to reject weak index candidates; (5) execute real before/after experiments against representative nonproduction data; (6) apply an eligible change under explicit limits; (7) measure comparable real workload afterward. Do not describe this as a built or proven system.
- Start with keeping query meaning unchanged. A new eligible nonunique index can let PostgreSQL execute the same SQL more efficiently. Arbitrary query rewrites add questions about duplicates, nulls, ordering, pagination, authorization and side effects. Faster wrong answers do not count.
- For an index-shaped incident, collect actual before/after query duration, processed/filtered rows, buffer reads/hits, temporary work, workload throughput and error rate. Also compare write duration/throughput and index size. App page-duration improvement requires app measurements; an SQL timing reduction alone does not establish a page became faster.
- Test common, rare and no-result parameter values; small and large tenants; relevant user roles; realistic concurrent reads/writes. Preserve version, settings and prepared-query behavior where possible. Repeat baseline and treatment on comparable data and cache conditions. Label hardware/concurrency mismatches instead of turning clone milliseconds into production promises.
- A full-table scan is not automatically a bug. It can be the correct plan for a tiny table or a query returning much of the table. No-action fixtures must include these cases.

### Gaps
- No actual Pooltune candidate selection, query replay, measurement, or repair has been tested. No universal required speedup percentage or safe index-size threshold was established.
- The exact index repair account's production SQL, parameters, dataset and full workload are not available to this researcher. A reproduction can establish the class of repair, not independently reproduce the original page-speed claim.

## What kind of clone can Supabase actually provide?

### Takeaway
Supabase supports useful experiment environments, but an ordinary branch is not automatically a realistic production-data clone. Current documentation includes a newer data-copy option with additional requirements. Data copying and environment creation must remain explicit owner-controlled capabilities with cost and external-effect isolation.

### Cited Findings
- Branches are separate environments and data-less by default. Schema is reconstructed from migration history rather than blindly copied; settings and Edge Functions are copied according to the documented rules. [Branching overview](https://supabase.com/docs/guides/deployment/branching)
- Current Dashboard branching documentation provides an **Include data** option when the project has the Point-in-Time Recovery add-on. That copies production data, uses a larger disk and matches the source compute size, increasing cost. Therefore do not repeat the obsolete blanket claim that branches cannot contain production data. [Dashboard branches](https://supabase.com/docs/guides/deployment/branching/dashboard)
- Preview branches incur compute, disk, egress and other applicable usage. They are outside Spend Cap protection. Current listed default Micro starting compute price is $0.01344/hour, but a production-sized data branch is not necessarily Micro. Compute credits do not apply to Branching Compute. [Branching billing](https://supabase.com/docs/guides/platform/manage-your-usage/branching)
- Restore to a New Project is documented as beta, requires a paid plan and source physical backups, and incurs new-project costs. It copies database data, indexes, roles and auth records, but project services/settings need review and manual reconfiguration. Its binary restore can immediately start copied pg_cron, pg_net or wrapper activity; there is no pre-restore option to exclude or pause it. Supabase recommends a logical CLI restore when those definitions must be inspected or removed before external activity begins. [Restore to a new project](https://supabase.com/docs/guides/platform/clone-project)
- pg_stat_statements aggregates statements by query structure, generally replacing literal values with parameters. Entries expose historical counters, not a complete replayable request stream. Cross-server identity stability has limitations, particularly with logical copies and major-version differences. [pg_stat_statements](https://www.postgresql.org/docs/17/pgstatstatements.html)

### Inferences
- Distinguish three levels: schema plus synthetic fixtures tests correctness and known failure mechanisms; representative seeded/copied data tests workload-sensitive behavior; a close production copy with comparable settings/load gives stronger performance evidence. None automatically reproduces production concurrency, warmed caches, request sequencing, or network timing.
- A database snapshot does not include a faithful future workload. If old query statistics happen to be copied/persisted, they remain historical evidence rather than measured execution on the clone. Establish fresh experiment counters and run actual tests.
- Existing data-backed staging could suffice. Do not make a paid production clone mandatory for every free-user investigation, but do not pretend a tiny synthetic branch proves benefit on a large customer's skewed dataset.
- Production data copies require appropriate treatment, and duplicated scheduled jobs/webhooks must not accidentally perform real-world work. This is a concrete documented clone behavior, not a generic hypothetical warning.

### Gaps
- The data-copy option is verified in current Dashboard documentation. Its programmatic availability, required API fields, account entitlement and behavior have not been probed. Do not promise an automatic clone integration from this research alone.
- No user's plan, backup availability, extensions, source compute size, data transfer approval or affordable experiment budget has been checked. No branch or project has been created.

## What would prove the agent and its harness are good enough?

### Takeaway
The evaluation target is a whole diagnostic-and-repair workflow, including correctly leaving healthy or out-of-scope cases alone. Passing a fixed missing-index demo is insufficient, but the missing-index class is broad enough to support a useful initial vertical slice across varied schemas and workloads.

### Cited Findings
- Anthropic distinguishes the agent harness from the evaluation harness, and evaluates the model plus harness together. It recommends grading actual environment outcomes, not merely claims in the transcript, and running repeated trials because behavior varies. Capability tests explore new ability; regression tests protect previously demonstrated ability. [Agent eval guidance](https://www.anthropic.com/research/demystifying-evals-for-ai-agents)
- CREATE INDEX CONCURRENTLY avoids blocking ordinary writes but takes more work, waits for existing transactions and cannot run inside a transaction block. Failure may leave an invalid index that still adds write overhead. Only one concurrent index build can run on a table at a time. [CREATE INDEX](https://www.postgresql.org/docs/17/sql-createindex.html)

### Inferences
- Build evaluations where the initial prompt says only that a feature slowed, supplying realistic observable evidence. Do not tell the agent which index or root cause is missing. Hide the expected diagnosis and score the resulting state independently.
- Proposed case matrix: missing single-column index; missing compound index; existing equivalent index; tiny-table healthy scan; stale statistics; skewed tenant; bad prepared-plan behavior; lock-induced latency; slow network/app batching; capacity ceiling; insufficient parameter samples; denied mutation permission; failed concurrent build; repeated run after a completed repair. These exercise successful repair, abstention, escalation and recovery.
- A deterministic monitor can detect deviations and supply facts. The model's useful role is choosing investigations and explaining competing causes; execution eligibility and outcome checks should not depend solely on its confidence.
- Compare selected capability families against the same model with all eight available, using matched workloads and repeated trials. Evaluate successful end-to-end repairs, harmful or unnecessary changes, incorrect success claims, recovery, runtime and cost. More tools are justified when coverage improves without unacceptable failure rates, not merely when individual tool calls work.
- A valid production index generally becomes available to the whole workload. Standard index creation does not provide a simple one-percent-user canary. Test offline, bound the build, monitor broadly, and support dropping only Pooltune-owned eligible indexes when needed. An index may improve one query while changing plans or write cost elsewhere.
- No-action, stopping, and supported rollback are first-class successes. No index mutation should count as improvement simply because the agent created the requested object.

### Gaps
- These evaluation cases and acceptance dimensions are proposals. They have not been implemented, run, or used to establish that the existing eight-family design is reliable.
- No single numeric pass threshold is justified by the sources. The product must decide acceptable harm/abstention rates and confidence requirements before unattended production use.
