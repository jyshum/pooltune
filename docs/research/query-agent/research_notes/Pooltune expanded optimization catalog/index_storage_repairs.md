# Pooltune expanded index and storage optimization catalog

Research date November 11, 2026. Research-only proposals; no live tests or customer changes. Prior scope allows plain-column nonunique btree indexes and removal of Pooltune-created indexes. The broader actions below are proposed extensions, not existing approval. Preserve no customer-transaction interruption and no automatic spending.

## Which additions provide real value without inflating the action count?

### Takeaway

Prioritize broader, evidence-tested index design as one expanded capability; add measured single-index concurrent rebuilding as a second; evaluate exact duplicate-index removal as a separately authorized third. Covering, GIN, BRIN, partial and expression indexes are variants of one action family, not five independent product features. Table fillfactor and materialized-view refresh are weaker immediate V1 additions.

### Cited Findings

- Covering indexes can enable index-only scans, but avoiding heap access also depends on all-visible heap pages. Recently changed pages require heap visibility checks even with a covering index. [PostgreSQL index-only scans](https://www.postgresql.org/docs/current/indexes-index-only-scans.html)
- INCLUDE stores non-key payload columns; wide payload increases index size and can make inserts fail if tuples exceed index size limits. These columns are not search keys. [PostgreSQL CREATE INDEX](https://www.postgresql.org/docs/current/sql-createindex.html)
- Partial-index applicability requires proving the query predicate implies the index predicate during planning. A parameter whose value varies cannot generally prove that implication for a generic prepared plan. [PostgreSQL partial indexes](https://www.postgresql.org/docs/17/indexes-partial.html)
- Expression indexes avoid recomputation during searches but evaluate expressions on insert and non-HOT update. [PostgreSQL expression indexes](https://www.postgresql.org/docs/17/indexes-expressional.html)
- BRIN targets very large tables with physical correlation; its block summaries are lossy and require rechecking candidate rows. `pages_per_range` trades precision for index size. [PostgreSQL BRIN](https://www.postgresql.org/docs/17/brin.html)
- JSONB GIN `jsonb_ops` and `jsonb_path_ops` support different operators. Path ops supports containment/jsonpath operators but not key-existence operators, and searches for structures without scalar values can be poor fits. [PostgreSQL JSONB indexing](https://www.postgresql.org/docs/17/datatype-json.html#JSON-INDEXING)
- pg_trgm supports accelerated LIKE/ILIKE and regex patterns; patterns with no extractable trigrams can degenerate into full-index scans. It is a trusted extension in upstream PostgreSQL. [PostgreSQL pg_trgm](https://www.postgresql.org/docs/17/pgtrgm.html)
- Supabase lists pg_trgm and pgstattuple among available extensions, but available/installed versions still need per-project discovery. [Supabase extension catalog](https://supabase.com/docs/guides/database/extensions)

### Inferences

Recommended ranking:

1. **Expand `create_index_candidate` first.** Begin with bounded INCLUDE payloads and workload-specific JSONB/trigram GIN templates; optionally BRIN for large time-correlated event tables. Keep predicate/expression templates narrower until actual prepared/RLS query paths are reproduced. These improve common searches the initial btree-only subset cannot serve.
2. **Add `reindex_one_concurrently` after crash-recovery tests.** Useful for measured index-space waste and degraded index access; use one named application index, not whole-database rebuilding. Avoid marketing it as a routine response to any high dead-tuple count.
3. **Offer `remove_exact_duplicate_index` as explicit opt-in.** A conservative definition-equivalent pair can provide stronger evidence than “unused index.” It still deletes a customer object and cannot offer instant rollback. Existing approval to remove only Pooltune-created indexes does not cover this.
4. **Keep table fillfactor experimental/later.** The SQL change is simple, but benefit emerges as pages are reused/created and rollback does not restore physical layout. It needs a sustained update-heavy fixture and longer observation than many first-version repairs.

Autoanalyze trigger tuning belongs to the existing table-maintenance/statistics policy family. It is useful additional diagnosis coverage, not a new product action merely because the parameter names differ. Existing VACUUM can address low visibility-map coverage; it is not necessary to invent a separate “index-only scan repair” permission.

### Gaps

- No measured workload distribution establishes the market frequency of each candidate. The ranking is engineering judgment for hosted Supabase applications, not a claim of measured ROI.
- HypoPG/Index Advisor support differs by extension version and access method. Do not assume every GIN/BRIN/INCLUDE/partial candidate is supported by the hypothetical-index tooling. Real disposable-project plans and workloads remain necessary when hypothetical support is absent.

## How should expanded indexes, rebuilding and deduplication be implemented safely?

### Takeaway

Each candidate needs a demonstrated access-path problem, a resource budget, exact object identity and a meaningful success test. Structural success is only step one. Rebuilds and deletion have distinct recovery semantics from adding a disposable new index.

### Cited Findings

- REINDEX CONCURRENTLY scans/waits in multiple transactions, requires extra resources, cannot run in an explicit transaction block and leaves recoverable intermediate objects on failure. `_ccnew` invalid leftovers indicate a failed replacement; `_ccold` leftovers indicate the rebuild succeeded but old-index removal did not finish. Names may have numeric suffixes. PG17 permits REINDEX with table MAINTAIN. [PG17 REINDEX](https://www.postgresql.org/docs/17/sql-reindex.html)
- PG15 instead requires ownership for a single index/table rebuild. [PG15 REINDEX](https://www.postgresql.org/docs/15/sql-reindex.html)
- pgstattuple/pgstatindex access is restricted by default to pg_stat_scan_tables or appropriate grants. `pgstatindex` reports btree leaf density, empty/deleted pages and fragmentation. Results accumulate page by page rather than representing one instantaneous snapshot. [PostgreSQL pgstattuple](https://www.postgresql.org/docs/17/pgstattuple.html)
- `pg_index` exposes key versus INCLUDE attribute counts, uniqueness/constraint flags, validity, readiness, replica identity, key attributes, collations, operator classes, ordering options, expression trees and predicates. These distinctions matter for identifying equivalence. [PostgreSQL pg_index catalog](https://www.postgresql.org/docs/17/catalog-pg-index.html)
- DROP INDEX CONCURRENTLY requires ownership, cannot run in a transaction block, supports one index and no CASCADE, and cannot drop partitioned or constraint-supporting indexes through that form. [PostgreSQL DROP INDEX](https://www.postgresql.org/docs/17/sql-dropindex.html)

### Inferences

All example numbers are illustrative, not preset recommendations. Common executor rules: schema-qualified quoted identifiers from catalog identity; exact allowlisted templates; project/table serialization; before-state durable ledger; fresh authorization and identity checks; reject migrations/maintenance conflicts; separate session SET commands on a direct/session connection; bounded own lock/statement timeouts; no killing other sessions. Concurrent index DDL is one top-level statement, never an RPC/function or transaction-wrapped migration. Table ownership powers and schema CREATE are required for creation. Missing extensions require a separate installation grant; do not silently install or upgrade them.

### 1. Expanded index-design contract

`create_index_candidate(table_oid, table_fingerprint, access_method, key_columns_or_allowlisted_expression, include_columns?, approved_predicate?, operator_class?, storage_options?, evidence_id, cost_budget)`

The initial executor should permit separately tested templates rather than arbitrary SQL strings:

| Template and example | Required evidence | Main failure/false-positive guard |
|---|---|---|
| Covering btree: `CREATE INDEX CONCURRENTLY pt_orders_customer ON public.orders (customer_id) INCLUDE (status, total);` | Recurrent selective read needs these payload columns; heap-fetch cost matters; payload width bounded; index-only plan plausible | A frequently updated table may lack all-visible pages. Adding wide payload can cost more storage/WAL and inhibit HOT than it saves. Check actual Heap Fetches, not just plan-node name |
| JSONB GIN: `CREATE INDEX CONCURRENTLY pt_events_payload ON public.events USING gin (payload jsonb_path_ops);` | Significant supported containment/jsonpath workload; representative predicates and parameter distributions | Key-existence queries need a different operator-class choice; don't apply path_ops universally. Observe insertion cost/pending-list behavior; do not change query semantics |
| Trigram GIN: `CREATE INDEX CONCURRENTLY pt_profiles_name ON public.profiles USING gin (name extensions.gin_trgm_ops);` | Expensive substring/ILIKE queries with useful extractable trigrams; extension/schema discovered | Short/wildcard-heavy searches may not benefit; qualified opclass is discovered, not assumed `extensions`; keep original comparison operators and collation |
| BRIN: `CREATE INDEX CONCURRENTLY pt_events_time ON public.events USING brin (created_at) WITH (pages_per_range = 128, autosummarize = on);` | Very large table, strong physical correlation, range predicates, lower projected maintenance/storage than btree | Correlation is only a hint. Test bitmap rechecks and heap blocks; out-of-order inserts can weaken summaries. Include summarization behavior in verification |
| Partial btree: `CREATE INDEX CONCURRENTLY pt_open_orders ON public.orders (customer_id) WHERE status = 'open';` | Stable small subset and actual query plan whose predicate implies exactly this condition | Test real prepared/generic/custom behavior and roles. A literal toy EXPLAIN is insufficient for parameterized PostgREST queries |
| Expression btree: `CREATE INDEX CONCURRENTLY pt_users_lower_email ON public.users ((lower(email)));` | Actual workload uses matching expression/collation and benefits materially | Restrict to allowlisted built-in immutable behavior, no arbitrary user functions/casts; nonunique only; account for write expression cost and expression-statistics refresh |

Do not require every template for initial release. Covering + one observed GIN use case can prove the expanded family without claiming exhaustive optimization.

**Eligibility:** Current hot query evidence, correct application role/RLS context, parameter-distribution coverage, existing-index comparison, stable enough statistics, estimated storage/writes and disposable test or supported hypothetical evaluation. Plain sequential scans on small/high-selectivity workloads can be the best plan. Preserve result sets, errors and RLS behavior; compare same workload to a no-change baseline. Never add uniqueness as an optimization.

**Execution and outcome:** Concurrent create, track pg_stat_progress_create_index and catalog validity/readiness/live flags. Check actual representative plans; compare duration/call, buffers/call, completed throughput and write/WAL/storage cost. Include low-cardinality and adversarial search parameters, not just favorable cases. Covering-index diagnosis should record plan `Heap Fetches` and visibility-map context; if the index already covers the query and stale visibility is the issue, existing bounded VACUUM may be the correct action. Do not create a duplicate index as a substitute for vacuum.

**Recovery:** On timeout/disconnect, reconcile actual catalog/progress before replay. An invalid new index can impose write overhead. Only provenance-matched new objects may be cleaned automatically; concurrent drop, no CASCADE, with resource/time bounds. Reversion removes the added index, not the application's data. Expression/index payload limits mean insert-error monitoring matters as well as latency.

### 2. Measured single-index concurrent rebuild

`reindex_one_concurrently(index_oid, expected_definition_hash, expected_table_oid, physical_evidence_id, storage_budget, cleanup_authority)`

**Evidence:** Measure index bytes and btree physical page statistics with a bounded `SELECT * FROM <extension_schema>.pgstatindex('public.orders_customer_idx'::regclass);`, allowing for configured fillfactor, normal page splits and future reuse. Correlate genuine empty/deleted/underfilled page waste with growth and a meaningful workload/storage problem. `n_dead_tup` is a table estimate, and low/zero `idx_scan` does not measure index bloat. Leaf density alone below 100% is not proof that rebuilding pays off. A sampled SQL bloat estimate is screening evidence, not exact reclaimable bytes.

**Execution:** `REINDEX INDEX CONCURRENTLY public.orders_customer_idx;` as a top-level command on direct/session-affine transport. Start with ordinary valid nonunique btree application indexes. Permit one at a time. Require room for the replacement alongside the original, temporary build files, WAL and workload growth; no auto disk purchase. Exclude active corruption incidents from an optimization autopilot: suspected corruption requires integrity investigation and a dedicated recovery plan, not casual rebuild/retry.

**Privileges:** Version-specific PG15/16 ownership versus PG17 MAINTAIN must be verified. Critically, MAINTAIN alone can allow REINDEX while lacking ownership needed to DROP a failed transient index. A complete capability needs authorized cleanup or an explicit manual-cleanup outcome; don't falsely claim this whole lifecycle needs only MAINTAIN.

**Verification:** The canonical index name/definition/constraints remains correct but its physical identity/OID can change. Record pre/post object identities; require replacement validity/readiness/live state and no unexplained leftovers. Compare bytes, physical density and real query/write metrics; success is not merely a successful SQL command. A smaller file without better latency can still meet a documented space objective, but label that objective accurately.

**Crash/partial success:** Track old/new definitions and identities through rename/swap phases. `_ccnew`/`_ccold` suffixes are hints, never authorization to delete every matching name. Reconcile which valid index serves the table before cleanup. Bound cleanup and escalate unrecognized states. This action is not transaction-rollbackable across all phases; after success there is no supported switch back to the old physical file. Its rollback policy is stop/fix-forward, not rebuild the bloated original. Do not alter index storage parameters in the same experiment.

### 3. Exact duplicate customer-index removal

`remove_exact_duplicate_index(drop_oid, keep_oid, pair_fingerprints, expected_dependency_snapshot, recreate_definition, explicit_customer_index_grant)`

**Evidence and eligibility:** Start only with two valid/ready/live ordinary nonunique btree indexes on the exact same table, same ordered keys, INCLUDE columns, collations, opclasses, ordering/null flags, relevant storage properties and tablespace. Exclude predicates/expressions at first, plus unique/primary/exclusion indexes, replica identity, clustered indexes, extension-owned objects, partition indexes and dependencies. Check pg_constraint and pg_depend, not just names or a normalized CREATE INDEX string. The retained index must be independently healthy. An index prefix that covers some queries is not an exact duplicate, and one index's zero scan count is not proof the other is equivalent.

**Execution:** Persist exact recreation SQL and metadata; recheck pair/dependencies, then `DROP INDEX CONCURRENTLY public.orders_customer_duplicate;` with no CASCADE and no transaction wrapper. Requires index ownership and separate permission to remove this customer object. The prior allowlist for Pooltune-created objects remains until the user adopts this extension.

**Verification:** Retained index remains usable, schema/constraint checks unchanged, representative reads and writes produce equivalent results/errors, no unexpected planning regression, storage usage/write work improve where expected. Account for stats resets and incomparable traffic. When exact equality already establishes redundant access-path semantics, don't pretend a long zero-scan observation is the core proof.

**Undo:** Recreate concurrently from stored definition, which consumes time/resources and may fail. This is restoration, not instant rollback. Preserve metadata and alert on failed reconstruction. Even exact duplicates may have operational name references outside PostgreSQL catalogs (maintenance scripts), so ask onboarding for external management ownership or keep this action review-gated. No broad “drop unused indexes” action is recommended.

### Gaps

- New template support requires runtime integration tests per PostgreSQL/extension version. No build-cost or success-rate claim was measured here.
- Accurate bloat inspection itself reads pages and can be expensive. It must have a size/IO budget and may be deferred, not run on every scrape.
- Catalog equivalence does not discover every external script relying on an index name. This is an operational ownership limitation of duplicate removal.

## What should remain deferred or be folded into existing families?

### Takeaway

Table fillfactor, autoanalyze scheduling and materialized refresh are real tools, but their action effects differ. Fold analyze triggers into existing maintenance policy, test fillfactor later, and treat materialized-view freshness as an explicit application contract rather than an inferred performance fix.

### Cited Findings

- HOT updates require unchanged indexed columns (with summarizing-index exceptions) and enough space on the old row's page. Lower table fillfactor can improve HOT opportunity; pg_stat_all_tables exposes HOT update counts. [PostgreSQL HOT](https://www.postgresql.org/docs/17/storage-hot.html)
- Fillfactor reserves page space during inserts. ALTER TABLE storage changes do not immediately rewrite existing contents; fillfactor and autovacuum parameters use SHARE UPDATE EXCLUSIVE, and table ownership is required. [PostgreSQL table storage](https://www.postgresql.org/docs/17/sql-createtable.html#SQL-CREATETABLE-STORAGE-PARAMETERS); [PostgreSQL ALTER TABLE](https://www.postgresql.org/docs/17/sql-altertable.html)
- Concurrent materialized-view refresh requires an already-populated view and a suitable all-rows UNIQUE index on ordinary columns; one refresh per view runs at a time. It replaces the old data. PG17 requires MAINTAIN. [PostgreSQL REFRESH MATERIALIZED VIEW](https://www.postgresql.org/docs/17/sql-refreshmaterializedview.html)
- Supabase provides pg_repack with extension/client version requirements, qualifying table keys and substantial disk-space requirements. [Supabase pg_repack](https://supabase.com/docs/guides/database/extensions/pg_repack)

### Inferences

- **Fillfactor pilot:** `ALTER TABLE ONLY public.events SET (fillfactor = 85);` in a bounded transaction with short lock_timeout. Require update-heavy workload, nonindexed changed columns, low measured HOT rate and evidence of page-space shortage, plus storage budget. If every update changes an indexed column, more page space may not solve HOT loss. Compare delta n_tup_hot_upd/n_tup_upd and WAL/update under comparable traffic over enough churn. Restore previous reloption or RESET only if absent before; restoring settings does not compact expanded pages. Do not append VACUUM FULL/CLUSTER to make effects immediate, because that introduces table rewrite and interruption. This is a candidate storage policy, not a guaranteed fast fix.
- **Autoanalyze triggers:** `ALTER TABLE ONLY public.orders SET (autovacuum_analyze_scale_factor = 0.02, autovacuum_analyze_threshold = 1000);` is a concrete future-statistics scheduling action when changes repeatedly outrun analysis. It belongs alongside already-researched per-table autovacuum tuning. Track analyze intervals, modification deltas, planner estimates and overhead; defaults are restored with exact previous override state. Threshold numbers must fit observed change rate and cost. It does not repair existing stale stats until ANALYZE actually runs, and it does not fix column-correlation errors that need richer statistics.
- **Materialized views:** If a known job repeatedly runs a blocking REFRESH and a suitable unique index already exists, proposing `REFRESH MATERIALIZED VIEW CONCURRENTLY public.daily_summary;` can preserve reader availability. Rewriting the known job command is a new contract, because refresh changes returned freshness and can execute user functions. Require explicit freshness SLA, same refresh query, row/content validation and caller cadence; no automatic creating a materialized view, redirecting application reads, adding uniqueness, or changing refresh cadence from inferred preference. Useful later, not a generic default V1 repair.
- **pg_repack:** Do not call it an automatic no-interruption bloat fix from the simplified Supabase description alone. It adds a CLI/extension synchronization lifecycle, transient objects/triggers, disk amplification and lock behavior requiring upstream-specific validation. Concurrent single-index rebuild is the narrower first maintenance expansion. This is sequencing, not a claim that pg_repack is impossible.
- For every action, keep a no-op outcome first-class. Cases already handled by native autovacuum, an existing index, an appropriate sequential scan or PostgreSQL's internal maintenance should not produce a gratuitous write merely to demonstrate agent activity.

### Gaps

- Materialized-view caller semantics and freshness requirements are not in database catalogs. An optimizer cannot safely invent them.
- Fillfactor benefit on an existing full table may take substantial churn; this research does not establish an observation horizon. Report unverified rather than forcing a rewrite.
