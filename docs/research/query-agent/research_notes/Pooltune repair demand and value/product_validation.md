# Pooltune repair demand, customer value, and parallel adapter work

Prepared November 14, 2026. This is research and a proposed next plan. No product code changed and no workload, model call, deployment, or database mutation ran during this investigation. Source inspected at `e0606a28145f5406503defa87e16070a580490a9`. Working-tree changes at inspection were unrelated legacy file deletions/duplicate-name files. Historical runtime claims below are recorded results, not fresh executions.

## Do six repair families solve enough real problems to justify the product?

### Takeaway

The existing tests establish that bounded repairs can work. They do not establish how often customers need them, typical weekly savings, or willingness to pay. The next useful product test is a small real-project observation pilot, followed by explicitly enrolled repairs.

### Cited Findings

- The current roadmap calls the six successful repair cases synthetic staging cases. The six families are a single-column index, targeted ANALYZE, joint statistics, plain VACUUM, function-scoped memory, and enrolled Cron scheduling. A memory attempt initially failed benefit verification and restored the old setting. The recorded follow-up passed unchanged gates. General customer rollout and broader workloads remain open. [Roadmap](../../../../design/hosted-query-agent/roadmap.md#L7)
- PostgreSQL already runs automatic vacuuming and analysis. Its manual says native vacuuming is sufficient for many installations. Automatic ANALYZE responds to row changes, not whether a distribution change matters to an important query. This supplies both a baseline competitor and plausible gaps. It does not give an incidence rate for those gaps. [PostgreSQL maintenance](https://www.postgresql.org/docs/current/routine-vacuuming.html)
- Supabase's index advisor already recommends index statements for submitted queries. Its presence means a recommendation alone is not a unique product outcome. [Supabase index advisor](https://supabase.com/docs/guides/database/extensions/index_advisor)
- `pg_stat_statements` exposes execution count, total and mean execution time, logical block activity, temporary block activity, and other counters. It does not contain a p95 latency column. [PostgreSQL statement statistics](https://www.postgresql.org/docs/current/pgstatstatements.html)
- The hosted index fixture measured candidate mean execution of 9.31655 ms before and 0.02765 ms after. Each side used 40 candidate reads, 40 healthy reads, and 40 writes. A separate traffic driver completed 720 queries. The report explicitly says this does not establish customer peak throughput or concurrent-write safety. [Hosted measurement](../../../../design/hosted-query-agent/hosted-diagnostic.md#L37)
- Supabase charges for the selected dedicated server's running hours independently of database use. As checked today, Small is $0.0206/hour and Medium $0.0822/hour. Faster queries on the same size and uptime do not themselves reduce this compute charge. [Supabase compute billing](https://supabase.com/docs/guides/platform/manage-your-usage/compute)
- Railway combines subscription charges with measured CPU, memory, storage, and egress use, with included usage affecting the final invoice. A reduction in billed use can matter there, but query execution milliseconds are not CPU meter readings. Pooltune's Railway worker bill and a customer's Supabase database bill are separate things. [Railway pricing](https://docs.railway.com/pricing)

### Inferences

**How to count demand.** Count distinct customer opportunities, not every repeated call of a bad query as a new incident. An opportunity is a sustained, consequential symptom with enough evidence for an eligible repair that ordinary maintenance or existing advice has not already resolved. Report three denominators separately: projects observed, distinct query patterns observed, and executions observed. A single hot query may dominate work even if almost every distinct query is healthy.

**Use workload weight.** For comparable windows, a query's aggregate execution time is its calls multiplied by its mean execution time. Rank avoidable work using actual call counts and comparable before/after means. Prefer differences in cumulative `total_exec_time` and `calls` for the window, while recognizing resets and changed query identity. Do not average query means without call weighting. Gather p95 from request traces or another latency distribution; it cannot be recovered reliably from the aggregate mean alone. These are measurement recommendations derived from the documented counters, not a customer outcome claim.

**A fixed count such as 1,000 is not a business threshold.** If an illustrative eligibility rule required 1,000 executions, a 100-query-per-second query reaches it in 10 seconds; a query run once per hour needs about 41.7 days. Neither count says whether the repair pays for itself. Important slow reports can have low volume. Tiny fast queries can have very high volume. Any minimum sample requirement must fit confidence and workload shape, while business value depends on saved work, user impact, and operator effort. This is arithmetic and a proposed decision principle, not a statement that Pooltune currently uses that particular threshold.

**Explicit hypothetical weekly savings.** The recorded fixture difference is `9.31655 - 0.02765 = 9.28890 ms` per call. Assume a real customer's query achieved exactly that difference, the rate below persisted 24 hours per day for seven days, query inputs and data remained comparable, and every counted call received the benefit. These are intentionally strong assumptions, not measured customer traffic.

| Maintained query rate | Calls per day | Aggregate execution time saved per day | Calls per week | Aggregate execution time saved per week |
| --- | ---: | ---: | ---: | ---: |
| 1/second | 86,400 | 802.56096 seconds, about 13.38 minutes | 604,800 | 5,617.92672 seconds, about 1.56 hours |
| 10/second | 864,000 | 8,025.6096 seconds, about 2.23 hours | 6,048,000 | 56,179.2672 seconds, about 15.61 hours |
| 100/second | 8,640,000 | 80,256.096 seconds, about 22.29 hours | 60,480,000 | 561,792.672 seconds, about 156.05 hours |

These totals sum database execution durations across calls. They are not CPU-hours, wall-clock hours eliminated from a server, human hours saved, or promised cash savings. Concurrent calls overlap. The fixture was not a 100-query-per-second load test. Index maintenance, extra storage, diagnostic reads, verification traffic, model calls, and worker operation must be included in net cost.

For an actual dollar example, if a measured and separately validated optimization allowed a customer to run Small instead of Medium for all 168 hours of a week, the current gross compute-price difference would be `(0.0822 - 0.0206) × 168 = $10.3488`, approximately **$10.35**. That is a conditional price calculation, not a forecast. It excludes credits, taxes, other billed resources, Pooltune's fee, and downtime or other effects of changing size. Keeping the same instance can instead create headroom or delay an upgrade while leaving this week's compute invoice unchanged.

**Proposed pilot, not a validated industry standard.** Recruit 5–10 real projects from the intended small-team segment. Observe each passively for 7–14 days, with the owner's consent and limited data access. Capture imports, releases, scheduled work, and normal quiet periods where possible. This size is an affordable discovery experiment, not enough to estimate industry prevalence.

1. Record ordinary autovacuum/autoanalyze behavior and what existing provider advisors already find. Do not disable these to manufacture product opportunity.
2. Measure project-level opportunity incidence and concentration: how many projects have a meaningful eligible problem, how many repeated incidents, and what fraction of aggregate execution work sits in those cases.
3. Have owners enroll bounded targets and permitted repair families. Compare representative baseline/post-repair windows and retain healthy queries, writes, results, and user-facing p95 where available. Follow the result for a week to see whether benefit persists.
4. Record database work, errors, diagnostic/verification overhead, actual provider bill effects, model/worker costs, operator investigation time, and whether the owner would pay the proposed price or renew. Do not substitute hypothetical salary arithmetic for observed effort saved.
5. Attribute incremental value by repair family. Ask whether each extra family solves important cases the simpler product misses. A six-family catalog is a toolbox, not a demand argument.

**Practical decisions after the pilot.** Continue if unrelated real teams repeatedly get meaningful verified benefit, can grant the needed scope, and show credible willingness to pay more than delivery/support costs. Narrow if most benefit comes from one or two families or one workload niche. Pivot toward reviewed diagnosis or a different customer problem if symptoms mostly need application/architecture changes, existing maintenance/advisors already suffice, permission friction blocks useful repair, or recurring value is too small. Agree concrete pilot targets with owners before results are inspected; no single mandatory pass percentage works for every segment. These are proposed judgments, not measured market facts.

### Gaps

- No representative prevalence survey or customer fleet measurements were established in this research. No defensible claim such as “X% of queries need repair” follows from the synthetic cases.
- No actual weekly customer volume, CPU-meter reduction, provider invoice reduction, engineering time saved, acquisition economics, retention, or willingness to pay is established.
- A week or two can miss monthly imports, seasonal traffic, rare incidents, and longer-term index write/storage costs. A passive pilot supports discovery, not a production safety guarantee.

## What did the latest Railway test add beyond the earlier six repairs?

### Takeaway

The latest test connected the new multi-cause investigator to the already-existing guarded index executor in a hosted worker. It did not build a seventh repair or qualify the other five adapters.

### Cited Findings

- The older TypeScript evidence schema permits at most one of ANALYZE/statistics/vacuum/memory/Cron evidence. The existing Go input contract likewise counts categories and refuses more than one before invoking the investigator; its proposal check enforces the expected repair family. [Old schema](../../../../../investigator/graph.ts#L32), [Go contract](../../../../../internal/hostedquery/worker_dispatch.go#L132)
- The diagnostic graph compares causes, requests named measurements from Go, reuses available evidence, and may decline a repair. The separately recorded diagnostic qualification used 15 bounded cases and fresh local PostgreSQL evidence-reader tests. Replay of retained measurements is explicitly distinguished from new live repair trials. [Diagnostic design and results](../../../../design/hosted-query-agent/diagnostic-loop.md#L23)
- Task 30 connected the graph using `AdaptDiagnostic` and a fixture-bound evidence reader. Task 31 qualified the hosted index route, healthy traffic, an actual service restart, read/write verification, and cleanup. [Implementation Tasks 30/31](../../../../design/hosted-query-agent/implementation.md#L336)
- `AdaptDiagnostic` transforms supplied evidence, checks each requested tool and returned identity, rejects missing/invalid observations, and attaches diagnostic attempt/call/read/timing history to the existing proposal report. [Adapter](../../../../../internal/graphrunner/consumer.go#L10)
- The index reader supports `plan`, `indexes`, and `analyze`. It marks `statistics`, `vacuum`, `memory`, and `cron` unavailable. It accepts no model-provided SQL or target, checks journal ownership and identities, and imposes bounded read-only collection. [Reader](../../../../../internal/hostedquery/diagnostic_reader.go#L24)
- The November 14 recorded hosted run is source `4918260`, deployment `58b9a26c-db3b-4c71-8c19-8cee332d24ce`, job 113. Sonnet used **one real call, 3.582 seconds**, rejected statistics/cleanup as the cause of the selective scan, and requested no additional evidence. Worker processing took **16.124 seconds**; total traffic-start-to-result time including deliberate restart was **60.913 seconds**. [Hosted run](../../../../design/hosted-query-agent/hosted-diagnostic.md#L19)
- Restart occurred while the exact job was queued and unclaimed. The replacement claimed it once and completed one repair. This does not prove hosted mid-model continuation or a requested-reader round trip; those mechanisms have separately labeled local evidence. [Restart scope](../../../../design/hosted-query-agent/hosted-diagnostic.md#L47)
- CLI validation still permits diagnostic mode only for the enrolled Rookery index-repair fixture. The other five repair flags still dispatch to existing executors through the earlier investigator route. [CLI restriction](../../../../../cmd/pooltune-consumer/main.go#L234), [Dispatch selection](../../../../../cmd/pooltune-consumer/main.go#L145)

### Inferences

The earlier tests answered: “Can an allowed repair work when the evidence contract is already narrowed to its family?” The latest integration answered: “Can the new evidence-comparing investigator run inside the hosted queue-to-repair workflow and preserve the existing guards?” Both are useful, different proofs.

The new investigator can consider alternatives without receiving general mutation authority. Existing family checks still prevent its diagnosis from turning an index enrollment into permission for an unrelated action. This is a safety boundary to retain in the remaining integration work.

### Gaps

- Five live diagnostic adapters and their family-specific readers remain incomplete. Existing executors are present; calling them “five unbuilt tools” would be incorrect.
- Broad customer qualification, concurrent committed writes to the repaired table, production operation, and complete failure timing reports remain outside the demonstrated result. [Recorded limits](../../../../design/hosted-query-agent/hosted-diagnostic.md#L45)

## Can the remaining five adapters be implemented in parallel?

### Takeaway

Yes, most family-specific implementation and local testing can proceed in parallel after a shared contract is agreed. Integration and shared Rookery qualification need controlled ownership and sequential live runs.

### Cited Findings

- Separate queued executor files already exist for [ANALYZE](../../../../../internal/hostedquery/queued_analyze.go#L13), [statistics](../../../../../internal/hostedquery/queued_statistics.go#L12), [VACUUM](../../../../../internal/hostedquery/queued_vacuum.go#L12), [memory](../../../../../internal/hostedquery/queued_memory.go#L12), and [Cron](../../../../../internal/hostedquery/queued_cron.go#L47).
- These executors use the same `lockTrial` function on the target. It acquires one shared advisory lock and refuses when another session owns it. [Shared trial lock](../../../../../internal/hostedquery/index_journal.go#L44)
- The shared capacity check requires a writable primary, statistics visibility, no ungranted database locks, and at most two other active client backends. Several repair executors check it before baseline/action work. It is a low-activity staging guard, not permission for five competing experiments. [Capacity guard](../../../../../internal/hostedquery/index_trial.go#L258)
- Job claiming serializes through a project row lock and checks competing consumer claims. Common worker proposal checks enforce identity, freshness, and repair family. [Queue claims](../../../../../internal/hostedquery/worker_dispatch.go#L53), [Shared proposal checks](../../../../../internal/hostedquery/worker_dispatch.go#L132)
- Shared integration surfaces include the consumer mode switch, evidence reader, Go adapter, diagnostic policy/schema, and model prompt. [Consumer](../../../../../cmd/pooltune-consumer/main.go#L145), [Reader](../../../../../internal/hostedquery/diagnostic_reader.go#L30), [Adapter](../../../../../internal/graphrunner/consumer.go#L12), [Prompt](../../../../../investigator/diagnostic-model.ts#L6)
- Cron's long observation windows are designed to remain in the Go durable scheduling/evidence layer, outside a model waiting for minutes in the bounded diagnostic loop. [Diagnostic operational boundaries](../../../../design/hosted-query-agent/diagnostic-loop.md#L70)

### Inferences

**Proposed work split, not build authorization:** First agree one adapter/reader contract and exact ownership of shared files. Then assign family-specific readers, bridges, and tests independently, using isolated branches/worktrees and separate local PostgreSQL databases or clusters. Each family should prove sufficient-evidence reuse, meaningful requested evidence, healthy/unsupported decline, changed enrollment refusal, timeout, and durable recovery while preserving the existing executor gates.

ANALYZE and statistics deserve coordinated evidence design because stale single-column distributions and missing relationship information can compete as explanations. Memory needs its existing function identity and aggregate concurrency budget preserved. Cron needs its durable future-run observations preserved rather than a longer model call. These are distinct integration risks, so equal “one adapter each” assignments do not imply equal completion time.

One integration owner should merge the mode routing, named-tool registry, shared schema/prompt changes, trace reporting, and queue/permission guard touchpoints. A prompt change for one family needs the combined diagnostic regression suite, since the same investigator evaluates every family. Parallel edits to the same prompt or consumer branch create avoidable conflicts.

After local evidence and review, run one shared Rookery family qualification at a time. Save its report, verify healthy traffic and protected work, exercise the relevant recovery boundary, clean only owned fixtures, then start the next run. Do not remove or bypass the global trial lock/capacity guard to claim parallel speed. Truly parallel live trials would require isolated targets and separately qualified coordination, which is additional scope.

### Gaps

- This research did not execute new tests or estimate a reliable number of engineering hours. Existing family executors reduce the work, but fresh readers, prompt regressions, and recovery qualification still need implementation effort.
- A five-way build can be divided logically even if available agent slots require waves. The user's question asks whether and how to parallelize; this note does not start those builds or live trials.
