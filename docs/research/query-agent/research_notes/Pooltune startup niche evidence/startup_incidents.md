# Startup database incidents and Pooltune niche evidence

## Which firsthand cases reveal actual startup database pain?

### Takeaway
The strongest cases separate avoidable database work, application transaction design, and genuine workload architecture limits. These anecdotes establish that the problems exist, not how prevalent they are or which niche has the largest market.

### Cited Findings

**1. incident.io: intermittent connection-pool timeouts (April 20, 2023).**
- Requests sometimes waited almost 20 seconds to acquire a connection. The team made 24 performance/observability changes over two weeks. Normal-looking database load and no obvious traffic surge made diagnosis difficult. Indexes, query rewrites, and asynchronous webhook handling did not eliminate the timeouts. The eventual fix removed unnecessary transactions around Slack modal submissions, retaining transactions only where needed. The author reported four subsequent months without database timeouts. This was an application transaction-lifetime problem, not simply a missing pooler. The story reports no compute-upgrade comparison or dollar savings. — [Original company postmortem](https://incident.io/blog/database-performance)

**2. Firsthand engineer account: quota-check query overwhelmed a tenant's database (February 12, 2026).**
- Aditya Jindal reports an investigations page taking 30–40 seconds. A different, frequent quota-check query scanned a 1.7-million-row table because the existing composite index did not match its filters/order. A corrected index reduced a shown execution from 605ms to 0.048ms; the author reports database load falling at least 20% and the page improving to about two seconds, before further page-query changes. The source includes query plans. Company identity and workload representativeness are not established in the article. No compute-upgrade experiment or bill savings is reported. — [Engineer’s account](https://adityajindal.dev/writing/postgres-index-win)

**3. Lago: event analytics interfered with the rest of the app (February 24, 2024 wiki revision).**
- Lago reports millions of events ingested per minute and heavy load times affecting its application. Its account identifies the difficulty as running expensive analytics alongside ingestion. It moved streamed events and analytics to ClickHouse while retaining Postgres for other data needs. The team reports this hybrid approach succeeded; the article does not provide controlled before/after latency or cost figures. Treat this as workload separation, not proof that every high-volume application must leave Postgres. Some generalized database explanations in the article are oversimplified, so cite its own operational experience rather than its universal claims. — [Lago’s official repository wiki](https://github.com/getlago/lago/wiki/Using-Clickhouse-to-scale-an-events-engine)

**4. incident.io: Postgres queue bottlenecks under load (October 6, 2026).**
- The team describes about 15 million escalation checks daily and a 3.5x improvement in peak capacity from multiple changes. Adding workers initially increased contention before CPU was saturated. They inspected detailed wait events using pg_stat_activity, moved queue records to a smaller table, separated claiming work from processing it, removed nested transactions from that path, and reduced concurrent claimers. They also deliberately left a frequently updated column unindexed and tuned maintenance. This was a load-testing/engineering improvement story, not a claim all changes were discovered during a customer outage. No direct bill reduction is given. — [Original engineering post](https://incident.io/blog/how-savepoints-quietly-throttled-our-postgres-queue)

**5. Linear: large-customer sync queries exceeded a suitable Postgres read design (August 18, 2026).**
- Some workspaces produced nearly one million sync actions daily, across over 20TB of sync actions. Large permission/filter intersections produced unstable tail latency. More replicas did not reduce work per query. Linear built a dedicated turbopuffer metadata read path while keeping authoritative payloads in Postgres; it fetched large payloads only after filtering. It compared result IDs against the old path in shadow operation before serving production traffic. This is a mature, large-workload example, not representative evidence about small startup databases. No exact dollar savings are published in this article. — [Original engineering post](https://linear.app/now/rebuilding-delta-sync-read-path)

**Supplemental named-company SQL repair: Heap (February 24, 2014).**
- Heap describes an import-pipeline function for removing duplicate events that became slow on large inputs. Repeatedly accessing positions in variable-length arrays caused quadratic work. Rewriting it around unnest removed the repeated array parsing. Their 100k-element benchmark fell from almost 40 seconds to about half a second. These timings were measured on the author's laptop, not reported production latency. This is a historical named-company example of fixing a database function without moving databases; it does not establish that a modern model can safely make the change. — [Heap original](https://www.heap.io/blog/dont-iterate-over-a-postgres-array-with-a-loop)

**Supplemental cost/headroom case: Heap (May 19, 2017).**
- A specialized sharded partial-index design caused hours of event-ingestion delay. Profiling found repeated index-metadata work; batching roughly 50 events per insert improved reported production ingestion throughput about 10x. The author says they had added no CPU in the six months since the discovery. The title's millions of dollars is a projected next-year saving, not an audited realized reduction. This is a historical, unusual, large analytical workload. The fix required ingestion changes, and its host-level CPU profiler would not simply be available through a normal hosted-Supabase SQL connection. — [Heap original](https://www.heap.io/blog/basic-performance-analysis-saved-us-millions)

### Inferences
- Case 2 is closest to an unattended, bounded database optimization product: detect expensive repeated work, inspect the actual query/plan, add an eligible index, and verify both target-query and surrounding workload outcomes.
- Case 1 is a critical negative test for Pooltune: a pool-wait symptom does not authorize pool expansion or imply that a pool setting is the correct fix. Application tracing and code context may be necessary.
- Case 4 shows why locks and write pressure are problem families, not single automatically eligible fixes. Maintenance tuning may help some cases, but this root cause required application/data-model changes.
- Cases 3 and 5 define a sensible boundary: architectural workload separation should not be marketed as something eight local tuning operations can automatically accomplish.
- A defensible proposed niche is reducing avoidable Postgres work behind slow or increasingly expensive startup application queries. It is a hypothesis informed by cases, not validated customer demand.

### Gaps
- No representative startup survey or incident-frequency denominator was found.
- No direct Supabase customer production incident among these selected strong sources.
- No causal dollar-saving evidence in these cases. Do not turn CPU reduction into a claimed bill reduction.
- Exact startup stages, team sizes, and database versions are not consistently reported. Linear's disclosed workload is clearly much larger than an early product.
- The engineer account in case 2 has stronger query evidence than company/context provenance; label it as a firsthand account rather than a verified named-startup case.

## What could a bounded Supabase agent actually contribute?

### Takeaway
The most interview-defensible promise is a limited, measurable repair loop for avoidable work. It should explicitly recognize when the root cause belongs to application logic or architecture.

### Cited Findings
- Supabase provides an Index Advisor, explains that indexes have write/storage costs, and warns that the planner may intentionally ignore a suggested index. Estimated plan costs are not measured elapsed times. — [Supabase index documentation](https://supabase.com/docs/guides/database/postgres/indexes)
- incident.io describes monitoring sequential rows read to identify potential missing indexes as tables grow, then inspecting plans and measuring candidates. Its 500k-row email-query timing example is demonstrative, not a measured production incident; do not cite it as real customer impact. — [incident.io indexing practice](https://incident.io/blog/choosing-the-right-postgres-indexes)
- PostgreSQL says native autovacuum is sufficient for many installations, while some workloads require adjustments. Ordinary VACUUM typically makes internal space reusable; table-rewriting approaches require stronger locks and extra space. — [PostgreSQL routine vacuuming](https://www.postgresql.org/docs/17/routine-vacuuming.html)
- Heap reports that a minor customer dropdown change could eliminate an index-only scan and substantially slow a query. It evaluated SQL changes across hundreds of representative queries, alternated control/experimental execution order to reduce cache bias, and examined variation rather than relying on one plan. This 2021 report concerns a large analytics platform, but directly supports the user's harness concern. — [Heap query experiments](https://www.heap.io/blog/optimizing-postgres-queries-at-scale)

### Inferences
- For index-like repairs: database telemetry can identify high total resource consumption even if it is not the highest-call-rate query. Plans and representative parameters establish whether a candidate fits.
- Verification should measure workload outcomes under comparable load and include write cost, errors, and completed work, not just one faster SELECT.
- An agent should separate “eligible to fix autonomously,” “requires application change,” and “requires a larger architectural decision.” A correct abstention is a successful diagnosis, not product failure.
- A broad library of eight repairs can support one product outcome, but must not imply every slow-database case is repairable using those eight.
- The harness needs cases with misleading symptoms: pool waits caused by unnecessary transactions, one slow screen caused by another query, and write pressure worsened by excess indexes.

### Gaps
- None of these scenarios has been reproduced on Jared's projects.
- A real Supabase project capability/permission check is needed before promising any particular operation is available for all plans and roles.
- Production repair coverage, regression rate, and human time saved remain unmeasured.

## What should we say about scale and cost?

### Takeaway
More compute is sometimes an appropriate purchase, but these cases show multiple problems where reducing work or redesigning coordination was necessary. Do not promise avoidance of all upgrades.

### Cited Findings
- incident.io's queue work targeted bottlenecks that prevented the system from using available CPU; the stated goal was eventually to reach a genuine CPU ceiling where a larger CPU would be useful. — [Queue engineering account](https://incident.io/blog/how-savepoints-quietly-throttled-our-postgres-queue)
- Linear explicitly states that replicas did not change the expensive per-request filtering work. — [Linear read-path account](https://linear.app/now/rebuilding-delta-sync-read-path)
- Lago kept Postgres for its core data while adding another database for a distinct events/analytics workload. — [Lago account](https://github.com/getlago/lago/wiki/Using-Clickhouse-to-scale-an-events-engine)

### Inferences
- “Improve performance using the capacity you already have, and show when capacity is truly the limit” is more defensible than “never buy more compute.”
- Improving a fixed-price instance's efficiency may create headroom without immediately lowering the bill. A cost claim needs an actual downsizing or avoided-upgrade comparison, with reliability constraints.
- Calls, events, and transactions alone do not tell us whether a company uses MongoDB or Postgres. Do not infer Angel's database or architecture from his vague description.
- Suitable interview framing: research-derived problem, controlled workload evaluation, measured repair coverage, and explicit non-goals. Do not claim personal production-scale experience or customer savings that have not happened.

### Gaps
- No verified information about Angel's database, transaction semantics, storage needs, or actual bottleneck.
- No substantiated typical startup database spend, or economic comparison between paying for more capacity and engineering time, was established in this research.
