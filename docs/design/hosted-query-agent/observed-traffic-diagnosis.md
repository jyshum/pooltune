# Reusing measured traffic in an investigation

The detector already measures how long normal queries take. The consumer now keeps those measurements next to the estimated plan. This gives an investigation two different pieces of evidence. The timings establish whether the lookup repeatedly missed its target. The plan shows how PostgreSQL currently expects to find the row. It takes measured plan work to establish whether that scan is the reason for the slowdown.

## Design choices

- **Reuse the traffic we already measured.** The detector saves the number of completed calls and their mean execution time for each of the five decision windows. The consumer reads that data with the job and adds no database queries for the latency summary. This avoids running a separate benchmark just to confirm the slowdown. It also means the summary describes the mix of calls we observed, including any variation in query inputs.
- **Keep recent observations separate from forecasts.** We chose a ten-minute limit between the last sampled window and the plan observation for this slice. This gives the queue room to retry while keeping an old traffic burst separate from a current plan. The other four windows belong to the same continuous detector sequence. The limit is an initial operating choice. It still needs tuning under real queue delays and workload patterns.
- **Give each stopped investigation a reason.** The saved result can report that a matching index already exists, recent measured latency is missing, the estimated plan did not show a sequential scan, or measured plan work is still needed. Older queued jobs can still produce an estimate. They have no per-window sample counts, so they keep their original queue evidence and do not receive the new summary.
- **Use the existing repair evidence contract.** The enrolled path stops before the index investigator. It would take a measured plan to show how many rows and blocks the scan actually touched. Real workload counters can help narrow that next step. A fresh query execution is justified only when existing evidence leaves a question that changes the decision. [Evidence reuse policy](../../product/query-performance-v1.md#evidence-reuse-and-investigation-cost)

## Railway staging results

The staging detector opened job 12 from 240 pairs of parameterized reads on the two 100,000-row Rookery fixtures. Both lookups used the 1 ms target and the dedicated test login. Including warmup, PostgreSQL counted 248 executions per lookup, exactly the number the driver issued.

| Measure | Result |
| --- | --- |
| Slow lookup, all calls | 7.811 ms mean |
| Indexed lookup, all calls | 0.456 ms mean. The detector recorded `no_change` and created no job. |
| Five slow-query windows copied into job 12 | 8.943, 8.787, 7.436, 7.445, and 7.532 ms. The windows represented 10, 20, 20, 21, and 20 calls, for 91 total. |
| Time from traffic start to queue | 17.953 seconds, using the short staging windows |
| Time from queue to saved result | 2.691 seconds on the first attempt |
| Saved diagnosis | `missing_measured_plan`, with an estimated sequential scan and no matching index |

The saved window means and counts matched the original queue record exactly. The consumer used its existing plan and catalog checks. The run did not invoke the model, run a measured plan, or change an index on the target. Railway returned to its normal measured fixture configuration. The temporary credential and schema were removed and the public index fingerprint was unchanged.

## Verification

The local integration test writes five detector windows to a real PostgreSQL queue. It verifies one job for the 8–12 ms lookup and zero for the healthy 0.25 ms lookup, with a 1 ms target. A fresh result kept all five windows in order, with 8, 16, 24, 32, and 40 calls. The test also checks the matching-index, stale-data, older-job, missing-mean, and alternate-plan paths. The [real PostgreSQL estimate test](../../../internal/hostedquery/enrolled_evidence_integration_test.go) also checks that the lookup execution count stays at one. The race-enabled Go suite, Go vet, investigator tests, TypeScript checks, and launcher syntax passed.

These results are enough to move on to qualifying measured plan evidence. The elapsed times show the staging handoff worked on this run. They are not a production speed baseline or evidence that an index will improve the query. For the next step, qualify a source of actual plan work for the enrolled query. The product design favors using an existing measurement when it matches the input, query identity, and database context.
