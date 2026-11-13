# Pooltune V1 capabilities and safeguards

**Revised V1 promise: Pooltune investigates recurring problems in hosted Supabase, applies a small set of customer-authorized repairs, and shows whether they helped.** Keep the three accepted action families: existing-pool ceilings, narrowly eligible index additions with supported removal, and separately opted-in targeted `ANALYZE`. Add stronger diagnosis, capability discovery, recovery, and outcome measurement before adding more database controls. The research establishes usable Supabase interfaces, but also exposes two major implementation constraints: changing PostgREST's pool is not an ordinary hot reload, and creating indexes requires substantial database authority. Start with Jared's own hosted projects, after explicitly connecting a suitable test project, and enable each action only after its contract passes controlled tests. This is a research-backed brainstorming revision, not an approved design or implementation plan; no live project was accessed or changed.

Prepared November 11, 2026. Evidence comes from current official documentation and source examples collected in the accompanying research notes. Documentation support, sample metric availability, and successful runtime testing are different evidence levels. **No Pooltune runtime or performance result is claimed here.**

## V1 needs better judgment more than additional controls

The useful product is a closed operational loop: notice a recurring symptom, investigate its cause, perform an eligible intervention, check the result, and retain an understandable history. The customer chooses allowed action families once, with bounded scope, rather than approving every individual repair. Application traffic continues through its existing Supabase route. Pooltune adds administrative observation and maintenance, not a traffic proxy, connection pooler, or application queue.

A report-only product would duplicate substantial existing functionality. Supabase already publishes a read-only performance-agent workflow and offers query diagnostics, advisors, and connection monitoring. Pooltune's proposed differentiation is reliable execution and verification that remove operator work. That remains a product hypothesis, not evidence of demand or superiority. ([Supabase performance agent](https://supabase.com/docs/guides/observability/automate-with-agents/performance), [Supabase observability launch](https://supabase.com/blog/operate-with-confidence-check-production-with-your-coding-agent))

### The scope change is operational completeness

| Scope | Revised V1 recommendation |
|---|---|
| Accepted automatic actions | Bounded pool-ceiling changes; eligible new indexes and removal of those exact Pooltune-created indexes; targeted `ANALYZE` with separate opt-in |
| Newly recommended capabilities | Project capability inventory, route detection, evidence-based eligibility, durable action reconciliation, manual-change detection, independent outcome verification, and explicit inability-to-fix results |
| Newly explicit diagnosis coverage | CPU, I/O, locks, RLS-related reads, temporary-file spills, write pressure, suspected bloat, and capacity limits |
| Still outside autonomous V1 scope | Customer-transaction cancellation; arbitrary customer-index deletion; query, application, or RLS-policy rewrites; global memory tuning; VACUUM/REINDEX; timeout changes; compute purchases |
| Product honesty | Separate “change applied” from “workload improved”; show missing evidence and inconclusive results; describe `ANALYZE` as non-reversible maintenance |

CPU and I/O are symptoms, not additional repair families. An index can reduce wasteful selective reads; statistics refresh can correct a bad plan; a smaller pool can reduce harmful concurrency. The same symptoms can arise from legitimate demand, locks, spills, or hardware limits. The agent must explain an unsupported cause rather than choose an irrelevant permitted action.

“Hosted Supabase first” does not yet select a connection route. Ordinary `supabase-js` Data API calls use PostgREST's pool; SQL drivers can use Supavisor or connect directly. Auth and Realtime also have separate pools. Supavisor already grows backend connections on demand within its ceiling, which applies per user/database/mode combination. Onboarding must discover these paths. Prioritizing the Data API when observed is our proposal, not a customer decision already made. ([Supavisor FAQ](https://supabase.com/kb/troubleshooting/supavisor-faq-YyP5tI))

### Three findings materially update the handoff

**Scoped credentials now offer a better pilot path.** Newly created Supabase personal access tokens can be restricted to selected projects and permissions; legacy tokens retain broader authority. The Management API now documents a metrics route using analytics-read permission. Together these can avoid collecting a privileged project data-access secret solely for monitoring. ([Personal access tokens](https://supabase.com/docs/guides/platform/personal-access-tokens), [Management metrics endpoint](https://supabase.com/docs/reference/api/v1-scrape-project-metrics))

**Logs have a newer query surface.** Current documentation uses a unified ClickHouse `logs` table filtered by `source`. Older source-as-table or BigQuery examples should not become the new collector. The API accepts windows of at most 24 hours. ([Current logs querying](https://supabase.com/docs/guides/observability/advanced-log-filtering))

**A supported configuration write does not prove safe live tuning.** PostgREST exposes `db_pool` through Supabase, but upstream marks `db-pool` non-reloadable. Hosted rollout, draining, propagation, and in-flight-request behavior need direct tests. Preserve this accepted family while withholding automatic execution for unvalidated configurations. Apply the same discipline to Supavisor shrinking and reversion. ([PostgREST configuration](https://docs.postgrest.org/en/latest/references/configuration.html), [Supabase PostgREST update](https://supabase.com/docs/reference/api/v1-update-postgrest-service-config))

## Supabase provides useful evidence with uneven coverage

The first collection pass should produce a capability record, not a misleading green dashboard. Record server and extension versions, reachable routes, privileges, available series and labels, log retention, configuration readability, and each action's runtime-validation status. Distinguish `documented`, `authorized`, `observable`, and `runtime validated`. Missing requirements disable the dependent action, not necessarily the entire product.

### Minimal onboarding has two privilege levels

Proposed onboarding is: choose one hosted project; authorize observation through secure credential entry; discover capabilities; select allowed action categories; add a maintenance connection if SQL repairs are enabled; optionally connect application traces. No secrets should be pasted into chat. Keep the common path short and explain additional access only when its dependent feature is selected.

| Need | Practical interface | Permission or constraint |
|---|---|---|
| Metrics and incident logs | `GET /v1/projects/{ref}/analytics/endpoints/metrics`; `GET /v1/projects/{ref}/analytics/endpoints/logs` | OAuth `analytics:read` or scoped PAT `analytics_logs_read` |
| SQL diagnostics | `POST /v1/projects/{ref}/database/query/read-only` | `database:read` / `database_read`; runs as `supabase_read_only_user`; actual view access needs probing |
| Supavisor settings | `GET/PATCH /v1/projects/{ref}/config/database/pooler` | Read: OAuth `database:read` / PAT `database_pooling_config_read`; write: OAuth `database:write` / PAT `database_pooling_config_write`; executor permits only `default_pool_size` |
| PostgREST settings | `GET/PATCH /v1/projects/{ref}/postgrest` | Read: OAuth `rest:read` / PAT `data_api_config_read`; write: OAuth `rest:write` / PAT `data_api_config_write`; executor permits only `db_pool` |
| Targeted maintenance | Dedicated direct or session-affine SQL connection | Version-specific privileges, secure credentials, and tested transaction behavior |
| Application outcomes | Existing tracing integration or optional small instrumentation package | Actual operation durations, outcomes, and deadline/abort information |

The endpoint references document these scopes; the read-only SQL interface is beta and does not promise all required diagnostic privileges. If insufficient, offer a dedicated monitoring login with the minimum demonstrated grants. Do not silently substitute an owner credential. Configuration responses also need field allowlisting before storage or model exposure because examples include secret-bearing fields. ([Read-only SQL](https://supabase.com/docs/reference/api/v1-read-only-query), [Pooler read](https://supabase.com/docs/reference/api/v1-get-pooler-config), [Pooler update](https://supabase.com/docs/reference/api/v1-update-pooler-config), [PostgREST read](https://supabase.com/docs/reference/api/v1-get-postgrest-service-config))

**Index execution is the hardest permission problem.** PostgreSQL requires relation ownership authority for index creation, alongside applicable schema privileges. Standard grants do not provide a narrow “create an index on this table only” capability. Membership in an owning role grants broader authority; an application-level SQL allowlist does not make that authority database-enforced least privilege. PostgreSQL 17 adds `MAINTAIN` for operations including `ANALYZE`, but it does not grant initial index creation. Earlier PostgreSQL versions have different ANALYZE ownership rules. ([PostgreSQL privileges](https://www.postgresql.org/docs/current/ddl-priv.html), [PostgreSQL 17 ownership check](https://github.com/postgres/postgres/blob/REL_17_STABLE/src/backend/tcop/utility.c), [PostgreSQL 17 ANALYZE](https://www.postgresql.org/docs/17/sql-analyze.html), [PostgreSQL 16 ANALYZE](https://www.postgresql.org/docs/16/sql-analyze.html))

A first pilot against one explicitly connected personal project could use a controlled local or self-hosted deployment and managed secret storage, avoiding premature commitment to a multi-tenant service holding owner-level credentials. This is an open deployment option. It does not remove the underlying database privilege risk. Keep mutation credentials exclusively in a narrow executor; do not imply a `SECURITY DEFINER` function solves concurrent-index execution, which must run outside a transaction block.

### Exact metric names establish what can be measured

Prefer `GET https://api.supabase.com/v1/projects/{ref}/analytics/endpoints/metrics` with the scoped Management credential. The direct alternative is `https://<project-ref>.supabase.co/customer/v1/privileged/metrics`, using HTTP Basic Auth with username `username` and a secret API key as password. That secret has elevated data access and bypasses RLS; naming it a metrics key does not make its authority metrics-only. ([Management metrics API](https://supabase.com/docs/reference/api/v1-scrape-project-metrics), [Direct metrics setup](https://supabase.com/docs/guides/observability/metrics/vendor-agnostic), [API key authority](https://supabase.com/docs/guides/getting-started/api-keys))

The following names are present in Supabase's official example exposition. Units and types determine valid calculations. They are **documented sample evidence, not a guaranteed series inventory for every project**. ([Official metrics sample](https://raw.githubusercontent.com/supabase/supabase-grafana/main/metrics.md))

| Metric | Type and unit | Interpretation |
|---|---|---|
| `node_cpu_seconds_total` | Counter, CPU-seconds by CPU/mode | Interval CPU/iowait rates |
| `node_memory_MemAvailable_bytes`, `node_memory_MemTotal_bytes` | Gauges, bytes | Memory headroom |
| `node_disk_read_bytes_total`, `node_disk_written_bytes_total` | Counters, bytes/device | Read/write rates |
| `node_disk_io_time_seconds_total` | Counter, seconds/device | Disk busy-time trend |
| `pg_stat_database_num_backends` | Gauge, connections | Database backend count |
| `pg_stat_database_temp_bytes_total` | Counter, bytes | Temporary-file growth |
| `pg_stat_database_deadlocks_total` | Counter, events | Deadlock rate |
| `pg_stat_statements_total_queries` | Counter, executions | Aggregate execution throughput |
| `pg_stat_statements_total_time_seconds` | Counter, seconds | Aggregate execution work |
| `pgrst_db_pool_max`, `pgrst_db_pool_available` | Gauges, connections | PostgREST capacity and availability |
| `pgrst_db_pool_waiting` | Gauge, requests | Queue snapshot |
| `pgrst_db_pool_timeouts_total` | Counter, events | Acquisition failure rate |

Supabase's dashboard additionally queries `supavisor_connections_active` and `supavisor_db_handler_db_connection_count`, with dimensions including user, mode, and instance identity. These are counts, not a wait-time histogram. Their absence from the example exposition reinforces the need for runtime discovery. No verified hosted Supavisor queue-depth or checkout-latency histogram contract was found. Auth's connection-wait metric must not be relabeled as Supavisor waiting. ([Official dashboard queries](https://raw.githubusercontent.com/supabase/supabase-grafana/main/dashboard.json))

A proposed initial cadence is the documented **60-second scrape interval**. Preserve raw counters, timestamps, source identity, and reset epochs. Derive rates only across compatible samples; a decreasing counter, changed identity, or reset ends that comparison window. Keep pool gauges per replica/service instance until label meaning is known. One-minute snapshots miss brief queues, while timeout counters can preserve evidence of intervening failures. This product targets recurring incidents, not rescuing the request currently waiting. ([Metrics setup](https://supabase.com/docs/guides/observability/metrics/vendor-agnostic), [PostgREST metric definitions](https://docs.postgrest.org/en/v16/references/observability.html))

### SQL, logs, and traces answer different questions

| Evidence source | Useful fields | Important limit |
|---|---|---|
| `pg_stat_activity` plus `pg_blocking_pids` | State, user, query/transaction age, wait event, blockers, `query_id` | Current sessions, not frontend pool queues or exact app request mappings |
| `pg_stat_user_tables` | `n_live_tup`, `n_dead_tup`, `n_mod_since_analyze`, analyze timestamps | Tuple counts are estimates; not exact bloat bytes |
| `pg_stat_user_indexes`, `pg_index` | Scan counters, index definition/validity | No scans in a short window does not establish uselessness |
| `pg_stat_database` | `temp_bytes`, deadlocks, commits/rollbacks, `stats_reset` | Aggregate database behavior |
| `pg_stat_io` | I/O operations, context, backend type, timing | PostgreSQL 16+; timing depends on configuration; does not separate kernel-cache reads from physical storage |
| `pg_stat_statements` | Calls, execution time, block work, temporary blocks, query identity | Aggregated successful execution statistics; no end-to-end percentile |

Use fresh transactions/snapshots for polling. For statement statistics, preserve database, user, query ID and top-level identity; track reset and entry eviction information. Compute interval mean as `Δtotal_exec_time / Δcalls` in milliseconds only when counters are comparable and calls are positive. Do not subtract lifetime maxima or synthesize p95/p99 from mean or standard deviation. Query text visibility can require `pg_read_all_stats`; normalized text still warrants bounded capture and redaction. ([PostgreSQL 17 statistics](https://www.postgresql.org/docs/17/monitoring-stats.html), [PostgreSQL 16 release](https://www.postgresql.org/docs/16/release-16.html), [Statement statistics](https://www.postgresql.org/docs/17/pgstatstatements.html))

Use `GET /v1/projects/{ref}/analytics/endpoints/logs` for narrow incident windows. Relevant sources include `edge_logs`, `postgres_logs`, `postgrest_logs`, `supavisor_logs`, and `pgbouncer_logs`. Preserve event IDs and source timestamps; distinguish an empty result from failed or incomplete collection. PostgreSQL SQLSTATE appears at `log_attributes['parsed.sql_state_code']`; attribute values are strings. Gateway fields include status, path, and `response.origin_time`, but its precise unit and boundaries were not established in the retrieved documentation. Validate before claiming latency. ([Logs API](https://supabase.com/docs/reference/api/v1-get-project-logs), [Log fields](https://supabase.com/docs/guides/observability/log-field-reference))

Metrics should drive continuous detection; logs should answer bounded follow-up questions. Log query usage is based on scanned data, so a small returned `LIMIT` does not establish a small bill. Retention varies by plan, and usage restrictions have a phased rollout. A paid log drain should not be required for the first pilot. ([Logs usage](https://supabase.com/docs/guides/platform/manage-your-usage/logs-query), [Rollout timing](https://supabase.com/changelog/50925-logs-usage-based-pricing), [Drain costs](https://supabase.com/docs/guides/platform/manage-your-usage/log-drains))

Application-visible success remains separate. A client can abandon a request before Supabase records successful completion. Supabase tracing propagates context into gateway and Edge Function logs but needs a tracing provider/exporter. A shared trace ID can connect supported events; it does not automatically identify a live PostgreSQL session. Without application instrumentation, label findings **provider-visible** and avoid promises about customer deadlines or application p95. ([Client tracing](https://supabase.com/docs/guides/observability/client-side-tracing))

An unresolved documentation contradiction affects setup: the Metrics guide says every project has an endpoint, while pricing marks the endpoint unavailable on Free. Probe an authorized project's actual endpoint instead of assuming either universal availability or a mandatory paid upgrade. ([Metrics guide](https://supabase.com/docs/guides/observability/metrics), [Pricing](https://supabase.com/pricing))

## Each repair needs a specific evidence and recovery contract

Every proposal should carry the exact target, evidence window, required capabilities, current-state fingerprint, customer grant, bounded resource budget, execution deadline, outcome criteria, and recovery rule. These are proposed Pooltune contracts, not Supabase guarantees. Numerical pool steps, load ceilings, minimum samples, and observation windows must be calibrated in the pilot; there is no justified universal “80% CPU means act” rule.

### Pool waiting with database headroom can justify an increase

**Evidence and alternatives.** Establish the affected route, persistent waiting or route-specific acquisition failures, backend use near its applicable ceiling, and CPU/I/O/memory plus global connection headroom. `PGRST003` specifically identifies PostgREST pool acquisition failure; an arbitrary HTTP 504 does not. Exclude lock waits, long queries, long transactions, connection establishment errors, and changed demand before treating the ceiling as causal. Supavisor's maximum-client error identifies a different limit from backend-pool size. ([PostgREST errors](https://docs.postgrest.org/en/v14/references/errors.html), [Supavisor troubleshooting](https://supabase.com/docs/guides/database/supavisor))

**Bounded action.** A typed `set_pool_ceiling(route, target, expected_old, requested_new, evidence_id)` emits exactly one permitted field: `default_pool_size` for Supavisor or `db_pool` for PostgREST. No mode switch, timeout extension, or `max_connections` change is bundled in. The budget must include active user/database/mode combinations, platform services, direct connections, and independently applied shared/dedicated pool limits. A simple sum of two nominal ceilings is insufficient. ([Pooling and limits](https://supabase.com/docs/guides/database/connecting-to-postgres/pooling-and-limits))

**Execution and verification.** Recheck current configuration, grant, load, and validated runtime capability immediately before dispatch. Persist the exact old representation, including any default/automatic semantics. Write once, read back with bounded polling, and separately observe runtime convergence. Compare queue/error rates and completed throughput under comparable demand while watching resource pressure. A stored setting is not proof the running service adopted it; backend counts alone are not proof of benefit.

**Revert and failed recovery.** Restore the prior setting only if the current state still matches Pooltune's write and the revert path is validated. If the response times out, inspect state before retrying. If someone changes the setting, stop dependent tuning instead of overwriting their work. Unknown telemetry or propagation means inconclusive or unknown effect, not success.

### Database overload can justify a decrease only when throughput survives

**Evidence and alternatives.** Require excessive simultaneous active work and worsening efficiency, not merely many idle connections. Check CPU/I/O contention against completed throughput and query mix. A single expensive query, cold cache, lock convoy, or storage ceiling can look similar. A Supavisor decrease cannot throttle a workload bypassing Supavisor.

**Bounded action and verification.** Use the same ceiling adapter with a separately validated shrink contract. Observe convergence before another change; do not assume existing connections disappear immediately. Success requires stable or improved completed throughput and relevant duration measures, reduced database pressure, and acceptable acquisition errors/waits. A quieter database because requests fail or clients give up is a regression. PostgREST itself recommends reducing query execution time and request volume before relying on a bigger pool. ([PostgREST connection pools](https://docs.postgrest.org/en/latest/references/connection_pool.html))

**Revert and limits.** Restore the previous ceiling under the same drift checks if guardrails fail. No transaction cancellation, forced disconnect, or new timeout policy belongs to this contract. Under the no-interruption constraint, unvalidated hosted shrink behavior makes the action diagnosis/proposal-only until tests pass.

### A selective access problem can justify a narrow new index

**Evidence and alternatives.** Rank recurrent queries by recent execution contribution and per-call work. Resolve their real table, role, RLS context, filters, ordering, and existing indexes. A sequential scan alone is not a defect: small tables and broad reads can legitimately favor it. Check stale statistics, parameter skew, generic plans, lock waits, and cache state. A plan collected as an owner can misrepresent ordinary users because owners commonly bypass RLS. ([PostgreSQL row security](https://www.postgresql.org/docs/current/ddl-rowsecurity.html))

Index Advisor and HypoPG can help propose candidates. Advisor costs are planner estimates, not measured latency; hypothetical indexes are session-local. Use them only when installed and accessible, clean up their state, and do not silently install extensions. Ordinary `EXPLAIN` provides estimates; `EXPLAIN ANALYZE` executes the statement and is not a universally safe production read. ([Index Advisor](https://supabase.com/docs/guides/database/extensions/index_advisor), [HypoPG](https://supabase.com/docs/guides/database/extensions/hypopg), [EXPLAIN](https://www.postgresql.org/docs/18/sql-explain.html))

**Bounded action.** Proposed first eligibility subset: ordinary permanent application tables; nonunique btree indexes; a small approved number of plain columns; supported built-in behavior; no expressions, predicates, partition parent, custom tablespace, or constraint change. `create_index_candidate(table_oid, table_fingerprint, columns, existing_index_hash, evidence_id)` accepts catalog identities, not model-written SQL. RLS filter-column indexing fits this same family if the real role-aware plan supports it; policy rewriting does not. ([Supabase RLS performance guidance](https://supabase.com/kb/troubleshooting/rls-performance-and-best-practices-Z5Jjwv))

**Execution.** Verify ownership authority, equivalent indexes, storage/I/O margin, conflicting maintenance, long blockers, and a session-affine autocommit connection. Generate one schema-qualified `CREATE INDEX CONCURRENTLY` with an action-specific name. It cannot run in a transaction block, performs additional work, waits for transactions, and can leave an invalid index after failure. Track `pg_stat_progress_create_index` and catalog state; allow one build per table and initially one mutation per project. A concurrent build is not impact-free. ([PostgreSQL 17 CREATE INDEX](https://www.postgresql.org/docs/17/sql-createindex.html))

**Verification and recovery.** Confirm the exact definition and valid/ready catalog state before measuring benefit. Compare query work and actual durations where available, alongside write latency/throughput, storage, and resource costs. Changed index counters support interpretation but do not establish exact per-request attribution. After a lost connection, inspect object identity and ongoing progress before retrying. An invalid artifact requires bounded cleanup or an explicit recovery task.

Revert only the exact Pooltune-created index after identity and dependency checks, ordinarily using `DROP INDEX CONCURRENTLY`. That operation has its own ownership, transaction, constraint, and partition restrictions. Never replace a customer index or use `CASCADE` to make cleanup succeed. Reversion cannot erase past build cost or affected requests. ([DROP INDEX](https://www.postgresql.org/docs/18/sql-dropindex.html))

### Stale planner statistics can justify targeted ANALYZE

**Evidence and alternatives.** Combine significant recent table changes, analyze history, modifications-since-analyze, and a plausible poor estimate/plan. PostgreSQL normally analyzes automatically, so age alone is not sufficient. When representative execution evidence exists safely, compare estimated and observed row counts; do not run expensive production measurements indiscriminately to obtain it. Correlated columns, parameter skew, inadequate statistics targets, or a missing index can persist after refresh.

**Bounded action.** `analyze_relation(table_oid, table_fingerprint, optional_columns, budget, evidence_id)` starts with regular nonpartitioned tables. Require the separate opt-in acknowledging no direct revert, version-correct privileges, no redundant fresh analysis, and acceptable load. Run one relation with bounded lock/runtime limits and `ANALYZE (SKIP_LOCKED)`, optionally using a version-supported buffer budget. `SKIP_LOCKED` avoids an initial conflicting relation-lock wait but does not guarantee no other waiting. PostgreSQL 16 supports `BUFFER_USAGE_LIMIT`; PostgreSQL 15 does not list it. ([PostgreSQL 15 ANALYZE](https://www.postgresql.org/docs/15/sql-analyze.html), [PostgreSQL 16 ANALYZE](https://www.postgresql.org/docs/16/sql-analyze.html))

**Verification and recovery.** Capture warnings and confirm the target actually ran through timestamps/counts or equivalent evidence. A skipped relation is not applied. Compare subsequent plan and workload under the registered criteria. Reconcile a disconnected command before repeating work. There is no supported normal rollback to previous planner statistics: on regression, stop further experiments and present the evidence for manual investigation. Re-running ANALYZE is not an undo.

### Other diagnoses prevent inappropriate repairs

| Diagnosis | Evidence and response | Automatic boundary |
|---|---|---|
| Locks or idle transactions | Repeated wait samples, blocking graph, transaction age, owner/application context | Explain blockers; no cancellation, termination, or forced transaction completion |
| CPU saturation | Align resource rates with query contribution, active work, and background activity | Index, statistics, or pool reduction only when their own evidence contract fits |
| I/O pressure | Read/write rates, query block work, temp files, free-space trend, cache context | Avoid index builds without headroom; provide manual capacity/data-lifecycle options |
| Sort/hash spills | Temporary-block/byte deltas plus representative plan | Eligible index or stats repair only; no global `work_mem` increase |
| RLS-heavy queries | Policy and role-aware plan, appropriate filter columns | Eligible supporting index; no live RLS disabling or automatic policy/function rewrite |
| Write/index overhead | Write rates, WAL context, index inventory, action history | Remove a proven regressing Pooltune-created index only |
| Dead tuples or suspected bloat | Estimated tuples, vacuum history, long snapshots, relation growth | Diagnose; no automatic VACUUM FULL, REINDEX, or autovacuum changes |
| Capacity ceiling | Sustained saturation after relevant query work, tier/client limits | Evidence-based upgrade advice; no automatic spending |

Memory settings especially need a separate future contract: `work_mem` can multiply across operators, workers, and concurrent sessions. Dead tuples also do not directly measure reclaimable disk bytes. Neither symptom justifies a convenient unrelated mutation. ([PostgreSQL resource settings](https://www.postgresql.org/docs/18/runtime-config-resource.html), [PostgreSQL cumulative statistics](https://www.postgresql.org/docs/18/monitoring-stats.html))

## A bounded investigator should sit inside a durable controller

Use one investigator model to compare hypotheses and choose useful diagnostic reads. Deterministic code should own permissions, object resolution, numeric limits, execution, and outcome computation. Separate responsibilities do not require multiple services or multiple LLMs. This follows the general agent-engineering preference for simple workflows with programmatic checks; current production architectures also separate model loops, tool execution, credentials, and durable event history. ([Building effective agents](https://www.anthropic.com/research/building-effective-agents), [Managed-agent architecture](https://www.anthropic.com/research/managed-agents))

| Boundary | Responsibility |
|---|---|
| Collectors and evidence store | Bounded reads, units, timestamps, source/reset identity, completeness, redaction |
| Incident controller | Persistence detection, bounded investigation budgets, project lease, cooldown and stop state |
| Investigator | Competing explanations, targeted evidence requests, candidate action or abstention |
| Policy validator | Current grants, exact target, freshness, scope, headroom and contract checks |
| Executor and reconciler | Typed external action, unknown-result recovery, supported compensation |
| Independent verifier/history | Registered outcome calculation, guardrails, coverage and auditable explanation |

Tools should be narrow and return useful evidence: `get_incident_metrics`, `get_query_deltas`, `get_blocking_graph`, `get_relation_health`, `get_existing_indexes`, `get_plan_estimate`, and `propose_action`. Use bounded fixed diagnostic templates instead of a production `execute_any_sql` tool. A read-only label does not make arbitrary SQL harmless: planning can pre-evaluate functions, and queries can expose data or consume substantial resources. ([PostgreSQL function evaluation](https://www.postgresql.org/docs/current/xfunc-volatility.html)) Plan capture should accept only supported query shapes whose application role, claims, settings, and parameters can be represented safely; otherwise record the missing context and abstain from dependent repairs. Every response should carry evidence ID, object/instance identity, timestamp/window, units, completeness, and reason for missing data. Bound top-N retrieval and disclose truncation. ([Effective tools](https://www.anthropic.com/research/writing-tools-for-agents), [Context engineering](https://www.anthropic.com/research/effective-context-engineering-for-ai-agents))

Credentials never enter model context. SQL comments, logs, schema descriptions, and error strings are untrusted evidence, not instructions. Structured outputs help constrain tools but do not eliminate prompt injection; tenant identity and destination restrictions must remain outside the model. ([Agent safety](https://developers.openai.com/api/docs/guides/agent-builder-safety))

### Persist intent before touching the external system

Store action identity, contract version, evidence IDs, current grant/stop generation, exact expected and desired state, limits, verification recipe, and compensation policy before dispatch. Keep execution state separate from outcome. An operation can be `applied` while its workload outcome is `inconclusive` or `regressed`.

A network timeout after dispatch means **unknown effect**. Read external state before retrying: the desired index might already exist, an operation might still be running, or configuration might have changed. Workflow persistence alone cannot atomically commit an external database/API effect with the local action record. Stable identity and action-specific reconciliation remain necessary. ([Durable execution and idempotency](https://temporal.io/blog/idempotency-and-durable-execution))

Manual changes invalidate stale assumptions. Recheck state before both apply and revert; preserve another actor's change. No documented conditional-write guarantee was established for these pool PATCH endpoints, so read-before-write still has a race window. A Pooltune lease serializes Pooltune, not the dashboard user. This limitation must be tested and exposed rather than described as atomic protection.

“Stop automation” should durably block new dispatch and retries while allowing read-only reconciliation. Show an in-flight action honestly. Revert is a separate audited action, and full stop should not quietly initiate new compensating writes. Safe cancellation of Pooltune's own maintenance requires its own identity/cleanup contract; it is not authorization to kill customer transactions. Cancellation and termination have distinct cleanup behavior even in durable workflow systems. ([Cancellation and termination](https://github.com/temporalio/documentation/blob/main/docs/encyclopedia/workflow/cancellation-and-termination.mdx))

### Verification must use criteria chosen before the outcome

The verifier first checks that the intended technical state exists, then checks safety/semantics and workload effects. Register the primary metric, guardrails, minimum samples, observation limit, and comparability rules before executing. The investigator cannot redefine success after seeing the result. Outcomes should include improved, unchanged, regressed, and inconclusive, alongside technical failure/unknown-effect status.

Every outcome needs coverage, windows, sample counts, and caveats. Changing traffic mix, deployments, cache warming, resets, or falling demand can invalidate a comparison. Lower CPU from fewer completed requests is not a benefit. Planner estimates are supporting evidence, not final measured outcomes. An inconclusive result needs a finite observation budget and predeclared keep/revert handling, not endless experiments. None of this proves causation from a single before/after comparison; controlled workloads provide stronger evidence.

**Proposed initial inconclusive policy, still open for selection:** after the registered observation deadline, restore an experimental pool setting to its prior stable baseline when the tested revert path is safe. Remove a Pooltune-added index whose benefit remains unproven, because its storage and write costs persist, but only after current identity, dependency, drift, and removal-safety checks pass. ANALYZE has no corresponding revert. A full stop or manual change overrides automatic compensation; show the remaining state and required decision. If restoration/removal fails, explicitly show that the new setting or index remains active and needs manual attention, rather than labeling the action reverted. Do not automatically apply a repair until its outcome deadline and recovery rule are known; calibrate the actual deadlines during the pilot.

## The pilot should prove restraint as well as repairs

Start with deterministic fixtures and controlled PostgreSQL workloads, then explicitly authorized disposable hosted Supabase tests. Local tests establish controller and SQL behavior; hosted tests establish actual API propagation, route-specific observability, and request survival. Follow with observation-only use on one personal project, then one independently validated action category at a time. The mention of available projects is useful pilot context, not permission to alter them now.

| Test group | Required cases and observable result |
|---|---|
| Known useful repairs | Selective missing index, stale statistics, real pool waiting with headroom, and excessive concurrency; verify workload and write costs |
| Correct restraint | Healthy workload, legitimate broad scan, lock blocker, absent telemetry, saturated resources, unknown RLS context |
| Hosted contracts | Increase/decrease/revert with in-flight requests on each supported route; configuration convergence; ownership and SQL transport behavior |
| Interrupted execution | Crash before/after dispatch, delayed response, duplicate delivery, completed index after lost acknowledgment, invalid index artifact |
| External change | Manual pool edit, schema migration, permission revocation, service/version change, statistics reset |
| Safety boundary | Prompt injection in evidence, cross-project target, unsupported SQL shape, stop during execution, no unauthorized compensation |

Evaluate actual final database/configuration state, not just a persuasive transcript. Repeat model trials because tool choice varies. Grade diagnosis, action eligibility, abstention, unsupported claims, and dangerous attempts separately from execution success. Trace grading can expose where the model chose the wrong tool or ignored a constraint. ([Agent evaluation practice](https://www.anthropic.com/research/demystifying-evals-for-ai-agents), [Workflow evaluations](https://developers.openai.com/api/docs/guides/agent-evals))

Compare three baselines: no intervention, deterministic detector plus fixed runbook, and the proposed investigator. Track unsafe-action rate, false-positive actions, correct abstention, technical apply/revert success, inconclusive results, observed workload benefit, investigator cost, and operator effort. Keep denominators, including failed attempts. Promote a family only when its policy/recovery tests and hosted contract tests pass and controlled repairs show relevant benefit without unacceptable regression. Sample counts and numeric launch targets remain to be selected; zero observed failures does not prove zero risk. Historical prototype benchmarks do not establish this product's benefit.

### The strongest alternative is a narrower repair product

A credible alternative starts with query/index/statistics repair and postpones automatic pool tuning, while retaining pool diagnosis. It avoids uncertain hosted pool rollout behavior and focuses on interventions whose mechanisms are easier to reproduce. Its cost is substantial: ownership delegation remains difficult, and it gives up connection-pressure incidents that motivated Pooltune. Keeping all three families in V1's design, but enabling only independently validated contracts, preserves the accepted direction without promising readiness prematurely.

A second alternative is deterministic runbooks with no investigator model. For a few well-defined incidents this could be cheaper, more predictable, and sufficient. The model earns its place only if it improves diagnosis across competing causes or reduces operator work. More autonomous tools or more LLMs are not evidence of a better product.

The next brainstorming decisions are the outcome promise with optional app telemetry; the first detected workload route and representative incident; the ownership/deployment model for index execution; whether index eligibility should initially exclude all partial/expression forms; and the retention/revert policy when a change remains inconclusive. These questions materially affect product feasibility. Framework, language, and deployment selections can follow once those contracts are credible.
