# Pooltune V1 diagnosis-to-remediation contracts

Research date: November 11, 2026. Research and proposed design only. No customer project was contacted or changed. Existing action families are preserved: Supavisor and PostgREST pool ceilings, eligible added indexes and reversion of Pooltune-created indexes, and separately opted-in targeted ANALYZE. Hosted Supabase only. No proxy, no interruption of customer transactions, no automatic billing changes, no arbitrary customer-index deletion, no arbitrary SQL writer.

## Which remediation interfaces actually exist, and what can their permissions and runtime behavior guarantee?

### Takeaway

Both pool-ceiling APIs are documented. Their hosted deployment/drain behavior is not established by those endpoint descriptions. Keep both families in V1, but make execution eligibility depend on validated runtime safety, with special attention to PostgREST's non-reloadable pool setting. Index creation needs substantially stronger database privileges than statistics observation or ANALYZE.

### Cited Findings

- Supavisor configuration: `GET /v1/projects/{ref}/config/database/pooler`, OAuth `database:read`, fine-grained token permission `database_pooling_config_read`. Response is an array with `identifier`, `database_type`, `db_user`, `db_host`, `db_port`, `db_name`, `default_pool_size`, `max_client_conn`, and `pool_mode`, plus connection-string fields. Do not assume one anonymous global pool record. [Supabase read pooler configuration](https://supabase.com/docs/reference/api/v1-get-pooler-config)
- Mutation is `PATCH /v1/projects/{ref}/config/database/pooler`, body includes optional integer `default_pool_size` and optional `pool_mode`; OAuth `database:write`, fine permission `database_pooling_config_write`. V1 should permit only the former field. [Supabase update pooler configuration](https://supabase.com/docs/reference/api/v1-update-pooler-config)
- PostgREST configuration: `GET /v1/projects/{ref}/postgrest`, OAuth `rest:read`, fine permission `data_api_config_read`. Secret-read permission is separately listed; output examples include `jwt_secret`, so readers must allowlist fields before storage/model exposure. [Supabase read PostgREST configuration](https://supabase.com/docs/reference/api/v1-get-postgrest-service-config)
- Mutation is `PATCH /v1/projects/{ref}/postgrest`, OAuth `rest:write`, fine permission `data_api_config_write`. Exposed integer fields include `db_pool`, `db_pool_acquisition_timeout`, `db_pool_max_lifetime`, and `db_pool_max_idletime`. V1's accepted ceiling action only requires `db_pool`. Neither increasing acquisition timeout nor changing pool lifetime is needed for this action. [Supabase update PostgREST configuration](https://supabase.com/docs/reference/api/v1-update-postgrest-service-config)
- Upstream PostgREST marks `db-pool` non-reloadable. Configuration reload through `NOTIFY pgrst, 'reload config'` therefore is not a documented way to apply this setting to a running process. The same non-reloadability applies to acquisition timeout. [PostgREST configuration](https://docs.postgrest.org/en/latest/references/configuration.html); [PostgREST v14 configuration](https://postgrest.org/en/v14/references/configuration.html)
- Supavisor pool size is a ceiling per distinct user/database/mode combination. Supabase services have their own pools; PostgREST is not routed through Supavisor by default. [Supavisor FAQ](https://supabase.com/kb/troubleshooting/supavisor-faq-YyP5tI)
- Shared Supavisor and dedicated PgBouncer read the same pool-size setting and apply it independently. Client limits and backend connection limits are different; the client limit depends on compute size. [Supabase pooling and limits](https://supabase.com/docs/guides/database/connecting-to-postgres/pooling-and-limits); [Connection setting FAQ](https://supabase.com/kb/troubleshooting/how-do-i-update-connection-pool-settings-in-my-dashboard-wAxTJ_)
- PostgreSQL 17's CREATE INDEX path invokes the relation-ownership check, and index creation separately checks schema `CREATE` and, for a non-default tablespace, tablespace `CREATE`. These are actual source checks, not an inference that `SELECT` or `MAINTAIN` suffices. [PostgreSQL 17 utility source](https://github.com/postgres/postgres/blob/REL_17_STABLE/src/backend/tcop/utility.c); [PostgreSQL 17 index command source](https://github.com/postgres/postgres/blob/REL_17_STABLE/src/backend/commands/indexcmds.c)
- Ownership powers can be inherited through membership of an owning role. PostgreSQL does not expose a narrow stand-alone `CREATE INDEX ON this table` grant through the standard relation privileges. `MAINTAIN` covers maintenance operations including ANALYZE and REINDEX, not initial CREATE INDEX. [PostgreSQL privileges](https://www.postgresql.org/docs/current/ddl-priv.html)
- PostgreSQL 15 and 16 ANALYZE ordinarily requires table ownership or superuser; database owners may analyze non-shared tables in their database. PostgreSQL 17 instead documents table `MAINTAIN` privilege, with the database-owner exception retained. Unauthorized tables may be skipped instead of causing the whole command to fail. [PG15 ANALYZE](https://www.postgresql.org/docs/15/sql-analyze.html); [PG16 ANALYZE](https://www.postgresql.org/docs/16/sql-analyze.html); [PG17 ANALYZE](https://www.postgresql.org/docs/17/sql-analyze.html)

### Inferences

- Proposed connection budget is topology-aware: reserve room for platform services, direct users, maintenance, and replication where applicable, then budget each active pool combination. A single equation `Supavisor ceiling + PostgREST ceiling < max_connections` is insufficient when multiple Supavisor combinations or both shared and dedicated poolers are used.
- Proposed adapter capability records distinguish `supported API`, `authorized`, `observable`, and `runtime validated`. A documented PATCH is not evidence of zero interruption, immediate application, or concurrency-safe compare-and-set behavior.
- Keep PostgREST in the accepted family. Until hosted tests establish safe application/drain behavior, its card can diagnose and prepare a precise proposed change, but cannot claim eligibility for automatic live execution under the no-interruption rule. Apply the same verification discipline to Supavisor decreases, rather than asserting existing sessions are gracefully drained.
- Do not request broad membership of Supabase's `postgres` role and market this as narrow permissions. If index execution needs ownership powers, explain their real breadth and constrain their use in a separate deterministic executor. A SECURITY DEFINER SQL RPC is not a workaround for concurrent index creation's top-level transaction requirement.
- Use direct/session-affine database transport for maintenance. Do not assume the Management API query endpoint or a migration wrapper permits `CREATE INDEX CONCURRENTLY`; that needs an explicit transport acceptance test. A transaction-pool route cannot safely preserve multiple session-level SET operations across requests.

### Gaps

- No official hosted API guarantee found for pool-change restart behavior, drain duration, in-flight request survival, lower-ceiling convergence, or rollback drain behavior. Upstream behavior is not a substitute for hosted validation.
- No documented conditional-write/ETag contract found for these PATCH calls. Local serialization does not exclude a dashboard user or another integrator changing the setting concurrently.
- The endpoint examples include nonsensical placeholder integers; they do not establish valid min/max or sentinel semantics. In particular, preserve and validate any platform auto/default representation before claiming an exact revert is possible.
- Hosted PostgREST versions, replica counts, pool metrics exposure, and configuration propagation delay must be discovered per project. No live validation was authorized or performed here.

## What should each actual V1 fix require, execute, verify, and undo?

### Takeaway

Use four tightly typed action contracts, with pool increases/decreases treated as distinct evidence cases. Candidate diagnoses can be broad, but a fixed executor should generate only eligible SQL/API payloads. Every action needs an ambiguous-result recovery path, not just a success response and a rollback button.

### Cited Findings

- PostgREST exposes upstream pool gauges `pgrst_db_pool_available`, `pgrst_db_pool_waiting`, `pgrst_db_pool_max`, and counter `pgrst_db_pool_timeouts_total`. Its admin server serves metrics, but that alone does not prove customers can access them on hosted Supabase. [PostgREST observability](https://docs.postgrest.org/en/v14/references/observability.html); [PostgREST admin server](https://docs.postgrest.org/en/v14/references/admin_server.html)
- `PGRST003` is an HTTP 504 caused by waiting for a pool connection. It establishes a pool-acquisition failure, but not why connections were unavailable. [PostgREST errors](https://docs.postgrest.org/en/v14/references/errors.html)
- Supavisor's documented max-client error means client connections exceed the compute-tier client limit. It is different from insufficient backend-pool capacity. Other documented pooler errors include inability to reach the database and cross-node communication timeouts. [Supavisor troubleshooting](https://supabase.com/docs/guides/database/supavisor)
- Concurrent CREATE INDEX avoids blocking ordinary writes as a normal index build does, but performs extra work and waits for transactions. Only one concurrent index build per table can run, partitioned-table concurrent builds are unsupported, and it cannot run in a transaction block. Failed concurrent creation can leave an invalid index that still adds update overhead. [PG17 CREATE INDEX](https://www.postgresql.org/docs/17/sql-createindex.html)
- Unique concurrent indexes can start enforcing uniqueness before the build succeeds. Expression/partial indexes introduce expression evaluation concerns; expression indexes need statistics before the planner has appropriate expression estimates. CREATE INDEX progress is exposed in `pg_stat_progress_create_index`. [PG18 CREATE INDEX](https://www.postgresql.org/docs/18/sql-createindex.html)
- `DROP INDEX CONCURRENTLY` requires ownership, accepts one index, cannot use CASCADE or run inside a transaction, and cannot drop constraint-supporting or partitioned indexes. It waits for conflicting transactions rather than blocking ordinary table access like nonconcurrent DROP. [PG18 DROP INDEX](https://www.postgresql.org/docs/18/sql-dropindex.html)
- ANALYZE and concurrent index creation take `SHARE UPDATE EXCLUSIVE`, which conflicts with itself and several schema-changing operations; neither is literally lock-free. [PostgreSQL lock modes](https://www.postgresql.org/docs/18/explicit-locking.html)
- ANALYZE's `SKIP_LOCKED` avoids an initial conflicting relation-lock wait but may still block on indexes or child relations. PostgreSQL 16+ supports `BUFFER_USAGE_LIMIT`, but PostgreSQL 15 does not list it. Statistics are sampled and a new sample can change plans. [PG15 ANALYZE](https://www.postgresql.org/docs/15/sql-analyze.html); [PG16 ANALYZE](https://www.postgresql.org/docs/16/sql-analyze.html)
- `index_advisor(query text)` returns estimated before/after startup and total costs, candidate `index_statements`, and `errors`. These are planner estimates, not measured latency improvements. HypoPG virtual indexes live only in their creating connection. [Index Advisor](https://supabase.com/docs/guides/database/extensions/index_advisor); [HypoPG](https://supabase.com/docs/guides/database/extensions/hypopg)
- `EXPLAIN ANALYZE` executes the statement and its side effects. This is fundamentally different from reading a plan. [PostgreSQL EXPLAIN](https://www.postgresql.org/docs/18/sql-explain.html)
- `pg_stat_statements` provides calls, aggregate timing, block and temporary-block counters, and reset/entry tracking information. Its listed timing statistics are not application end-to-end latency percentiles. [pg_stat_statements](https://www.postgresql.org/docs/18/pgstatstatements.html)

### Inferences

The following are proposed V1 design contracts, not existing Supabase features or approved implementation choices.

#### Shared action envelope

Every action should have `action_id`, project/database identity, exact action kind, target identities, evidence-window IDs, observation timestamp, required capabilities, expected old state/hash, policy revision, approval/grant reference, resource budget, execution deadline, verification recipe, and revert eligibility. Persist intent before mutation. Use one active mutation per project initially, and one build per table regardless of worker count.

States should distinguish `planned`, `preflight_failed`, `executing`, `effect_unknown`, `applied_unverified`, `verified_benefit`, `no_benefit`, `regressed`, `reverting`, `reverted`, and `cleanup_required`. A network timeout becomes `effect_unknown`, not permission to blindly retry. Success of the API/SQL operation is separate from proof of benefit.

Readers recheck privileges, schema/catalog identity, config drift, traffic/load, and active conflicting maintenance immediately before dispatch. Mutation credentials belong only to the executor. Model output chooses a known template with validated arguments, never supplies freeform SQL for execution. Use schema-qualified, safely quoted identifiers resolved from the catalog. Redact credentials and literal customer data from evidence given to the model.

#### Playbook 1: Pool-acquisition bottleneck while the database has headroom

**Evidence:** Determine actual connection route first. Require measured waiting/checkout delay or route-specific timeout evidence, sustained backend use near the correct ceiling, healthy CPU/I/O/memory and connection headroom, and a reasonably stable query mix. Prefer two windows or persistent incidents over one scrape. Collect root-cause evidence from `pg_stat_activity` and workload deltas.

**Confounders:** Lock waits, long queries, long/idle transactions, connection creation failures, authentication/DNS errors, regional pooler trouble, and a max-client limit can all look like pool pressure. A large client count alone does not prove that more backend connections are useful. Postgres does not expose a Supavisor frontend queue through `pg_stat_activity`.

**Typed action proposal:** `set_pool_ceiling(route: supavisor|postgrest, expected_old, requested_new, evidence_id, budget_id, runtime_capability_id)`. Executor emits exactly one allowlisted PATCH field. Never automatically change `pool_mode`, acquisition timeout, or `max_connections` as a side effect.

**Preflight:** Ensure the exact target/version has passed runtime-safety tests, authorization covers the chosen surface, old value matches, topology budget still fits, no other mutation is active, and a known previous setting is actually restorable. Increases should be a small approved step, with cooldown and hard total budget; numerical defaults should come from a pilot, not a universal 40% rule.

**Execution:** Persist the complete before value and candidate, PATCH once, read back with bounded polling, and independently observe runtime effect. A returned config value is desired-state evidence; it does not prove live pool convergence. If HTTP result is ambiguous, reread first. A 429 gets bounded retry policy only after reconciling current state.

**Verification:** Look for lower queue/timeout rate under comparable demand, maintained or improved completed throughput, acceptable error rate and DB resource use. If only backend counts are observable, say benefit is unproven. Do not manufacture P95 from pg_stat_statements means.

**Revert/failure:** Revert exactly the old value only if current state still equals this action's written value and runtime revert remains safe. If another actor changed it, stop and flag drift. Revert is also a config operation with propagation/restart risk. Failures or unavailable telemetry suspend further tuning.

#### Playbook 2: Excessive runnable concurrency is overwhelming the database

**Evidence:** High simultaneous active work, worsening throughput/latency and CPU/I/O/memory contention, plus an oversized pool relative to demonstrated throughput. Confirm this is runnable work, not merely many idle connections. Route-specific observation matters because a Supavisor decrease cannot throttle traffic that bypasses it.

**Confounders:** A single expensive query, lock convoy, an I/O ceiling, cold cache, WAL pressure, and a traffic change can create the same headline symptom. Pool shrinkage cannot repair an existing blocking transaction.

**Action and preflight:** Same typed ceiling action, with `requested_new < expected_old`. This action needs separate safe-shrink validation. Do not promise it immediately removes existing backend connections or cancels work. No customer PID cancellation, termination, or new timeout policy is part of this contract.

**Verification:** Completed throughput should hold or improve while DB pressure and execution latency improve. Also require acquisition waits, HTTP errors, and client timeouts to remain within the chosen budget. A quieter DB with abandoned requests is not success.

**Revert:** Restore old ceiling if service outcomes regress, only under the config-drift and runtime-safety checks above. If a smaller ceiling takes time to apply, wait for a bounded convergence window instead of issuing repeated decreases.

#### Playbook 3: Expensive selective reads that an eligible index can improve

**Evidence:** Rank queries by recent total execution contribution and per-call work, not the slowest historical outlier. Link a recurrent query fingerprint to its real schema, role, filters, sort, and relevant RLS policy. Inspect existing indexes and a representative plan. A selective missing access path plus candidate cost reduction is useful evidence; a sequential scan alone is not a defect.

**Confounders:** Small tables, low-selectivity reads, stale statistics, parameter skew, different application roles/RLS, generic versus custom plans, locks, cache state, and planner estimates versus runtime. A plan captured as `postgres` may bypass RLS and misrepresent user traffic. An advisor suggestion is input to validation, never executable authority.

**Typed action proposal:** `create_index_candidate(table_oid, expected_table_fingerprint, key_columns:[catalog_column_id + order], index_name, expected_existing_index_set_hash, build_budget, evidence_id)`. Suggested first subset: ordinary permanent application tables; nonunique btree; a bounded number of plain columns; built-in type/operator behavior; no predicate, expression, partition parent, custom tablespace, or unique/constraint change. Whether INCLUDE or partial/expression indexes belong later is a scope choice, not an automatic extension.

**Preflight:** Verify exact table identity/definition and ownership privileges plus schema CREATE, approved table/schema scope, no equivalent usable index, enough storage and I/O margin, no competing maintenance or long blocker, and a safe autocommit/session-affine transport. HypoPG/Index Advisor are optional installed capabilities; do not silently install extensions. Clean session-local hypothetical state after evaluation. Reconfirm the plan in the relevant role/context without disabling live RLS.

**Execution:** On one dedicated connection, set bounded session timeouts and application identity in separate commands, then send a single top-level `CREATE INDEX CONCURRENTLY "pooltune_<action_id>" ON "schema"."table" USING btree (...)`. Do not wrap in BEGIN, a function, or a single multi-statement transaction. Track backend PID/start time and `pg_stat_progress_create_index`. A lock timeout/statement timeout only bounds Pooltune's own operation; it never authorizes stopping customer transactions. Do not auto-retry into overlapping builds.

**Structural verification:** Inspect exact definition and table OID; require `pg_index.indisvalid`, `indisready`, and `indislive` in an acceptable final state. Record created index OID, definition fingerprint and action provenance. A matching name or `IF NOT EXISTS` alone cannot establish success.

**Outcome verification:** Compare representative plans, recent time/call, blocks/call, errors, and measured workload at comparable demand. Watch write cost, WAL, storage growth, and tail latency where available. An index being used is supporting evidence; it is not sufficient proof of net benefit. Allow a warmup and adequate number of calls; insufficient workload means unverified, not failed.

**Revert and partial success:** Only an index tied to this action's durable provenance and still matching its catalog identity/definition is eligible for `DROP INDEX CONCURRENTLY "schema"."name"`. Recheck new dependencies and never use CASCADE. If an app changed or repurposed it, suspend automatic deletion. On connection loss or crash, inspect progress and catalog before deciding to resume observation, verify success, or clean up an invalid leftover. If cleanup times out, record `cleanup_required` and stop additional index actions. No invalid index may silently be treated as rolled back.

#### Playbook 4: Stale or inadequate statistics are a plausible plan cause

**Evidence:** Combine modification counts and last analyze timestamps with recent bulk loading or distribution change and a bad plan/estimation discrepancy. An old timestamp on an unchanged table is weak evidence. Row-estimate divergence requires appropriately captured runtime evidence; ordinary EXPLAIN cannot provide actual rows.

**Confounders:** Correlated columns, parameter skew, low statistics target, generic-plan behavior, or missing indexes can persist after ANALYZE. Repeated ANALYZE is not a cure for every cardinality error.

**Typed action proposal:** `analyze_relation(table_oid, expected_fingerprint, columns?:catalog_column_ids, max_runtime, buffer_budget_if_supported, opt_in_grant, evidence_id)`. Start with regular nonpartitioned application tables so 'targeted' cannot accidentally expand through a partition/inheritance hierarchy.

**Preflight:** Separate opt-in must explicitly acknowledge statistics change has no simple post-commit undo. Check version-specific privilege: PG15/16 ownership rules versus PG17+ MAINTAIN, current stats age, active autoanalyze/maintenance, lock conflicts and load headroom. If already freshly analyzed, re-diagnose rather than issue redundant work.

**Execution:** One relation at a time; bounded `lock_timeout` and `statement_timeout`; `ANALYZE (SKIP_LOCKED) "schema"."table"` with optional validated column list. A version-supported buffer cap may be added by policy. Do not execute a database-wide ANALYZE or silently modify statistics targets/autovacuum settings. Capture notices and warnings because a command can skip its target.

**Verification/failure:** Require evidence the target actually ran: analyze timestamp/count or equivalent catalog evidence plus captured notices. Then compare plan and workload. A skipped relation is `not_applied`, not success. A timed-out/disconnected operation is reconciled, not immediately replayed. After a committed ANALYZE, 'rollback' is unavailable as a normal supported action; do not overwrite pg_statistic or promise restoring a plan. On regression, stop actions and provide evidence for manual investigation.

### Gaps

- Proposed timing windows, minimum sample sizes, allowed pool step size, index size cap, CPU/I/O headroom limits, and regression thresholds need workload calibration. No arbitrary threshold here should be represented as Supabase guidance.
- A source plan/normalized query may lack bind values and the exact request identity necessary to reproduce a representative RLS-aware plan. Make this a visible evidence limitation.
- Per-operation API/SQL crash semantics and the executor's transaction behavior require disposable-project integration tests. No protocol implementation was tested here.

## What additional diagnoses belong in V1, and should any become new autonomous actions?

### Takeaway

Expand diagnosis coverage because it prevents harmful pool/index changes and explains when the accepted fixes are relevant. Do not expand autonomous writes to killing sessions, RLS rewriting, memory changes, vacuum/reindex, arbitrary index deletion, timeouts, or billing. Most extra value can come from better causal diagnosis, precise manual remediation, and reliable verification.

### Cited Findings

- Supabase documents indexing RLS filter columns and wrapping eligible row-independent auth/helper functions in SELECT to permit initPlan caching. It also cautions that performance recommendations depend on the case and index changes may need reversal if they do not help. [Supabase RLS guidance](https://supabase.com/docs/guides/database/postgres/row-level-security); [Supabase RLS performance KB](https://supabase.com/kb/troubleshooting/rls-performance-and-best-practices-Z5Jjwv)
- `pg_stat_activity` exposes transaction age, state, wait events and query information subject to visibility privileges. Table statistics include estimated dead/live tuples, modifications since analyze and analyze timestamps; these are statistics, not exact byte-level bloat measurements. [PostgreSQL cumulative statistics](https://www.postgresql.org/docs/18/monitoring-stats.html)
- `work_mem` applies per sort/hash operation and can multiply across concurrent operations/sessions, with additional hash-memory scaling. Therefore globally raising it to eliminate one spill can increase total memory risk. [PostgreSQL resource settings](https://www.postgresql.org/docs/18/runtime-config-resource.html)
- PostgREST recommends reducing execution time and request count before treating a larger pool as a cure; its dynamic pool closes idle connections and its capacity is finite. [PostgREST connection pool](https://docs.postgrest.org/en/latest/references/connection_pool.html)

### Inferences

Proposed V1 diagnosis/result routing:

| Diagnosis | Useful evidence and important alternatives | Within accepted fix families | Additional action handling |
|---|---|---|---|
| Lock convoy or idle transaction | Repeated wait samples, `pg_blocking_pids`, blocker age/state, affected workload; distinguish CPU-bound execution from lock waits | Usually no immediate eligible fix; a proven selective index might shorten future locking time but is not incident relief | Explain owner/application path and affected queries. No `pg_cancel_backend`, `pg_terminate_backend`, forced commit/rollback, or timeout change |
| RLS-related slow reads | Policy definitions plus application-role plan, query contribution, eligible policy filter column, different users/selectivity | Eligible index under the same index contract | Suggest a concrete policy/function rewrite for manual review with semantics tests. Never disable RLS in live production to diagnose it; do not silently rewrite authorization |
| Temp-file spill | Delta temp blocks/bytes, representative sort/hash plan, query-level execution cost; distinguish reporting workload from defective plan | An eligible order/filter index or stale-stats correction may reduce work | Recommend query restructuring or carefully tested session memory settings. No automatic global `work_mem`/`hash_mem_multiplier` tuning |
| High CPU / excessive scans | Host CPU aligned with query timing and buffer work; distinguish useful high throughput, plan regressions, background work and overload | Eligible index, statistics fix, or validated ceiling reduction when evidence matches | Capacity advice and query rewrite proposal; no compute purchase or blanket bigger pool |
| I/O / cache / storage pressure | Read/write rates, cache context, query block deltas, temp files, WAL and free-space history | Index if it reduces selective reads; stats if misplanning explains excess reads | Avoid index builds when capacity is tight. Suggest query/data lifecycle or resource changes manually |
| Write pressure / index overhead | Write rate, WAL, checkpoint pressure, relation/index inventory, action history | Revert a proven regressing Pooltune-created index | No removal of customer indexes because counters show zero scans; observation window and other workloads may be incomplete |
| Dead tuples / suspected bloat | Dead/live estimates, vacuum history, long snapshots and relation growth; distinguish reusable free space from disk returned to OS | Diagnose; targeted ANALYZE only when stale planner stats are separately supported | No automatic VACUUM FULL, REINDEX, pg_repack, or autovacuum settings. These add action/privilege/runtime classes |
| Timeouts | Classify pool acquisition versus statement/lock/network/client errors | Fix their diagnosed underlying cause if it maps to an accepted family | Do not increase timeout just to erase an error signal; document responsible layer and manual next step |
| Capacity limit | Sustained resource ceiling after query improvements, client limit, compute-tier limits and route topology | No automatic billing change | Show evidence, cost-aware options and manual upgrade path; never claim tuning can manufacture physical capacity |

**Recommended scope revision:** Add diagnosis cards and reasoned 'no safe automated fix' outcomes for all of these, plus the typed capability model, effect reconciliation, drift detection, and action verification. This makes the existing three families substantially more useful without multiplying dangerous write operations. Consider RLS filter-column indexes an explicitly tested application of the index family, not a new authorization-policy mutation family.

**Pilot plan for Jared's own hosted projects:** First use a disposable hosted project or suitable branch and synthetic workload. Demonstrate healthy-DB acquisition pressure, CPU-concurrency overload, a missing selective index, RLS-sensitive read behavior, stale statistics, and a lock convoy. Observe known expected causes, not just happy paths. Test pool increases and decreases with in-flight requests/transactions on each supported route and confirm no interruption. Inject executor crashes before/after dispatch, timeout responses, invalid-index creation failures, concurrent customer configuration edits, stats resets, and privilege revocation. Verify ledger reconciliation and bounded cleanup. Then connect active personal projects in observation mode and compare diagnoses to actual events. Allow narrow actions only after their specific route/version/privilege tests pass. The user's mention of available projects is future pilot context, not current authorization to change them.

### Gaps

- Real hosted observability coverage may vary by plan/version. Where waiting or per-route backend counts are missing, report 'suspected pool pressure' and prevent automatic pool changes from weak evidence.
- Some Supabase RLS guidance is simplified. Do not treat 'a filter column must always lead a btree' as a universal optimizer law across PG versions; test real plans and existing composite-index behavior.
- No new autonomous action family is recommended as necessary to V1 from this evidence. That is a product judgment based on safety/complexity and the accepted scope, not a measured market result.
