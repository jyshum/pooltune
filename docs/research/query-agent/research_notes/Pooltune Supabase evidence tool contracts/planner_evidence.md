# Planner and relation evidence for the six accepted repairs

Research only, November 12, 2026. PostgreSQL 17 is the reference baseline, not an assumption about every customer's version. No customer query or SQL template below was executed. The accepted product scope remains unchanged.

## What exact evidence can relation inspection return?

### Takeaway
PostgreSQL exposes enough structural and statistical evidence to support index investigation and the other five repair types. Most values are context or suspicion signals, not proof that a repair is warranted. Permissions and collection settings determine what is actually visible.

### Cited Findings

| Source | Fields to collect for one enrolled relation | Meaning and limits |
|---|---|---|
| `pg_index` | `indexrelid`, `indrelid`, `indnatts`, `indnkeyatts`, `indkey`, `indclass`, `indcollation`, `indoption`, `indexprs`, `indpred`, `indisunique`, `indisprimary`, `indisexclusion`, `indisvalid`, `indisready`, `indislive`, `indisreplident` | Distinguishes key/INCLUDE columns, expressions/predicates, ordering/operator semantics, constraints, and live/ready/valid states. An invalid index can consume update work while being unusable for querying. [Catalog](https://www.postgresql.org/docs/17/catalog-pg-index.html) |
| `pg_indexes` | `schemaname`, `tablename`, `indexname`, `tablespace`, `indexdef` | Human-readable reconstructed definition. Use OIDs plus structured catalog data for identity and comparison. [View](https://www.postgresql.org/docs/17/view-pg-indexes.html) |
| `pg_class` | `oid`, `relnamespace`, `relname`, `relowner`, `relkind`, `relpersistence`, `relispartition`, `relam`, `relpages`, `reltuples`, `relallvisible`, `reloptions`, `relrowsecurity`, `relforcerowsecurity` | Object type/ownership, table options and RLS context; page/row/all-visible values are planner estimates, not freshly counted data. `reltuples=-1` means unknown. [Catalog](https://www.postgresql.org/docs/17/catalog-pg-class.html) |
| Size functions | `pg_relation_size(oid)`, `pg_table_size(oid)`, `pg_indexes_size(oid)`, `pg_total_relation_size(oid)` | Main fork, table storage including auxiliary/TOAST storage, all indexes, and total respectively. Partition parents need explicit descendant handling. [Functions](https://www.postgresql.org/docs/17/functions-admin.html) |
| `pg_attribute` | `attrelid`, `attnum`, `attname`, `atttypid`, `attnotnull`, `attisdropped`, `attstattarget` | Resolve attribute numbers, types and per-column statistics detail. Decode defaults by actual server version rather than hard-coding a sentinel. [Catalog](https://www.postgresql.org/docs/17/catalog-pg-attribute.html) |
| `pg_stats` | `schemaname`, `tablename`, `attname`, `inherited`, `null_frac`, `avg_width`, `n_distinct`, `most_common_freqs`, `correlation`; selectively `most_common_vals`, `histogram_bounds` | Distribution estimates; negative `n_distinct` is proportional to estimated rows. `correlation` concerns physical versus logical ordering, not correlation between two columns. Value/histogram fields contain customer values. Read visibility is restricted to accessible tables. [View](https://www.postgresql.org/docs/17/view-pg-stats.html) |
| `pg_stats_ext` | `statistics_schemaname`, `statistics_name`, `attnames`, `exprs`, `kinds`, `inherited`, `n_distinct`, `dependencies`, `most_common_freqs`, `most_common_base_freqs` | Existing multicolumn statistics and modeled dependence. Value arrays can reveal customer data; this view is owner-restricted, so absence can be a permission gap. [View](https://www.postgresql.org/docs/17/view-pg-stats-ext.html) |

Bounded cumulative fields (definitions: [PG17 statistics](https://www.postgresql.org/docs/17/monitoring-stats.html); view implementation: [PG17 system views](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/backend/catalog/system_views.sql)):

- `pg_stat_user_tables`: `relid`, `seq_scan`, `seq_tup_read`, `idx_scan`, `idx_tup_fetch`, `n_tup_ins`, `n_tup_upd`, `n_tup_del`, `n_tup_hot_upd`, `n_tup_newpage_upd`, `n_live_tup`, `n_dead_tup`, `n_mod_since_analyze`, `n_ins_since_vacuum`, `last_vacuum`, `last_autovacuum`, `last_analyze`, `last_autoanalyze`, `vacuum_count`, `autovacuum_count`, `analyze_count`, `autoanalyze_count`.
- `pg_stat_user_indexes`: `relid`, `indexrelid`, `idx_scan`, `last_idx_scan`, `idx_tup_read`, `idx_tup_fetch`.
- `pg_statio_user_indexes`: `relid`, `indexrelid`, `idx_blks_read`, `idx_blks_hit`.
- `pg_statio_user_tables`: corresponding heap/index/TOAST read/hit counters.
- Counts accumulate and may reset; snapshots lag and cache within a transaction. Rows/live/dead/modification figures are estimates. Index scans are not query counts; bitmap and index-only scans affect read/fetch counters differently.

### Inferences
- `idx_scan=0` is not permission or evidence to delete an index. It can reflect a short/reset observation window, rare critical use, or a constraint role. Customer-index deletion is outside V1 anyway.
- A high sequential-scan count is not a diagnosis. Small tables, broad reads and efficient parallel scans can legitimately use it.
- Index comparison must include predicate, expressions, operator class, collation, key order, INCLUDE columns and constraint purpose, not just names or a set of columns.
- Record `captured_at`, database/server identity, actual version, collection settings and visibility completeness with every snapshot. Unknown/filtered fields must not become numeric zero.
- Avoid routine transmission of MCV/histogram values. Summaries suffice initially; retrieving sensitive values needs an evidence-specific data policy.
- Index predicates, expression definitions and rendered plan filters can also contain customer literals. Structural inspection is not automatically value-free. Project explicit required fields rather than exporting whole `pg_stats` or plan objects indiscriminately; keep a redacted model-facing projection and any permitted raw evidence under a defined retention/access policy. Nothing here requires replication of customer application tables.

### Gaps
- This research proves documented schemas, not the grants available through a particular Supabase read-only identity. Probe exact views, columns, installed extensions, object kinds and permissions during connection qualification.
- Historical growth/churn requires Pooltune's own stored snapshots. Current catalogs alone do not provide a time series or app latency.

## What does a query-plan experiment establish, and what does it not?

### Takeaway
Keep catalog/statistics inspection separate from planning, hypothetical changes, and measured execution. Even plan-only operations require bounded execution and permission checks; measured plans deliberately run the customer query.

### Cited Findings
- Plain `EXPLAIN (FORMAT JSON)` exposes estimates. `ANALYZE` runs the statement; `BUFFERS` supplies runtime access counts; `TIMING OFF` avoids per-node timing overhead while still collecting runtime statistics. `GENERIC_PLAN` supports parameter placeholders but cannot be combined with `ANALYZE`. Planner costs are not milliseconds. — [EXPLAIN](https://www.postgresql.org/docs/17/sql-explain.html)
- Measured plans reveal actual row counts, filter rejection and loops. Node actual rows/times are per-loop averages. Parent/child work can overlap; summing all node values is not total query work. Sequential scans can be correct. — [Plan interpretation](https://www.postgresql.org/docs/17/using-explain.html)
- PG17 JSON keys include `Node Type`, `Relation Name`, `Schema`, `Alias`, `Index Name`, `Plan Rows`, `Actual Rows`, `Actual Loops`, `Rows Removed by Filter`, `Rows Removed by Join Filter`, `Rows Removed by Index Recheck`, `Heap Fetches`, `Shared Hit Blocks`, `Shared Read Blocks`, `Shared Dirtied Blocks`, `Shared Written Blocks`, analogous `Local` fields, `Temp Read Blocks`, `Temp Written Blocks`, `Sort Method`, `Sort Space Used`, `Sort Space Type`, `Hash Batches`, `Peak Memory Usage`, and top-level `Planning Time`/`Execution Time`. Fields depend on node/options; absence is not zero. — [PG17 implementation](https://raw.githubusercontent.com/postgres/postgres/REL_17_STABLE/src/backend/commands/explain.c)
- A function's volatility label is a promise supplied to the planner. Immutable functions can be folded during planning. — [Volatility](https://www.postgresql.org/docs/17/xfunc-volatility.html)
- PostgREST sets request role, search path and transaction-scoped JWT/request context. Supabase RLS uses authenticated identity and can differ under bypass/service roles. — [PostgREST transactions](https://docs.postgrest.org/en/stable/references/transactions.html), [Supabase RLS](https://supabase.com/docs/guides/database/postgres/row-level-security)
- `pg_stat_statements` normally normalizes literals away; it is not a replay log. It exposes total/mean execution, buffer/temp counters and successful-call counts, not every request's parameters. Nested statements require `track=all`; function-only visibility may be incomplete. — [PG17 extension reference](https://www.postgresql.org/docs/17/pgstatstatements.html)
- HypoPG objects exist only in their session and influence non-ANALYZE plans. Supabase explicitly requires hypothetical creation and EXPLAIN to share a connection. These estimates cannot measure actual candidate-index execution. — [Supabase HypoPG](https://supabase.com/docs/guides/database/extensions/hypopg), [Upstream usage](https://hypopg.readthedocs.io/en/rel1_stable/usage.html)
- The inspected Supabase index-advisor source forces generic plans and enumerates single-column hypothetical candidates. It returns estimated costs, suggested DDL and errors. It does not search composite indexes for Pooltune. Source pinned at `a6c959599357605cfdf262f96d30810b3201aa91`, `index_advisor--0.2.0--0.2.1.sql`; installed version still needs inspection. — [Pinned implementation](https://raw.githubusercontent.com/supabase/index_advisor/a6c959599357605cfdf262f96d30810b3201aa91/index_advisor--0.2.0--0.2.1.sql)

### Inferences
- Preserve a context reference: role, relevant claims, search path, input types and representative input set, prepared/generic/custom plan mode, and function settings. Do not silently replay as administrator. A valid generic plan may screen candidates but is insufficient to prove benefit for skewed inputs.
- Do not pass normalized SQL directly into an execution tool. Missing parameter values/types or unknown request context are structured blockers.
- Plan-only is lower impact than measured replay, but “no ANALYZE” is not a universal sandbox for arbitrary SQL, user functions, operators, type casts or planner support hooks. Use approved query identities, target dependencies, runtime limits and an execution identity with suitable privileges. A volatility label alone is not a security proof.
- Read-only transactions help prevent database writes, but are not a proof that arbitrary extensions/functions cannot produce external effects. Rollback cannot undo every external effect, consumed resources or sequence behavior. Exclude unqualified functions and writes from production read benchmarks.
- A clone is one experiment option, not a mandatory service requirement. It needs representative data distribution, roles, configuration and workload to be meaningful. If no acceptable measured test exists, return insufficient evidence. No unapproved clone creation/cost is implied.
- Generate a bounded set of compound nonunique indexes from query predicates/order/join keys, screen through HypoPG, then qualify actual effects and write/storage costs. The native advisor supplies suggestions rather than the complete investigator.

### Gaps
- Neither a lower estimated cost nor one faster warm-cache run establishes live improvement. Need comparable windows or repeated controlled runs, errors/completed work, neighboring queries and write overhead.
- Exact safe-query classification, accepted budgets and minimum input coverage are not settled by this research.

## How should bounded tools expose this evidence for all six repairs?

### Takeaway
Use four structured tool contracts with distinct effects and evidence levels. They support the accepted six repairs; none should be an arbitrary SQL execution endpoint.

### Cited Findings
- Poor estimates can persist with freshly sampled statistics when columns are correlated. Higher column targets and extended statistics are separate responses whose suitability depends on the query. — [Planner statistics](https://www.postgresql.org/docs/17/planner-stats.html)
- Index-only scans still fetch heap pages when visibility information is insufficient. The visibility map can therefore explain avoidable access even when an index exists. — [Index-only scans](https://www.postgresql.org/docs/17/indexes-index-only-scans.html)
- Function identity/settings are inspectable through `pg_proc.oid`, `proargtypes`, `proowner`, `prosecdef`, `provolatile`, `proparallel`, `proconfig` and the function definition. These distinguish overloaded routines and local settings. — [pg_proc](https://www.postgresql.org/docs/17/catalog-pg-proc.html)
- Supabase Cron uses `pg_cron`. Job-run evidence includes `jobid`, `runid`, `job_pid`, `database`, `username`, `status`, `return_message`, `start_time`, `end_time`. The upstream run table has owner-based RLS, so unobserved jobs may be invisible. Pinned source `5cedfa472ccc83567aa23ec645925ed8489a7797`. — [Supabase Cron](https://supabase.com/docs/guides/cron), [Run schema](https://raw.githubusercontent.com/citusdata/pg_cron/5cedfa472ccc83567aa23ec645925ed8489a7797/pg_cron--1.2--1.3.sql)
- Progress sources: `pg_stat_progress_create_index` gives `pid`, `relid`, `index_relid`, `command`, `phase`, `lockers_total/done`, `current_locker_pid`, `blocks_total/done`, `tuples_total/done`; `pg_stat_progress_analyze` gives phase/sample-block and extended-stat counts; `pg_stat_progress_vacuum` gives phase/heap-block and index processing. Counters are phase-specific. — [Progress reference](https://www.postgresql.org/docs/17/progress-reporting.html)

### Inferences

| Repair | Suspicion evidence | What still distinguishes diagnosis from signal |
|---|---|---|
| New index | Expensive query; selective predicates; access-heavy measured plan; missing suitable definition | Scan is unnecessary; representative inputs benefit; nonunique candidate permitted; neighboring reads/writes/storage acceptable |
| Targeted ANALYZE | Modifications since analysis, old/missing analysis history, plan estimate mismatch | Outdated information plausibly caused bad execution; native autoanalysis not already about to finish; action's actual outcome improves |
| Richer statistics | Fresh analysis but persistent estimate error; low target; multicolumn predicates; existing distributions/stat objects | Uneven sampling or column dependence, rather than missing index or arbitrary data access, explains error; supported statistic changes improve representative cases |
| VACUUM / threshold | Dead-row/churn estimates, cleanup history, low estimated visibility, index-only `Heap Fetches` | Cleanup is useful and not pinned by old snapshots; distinguish late trigger from blocked/under-resourced maintenance. High dead count alone is insufficient |
| Function work_mem | Temp-block deltas; measured inner sort/hash spill; exact enrolled routine/configuration | Spill belongs inside the function, not outer PostgREST sorting; necessary work; concurrency and total memory budget established. Function call totals alone cannot locate spill |
| Cron reschedule | Repeat overlap between enrolled run intervals, foreground regression and resource/wait evidence | Temporal overlap alone is not causality. Owner's deadlines/windows and comparable nonoverlap evidence are required; active jobs are not stopped |

Proposed contract outputs always include source/version, timestamps, scope, permission completeness, raw evidence references, derivations and missing-evidence reasons.

| Tool | Structured inputs | Effects and permission boundary | Results |
|---|---|---|---|
| `inspect_relation` | project, validated relation OID, allowed evidence sections, snapshot/row/byte budget | Fixed catalog/statistics reads; targeted readable objects; sensitive distribution values separately permitted | Definitions, validity, size, churn/maintenance/distribution summaries, collection completeness; no automatic repair verdict |
| `explain_candidate` | approved query ID, context reference, candidate-index specification or none, planning budget | Bounded plan-only or HypoPG session experiment; required object/function privileges; no persistent index | JSON plan, estimate differences, candidate usage, context fingerprint and unsupported conditions; clearly labelled estimated |
| `test_candidate` | qualified repair candidate, representative case set, explicit test environment, per-run and aggregate budgets | Controlled measured execution. Production qualification separate from catalog reading; no free-form DDL or arbitrary customer queries | Measured plans/durations/access/spill data, comparison coverage, errors, completed runs and inconclusive conditions; cannot claim live improvement solely from clone |
| `repair_progress` | Pooltune action ID, expected target identity and backend identity, read budget | Fixed progress/catalog reads for that action; no cancelling arbitrary backend | phase, phase counters, wait evidence, catalog state and command outcome if known; terminal state may be unknown and require reconciliation |

No progress row is not completion evidence. A command can finish or fail between polls, or visibility can be restricted. Reconcile durable action records with final object/settings state. Progress percentage is phase-local, not guaranteed whole-operation ETA.

**Illustrative observation SQL, not executed or production-ready.** `$1` is an already authorized relation OID supplied as a parameter. A tool still enforces query timeout, object scope, response byte cap and truncation reporting. These templates use the catalog/view/function references above; they do not inspect customer row data.

```sql
SELECT c.oid, n.nspname, c.relname, c.relkind, c.relowner,
       c.reltuples, c.relpages, c.relallvisible, c.reloptions,
       c.relrowsecurity, c.relforcerowsecurity,
       pg_catalog.pg_table_size(c.oid) AS table_bytes,
       pg_catalog.pg_indexes_size(c.oid) AS index_bytes,
       s.n_live_tup, s.n_dead_tup, s.n_mod_since_analyze,
       s.n_tup_ins, s.n_tup_upd, s.n_tup_del, s.n_tup_hot_upd,
       s.last_analyze, s.last_autoanalyze,
       s.last_vacuum, s.last_autovacuum
FROM pg_catalog.pg_class AS c
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
LEFT JOIN pg_catalog.pg_stat_user_tables AS s ON s.relid = c.oid
WHERE c.oid = $1::oid;
```

```sql
SELECT i.indexrelid, i.indisunique, i.indisprimary,
       i.indisexclusion, i.indisreplident,
       i.indisvalid, i.indisready, i.indislive,
       i.indnkeyatts, i.indnatts,
       pg_catalog.pg_get_indexdef(i.indexrelid) AS definition,
       pg_catalog.pg_relation_size(i.indexrelid) AS main_fork_bytes,
       s.idx_scan, s.last_idx_scan, s.idx_tup_read, s.idx_tup_fetch,
       io.idx_blks_read, io.idx_blks_hit
FROM pg_catalog.pg_index AS i
LEFT JOIN pg_catalog.pg_stat_user_indexes AS s
  ON s.indexrelid = i.indexrelid
LEFT JOIN pg_catalog.pg_statio_user_indexes AS io
  ON io.indexrelid = i.indexrelid
WHERE i.indrelid = $1::oid
ORDER BY i.indexrelid
LIMIT 101;
```

The second template illustrates a 100-item result budget plus one overflow row, not a chosen V1 limit. An overflow must report incomplete inspection, never silently imply no additional index exists. Fresh reads in separate short transactions avoid accidentally reusing one long-lived statistics snapshot. Re-resolve object identity and recheck catalog state before any later repair.

### Gaps
- No grants, version behavior, extensions, connection affinity, SQL templates or safety policies were tested on a live Supabase project.
- These are proposed contracts, not implementation or approved numerical settings. Preserve the hosted Rust controlled-tools / TypeScript-LangGraph investigator / Pooltune-owned PostgreSQL split from the accepted product plan.
