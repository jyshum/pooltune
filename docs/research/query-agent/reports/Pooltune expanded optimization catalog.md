# Pooltune expanded optimization catalog

**Yes: Pooltune can offer more useful optimization operations, and several belong in the proposed V1.** Add extended statistics for correlated columns, higher statistics targets for proven skew, and measured single-index concurrent rebuilds. Expand the existing index family with tested covering and search-index templates rather than counting every index type as a separate feature. A reviewed RLS/RPC migration lane can also perform substantial optimization work, provided it prepares and tests a concrete change instead of silently rewriting production access policies. These extend the existing catalog, including the three recent additions supported in principle: selected RPC memory, targeted VACUUM, and per-table autovacuum policy. The intended product is now an **open-source database optimization agent, starting with hosted Supabase**, not merely a traffic-scaling tool.

Prepared November 11, 2026. This is a research-backed brainstorming catalog, not an approved implementation plan. Earlier reports remain unchanged. No project was accessed, no tests were run, and no repository was published. The scope still excludes a new traffic proxy, live customer-transaction interruption, and automatic spending. Examples use illustrative objects and values; PostgreSQL 17 is a research reference, not an assumption about the enrolled project's version.

## Better statistics add two distinct repairs

PostgreSQL chooses query plans from estimates. Refreshing existing statistics with ANALYZE cannot always fix a bad estimate because the information being collected is insufficient. Two concrete additions improve that information: multicolumn statistics for relationships within one table, and greater detail for one skewed column. They complement indexes instead of creating another access path. ([Planner statistics](https://www.postgresql.org/docs/17/planner-stats.html))

### Create extended statistics for proven same-table relationships

**Evidence and eligibility.** Locate the first meaningful row/group estimate error in a recurring expensive query, using representative application roles, RLS claims, and parameters. Rule out simply stale statistics. Compare individual-filter estimates with their conjunction, or estimated group count with actual evidence. Use `dependencies` for supported dependent equalities, `mcv` for common joint values, and `ndistinct` for multicolumn grouping. These are not a general repair for join-selectivity estimates. `pg_stats.correlation` measures physical ordering against one column, not relationships between two columns. ([CREATE STATISTICS](https://www.postgresql.org/docs/17/sql-createstatistics.html), [Correlation field](https://www.postgresql.org/docs/17/view-pg-stats.html))

**Operation and authority.** A proposed `create_extended_statistics(relation_oid, column_ids, kinds, expected_catalog_hash, budget)` initially supports two or three plain columns on an ordinary application table. It requires table-owner authority, applicable schema privilege, and a separately permitted ANALYZE path. Check for equivalent objects, conflicting maintenance, table size, and bounded lock/I/O cost. An illustrative executor sequence is:

```sql
CREATE STATISTICS public.pooltune_orders_tenant_status (mcv)
ON tenant_id, status FROM public.orders;
ANALYZE public.orders;
```

The tenant/status example is useful only when observed distribution and workload justify it. CREATE alone does not populate the statistics; ANALYZE is required. The object affects subsequent planning across Data API, RPC, and direct connections without an application deployment. Affected prepared statements can replan after statistics updates, which adds planning work worth measuring. ([CREATE STATISTICS](https://www.postgresql.org/docs/17/sql-createstatistics.html), [Prepared statements](https://www.postgresql.org/docs/17/sql-prepare.html))

**Verification and recovery.** Confirm the exact object and populated data, then compare estimates, plan shape, buffers, spills, execution time, and relevant application outcomes. Include other important queries touching the table. Better estimates alone do not establish a faster workload. Recover by dropping only the unchanged Pooltune-created object under appropriate ownership. That removes additional planner input but cannot restore ordinary statistics refreshed by ANALYZE or recreate a previous plan. Do not edit system statistics directly. ([DROP STATISTICS](https://www.postgresql.org/docs/17/sql-dropstatistics.html))

The release fixture should include correlated equalities, skewed joint values, grouping errors, contradictory predicates, and a join-only error that the agent correctly declines. Dependency statistics have specific supported predicate forms; range and arbitrary expression comparisons are not interchangeable with equalities. ([Multivariate examples](https://www.postgresql.org/docs/17/multivariate-statistics-examples.html), [Statistics applicability](https://www.postgresql.org/docs/17/planner-stats.html))

### Raise one column's statistics target for persistent skew

**Evidence and action.** Fresh statistics can still miss important common values or represent a distribution too coarsely. Require repeated estimate error, a plausible skew/histogram mechanism, and a controlled higher-target comparison that helps relevant parameter classes. A large table alone is not evidence. A typed `set_column_statistics_target` changes one owner-authorized column, then refreshes its data:

```sql
ALTER TABLE public.orders ALTER COLUMN customer_id SET STATISTICS 250;
ANALYZE public.orders (customer_id);
```

Here 250 is a pilot example, not a universal optimum. More detail increases sampling, storage, and planning/maintenance costs. Capture raw catalog state and normalized `{inherited|explicit,value}` before changing it. Version adapters must recognize inherited representations such as NULL or legacy -1 rather than assume one sentinel. If an extended object also needs refreshing, include its relevant columns or analyze the table; a one-column command is not evidence that every multicolumn object was refreshed. ([ALTER TABLE](https://www.postgresql.org/docs/17/sql-altertable.html), [Column catalog](https://www.postgresql.org/docs/17/catalog-pg-attribute.html), [ANALYZE](https://www.postgresql.org/docs/17/sql-analyze.html))

**Verification and recovery.** Measure execution plus planning time, common and rare parameters, plan stability, and later analysis duration. Metadata summaries usually suffice; common-value arrays can contain customer values and should not automatically enter model context. Restore the prior explicit target or the deployed version's inherited/default form. A subsequent ANALYZE creates a fresh sample, not the old statistics. Describe this as policy restoration with bounded follow-up, not exact performance rollback.

These operations should enter V1's planner-statistics family alongside existing ANALYZE. Recurring autoanalyze trigger changes belong with the already discussed table-maintenance policy; they are not a third newly discovered optimization family.

## Broaden indexes while separating creation, rebuilding, and deletion

One index-creation operation can support several rigorously tested templates. Every template still needs the real application's predicate, role/RLS context, prepared-plan behavior, parameter distribution, resource budget, and comparison with existing indexes. HypoPG or Index Advisor support must be discovered per version and access method; unsupported hypothetical evaluation calls for a controlled real build, not invented cost evidence.

| Template | Example mechanism | Eligibility and important limit |
|---|---|---|
| Covering btree | `ON orders(customer_id) INCLUDE (status, total)` | Repeated selective reads incur material heap-fetch cost; payload width/write cost is bounded. INCLUDE columns are not search keys. |
| JSONB GIN | `USING gin (payload jsonb_path_ops)` | Actual containment/jsonpath workload matches the operator class. Key-existence operations need a different choice. |
| Trigram GIN | `USING gin (name discovered_schema.gin_trgm_ops)` | Installed pg_trgm and useful substring/ILIKE patterns; short or wildcard-heavy searches can disappoint. |
| BRIN | `USING brin (created_at)` | Very large physically ordered table and range reads; lossy block summaries need rechecks. |
| Partial btree | `ON orders(customer_id) WHERE status = 'open'` | Actual planner can prove the query implies the predicate; generic parameters often cannot. |
| Expression btree | `ON users ((lower(email)))` | Matching expression/collation with an allowlisted safe built-in expression; extra write evaluation cost is justified. |

These mechanisms and limitations are documented PostgreSQL behavior. They should not be marketed as six independent agent features. ([Index-only scans](https://www.postgresql.org/docs/current/indexes-index-only-scans.html), [JSONB indexing](https://www.postgresql.org/docs/17/datatype-json.html#JSON-INDEXING), [Trigram indexes](https://www.postgresql.org/docs/17/pgtrgm.html), [BRIN](https://www.postgresql.org/docs/17/brin.html), [Partial indexes](https://www.postgresql.org/docs/17/indexes-partial.html), [Expression indexes](https://www.postgresql.org/docs/17/indexes-expressional.html))

**Creation contract.** Extend `create_index_candidate` with validated method, columns, optional INCLUDE, approved predicate/expression, and discovered operator class. Retain table ownership/schema checks and top-level `CREATE INDEX CONCURRENTLY` on a session-affine autocommit connection. Do not install extensions silently; pg_trgm must already be installed or receive a separate installation grant. Never add uniqueness as a performance experiment. Start release testing with bounded INCLUDE and one observed GIN use case, then independently qualify BRIN, partial, and expression templates. Supabase's extension list establishes availability, not installation in every project. ([Supabase extensions](https://supabase.com/docs/guides/database/extensions), [CREATE INDEX](https://www.postgresql.org/docs/current/sql-createindex.html))

**Verification and recovery.** Check validity and representative plans, then read/write duration, buffers, WAL, storage, and errors at comparable traffic. Covering-index success needs actual heap-fetch reduction: recently changed pages can still require heap visibility checks. If an existing index covers the query and visibility is the problem, the existing bounded VACUUM operation can be the right repair. Wide INCLUDE payloads also deserve insert-error testing. On failure reconcile progress/catalog state, then clean up only provenance-matched new objects. Removal cannot undo build cost or affected requests.

### Add measured single-index REINDEX CONCURRENTLY

This is a genuine new maintenance operation. **Evidence** should combine index growth and a useful space/performance objective with bounded physical inspection, such as installed `pgstatindex` for a btree. Account for configured fillfactor, normal page splits, future reuse, and inspection I/O. Table dead tuples and a density below 100% do not prove profitable index rebuilding. The inspection extension has its own execution grants and non-instantaneous sampling behavior. ([pgstattuple tools](https://www.postgresql.org/docs/17/pgstattuple.html))

A proposed `reindex_one_concurrently(index_oid, definition_hash, table_oid, physical_evidence, storage_budget, cleanup_authority)` starts with one ordinary valid nonunique btree:

```sql
REINDEX INDEX CONCURRENTLY public.orders_customer_idx;
```

Require room for old and replacement indexes, temporary work, WAL, and growth; serialize maintenance and use a top-level session-affine command. PostgreSQL 17 supports table MAINTAIN authority, while earlier supported versions need ownership rules checked. Crucially, MAINTAIN does not itself grant ownership needed to DROP a failed transient index. Either establish cleanup authority or explicitly expose a manual-cleanup outcome. Do not turn this optimization into automatic corruption repair. ([PostgreSQL 17 REINDEX](https://www.postgresql.org/docs/17/sql-reindex.html), [PostgreSQL 15 REINDEX](https://www.postgresql.org/docs/15/sql-reindex.html), [DROP INDEX](https://www.postgresql.org/docs/17/sql-dropindex.html))

**Verify** the canonical definition and replacement validity, recording changed physical identities/OIDs, absence of unexplained leftovers, bytes, and relevant workload metrics. A smaller file can meet a declared space objective without proving latency improvement. **Recover** by identifying the actual swap phase: `_ccnew` often indicates failed replacement; `_ccold` often indicates unfinished old-object removal. Names are hints, never deletion authority. Match identities, definitions, and live serving state before cleanup. There is no supported rollback to the original bloated physical file after success. Failure-phase fixtures, including missing cleanup privilege, are a release requirement.

### Keep exact duplicate customer-index removal separately granted

The previous permission to remove Pooltune-created indexes does not authorize deleting existing customer objects. A proposed `remove_exact_duplicate_index(drop_oid, keep_oid, fingerprints, dependency_snapshot, recreation_definition)` should initially require two healthy ordinary nonunique btrees with identical ordered keys, INCLUDE, collation, opclasses, ordering/null flags, relevant storage, and table identity. Exclude constraints, replica identity, clustered/partition/extension indexes and dependencies. Inspect catalogs rather than relying on names, zero scans, or shared leading columns. External maintenance scripts can reference names even without catalog dependencies. ([Index catalog](https://www.postgresql.org/docs/17/catalog-pg-index.html))

The owner-authorized executor can issue one `DROP INDEX CONCURRENTLY` after saving exact recreation metadata and rechecking the surviving index. Verify read correctness/plans and write/storage effects. Compensation rebuilds the old definition concurrently, costing time, I/O and space and potentially failing; it is not instant rollback. This is a worthy catalog operation, but adopting its separate customer-index deletion permission remains an open scope decision.

## Include prepared optimization patches without silently editing authorization

A reviewed change lane can perform substantive agent work: diagnose, generate an exact migration or repository diff, run correctness/security fixtures, compare plans, and package forward/inverse changes for review. It materially expands the product workflow beyond operational settings. Recommend it for two narrow patterns: repeated row-independent authorization helpers and inefficient membership/RPC query shapes.

For a verified simple SELECT policy, one candidate is:

```sql
ALTER POLICY "read_own_documents" ON "app"."documents"
USING ((SELECT auth.uid()) = owner_id);
```

Supabase documents the scalar SELECT technique for helpers whose result does not depend on the current row. The agent must inspect the original policy, helper identity, role lists, policy composition, and expression structure; an advisor finding is a candidate, not permission. A custom `can_access(row_id)` cannot be converted into a once-per-statement helper. Use a parsed transformation, not arbitrary string replacement. Applying a policy change requires table ownership. ([Supabase RLS guidance](https://supabase.com/docs/guides/database/postgres/row-level-security), [ALTER POLICY](https://www.postgresql.org/docs/current/sql-alterpolicy.html))

Membership optimization can replace repeated correlated work with an appropriate user-scoped set or indexed join, but must preserve NULL/duplicate behavior, membership-table RLS, role inheritance, and large-membership performance. An explicit application filter helps only when it matches the intended operation: “my documents” differs from “all documents shared with me.” A supporting index might solve the issue without rewriting policy. ([RLS performance](https://supabase.com/docs/guides/database/postgres/row-level-security-performance))

The artifact needs exact before-state hashes, schema/source revision, affected roles/routes, migration, inverse migration, and actual test results. Test anonymous access; tenant A access; tenant B denial; memberships and revoked memberships; read results including multiplicity/order; write effects and USING/WITH CHECK; pooled-session claim changes; and preserved RPC signature, grants, security mode and search path. Compare against the expected access contract, not only old behavior, because an existing security defect is not the target. Use isolated non-sensitive fixtures; never disable production RLS to make benchmarking easier.

The agent can prepare and test these changes automatically when connected to suitable evidence and a test environment. Production apply/repository merge follows the separate review/deployment permission. Without a repository, produce a tested migration artifact rather than pretend a PR exists. Restore unchanged policy/function definitions on regression, but acknowledge that an inverse migration cannot undo leaked information or already accepted writes. No real migration or test has been produced in this research.

Do not invent SECURITY DEFINER helpers, label JWT-dependent functions IMMUTABLE, or mark functions PARALLEL SAFE for speed. Those properties change semantic/security assumptions and require independent source/dependency analysis. ([Function volatility](https://www.postgresql.org/docs/current/xfunc-volatility.html), [Parallel safety](https://www.postgresql.org/docs/current/parallel-safety.html))

A narrower additional experiment is `ALTER FUNCTION public.search_orders(uuid, text) SET plan_cache_mode TO force_custom_plan` for an existing PL/pgSQL RPC with a demonstrated harmful internal generic plan. It needs owner authority, exact signature/proconfig restoration, warmed real-route tests across common/rare parameters, and planning-plus-execution benefit. It cannot change an outer PostgREST plan already chosen, and dynamic EXECUTE already replans. Keep this as a conditional RPC operation; JIT and parallel-setting experiments need their own evidence and should not become blanket V1 tuning. ([PL/pgSQL planning](https://www.postgresql.org/docs/17/plpgsql-implementation.html), [Plan controls](https://www.postgresql.org/docs/17/runtime-config-query.html), [ALTER FUNCTION](https://www.postgresql.org/docs/17/sql-alterfunction.html))

## Open source supports a useful eight-family catalog

Open source is a sound product direction without requiring unique database primitives. Recommend a self-hosted controller, operator-held executor credentials, replaceable model adapter, transparent machine-readable action contracts, repeatable fixture evaluations, and exportable evidence/history. Self-hosting Pooltune can still target hosted Supabase first. A provider adapter boundary enables later portability; it does not mean other databases/providers are supported now. License selection and making an existing private repository public remain separate decisions. Open-source availability does not remove model, compute, storage, or provider telemetry costs.

Provider overlap should be stated accurately. Azure SQL Database already provides automated index creation/removal and plan correction with verification/reversion. Azure PostgreSQL's index-tuning system documents recommendation modes, while its distinct Intelligent Tuning automates some write-related settings. Cloud SQL PostgreSQL Index Advisor requires Enterprise Plus and Query Insights. These are different products and restrictions, not evidence that every native optimizer is a separate paid add-on. Pooltune can still be valuable through accessible, inspectable operator-controlled execution. ([Azure SQL tuning](https://learn.microsoft.com/en-us/azure/azure-sql/database/automatic-tuning-overview?view=azuresql-db), [Azure PostgreSQL index tuning](https://learn.microsoft.com/en-us/azure/postgresql/monitor/concepts-autonomous-tuning), [Azure PostgreSQL Intelligent Tuning](https://learn.microsoft.com/en-us/azure/postgresql/monitor/concepts-intelligent-tuning), [Cloud SQL Index Advisor](https://docs.cloud.google.com/sql/docs/postgres/use-index-advisor))

The proposed consolidated V1 catalog has **eight families with separately gated operations**, not eight unrestricted permissions:

| Family | Enumerated operations and status |
|---|---|
| 1. Planner statistics | Existing targeted ANALYZE; new extended-statistics creation plus analysis; new column-target changes plus analysis |
| 2. Index design/lifecycle | Existing narrow concurrent additions and own-index removal; proposed INCLUDE/GIN templates, then individually tested BRIN/partial/expression; separately granted exact customer-duplicate removal remains open |
| 3. Table/index maintenance | Recent in-principle targeted VACUUM; recent per-table autovacuum policy including relevant autoanalyze triggers; new measured single-index concurrent REINDEX |
| 4. Scoped RPC execution | Recent in-principle work_mem; conditional internal custom-plan experiment; JIT/parallel controls deferred pending direct evidence |
| 5. Existing-pool control | Previously accepted bounded ceilings, with route/headroom and hosted application/restoration validation |
| 6. Scheduled work | Previously researched opt-in schedule-only staggering of enrolled cron jobs; not job cancellation |
| 7. WAL/checkpoint policy | Previously researched conditional checkpoint budgets and writer-scoped compression; not newly discovered this turn |
| 8. Reviewed code/policy optimization | Proposed prepared/tested RLS helper, membership, and RPC/application migrations; distinct production review/deployment authority |

The automatic core should prioritize families 1–5, enabling operations individually after their fixtures, permission checks, resource limits, ambiguity recovery, and real application-path verification pass. Families 6–7 depend on an eligible workload and explicit grants. Family 8 makes a broader V1 useful for problems that require logic changes; a narrower release can deliver its tested artifacts for manual deployment. Existing capability gaps disable dependent operations rather than justify unrestricted SQL.

Every released operation needs one positive fixture, realistic no-op/false-positive cases, role/parameter coverage, failure-phase recovery, and an outcome beyond successful SQL. Full stop and manual drift still override automatic dispatch/recovery; failed restoration must leave the active state visible. Non-reversible sampling/maintenance and costly rebuild compensation must be disclosed in the action record. No benchmark or supported-action claim should precede those tests.

The remaining scope choices are specific: whether customer-index deletion is included under a new grant; whether V1 delivers the reviewed migration lane; and which expanded index templates match the first hosted project's workload. The two statistics operations and measured concurrent rebuild are the strongest new additions to test now. That keeps V1 broad enough to optimize database work while tying each capability to an executable, independently verifiable contract.
