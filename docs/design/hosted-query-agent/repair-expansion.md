# Compound indexes and the remaining investigator connections

Status: implementation and local qualification complete; hosted qualification in progress. This document does not claim production readiness.

## The problem this build solves

The first index repair handled one column. A query can need two columns together: each condition alone matches many records, but their combination identifies the record we want. Compound indexes were already part of V1. This build adds the first bounded two-column case.

The other five repair tools already worked in controlled staging tests. Their earlier model path received evidence for one known cause. This build connects those tools to the newer investigator, which compares causes and can ask Go for more evidence.

## How it works

Go collects evidence and identifies the enrolled target. Claude compares explanations and proposes a permitted repair, requests a named observation, or chooses no change. Go independently checks current conditions, applies the qualified change, measures its effect and saves the outcome. The model never supplies SQL or database credentials.

The compound case uses two ordered enrolled columns. The order travels with the evidence, model result and recovery record. Existing single-column jobs keep their old format. Supported compound construction is currently a nonunique B-tree on the fixed fixture's two equality columns; arbitrary customer query shapes remain unqualified.

Fresh evidence readers are deliberately smaller than repair experiments. They use read-only transactions and brief table locks that permit ordinary writes while preventing the table from being replaced during inspection. The statistics readers measure stale estimates or relationships between columns. Cleanup measures extra table checks. Function memory checks disk spill and the admitted memory allowance. Scheduling checks recorded complete periods, exact job identity and remaining deadlines.

## Trade-offs

One shared investigator keeps the tools, budgets and recovery rules consistent. Separate agents per repair would duplicate that coordination. Supplying enough evidence together still permits one Claude call; genuinely missing evidence can require another call within the existing budget.

The first compound case is intentionally bounded. General index search would support more queries but would require more candidate-selection, write-cost and recovery evidence. Passing this case does not complete every compound shape in V1.

Implementation and isolated local tests ran in parallel. Hosted repair trials stay sequential because the shared trial lock protects Rookery and overlapping experiments would distort the timings. These are engineering tests on owned synthetic data, not customer-population measurements.

## Evidence so far

- Local compound tests demonstrate roughly 25,000 matches for either individual key and one for their combination. Actual index execution reduces examined rows from 100,000 to one.
- A local mechanism run remained inconclusive on the unchanged 5 ms absolute benefit gate despite a large relative speedup. A valid index is not automatically a worthwhile repair.
- Real Sonnet passed 18/18 bounded diagnostic cases, including declining already-covered and unselective compound cases. These are retained/controlled evidence evaluations with no database mutation. [Cases](evidence/repair-expansion/model-cases.json) and [results](evidence/repair-expansion/model-results.jsonl).
- Independent reviews identified recovery and measurement defects before hosted execution. The progress log retains the findings and correction evidence.

## Remaining work in this batch

Qualify each connected path with real Sonnet in Railway against Rookery, record read/write outcomes and timing breakdowns, verify cleanup, and update map colors only to match demonstrated scope. Source and recovery reviews are complete. General customer onboarding, permissions, dashboard, sign-in and operating controls remain separate roadmap work.

## First hosted finding: the test disabled a useful PostgreSQL plan

The first compound run failed before repair. Its supposedly healthy query was still slow because an inherited test setting disabled bitmap index access. PostgreSQL can use an index to gather matching row locations, then fetch those rows together; that is a valid index plan, not a failed repair.

The detector correctly flagged the actual slowdown. Go recognized the existing index and made no change. A local reproduction changed only the disabled bitmap setting and reduced examined rows from 100,000 to one. The corrected driver preserves PostgreSQL's normal bitmap choice for the compound case. Thresholds and fixture data are unchanged. The corrected hosted run passed; the failed run and cleanup evidence remain recorded.

## Live results in progress

These trials use real Sonnet through the diagnostic graph, the hosted consumer and Go repair tools. Normal parameterized traffic creates the investigation. Each row is one owned synthetic case, not a reliability estimate across customer databases.

| Repair | Query database time before → after | Detect sustained slowdown | Queued job → saved result | Claude within that time |
|---|---:|---:|---:|---:|
| Two-column index | 11.370 → 0.036 ms | 12.113 s | 16.965 s | 3.216 s |
| Refresh stale statistics | 10.260 → 0.024 ms | 12.836 s | 12.957 s | 3.228 s |
| Describe relationships between columns | 13.520 → 0.276 ms | 8.783 s | 17.245 s | 5.423 s |
| Targeted cleanup | 38.844 → 8.627 ms | 8.710 s | 16.295 s | 3.326 s |
| Function-scoped memory | 220.592 → 147.383 ms | 12.851 s | 20.550 s | 5.594 s |

Each used one model call and no extra evidence requests because the initial observations were sufficient. Healthy traffic and brief spikes created no jobs. Each completed 1,974 traffic requests except the lower-rate memory case, which completed 1,057. All had zero errors or misses, removed their fixtures and preserved the public-index fingerprint. Raw reports retain separate protected-read/write samples and stage timings.

Continuous committed writes target a separate protected test table. Controlled before/after writes for ANALYZE and richer statistics change the candidate inside rolled-back transactions; cleanup's protected writes target the healthy table. These samples do not qualify heavy concurrent customer writes against the repaired table. Calculated queue/publication residuals are not pure queue-wait measurements.

The worker is in Railway's `europe-west4-drams3a` region while the Rookery route is `aws-0-us-west-2`. Lightweight lookup and protected-traffic requests still take roughly 150 ms end to end, much more than their database execution time. The heavier repaired memory query takes roughly 290 ms at the client, including about 147 ms of database work. Matching the worker and database locations is a later deployment optimization. The location stays fixed during this batch; these results do not claim to remove network delay.

## Where the worker time went

These are seconds within the successful worker attempt. Investigation includes Claude and graph overhead. Apply includes its checks and connection work. The statistics tool reports apply and verification together. Setup and deployment are excluded. Detection happens before these stages; result publication also contributes to the queued-job total above.

| Repair | Baseline evidence | Investigation | Apply | Verify | Other attempt work |
|---|---:|---:|---:|---:|---:|
| Two-column index | 3.560 | 3.721 | 1.481 | 3.213 | 3.925 |
| Refresh statistics | 1.707 | 3.670 | 1.549 | 1.671 | 3.305 |
| Joint statistics | 2.823 | 5.870 | 4.084 combined | included | 3.501 |
| Cleanup | 2.136 | 3.829 | 3.914 | 2.008 | 3.431 |
| Function memory | 5.051 | 6.039 | 1.279 | 4.136 | 3.041 |

“Other attempt work” is subtraction, covering remaining setup, validation and coordination; it is not a separately instrumented stage. The detailed [summary](evidence/repair-expansion/summary.json) and raw reports retain original precision.

## Monitoring cost and limits

Each successful non-Cron trial compared two 12-second windows with monitoring off and two with it on. The extra selected collector statements consumed 2.14–2.26 milliseconds of database execution per second during the on windows. Client p95 values were similar within these short samples; no traffic was missed. This measures those statements, not total database CPU, memory or connection cost, and does not establish a production overhead guarantee.

The conditions are deliberately constructed. Planner-control sessions expose seeded stale or correlated statistics, cleanup uses owned test updates, and memory uses a known spill inside an enrolled function. Passing them proves the mechanism and guarded workflow. Representative customer workloads, heavy writes and all remaining repair variants still need qualification.

## Findings that changed the implementation

**Fresh evidence needs a network budget.** The first live requested-evidence probe ran out of its two-second limit before collecting the plan. Opening the connection took 0.858 seconds; each subsequent small request took about 0.15 seconds. The fix combines transaction setup and locks, reuses facts already proved by exact identity checks, and batches measured observations after validation. It preserves the same read-only mode, locks, checks and deadline. Local PostgreSQL tests with added network delay passed. The live rerun also passed: initial plan/index collection took 3.537 s across two bounded calls; Claude requested fresh ANALYZE evidence, which Go collected in 1.761 s. Two real Sonnet calls took 6.139 s, and the investigation including the requested read took 8.338 s. This separate probe applied no repair and confirmed unchanged table identity and statistics, plus owned cleanup. Its total test runtime was 13.16 s, including setup and checks; it is not a queued-repair timing. Other families' requested-reader timing remains separate coverage from their successful repair runs using initial evidence.

**A scheduled repair needs enough time to observe scheduled results.** The first diagnostic Cron run made one real Sonnet decision, changed the schedule, preserved the verification child across a consumer-process restart and recorded all six periods. Its 20-minute campaign timer expired just before the child could run. Cleanup succeeded, but the final saved verification result was missing, so the run is incomplete. The corrected campaign cap is 24 minutes, covering clock alignment, controls and six real two-minute periods. The production worker deadlines, model limits, benefit checks and three-minute cleanup allowance are unchanged. A regression reproduces the old cutoff. The corrected full live run is pending.
