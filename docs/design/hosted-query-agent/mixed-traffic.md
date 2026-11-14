# Mixed traffic through the three live repairs

This qualification asks whether Pooltune improves a sustained problem while ordinary work continues. The earlier runs proved individual operations. This workload adds overlapping database sessions, a brief slowdown that should be ignored, and a monitoring cost comparison. It uses disposable synthetic Rookery tables from Railway staging, not customer application traffic.

## Hosted results

Final deployment: `277646ed-8ab9-4411-8067-0c176c1097ef`. Each row below measures the sustained phase after established healthy history. Setup, seed loading, monitoring comparisons and spike testing are excluded from the detection-to-result total. Claude time is included in the queue-to-result column, not added again.

| Repair | Job | Detect | Claude | Queue to verified result | Total | Mean database execution, before → after |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Index | 63 | 12.479 s | 2.028 s | 16.345 s | 28.824 s | 8.832 → 0.024 ms |
| Targeted ANALYZE | 68 | 12.809 s | 2.904 s | 12.225 s | 25.034 s | 10.303 → 0.025 ms |
| Joint statistics | 72 | 8.911 s | 4.160 s | 15.764 s | 24.675 s | 16.712 → 0.276 ms |

The database before value is the mean from the first ten seconds of sustained traffic, before any repair is applied. The after value is the mean from the twelve-second phase after the saved result is observed. The driver checks for completion every ten seconds, so traffic also continues between actual completion and that observation. Client response percentiles are separate measurements.

| Where queue-to-result time went | Index | ANALYZE | Joint statistics |
| --- | ---: | ---: | ---: |
| Baseline evidence and samples | 3.661 s | 1.618 s | 2.697 s |
| Graph including Claude | 2.471 s | 3.297 s | 4.744 s |
| Apply repair | 1.604 s | 1.477 s | Combined below |
| Verify repaired outcome | 3.438 s | 1.580 s | Combined below |
| Apply and verify where measured together | — | — | 3.868 s |
| Remaining claim wait, connection setup, checks and state writes | 5.170 s | 4.253 s | 4.455 s |
| Queue to saved result | 16.345 s | 12.225 s | 15.764 s |

The remaining-time row is a subtraction from the observed queue-to-result interval, not independent tracing of each listed activity. Claude's request is included within graph time. The repair workflow is background work, so application requests continue rather than waiting for the full investigation to finish.

The corrected index run completed all 1,974 offered requests with zero errors or missed requests. Its three-second spike averaged 13.542 ms in PostgreSQL and created zero jobs. The sustained case created exactly one job, completed on its first attempt and used one real Claude request. Cleanup removed the fixture and preserved the twelve public indexes. [Index report](evidence/mixed-traffic/final-index.json)

The corrected ANALYZE run also completed all 1,974 offered requests without errors or missed slots. Its spike averaged 11.142 ms and created zero jobs. One sustained job completed on its first attempt with one Claude request. Cleanup and public-index checks passed. [ANALYZE report](evidence/mixed-traffic/final-analyze.json)

Joint statistics completed another 1,974 offered requests, with zero errors or missed slots. Its spike averaged 13.699 ms and created zero jobs. Job 72 completed on its first attempt using one Claude request and retained the joint summary after verification. Its ongoing read mean combines the two alternating inputs, not a guarantee for every possible parameter. Fixture cleanup and public-index checks passed. [Joint-statistics report](evidence/mixed-traffic/final-statistics.json)

Across the final runs, 5,922 requests completed successfully. Each healthy query produced no investigation, and each sustained target produced exactly one. All three proposals matched the admitted repair and were applied by Go. These remain prequalified fixture causes, not unrestricted diagnosis across all possible causes.

The index monitoring comparisons measured twelve collector queries over 24 seconds, totaling 36.944 ms of PostgreSQL execution time. That is about 1.54 ms of collector execution per elapsed second. Target request p95 was 149.88/150.09 ms with monitoring off and 150.45/149.79 ms with it on. These short comparisons show no consistent latency increase. They do not establish a precise overhead percentage or include all process startup and Pooltune-state work.

The ANALYZE comparison recorded twelve collector queries totaling 39.952 ms over 24 seconds, about 1.66 ms per elapsed second. Target p95 was 151.46/151.22 ms with monitoring off and 151.43/151.58 ms with it on.

The joint-statistics comparison recorded twelve collector queries totaling 39.353 ms over 24 seconds, about 1.64 ms per elapsed second. Target p95 was 144.01/144.30 ms off and 144.37/144.27 ms on. Across all three experiments, steady collection used about 1.54–1.66 ms of measured database execution per elapsed second. This is not a CPU-utilization percentage.

In this deployment, the client round trip remains around 150 ms even when target database execution is below 0.03 ms. The service configuration places Railway in Europe while the pinned Rookery route is US West. The measurements show that database speed and end-to-end request speed are different. Network distance is a likely contributor, but this run did not isolate network time. Moving staging closer to Rookery is a separate deployment comparison, not a change made by this test.

## Workload and measurements

Four independent sessions offer eight target reads, four healthy reads and two committed, value-changing writes per second. Joint-statistics reads alternate between the two enrolled input combinations. The other repairs use the single qualified lookup. Writes change ten rows in the healthy table. The repair's own before/after samples also measure rolled-back candidate updates. Concurrent committed candidate writes remain unqualified because the statistics fixture requires its exact distribution to remain unchanged.

The driver records offered requests, successes, errors, missed scheduled requests and response time from scheduled start through completion. A late request cannot silently lower the offered rate. Failed requests remain in latency measurements. Database statement counters separately measure execution time and logical page accesses. These are not CPU, memory, physical disk I/O or billing measurements.

Two monitoring-off phases surround two monitoring-on phases, each twelve seconds long. This reverses comparison order to help reveal warmup and time trends. A healthy-history phase runs for 24 seconds, followed by a three-second slowdown and 24 seconds of recovery. The sustained phase then runs until a result is saved, up to sixty seconds. Traffic continues for twelve seconds after the result.

The enrolled database objective is 3 ms. The detector polls every two seconds and requires at least three breaches in its latest five comparable four-second windows. Healthy history already exists before the sustained fault. That lets three slow windows trigger a job in 8.911–12.809 seconds here, depending on where the fault falls within a window. A partly slow window can exceed the objective. Earlier cold-start runs needed about twenty-one seconds to fill the initial five-window history. This is a different starting condition, not a claimed detector speedup. The spike must actually exceed that objective in PostgreSQL counters, with zero investigations afterward. The sustained case must create one investigation, make one real Claude request, produce a verified repair, and return ongoing database execution below the objective. Traffic failures or missed requests fail the overall qualification even when the repair itself works.

## Controlled faults and boundaries

For the index scenario, the driver creates its own baseline index, removes it briefly, restores it, then removes it for the sustained case. It tracks that baseline object's identity for early-failure cleanup. The Go repair owns any newly built repair index through its existing journal.

For the statistics scenarios, the fixture already contains stale or insufficient statistics. Session-only planner settings initially allow the existing index to provide the healthy baseline. Enabling sequential scans exposes the bad choice during the spike and sustained phase. This deliberately controlled transition tests detection and recovery. It does not reproduce a naturally evolving customer distribution.

All workload sessions disable bitmap scans and parallel execution and use random_page_cost=5. Comparisons use identical settings within each case. Other workloads and planner settings need their own qualification. The existing private staging identity is reused. Dedicated customer-role automatic repair remains separate work.

## Interference found and corrected

The first index run repaired its target but caused nine healthy-table writes to fail with lock timeout `55P03`. The first ANALYZE run caused six. The old verification sampler held a write-blocking lock on both the candidate and healthy tables. Its internal rolled-back update measurements did not reveal interference with independent sessions.

The shared sampling helper now freezes the candidate table as before but takes a weaker lock on the healthy control. That lock allows ordinary writes while still excluding table changes and competing index maintenance. A local concurrent regression test reproduced the healthy-write failure before the change. It then verified healthy writes succeed, candidate writes remain excluded, and healthy schema/index changes remain blocked for all three repair modes. Independent review found no remaining blocker in this narrow fix.

This is still fixture qualification. Writes to the candidate table can still be blocked by its unchanged protective lock. Production qualification needs representative concurrent candidate writes and a bounded evidence strategy that does not freeze customer work. Passing separate healthy-table traffic is not that qualification.

## Design trade-offs

Keeping evidence collection and repairs in the existing Go tools preserves identity checks, bounded operations and recovery behavior. The TypeScript driver only creates traffic and coordinates the test. Claude continues to receive the collected evidence in one request and returns a typed proposal. Go validates, applies and verifies it.

Short windows make this staging experiment quick. They are not a universal customer setting. Query frequency, the customer's target and the cost of a mistaken repair still determine production policy. Multiple sessions expose interference that a serial demonstration can hide. They do not establish maximum capacity or a service-level guarantee.

## Verification and incomplete attempts

The initial index and ANALYZE reports are retained under [evidence/mixed-traffic](evidence/mixed-traffic/). Both correctly report a failed mixed-traffic qualification despite the successful repair. The initial joint-statistics attempt stopped reporting after its first monitoring-off phase. Inspection found no remote test process or active traffic sessions. The owned leftover fixture was removed through Go and the twelve public indexes matched their original fingerprint. The interruption's cause was not established, and this attempt supplies no repair or latency result. Final runs use a detached, time-bounded runner with reports saved on Railway before retrieval. A later polling connection timed out during the final joint-statistics run, but the detached run continued and its complete successful report was retrieved without another model call.

The new concurrent test failed with `55P03` before the lock change and passed afterward. Focused repair integration tests, all eight TypeScript tests, TypeScript checks and Go vet passed. The first full race-enabled integration run failed `TestWorkerTrafficFromAnotherSession` with an idle-transaction timeout. That existing test passed alone, and the full suite passed on rerun. This transient is recorded rather than omitted. The narrow lock change and driver received independent review.

## Reproduce

Deploy the existing staging image with `investigator/traffic.ts`. Supply the existing private target, state, CA and Anthropic environment through the established staging runner. Run `node investigator/traffic.ts index`, `node investigator/traffic.ts analyze`, and `node investigator/traffic.ts statistics` sequentially. The CLI checks the Rookery route, creates a fresh fixture, runs temporary detector and consumer processes, stops them, removes the fixture and verifies that public indexes are unchanged. Save each JSON report. A failed process or failed report must not count as qualification.

## Next repair families

Bounded VACUUM, enrolled function memory and enrolled Cron scheduling must reuse the same evidence standard: a real Claude choice, sustained and brief-fault controls, traffic during the action, before/after benefit, interference, recovery and cleanup. Each needs a repair-specific fixture and guard. The current tests do not grant those three operations or establish their implementation.
