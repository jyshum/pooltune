# Supabase startup pain, native coverage, and niche fit

## What concrete startup pain supports a focused product?

### Takeaway
The strongest concrete Supabase example found is interference between new analytical work and ordinary customer requests. Other first-person evidence supports queries worsening as data grows, but also shows that the largest fix can be application request batching. This supports a workload-focused product hypothesis, not a universal claim about startup bottlenecks.

### Cited Findings
- **Chatbase, vendor-published customer case, not an independent engineering postmortem.** Supabase reports that Chatbase's introduction of Metabase business reporting in early 2026 added heavy analytical queries. Scheduled reports hit a 3,000 IOPS ceiling and 125 MB/s throughput limit, while average IOPS remained below half capacity. Peak work displaced cache and delayed unrelated transactional queries. The stated plan combines identifying and rewriting/indexing expensive queries, increasing disk IOPS/throughput, and adding a read replica for analytics. Some steps are described as a plan, so do not present all as completed or measured savings. The story provides no database bill or verified before/after latency improvement. [Chatbase customer story](https://supabase.com/customers/chatbase)
- **Independent first-person anecdote, anonymous app rather than verified startup-scale case.** A developer reports a Next.js/Supabase app becoming slower as user data grew. Slow-query reports plus an LLM identified problematic queries and a missing index. The largest improvement came from consolidating sequential requests into one RPC, reportedly reducing one code path from about one second to below 100 ms. This supports pain and a manual competitor workflow; it does not demonstrate population prevalence or isolated DB tuning performance. [First-person account](https://www.reddit.com/r/SaaS/comments/1qqacnk/using_supabase_slow_query_reports_llms_to_fix/)
- **Supabase itself documents the mechanism behind tenant-visible slowdowns:** RLS policy evaluation costs depend on candidate rows scanned; diagnosis must reproduce role and JWT claims. It recommends appropriate policy-column indexes, eligible statement-level helper evaluation, and role scope. It also shows why application filters can change plan quality. These are engineering mechanisms, not evidence that most startups have this pain. [RLS performance](https://supabase.com/docs/guides/database/postgres/row-level-security-performance)
- Supabase compute charges depend on selected server resources and running hours, independently of actual database utilization. Reducing CPU use does not itself lower a fixed-size project's compute invoice. [Compute usage](https://supabase.com/docs/guides/platform/manage-your-usage/compute)

### Inferences
- The customer story is a good explanation of why “buy more compute” is incomplete. Query inefficiency, disk capacity, and workload isolation can all matter. More capacity may still be a correct component of the solution.
- A plausible initial customer is a small SaaS engineering team with real production users and recurring slow customer-facing queries, but no dedicated database specialist. A plausible job is: keep those queries fast as records, customer mix, and releases change.
- “Prevent avoidable Postgres slowdowns as startup workloads evolve” is a reasonable umbrella. It remains broader than an initial testable promise. “Keep customer-facing queries fast on Supabase as data and releases change” is narrower and maps to a clear workload and outcome.
- Cost should initially be a secondary outcome: more useful work on current resources or evidence for postponing an upgrade. Do not promise invoice savings without observed right-sizing or measured billed usage changes. There are no credible savings percentages from these sources.
- A very narrow “keep reports from slowing the app” niche has a concrete story but may quickly require replicas, routing changes, or a warehouse. Those exceed the current unattended repair catalog.

### Gaps
- No quantitative evidence establishes how often early-stage Supabase startups face each problem, how much engineering time they spend, or willingness to grant an autonomous agent production changes.
- No strong independent Supabase startup postmortem about per-tenant skew or autovacuum was found in this bounded search. Do not substitute SEO tutorials or vendor benchmarks for customer evidence.
- Jared has not reported a personal production incident. That limits first-hand validation, not the legitimacy of building controlled benchmarks or an interview project.

## Do the eight families form one niche, and where is the agent gap?

### Takeaway
The catalog has a common technical theme, reducing avoidable Postgres work and contention, but is too broad to constitute a single autonomous V1 promise by itself. A focused product can keep the catalog while exposing only the operations relevant to an enrolled workload and releasing them individually after evaluation.

### Cited Findings
- The prior catalog enumerates eight actual families: planner statistics; index design/lifecycle; table/index maintenance; scoped RPC execution; existing-pool control; scheduled work; WAL/checkpoint policy; reviewed code/policy optimization. The catalog explicitly distinguishes gated operations, future proposals, and reviewed changes. [Existing research catalog](/Users/jshum/Desktop/Tech%20Stuff/Resumes/softEng/reports/Pooltune%20expanded%20optimization%20catalog.md)
- Native Supabase already exposes Index Advisor recommendations, including estimated cost changes; recommendations may not be used and estimated costs are not observed duration. Indexes also increase storage and write work. [Managing indexes](https://supabase.com/docs/guides/database/postgres/indexes)
- Supabase's documented hourly performance-agent prompt checks blockers, execution-time regressions, and advisor findings using read-only tools. It explicitly does not create indexes, rewrite queries, or cancel sessions, and calls for comparable historical measurements. This is a documented prompt/workflow, not necessarily a hosted autonomous service. [Performance agent](https://supabase.com/docs/guides/observability/automate-with-agents/performance)
- The Metrics API documents CPU, IO, WAL, connection, and query series. The endpoint is beta, so names can evolve. Database inspection is available through SQL/MCP/CLI, including bloat estimates, maintenance state, costly queries, and blockers. These establish observable inputs, not sufficient business context. [Metrics API](https://supabase.com/docs/guides/observability/metrics), [Database inspection](https://supabase.com/docs/guides/observability/inspect)
- Native coverage footnote: OrioleDB public beta targets update-heavy storage and cleanup overhead, but Supabase explicitly says it is not production-ready, requires selection when creating the project, and cannot be added to an existing project. It is relevant future competition to a cleanup-only niche, not a proposed Pooltune migration or current universal solution. [OrioleDB beta](https://supabase.com/blog/orioledb-public-beta)

### Inferences

Suggested mapping to the narrower hypothesis, **keep important customer-facing Supabase queries fast as data and releases change**:

| Family | Role in the hypothesis | Scope implication |
|---|---|---|
| Planner statistics | Core: help the DB choose an efficient execution method when data distribution changes | Strong fit for a query-centered initial workload |
| Index design/lifecycle | Core: avoid repeated unnecessary record searches | Prove read benefit and bounded write/storage costs |
| Table/index maintenance | Supporting: cleanup problems can make the same queries costlier | Enable only for demonstrated maintenance causes; not blanket scheduled rebuilding |
| Scoped RPC execution | Supporting: certain database functions have spill or plan problems | Only enrolled functions with measured eligibility |
| Existing-pool control | Peripheral capacity handling | Diagnose first; pool ceiling changes should not define the headline |
| Scheduled work | Peripheral prevention of interference | Requires explicit job deadlines and flexible windows |
| WAL/checkpoint policy | Peripheral write-IO management | Weak fit for first query-focused demonstration; can remain catalog/deferred |
| Reviewed code/policy optimization | Core to some high-value fixes but different execution authority | Can prepare and test patches; do not quietly treat as routine unattended settings |

- This mapping is a product recommendation, not measured feature-market fit. All eight can exist in a longer-term product without all eight being automatic, equally prominent, or in the first release.
- The remaining gap is an executable repair workflow with outcome verification: identify the cause, select an eligible bounded action, perform it under grants, measure meaningful improvement, and stop or compensate where appropriate. Another dashboard or generic “add an index” recommendation overlaps substantially with existing tools.
- For a query-centered niche, an initial evaluation can hold a known workload and result/access contract constant, change data shape or release inputs, and measure recovery. Broader action availability is not model distillation, but it expands action selection and validation burden. The harness should expose a relevant subset, not merely append every possible tool to every prompt.
- Stronger narrow alternatives: (1) release regression repair gives a clear trigger and before/after baseline but needs deployment context; (2) write-heavy table maintenance has bounded routine operations but may be solved by native autovacuum or one durable setting; (3) scheduled-report interference needs fewer catalog tools but often has architectural remedies outside scope. There is no evidence here that the narrowest automatically wins.
- Documented APIs and SQL operations establish feasibility. None of these integrations or autonomous outcomes has been tested on Jared's projects. App-visible latency and correctness need additional evidence; aggregate database measurements alone cannot certify page performance.

### Gaps
- No evidence that adding the eight families preserves agent reliability. That requires per-family and mixed-case evaluation, false-positive cases, missing permissions, stale evidence, and refusal when the actual fix is outside scope.
- No evidence that automated repairs should rewrite any arbitrary query or authorization policy. Reviewed migrations remain a separate lane.
- No live capability probes, integration tests, permissions checks, or regressions tests were run during this research.

## Does a calls-and-transactions product imply NoSQL or MongoDB?

### Takeaway
No. “Calls” describes application activity, and “transactions” can describe grouped database work or a business event. Neither identifies the database. Schema design and storage planning are central concerns in PostgreSQL too.

### Cited Findings
- PostgreSQL supports transactions that group steps into an all-or-nothing operation and explicitly illustrates financial transfers. Transactions are not a NoSQL-specific concept. [PostgreSQL transactions](https://www.postgresql.org/docs/17/tutorial-transactions.html)
- PostgreSQL supports structured relational data alongside JSON/JSONB documents and JSONB indexing; its documentation describes the approaches as complementary and notes that document structure and size affect concurrency. [PostgreSQL JSON](https://www.postgresql.org/docs/17/datatype-json.html)
- Supabase separately offers file/object storage for media and general files. A product need not put every byte of a call recording into database rows. [Supabase Storage](https://supabase.com/docs/guides/storage)

### Inferences
- A possible voice/call application could store account, call, status, and billing records in Postgres; audio recordings in object storage; transcripts in text or JSON. This is an illustration, not a claim about Angel's stack.
- The phrase “we use a pooler; the smart part is how we connect it” is insufficient evidence to infer MongoDB, schema strategy, or the actual bottleneck.
- Separate four questions: how fast data arrives, how long it must be retained, which questions must be answered quickly, and what consistency/authorization rules apply. These determine storage architecture more directly than the label “calls and transactions.”

### Gaps
- Angel's provider, database, schema, workload, and actual incident remain unknown. Do not build a product niche on a guessed diagnosis.
