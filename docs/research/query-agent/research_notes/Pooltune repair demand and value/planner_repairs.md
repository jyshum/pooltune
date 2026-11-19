# PostgreSQL planner repairs: demand, frequency, and value

## Do missing indexes cause meaningful real production problems, and how often?

### Takeaway
Yes, a missing or unsuitable non-unique index can produce large production improvements, including relief for unrelated queries sharing the database. The research establishes existence and mechanisms, but does not establish a representative repair rate for small Supabase SaaS projects or the chance of finding a valuable repair during one week.

### Cited Findings
- A firsthand RDS account reports a quota-check query running about 500,000 times/day against 1.7 million rows. An existing index omitted the needed `state` column. A replacement non-unique composite index changed a shown execution from **605.274 ms to 0.048 ms**, with three buffer hits after the change. The author reports RDS load falling at least 20% and an investigations page improving from 30–40 seconds to about two seconds; later work brought the page to roughly 300 ms. These are author-reported outcomes from one unnamed company's tenant cluster, not independent measurement or Supabase-specific evidence. The plans return zero rows in both versions. — [Aditya Jindal, February 12, 2026](https://adityajindal.dev/writing/postgres-index-win)
- Netdata describes monitoring its own production PostgreSQL database and finding a rows-fetched ratio below 0.1%, consistently for more than a week. It reports that creating missing indexes corrected that ratio. It does **not** report query latency, dollars saved, engineering hours, customer count, or a weekly stream of new missing indexes. This is a first-party production example from an observability vendor, with promotional selection bias. — [Netdata, October 5, 2022](https://www.netdata.cloud/blog/missing-indexes-in-postgresql/)
- Datadog reports that over 99% of observed production tables had some index; covering and partial indexes accounted for 0.3% and 1.8% of indexes. Only 2% of JSONB columns carried an index. Its indexing analysis uses schema metadata, excludes system schemas and default primary-key indexes, and includes tables without indexes. Findings generally use May 1–16, 2026 data from its observed customer population. These are **index adoption statistics**, not measured missing-index incidents or demonstrated repair opportunities. The published methodology does not establish a small-Supabase cohort or population-representative sampling. — [Datadog State of Postgres, Fact 6 and methodology](https://www.datadoghq.com/state-of-postgres/)
- PostgreSQL explicitly advises experimenting with real data. Small or artificially distributed test datasets can yield misleading indexing conclusions. It also explains that forcing an unused index may reveal either inaccurate estimates or that an index truly is inappropriate; timing the alternatives matters. — [PostgreSQL 17: Examining Index Usage](https://www.postgresql.org/docs/17/indexes-examine.html)

### Inferences
- **High confidence:** the synthetic 9.32 ms → 0.028 ms result is technically plausible and illustrates a real repair class. It does not demonstrate that customers often need that repair or will pay for it. The original trial figures are supplied in the research assignment; this note does not independently validate the benchmark.
- **High confidence:** low use of advanced indexes is not proof that those indexes are needed. A JSONB value may only be retrieved by a separately indexed record ID. Datadog's headline about ineffective indexing exceeds what schema adoption counts alone can establish.
- **Moderate confidence:** small teams with growing tables, changing query shapes, skewed tenants, or new features are plausible beneficiaries. Published selected successes cannot rank these segments by incidence.

### Gaps
- No representative denominator was found for the percentage of small/growing Supabase SaaS projects with a valuable missing non-unique index, new opportunities per project-week, or time developers spend fixing them.
- The Jindal account is closer to an application workload than a benchmark, but the company size is unspecified and the workload already makes about half a million daily checks. It should not be described as representative of a tiny SaaS.

## How much opportunity remains after native ANALYZE, and when do richer statistics help?

### Takeaway
Automatic analysis already handles the ordinary refresh job. Targeted refreshes and richer statistics remain valid conditional repairs, especially when existing estimates are wrong, but they do not justify a promise that every database needs weekly intervention.

### Cited Findings
- Default PostgreSQL automatically analyzes tables when initially loaded and as data changes. `ANALYZE` can target selected tables or columns. Statistics use sampling, so fresh estimates can still be wrong; repeated analysis can produce different estimates. PostgreSQL 17 identifies a concrete manual-maintenance exception: partitioned parent tables and inheritance parents whose children alone change do not receive the necessary automatic parent analysis. Higher statistics targets increase analysis time and space. — [PostgreSQL 17: ANALYZE](https://www.postgresql.org/docs/17/sql-analyze.html)
- A higher per-column target can improve estimates for irregular distributions. Extended statistics capture cross-column relationships that ordinary per-column statistics cannot represent. PostgreSQL does not automatically choose all relevant column combinations: an operator creates a statistics object, then manual or automatic `ANALYZE` collects its data. Dependencies, distinct-combination counts, and common-value combinations serve different estimation problems. The documentation recommends choosing groups implicated in bad plans, since unnecessary statistics waste analysis and planning work. Functional-dependency statistics have restricted applicability, including equality to constants and constant `IN` lists; they do not solve all estimation failures. — [PostgreSQL 17: Statistics Used by the Planner](https://www.postgresql.org/docs/17/planner-stats.html)
- GoCardless documents production queries that normally ran in under 50 ms becoming long-running due to poor statistics on a payment-transitions table approaching half a billion rows, in a roughly 2 TB database. Repeated `ANALYZE` initially helped, but estimates remained unstable. Raising one column's statistics target to 1,666 was insufficient; 5,000 stabilized the value. The company says ordinary sample sizes had otherwise worked very well in its experience. This is richer **single-column statistics**, not an extended-statistics success case, and the scale is far above the intended small-SaaS customer. — [GoCardless: Debugging the Postgres query planner](https://gocardless.com/blog/debugging-the-postgres-query-planner)

### Inferences
- **High confidence:** compare Pooltune against an ordinarily configured database with autoanalyze running, not a deliberately neglected baseline. Simple refresh automation has less incremental value when native maintenance already works.
- **High confidence:** choosing useful extended-statistics objects is a genuine remaining expert task. Once created, normal autoanalyze can maintain their collected data. Their setup does not automatically create a fresh paid repair every week.
- **Moderate confidence:** planner-repair diagnosis should separate stale information, insufficient sampling, cross-column relationships, and an absent access path. Repeated `ANALYZE` is a weak lasting answer when the real issue is inadequate sampling or missing relationship information.

### Gaps
- No representative incidence or weekly frequency was found for any of these three statistics repair types among small Supabase SaaS projects.
- The often-cited Affinity “3000x speedup” extended-statistics article was discovered, but its primary article was inaccessible (HTTP 403). Its multiplier is excluded from the evidence base rather than repeated from secondary summaries.
- No verified small-SaaS dollar-saving case for targeted `ANALYZE` or extended statistics was established.

## Can a week of monitoring deliver enough value, and does that value recur?

### Takeaway
A week can uncover an existing costly query and a single repair can deliver continuing benefit. That is different from showing that a new repair occurs every week, or that monitoring itself reduces a bill.

### Cited Findings
- Netdata's issue persisted for over a week before repair; this supports a detection window for one pre-existing problem, not recurring weekly demand. — [Netdata](https://www.netdata.cloud/blog/missing-indexes-in-postgresql/)
- Jindal's high call count illustrates how one persistent index mistake can create ongoing database pressure and affect a different screen. The source does not establish subsequent repair cadence or invoiced savings. — [Firsthand production index account](https://adityajindal.dev/writing/postgres-index-win)
- PostgreSQL recommends checking index use against the actual running workload and experimenting with real data. This supports measuring workload value rather than counting indexes added. — [Examining Index Usage](https://www.postgresql.org/docs/17/indexes-examine.html)

### Inferences
- **Arithmetic, not a forecast:** the supplied trial saves 9.292 ms per execution, approximately a 333-fold speedup. At 10,000 equivalent executions/week, that is 92.92 seconds of aggregate query execution time; at one million/week, 9,292 seconds, or about 2.58 hours. These totals are not CPU-hours, dollars, or necessarily user waiting time. Concurrent execution, bottlenecks, cache state, writes, and page-level latency prevent those conversions.
- **High confidence:** call frequency is a multiplier on the benefit of one fix. It is not the arrival rate of new fixes. An index can remain useful for months without needing to be recreated. An initial audit can therefore be valuable even when a recurring subscription is not yet justified.
- **Product hypothesis:** a representative first week should measure the number of distinct useful repairs, before/after execution under comparable traffic, affected important requests, investigation effort saved, and regression or maintenance costs. Subsequent weeks must separately measure **new** opportunities and persistence of earlier gains. Recounting an earlier fix as a new weekly win would misstate recurring value.
- **Decision implication:** there is enough evidence to keep a bounded query-repair experiment. There is not enough to promise weekly cost savings or reject a pivot. A one-time performance audit, incident-triggered repair, or release/growth check could fit the demonstrated work better than an always-active optimizer; this is a hypothesis to test, not validated demand.

### Gaps
- No sourced median time-to-first-useful-repair, weekly savings, engineering-hours saved, paid conversion, or retention estimate was established.
- Healthy projects can yield no worthwhile repair during the observation period. The probability is unmeasured. A week also cannot establish long-term repair recurrence or capture every less-frequent workload.
