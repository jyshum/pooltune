# Pooltune detection and verification defaults v0

Status: concrete engineering proposal requested by Jared on November 12, 2026, ready for review and Rookery calibration. The values below are our starting policy, not PostgreSQL/Supabase recommendations, a statistical guarantee, or approval of live repairs. No traffic or repairs were run to choose them. The six repair scopes, capability-first evidence policy, adaptive scheduling and existing grants remain unchanged.

## 1. Measurements and comparison rules

Keep three references distinct. The **detection baseline** describes historical healthy behavior. The **benefit baseline** describes the current workload immediately before repair, including its degradation. The **protection baseline** describes the pre-change behavior of other affected reads/writes. For example, a query historically at 200 ms that deteriorated to 600 ms and returns to 200 ms improved by 67% against its 600 ms benefit baseline. Do not demand that it outperform its old healthy 200 ms to count as a successful repair. References to baseline in triggers mean detection baseline; benefit formulas use benefit baseline; side-effect regression gates use protection baseline (the target's own post-repair regression uses its benefit baseline).

- Collect bounded resource/query/table snapshots every **60 seconds**, staggered by project. Form **5-minute query windows**. Evaluate urgent safety signals separately; they do not wait for a full success window.
- Query identity includes project, database, role, query ID and top-level/nested status. Record schema, settings and deployment epochs separately. Rank top-level statements to avoid adding the same nested work twice.
- Interval mean execution time is `delta(total_exec_time) / delta(calls)`. Logical shared-block accesses per call use `delta(shared_blks_hit + shared_blks_read) / delta(calls)`. Temporary bytes written use `delta(temp_blks_written) * actual_block_size / delta(calls)`. Shared-buffer reads are not proof of physical disk reads; database execution time is not CPU time or application response time.
- Discard intervals crossing known counter resets, query eviction/recreation, project restart or incompatible collection changes. Never reset shared customer counters for our comparison. No calls means no duration estimate, not zero latency. Unknown telemetry is not zero.
- Native aggregates do not identify parameter distributions or supply per-query p95, attempted-request counts, error rates or peak execution memory. Use enrolled inputs, application/test instrumentation, available logs and plans where required. Label unsupported metrics unavailable. A missing error denominator cannot establish a zero error rate.
- Freeze the comparison baseline before applying a repair. Match query/role, input size/customer-size buckets and load. Allow exactly the recorded repair's intended schema/statistics/settings/schedule change; all unrelated changes invalidate or require requalification of the comparison. Baseline version changes need explicit reasons. Discard contaminated windows rather than learning a regression as the new normal.
- Default load matching: observed offered request rate and concurrency within **20%** where available, with comparable input mix and rows requested/returned. Completed statement count alone cannot prove matched offered demand; saturation may reduce completions. If important workload confounding cannot be resolved, outcome stays inconclusive or is limited to measured enrolled inputs.
- Track the target query, queries touching changed objects, writes to those objects, enrolled important queries, and top **20** statements by interval execution time. Small/typical/large input buckets are separate protected cases when relevant. This set is an initial coverage policy, not proof that every query is protected.

Why: one-minute collection is useful without continuous model calls; five-minute windows reduce sensitivity to isolated requests. Matching context prevents attributing cache, traffic or tenant-mix changes to a repair.

## 2. Initial investigation triggers

These open or update an investigation. None authorizes mutation without diagnosis, current grants and the operation's resource contract.

| Signal | Default trigger | Why |
| --- | --- | --- |
| Sustained latency regression | Mean execution time is **at least 50% and 20 ms above baseline**, in **3 consecutive qualifying 5-minute windows**, with **at least 20 calls per window**. | Relative and absolute change filter tiny effects; persistence filters spikes. |
| Persistent expensive work, including cold start | Statement uses **at least 30 aggregate execution seconds per 5 minutes** and **at least 5% of observed top-level execution time**, for **3 qualifying windows**. | Finds frequent fast queries and persistently slow queries without requiring a prior regression. This ranks database time, not CPU utilization. |
| Rare but slow query | **3 comparable executions of at least 1 second within 24 hours**. If an owner latency objective exists, use that objective instead of the generic one-second threshold. | Low-volume applications must remain observable. Starts diagnosis; does not establish a production repair outcome. |
| Important query objective breach | **3 of the latest 5 comparable observed executions/windows** violate the enrolled latency objective; use the measurement type the objective names. | Explicit importance can override default workload ranking. A mean cannot stand in for a p95 objective. |
| Failure or severe pressure | Immediate diagnostic priority for a relevant new failure; for a rate trigger, **at least +1 percentage point** above baseline and **5 excess failures in 5 minutes**, with **at least 100 observed attempts**. OOM/restart/correctness events do not need a sample-count minimum. | Rapid investigation is justified, but expensive emergency trials are not automatically allowed. Use only actual reliable failure/attempt telemetry. |

Baseline for the regression trigger: at least **12 valid 5-minute windows and 200 calls**, with at least **10 calls in each window**, selected from the previous 24 hours and excluding the suspect period. Initially this means roughly an hour of useful history, possibly longer on a quiet app. Prefer matched time-of-day history when several days exist. Without a comparable baseline, use expensive-work/rare-query/objective paths, not fabricated regression percentages.

After investigating without action, reuse findings for **60 minutes** unless a schema/data/plan change, new evidence, or severity escalation invalidates them. Deduplicate by affected query/object and cause. A cooldown does not suppress an urgent failure.

## 3. Shared success, regression and uncertainty rules

### A useful performance benefit

Use one of two prespecified routes; do not choose a favorable metric after seeing results:

- **Latency route L:** target mean execution time falls by **at least 20% AND 5 ms**.
- **High-volume route H:** target mean falls by **at least 10%**, saving **at least 10 aggregate execution seconds per 5 minutes at the baseline call rate**. Savings = `(baseline_ms - after_ms) * baseline_5min_calls / 1000`. This is measured execution-time reduction at normalized volume, not a financial saving or CPU saving.

Both routes require the repair-specific mechanism evidence below, comparable observations and no regression gate. Lower planner cost, dead-row count, or spill count alone is a technical improvement, not proof of customer performance benefit.

“No regression” means the operation's required guard measurements were available, sufficiently sampled and passed. Define that coverage before the action. Missing write traffic cannot qualify index write overhead; missing failure denominators cannot establish an unchanged error rate; missing large-customer cases cannot establish protection for that cohort. A guard can be inapplicable only for an evidenced workload property, such as an explicitly read-only table, not because telemetry is absent. If required coverage is missing, report any narrower demonstrated benefit and an overall inconclusive qualification. Optional end-to-end metrics remain optional, but the report must not imply they were measured. Additional representative tests are justified only if they resolve this specific gap within the authorized budget.

For normal traffic, collect **at least 6 qualifying 5-minute windows and 200 calls on each side**; each window has at least **20 calls**. The reference mean is call-weighted. Required representative input buckets must also be covered; aggregate benefit cannot hide a harmed large customer.

Noise check: the absolute mean difference must exceed **3 times the larger before/after median absolute deviation of the comparable 5-minute means**. This is a starting engineering heuristic, not a 95% confidence claim. Highly variable workloads extend observation or use controlled representative measurements; counts alone do not prove confidence.

Prefer valid existing evidence. If ordinary traffic is insufficient, bounded enrolled trials can establish an explicitly **tested-input** outcome: initially **10 measured runs before and 10 after per relevant representative input**, keeping roles, data and execution/cache conditions comparable, with the same effect/noise checks applied to the per-run distributions. Request only missing runs and remain within execution budgets. Do not repeatedly drop/rebuild an index for alternation on production. A short trial cannot establish whole-app tail latency, peak concurrency safety or long-term behavior. No blanket replay of writes or arbitrary functions. If the budget cannot cover the necessary test, remain inconclusive.

### Shared regression gates

Evaluate during execution and after the change. A resource guard is a protective stop, not proof the repair caused the problem.

Fast protection is separate from the slower outcome-confirmation table below. While our resource-consuming operation runs, perform bounded **15-second** activity/progress checks through its qualified connection; this does not make one-minute resource metrics update faster. Stop new mutations immediately on a credible correctness/availability failure. If a check shows our precisely identified backend blocking a protected customer request for **at least 1 second**, cancel our blocking operation and reconcile rather than waiting for two five-minute windows. Identify sessions by project, PID, backend start and recorded action, not PID alone. For available recent app/test measurements, **at least 2x baseline latency and +100 ms on 3 consecutive comparable requests**, or **3 consecutive unexpected errors**, is a protective intervention signal during our work even when normal call-count floors are unmet. This can halt our work before causality is established; label that distinction. Keep monitoring after repair completion because low throughput or a few stuck calls can themselves prevent reaching a sample floor. No observed fast signal is not proof of no interference.

| Gate | Proposed threshold and response |
| --- | --- |
| Important reads get slower | **At least +20% AND +5 ms** versus their matched baseline in **2 consecutive qualifying 5-minute windows**. Include affected representative input buckets. Freeze further changes and investigate attribution; revert reversible regression when supported. |
| Writes get slower | **At least +10% AND +2 ms**, on the same evidence rule. Index write cost can persist even if reads improve. |
| Very frequent short statements | Even below the absolute latency gates, regression also triggers at **at least +10% mean AND +5 normalized execution seconds per 5 minutes**. This catches material aggregate harm to sub-millisecond statements. |
| Completed work falls | **More than 10% lower completed work** for **2 windows**, only with matched offered demand, input mix and concurrency. If these are unknown, do not infer reduced throughput from fewer calls. |
| Errors increase | The failure-rate gate above, or one confirmed repair-related correctness/availability failure. Stop new mutations immediately and reconcile. |
| Tail latency, when measured | **At least +20% AND +10 ms in p95** across **2 windows**, with **at least 200 individual observations per compared window** or a trustworthy compatible histogram. p95 is never fabricated from statement aggregates. |

The 20-call window floor applies to aggregate query latency gates; a sparse important query can instead trigger immediate investigation on a credible severe failure or explicit objective breach. Mechanism-specific failures do not wait for a latency threshold. The noise check is needed to label a measured regression, but credible safety threats can stop new work without waiting for statistical certainty.

Outcome states: **improved**, **regressed**, **inconclusive**, **failed**, or **no permitted repair**. An applied operation without enough follow-up evidence is **inconclusive**, never improved. At **24 hours** without sufficient ordinary-query evidence, report inconclusive and retain monitoring without automatically repeating experiments. Job-cycle and recurring-maintenance evaluations use their cycle count instead of this deadline.

An inconclusive persistent change is not automatically retained forever: preserve its action record and a bounded verification deadline; decide retention or removal under its repair-specific contract. Do not auto-delete an index merely because traffic fell. Do not repeat ANALYZE/VACUUM to force a favorable result.

## 4. Repair-specific diagnostic and outcome cutoffs

These are investigation clues followed by evidence requirements. A threshold crossing alone is never a diagnosis.

### 1. Nonunique index

- Investigate after a shared worthwhile-query trigger plus plan evidence of avoidable scans, filtering, sorting or table access. A screening clue is **at least 10 examined rows per surviving row and at least 10,000 examined rows** at a relevant scan node. This is not a rule that sequential scans are bad; count actual node work/loops, not pg_stat_statements.rows. Other index opportunities, such as ordering, need not meet that ratio.
- Success: **L or H**, plus measured confirmation of the intended mechanism (less scan/block work or avoided sorting/spilling). If block-access reduction is the stated mechanism, target **at least 30% fewer shared-block accesses per call**. If the baseline has negligible block work, use the actual mechanism, not an artificial ratio.
- Regression: shared gates, particularly writes; invalid/not-ready index or storage-budget breach is failure/stop, not success. Inspect and remove only the qualifying Pooltune-created index when appropriate. Building a valid index alone is not improvement.

### 2. Targeted ANALYZE

- Investigate after a worthwhile-query trigger and either a **10x row-estimation error** at a causally relevant fully consumed plan node, or modifications since analysis equivalent to **at least 10% of estimated live rows AND at least 1,000 modifications**. Modification counters are approximate events, not distinct changed rows. A timestamp alone does not establish stale statistics.
- Success: **L or H**, plus the responsible estimate error falls to **3x or less**, or decreases by **at least 50%** with the better execution explained. A remaining large error stays visible as residual uncertainty.
- Regression: shared query/input gates; freeze further changes and investigate the new plan. ANALYZE has no exact rollback to its previous sample; repeated ANALYZE is not a recovery guarantee.

For estimate error use `max(actual, estimated, 1) / max(min(actual, estimated), 1)` as a stabilized screening measure. Treat zero-row cases separately and compare per-loop rows consistently. Exclude early-terminated nodes where LIMIT/EXISTS/parent behavior prevents actual rows from reflecting the full estimate.

### 3. Richer statistics

- Investigate a **10x relevant estimate error remaining after a valid recent ANALYZE** and without intervening material data change. Require a supported skew/correlation explanation and representative cases; seek **2 distinct relevant input cases** when the issue varies by input. Do not repeatedly refresh already adequate statistics.
- Success: **L or H** on the affected cohort and the same estimate-improvement test as ANALYZE; all enrolled small/typical/large cases pass regression checks. Record extra analysis time and statistics storage.
- Regression: shared gates or more than **25% additional ANALYZE duration** for comparable data/conditions on **2 observations** triggers review of maintenance cost, not an automatic conclusion that the customer is worse off. A confirmed maintenance-budget breach fails qualification. Restore the prior target/remove only our statistics object when appropriate, recognizing that sampled-statistics state has no exact undo.

### 4. Plain VACUUM / selected cleanup settings

- Investigate a worthwhile query plus estimated dead rows of **at least 20% of live+dead rows AND at least 10,000 dead rows** for **3 windows**, or an intended index-only scan with **at least 0.5 heap fetches per returned row AND 1,000 heap fetches** in representative measured plans. These are alternative clues. Check active autovacuum, eligibility of row versions for cleanup and blockers first.
- Success for one cleanup: **L or H**, plus **at least 50% fewer heap fetches per returned row** when visibility was the cause, or demonstrated reduction in avoidable access work when backlog was the cause. A **50% dead-row-estimate reduction** supports cleanup completion but does not alone prove query improvement. Plain vacuum need not shrink the database file.
- Success for a recurring threshold adjustment additionally requires **2 comparable cleanup cycles** with the backlog controlled and no shared regression; report a progress checkpoint after **7 days** if cycles are too sparse.
- Regression: shared gates or new maintenance interference. Stop only our running work if needed. Vacuum cannot be undone; restore an altered threshold for future behavior when justified. Transaction/slot blockers outside our scope are explained, not killed.

### 5. Enrolled function work_mem

- Investigate a worthwhile function plus **at least 8 MiB temporary bytes written per call OR 256 MiB per 5-minute window**, sustained for **3 windows**. Attribute the spill to the function's internal sort/hash work; aggregate database temp usage alone cannot justify changing it.
- Success: **L or H**, **at least 50% less temporary write volume per call**, and qualified memory headroom at representative concurrent demand. Reducing spill alone is not enough. Internal evidence may be unavailable with current tracking; report the gap instead of assigning database-wide work to a function.
- Regression: shared gates, a memory-pressure stop, or excessive projected concurrent demand. Restore the prior function setting for subsequent executions; do not promise immediate reclamation from calls already running. Unknown application concurrency means a durable increase is not yet qualified, regardless of one fast test.

### 6. Enrolled Cron rescheduling

- Investigate after **3 observed job executions** coincide with **at least +30% AND +20 ms foreground mean latency** compared with matched job-free periods. Require **at least 20 target-query calls per affected/control period**. Match offered workload and inspect contention/resource evidence; overlap alone is correlation.
- Success: **L or H** for the previously affected foreground cohort across **3 comparable future executions**, with **every required run completed by its deadline**, preserved frequency/dependencies and no shared regression in the new window. Apply the noise/context checks across the per-run observation periods; do not force a daily job into a 30-minute query-verification clock.
- Regression: any attributable missed/duplicated required execution or deadline violation, or shared harm in the new window. Adjust future schedules appropriately; do not cancel customer jobs or assume changing a cron expression clears queued work. Sparse jobs remain provisional until enough cycles occur.

## 5. Resource signals alongside these outcome rules

These are sampled protective rules, not hard CPU reservations or proof of causation. They supplement the still-required repair-specific resource estimate and limits.

- Initial heavy-work admission proposal: **5 consecutive fresh one-minute samples** with CPU below **70%**, available RAM above **25%**, and enough disk headroom for projected index/temp/WAL growth while preserving **20% free capacity**. This is conservative calibration input, not a reason to block lightweight diagnosis when the DB is busy.
- During our heavy work, CPU at **90% or higher** or available RAM below **15%** for **2 consecutive fresh samples** stops new heavy dispatch and prompts evaluation/cancellation of our own resource-consuming work. Disk free below **10%**, an operation-budget breach, or a credible OOM/correctness event triggers immediate protective handling on observation.
- Relevant resource readings older than **120 seconds**, missing peak-space estimates, or unknown memory/concurrency demand cannot count as spare capacity. Defer the dependent heavy action and continue permitted lightweight diagnosis. Surface persistent deferral rather than silently waiting forever.
- Resume admission only after the start conditions recover for **5 consecutive samples**, with **at least 10 minutes** since the stop and no unresolved action state. Never blindly restart canceled builds. Repeated stop/restart consumes real work.
- No more than **one active mutation per project** during initial qualification. This does not constrain application concurrency. Stop/cancel only Pooltune's identified operations; never customer transactions. Cleanup and uncertain-action reconciliation cannot be abandoned when a model budget expires.

These cutoffs intentionally leave a margin between starting and stopping, reducing repeated starts/stops around one threshold. Their false deferral rate and resource usefulness must be measured across compute tiers. Customers need not set them; stricter owner overrides remain available. If a consistently busy project has no viable execution window, the next action is to report the constraint and evaluate an appropriate operation-specific policy, not silently raise limits or buy compute.

## 6. Rookery calibration plan

No test was run for this document. Rookery's restored MCP access is verified; its database data size, actual workload distribution and repair eligibility are not yet measured.

1. Capture available counters/settings and an enrolled query with its real role/inputs. Confirm the app is using the intended database; local fallback behavior must not masquerade as successful database traffic.
2. Build a reproducible synthetic dataset and steady paced workload. Vary data size and input skew separately from concurrency. Reuse the existing load script only after qualifying its pacing, bounds and cleanup behavior.
3. Exercise a healthy/no-change case, a labeled actionable case, an apparent improvement caused only by easier inputs, and a regression case. Do not remove useful customer indexes to manufacture an incident. Construct labeled failure cases in isolated disposable test objects/environment.
4. Freeze the workload and baseline, apply one qualified repair, collect comparable evidence, then run the policy on recorded measurements. Test counter resets, missing telemetry and sparse traffic too.
5. Measure false investigations, missed labeled problems, incorrect success labels, missed regressions, time to diagnosis/verification, and monitoring overhead. Tune detection sensitivity separately from repair authority. Record all changes to these defaults and validate them on a held-out workload rather than repeatedly tuning against one example.

One index experiment can calibrate shared measurements and the index pathway; it does not qualify the other five repairs. Function/Cron features require their own enrolled test fixtures. No implementation, load generation, role creation, automatic repair release or universal performance guarantee is conferred by this proposal.

## Primary references for metric semantics, not our chosen thresholds

- [Supabase Metrics API](https://supabase.com/docs/guides/observability/metrics): resource collection and scrape guidance.
- [PostgreSQL 17 pg_stat_statements](https://www.postgresql.org/docs/17/pgstatstatements.html): interval counter inputs and their limits.
- [Cumulative statistics](https://www.postgresql.org/docs/17/monitoring-stats.html): approximate table counters, activity and maintenance history.
- [EXPLAIN](https://www.postgresql.org/docs/17/sql-explain.html): execution, loops, plan evidence and buffers.
- [ANALYZE](https://www.postgresql.org/docs/17/sql-analyze.html) and [CREATE STATISTICS](https://www.postgresql.org/docs/17/sql-createstatistics.html): sampled summaries and supported statistics.
- [VACUUM](https://www.postgresql.org/docs/17/sql-vacuum.html): maintenance effects and limits.
- [Resource settings](https://www.postgresql.org/docs/17/runtime-config-resource.html): per-operation memory and concurrency effects.
- [pg_cron](https://github.com/citusdata/pg_cron): job execution and scheduling semantics.
