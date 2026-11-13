# Query and data workload problems: a problem-first Pooltune exploration

## What actually makes an application slow as its workload changes?

### Takeaway

Three different changes can create pain: **more records to search, more people asking at once, or a harder question being asked**. The product should distinguish them before deciding whether a database change is useful. All examples below are illustrative causal examples, not measured customer incidents or market-prevalence claims. Research accessed November 12, 2026. No project was accessed.

### Cited Findings

- Supabase already tunes PostgreSQL for the selected compute size and gives it default connection limits. Supabase explicitly says these defaults assume a general workload and may need workload-specific tuning. [Performance tuning, updated October 29, 2026](https://supabase.com/docs/guides/platform/performance)
- An index lets PostgreSQL locate selected rows without inspecting every row, but maintaining indexes adds work to changes. The planner chooses whether using an available index is worthwhile. [PostgreSQL indexes](https://www.postgresql.org/docs/17/indexes-intro.html)
- Planner statistics are sampled approximations. Ordinary single-column statistics cannot represent dependence across columns; PostgreSQL does not collect every combination automatically. [Planner statistics](https://www.postgresql.org/docs/17/planner-stats.html)
- Autovacuum normally refreshes planner information through automatic analysis as data changes. Some situations, including partitioned parents and particular distributions, require additional attention. [Routine vacuuming](https://www.postgresql.org/docs/17/routine-vacuuming.html)
- A generic prepared plan saves repeated planning effort, while a parameter-specific plan can execute better for some values. PostgreSQL chooses between them using estimated costs. [Prepared statements](https://www.postgresql.org/docs/17/sql-prepare.html)
- Supabase documents RLS policy work per candidate row, shows a plan discarding 100,000 rows, and explains why application-role/JWT context matters when reproducing performance. This is a concrete published example, not a frequency estimate. [RLS performance, updated November 2, 2026](https://supabase.com/docs/guides/database/postgres/row-level-security-performance)
- Large `OFFSET` values still require computing skipped rows. Deterministic pagination also needs a sufficiently specific ordering. [PostgreSQL pagination](https://www.postgresql.org/docs/17/queries-limit.html)
- Supabase supports text search with language processing and ranking. Those choices determine which results the product returns, not just its speed. [Full text search](https://supabase.com/docs/guides/database/full-text-search)
- Prisma's official examples show the N+1 pattern: one query gets a list, then another query runs for each item. It documents batching/nested reads as alternatives and calls out over-fetching. This establishes a real engineering pattern, not the prevalence of Prisma or N+1 among Supabase users. [Prisma query optimization](https://www.prisma.io/docs/orm/v7/prisma-client/queries/advanced/query-optimization-performance)
- Supabase Edge Functions normally run near the caller; the provider recommends considering the database's region for intensive database work. [Regional invocation, updated August 4, 2026](https://supabase.com/docs/guides/functions/regional-invocation)

### Inferences

**Simplified analogy:** a database is a staffed archive. More stored documents makes finding things harder; more visitors creates a line; asking for a comparison of every document creates a harder task. Hiring more staff is not the same as improving the catalog or changing the request. This is only an analogy; real databases also share CPU, memory and disk.

#### 1. A familiar screen becomes slower as records accumulate

- **Pain:** “Opening my orders used to be instant. Now it takes several seconds.”
- **Example:** the screen needs one customer's latest 20 orders, but the database checks a growing orders history, filters most of it away, then sorts the survivors. Returning 20 rows does not prove it only did 20 rows of work.
- **Why it can recur:** records accumulate; developers add a filter or sorting choice; the most-used screen changes. This is a mechanism for recurrence, not evidence of how often customers experience it.
- **Normal handling:** PostgreSQL picks a plan and can use suitable existing indexes and memory caches. Managed hosting does not know which future screen matters enough to justify an extra maintained lookup structure.
- **Desired outcome:** the same authorized results arrive faster while avoiding unnecessary read work and preserving acceptable writes.
- **Business knowledge needed:** usually little to investigate an exact existing query; more to decide whether a sort/filter can be removed. “Remove the filter” is not a performance-only repair.

#### 2. Everything is fine alone, but slows down when people arrive together

- **Pain:** “The app is fast in the morning, but everyone waits during busy periods.”
- **Example:** one report takes a moderate amount of CPU. Twenty simultaneous reports compete with ordinary page loads. The table can be unchanged and the individual SQL text can be unchanged.
- **Why it can recur:** daily usage peaks, a launch, scheduled reports or app polling. More requests can expose previously tolerable waste.
- **Normal handling:** connection pools reuse connections; PostgreSQL and the operating system share finite resources. This does not make twenty expensive tasks cost the same as one.
- **Desired outcome:** keep ordinary requests responsive under expected concurrency. That may mean less work per query, fewer duplicate requests, or a product-level schedule; it is not automatically “increase the pool.”
- **Business knowledge needed:** high when choosing whose work can wait, freshness allowances, or user-level limits. A third-party agent should not invent those priorities or interrupt transactions.

#### 3. A new search, deep page, or report asks a harder question

- **Pain:** “Lists work, but searching, page 500, or the annual report freezes.”
- **Example:** showing the newest orders is different from sorting all historical orders by a calculated total. A text-search box may add word matching and relevance ranking. Skipping to a far-away page may still make the database walk through earlier results.
- **Why it can recur:** new features add new query shapes; customers widen date ranges; histories become longer. A perfectly functioning database can legitimately need more work for a different question.
- **Normal handling:** PostgreSQL optimizes the query it receives using available access paths; it does not decide that “last month” may replace “all time,” or that approximate counts are acceptable.
- **Desired outcome:** preserve the intended search/report experience while meeting a useful response-time goal. Sometimes a database optimization suffices; sometimes the application needs pagination changes, precomputed summaries, or a separate reporting flow.
- **Business knowledge needed:** high for ranking, freshness, count accuracy, result ordering and export completeness. These cannot be silently traded for speed.

#### 4. One customer or filter is slow while the average looks healthy

- **Pain:** “Most customers are fine. Our biggest customer keeps timing out.”
- **Example:** one tenant has millions of events while others have hundreds. Or “open” matches nearly every item in one workspace and almost none in another. A plan chosen from average estimates or reused across values may behave differently.
- **Why it can recur:** customers grow unevenly, bulk imports happen, seasonal states change, or a large account joins. No universal tenant-size threshold follows from the documentation.
- **Normal handling:** sampled statistics describe distributions; the planner estimates costs and chooses among plans; prepared-plan heuristics reduce planning overhead. These mechanisms can miss the specific expensive case.
- **Desired outcome:** improve the problematic customer/filter without slowing the rest. Measure representative parameter classes, not just an overall SQL average.
- **Business knowledge needed:** knowing which identifiers represent a tenant, which customers matter and which query inputs are representative. Normalized SQL alone may erase those distinctions. Never infer tenant identity from a column name alone.

#### 5. A simple-looking read hides repeated joins or permission checks

- **Pain:** “The SQL editor is fast, but signed-in users see a slow page.”
- **Example:** every candidate record causes a membership check against another table. A developer's privileged SQL test bypasses the policy, so it measures a different task. Joins can also revisit the same related data many times under a poor plan.
- **Why it can recur:** permission rules, team memberships, relation sizes and query filters change. More scanned rows can amplify work that was inconspicuous earlier.
- **Normal handling:** PostgreSQL enforces authorization and optimizes within the allowed semantics. It cannot assume a permission check can be skipped.
- **Desired outcome:** faster access with exactly the same allowed and forbidden results. An ordinary supporting index can be a database change; a rewritten policy requires proving authorization equivalence.
- **Business knowledge needed:** high for policy rewrites; lower for observing plan cost in the correct existing user context. Do not disable RLS in production as a diagnostic shortcut. Supabase explicitly confines that comparison to non-production.

#### 6. The database is quick, but the application still feels slow

- **Pain:** “The database says 5 milliseconds, but the page takes two seconds.”
- **Example:** a list causes 40 sequential follow-up calls; each adds a network trip. Or the page downloads large text fields it never shows. Or an edge function makes several cross-region database requests.
- **Why it can recur:** UI components independently load related data, the list grows, added fields enlarge responses, or hosting geography changes.
- **Normal handling:** PostgreSQL executes each request. It cannot normally combine separate future application requests or know that the UI ignores half the response.
- **Desired outcome:** reduce end-to-end waiting, unnecessary calls and transferred data. Faster execution of one already-fast SQL statement may barely help.
- **Business knowledge needed:** app flow and which fields/results are actually needed. A provider-only integration can raise a suspicion but cannot prove the complete page timeline or safely rewrite the app.

### Gaps

- Documentation shows mechanisms and examples, not market size, willingness to pay, or the fraction of Supabase projects affected. These require user interviews and later authorized observation.
- No representative dataset of production Supabase incidents was established. Avoid presenting illustrative scenarios as customer stories.
- A scan is not automatically bad: reading much of a table can be the right plan for a broad report. An average is not automatically wrong: identifying a harmful outlier requires actual evidence.

## What can a third-party product actually observe or help with on Supabase?

### Takeaway

Supabase supplies enough documented interfaces to investigate database work and some provider requests. Full page-level diagnosis and changes to application behavior need app context or instrumentation. Documented availability is not the same as a tested integration on Jared's projects.

### Cited Findings

| Question | Actual documented interface | What it gives and what it does not establish |
|---|---|---|
| Is the database or its API pool under pressure? | Management API `GET /v1/projects/{ref}/analytics/endpoints/metrics`; OAuth `analytics:read`, scoped token permission `analytics_logs_read` | Prometheus metrics. This is aggregate health, not a causal explanation of one slow page. [Endpoint](https://supabase.com/docs/reference/api/v1-scrape-project-metrics) |
| Which SQL patterns consume time or run often? | PostgreSQL `pg_stat_statements`: `calls`, `total_exec_time`, `mean_exec_time`, rows, buffer and temporary-block fields | Aggregated planning/execution information, with privileges and tracking prerequisites. It is not individual request tracing or a page p95. [Extension reference](https://www.postgresql.org/docs/17/pgstatstatements.html) |
| Can an integration inspect SQL metadata without a full write connection? | `POST /v1/projects/{ref}/database/query/read-only`; OAuth `database:read`, permission `database_read` | Beta endpoint runs as `supabase_read_only_user` and needs schema-qualified references. Access to each required catalog/extension still needs testing. [Endpoint](https://supabase.com/docs/reference/api/v1-read-only-query) |
| What is happening inside the database now? | `supabase inspect db`; MCP `execute_sql`; SQL through Explorer or an authorized database connection | Reads live PostgreSQL internals; commands cover blocking, cache hits and more. Some need extensions or a particular major version. [Inspection guide](https://supabase.com/docs/guides/observability/inspect) |
| Why does this particular request do so much work? | PostgreSQL `EXPLAIN`; Supabase client `.explain()` after enabling `pgrst.db_plan_enabled` | A plan for a known query, not automatic retrieval of every app request. The API facility is disabled by default and needs production access restrictions. Execution analysis runs the query. [Query plans guide](https://supabase.com/docs/guides/api/query-plans) |
| Are API requests failing, and which route is involved? | `GET /v1/projects/{ref}/analytics/endpoints/logs`, querying provider log sources | Time-bounded source events; the current guide uses unified `logs` SQL. Events are not guaranteed to include every query input or complete page timing. [Log queries](https://supabase.com/docs/guides/observability/advanced-log-filtering) |
| Can the app timeline join provider observations? | `supabase-js` tracing runtime, `tracePropagation`, OpenTelemetry or compatible SDK | Opt-in propagation requires an active tracing setup. Current docs distinguish versions including 2.112.0 and 2.112.3. This is app integration, not something provider credentials install automatically. [Tracing](https://supabase.com/docs/guides/observability/client-side-tracing) |
| Does Supabase already suggest database improvements? | Dashboard advisors and SQL `index_advisor` | The documented advisor suggests indexes; current limitations list single-column B-tree recommendations. It does not establish a measured production benefit for every suggestion. [index_advisor](https://supabase.com/docs/guides/database/extensions/index_advisor) |

- Supabase's SDK supports selecting specific columns and querying related tables together. These are app-facing tools, not a remote switch that makes existing application calls change. [JavaScript select](https://supabase.com/docs/reference/javascript/select), [joins and nested tables](https://supabase.com/docs/guides/database/joins-and-nesting)
- Metrics guides describe broad project access while pricing has shown Free-tier restrictions. Plan, project, version and privilege support must be checked instead of promising one universal onboarding experience. [Metrics](https://supabase.com/docs/guides/observability/metrics), [pricing](https://supabase.com/pricing)

### Inferences

**The useful boundary is between three kinds of help:**

1. **Understand:** connect resource pressure, costly SQL patterns, plans and provider errors to a plain-English explanation. This is possible with documented provider/SQL interfaces, subject to privileges and missing context.
2. **Change database behavior without changing the requested answer:** an authorized database connection can apply a justified supporting index or planner-information repair. These are SQL changes, not an SDK method called “optimize.” The earlier action catalog is a possible toolbox after selecting a problem, not the product's pillars.
3. **Change how the app asks:** batch related reads, fetch fewer fields, change pagination/search/reporting, or alter permission logic. Supabase supports building these patterns, but its Management API does not automatically edit the customer's app or determine equivalent business behavior. Future app-change proposals need a separate customer-approved route.

For problem 1, plans and SQL statistics can often identify wasted work directly. For problem 2, time-series load and waiting observations are essential. For problem 3, the exact feature/query and result requirements matter. For problem 4, representative tenant/filter inputs matter. For problem 5, the real authorization context matters. For problem 6, app traces are usually the most decisive evidence.

A useful product outcome can be “this is an app round-trip problem, and here is the evidence,” even if no database setting is changed. Counting applied fixes would reward unnecessary database changes.

No proxy is required to investigate these cases. No automatic compute purchase, customer transaction interruption, arbitrary policy rewrite or production query replay is assumed. Even read-only investigation must be bounded so it does not become the workload problem itself.

### Gaps

- No endpoint, metric, diagnostic role, SQL change, or tracing SDK was tested against a live project in this research.
- Exact current plan availability, retention, log-query budgets and diagnostic privileges need a future authorized capability test. A SQL read endpoint does not imply ownership or permission to change a table.
- Provider telemetry alone cannot reliably connect every query fingerprint to a specific UI feature, user deadline, tenant or business operation. Product scope must acknowledge that boundary.

## Which problem-based customer niches are plausible enough to investigate?

### Takeaway

Three hypotheses deserve comparison. They serve different pains and require different amounts of application knowledge. None is selected or validated by documentation.

### Cited Findings

- Supabase already exposes inspection, advisors, detection checks and agent-oriented observability prompts. A new product should not assume there is no existing diagnostic or AI assistance. [Supabase inspection and observability navigation](https://supabase.com/docs/guides/observability/inspect)
- The concrete RLS, query-shape and network behaviors above establish that several distinct causes can sit behind the same complaint, “Supabase is slow.” [RLS performance](https://supabase.com/docs/guides/database/postgres/row-level-security-performance), [regional invocation](https://supabase.com/docs/guides/functions/regional-invocation), [Prisma examples](https://www.prisma.io/docs/orm/v7/prisma-client/queries/advanced/query-optimization-performance)

### Inferences

| Customer/problem hypothesis | Proposed customer-facing promise | Strength | Tradeoff and unattended fit |
|---|---|---|---|
| Small teams whose core screens degrade as real data accumulates | “Keep the database work behind your main screens efficient as the app grows.” | Repeated existing query shapes provide a possible baseline. Some fixes preserve the same answers and need little business interpretation. | Provider-only evidence can miss app causes. Unattended diagnosis is plausible; automatic changes only for proven eligible cases. Must outperform existing advisors through measured follow-through, not another recommendation list. |
| Multi-tenant teams whose largest customers suffer while averages look fine | “Find why one customer's workload is slow and protect the experience for everyone.” | Pain is concrete and averaged dashboards can hide it. Explaining which workload differs may be valuable before any fix. | Requires tenant attribution and representative inputs. SQL normalization may hide distinctions. Fairness, priorities and permission semantics need customer decisions; weaker unattended fit unless context is supplied. |
| Teams adding search, reporting or data-heavy admin features | “Find the unnecessary work in a slow feature and show the change that actually helps.” | Clear feature-level outcome; can distinguish query design from database pressure and network trips. Good setting for an evidence-backed optimization review. | More app context and business semantics. Often better as an assisted investigation than an always-on autonomous operator. Reporting freshness and search ranking cannot be chosen silently. |

The hypotheses imply different products. The first could favor ongoing monitoring and a small safe repair toolbox. The second could favor tenant-aware diagnostics. The third could favor a guided feature-performance investigation. Building all three at once would hide the unresolved customer choice.

Suggested next research questions for real users: What screen failed? When did it start? Does it happen for everyone or certain accounts? What did built-in Supabase tools fail to tell them? Did they need a database fix, an app change, or confidence that no intervention was needed? Would they trust automatic changes, or mainly want someone to prove the cause? These are discovery questions, not implementation tasks.

### Gaps

- No interviews, willingness-to-pay evidence, competitive conversion evidence or support-ticket sample supports choosing a winner yet.
- Jared's own Supabase projects may provide learning fixtures later, but access and workload experiments require a separate explicit authorization. They would test feasibility and understanding, not prove a market.
