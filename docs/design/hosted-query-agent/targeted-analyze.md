# Targeted ANALYZE in the hosted repair queue

PostgreSQL uses summaries of its data to choose how to run a query. In this test an index already existed, but the summary said the lookup value was in nearly every row. A table scan made sense for that old distribution. The current data contained just one match. Pooltune detected the slow query, measured the planner's mistake, and refreshed the statistics for the affected column.

## Design trade-offs

- **Measured row error before changing statistics.** The V1 design permits evidence of a material row estimate error or enough modifications since analysis. This slice implements the measured error path for one enrolled lookup. It requires fresh detector windows and a fully executed plan with at least a tenfold row error. A measured plan makes the cause visible and costs extra executions. The bounded baseline also supplies the before readings for verification.
- **Use the existing index.** The repair uses table and column coordinates checked by Go and sends one column-scoped `ANALYZE`. It leaves the healthy table and the existing indexes alone. Taking a database-wide or table-wide sample would cover more possible causes and do extra work without helping this enrolled lookup. [PostgreSQL ANALYZE](https://www.postgresql.org/docs/17/sql-analyze.html)
- **Use the shared graph for the decision.** This case uses the existing LangGraph with a deterministic recommendation for the measured error. The index model path remains available. Go checks the recommendation, current fixture identities, ownership, limits, and the expected index. There is no model-generated SQL.
- **Measure the mechanism and the benefit.** Ten matched runs on either side cover the target lookup, an indexed healthy read, and an update of a non-indexed payload that is rolled back. The result requires both a better row estimate and a meaningful latency improvement, with no sampled write or read regression. The samples cover one lookup input. A payload-only update may use PostgreSQL’s cheaper update path. Committed writes, key-changing writes, and production concurrency still need qualification.
- **Treat interruptions as uncertain.** The action journal records intent before PostgreSQL refreshes the statistics. A replacement worker reuses a saved verified result. If the action is pending, the queue records that the statistics may have changed and does not automatically repeat it. PostgreSQL has no ordinary command to restore the exact previous sample.

## One Railway run on Rookery

The driver sent 210 pairs of reads to two private synthetic tables, each with 100,000 rows. The candidate had an existing index. Its statistics had been collected when every row had the same lookup value, then the fixture data was replaced with distinct values. The healthy indexed control recorded `no_change` and created zero jobs. The candidate created job 27 and finished on its first attempt with three graph checkpoints.

| PostgreSQL measurement | Before | After |
| --- | ---: | ---: |
| Planner estimate for the target lookup | 106,720 rows | 1 row |
| Actual rows returned | 1 | 1 |
| Rows examined | 100,000 | 1 |
| Shared-page accesses per lookup | 1,334 | 3 |
| Target lookup mean | 10.709 ms | 0.023 ms |
| Healthy lookup mean | 0.036 ms | 0.018 ms |
| Payload update mean | 0.248 ms | 0.050 ms |

The plan switched from a table scan to the same existing index. Each phase measured ten target reads, ten control reads, and ten rolled-back payload updates. The recorded outcome was `improved_tested_inputs`. A separate catalog check confirmed the index identities and the healthy table's last-analyzed value were unchanged. The statement counters recorded exactly one additional column-scoped `ANALYZE` after fixture setup. The test fixture was removed afterward and the 12 public index definitions matched their pre-run fingerprint.

| One timeline | Time |
| --- | ---: |
| Traffic start to queued job | 20.913 s |
| Queue to saved result | 8.332 s |
| **Traffic start to saved result** | **29.245 s** |
| Baseline within queue processing | 1.479 s |
| Shared graph within queue processing | 0.482 s |
| Refresh transaction within queue processing | 1.186 s |
| Verification within queue processing | 1.308 s |
| Other queue wait, database calls, guards and state writes | 3.877 s |

The detector used a 3 ms target, sampled every 2 seconds and required five windows of at least 4 seconds. Those are staging settings. The refresh time includes its database round trips and its transaction. The graph used a fixed decision, so this run has no Claude network time. The short sequential test establishes benefit for the chosen input.

## Reproduce and inspect

Use the same private staging route as the [queued index run](queued-index-repair.md#how-to-reproduce). Create the fixture with `pooltune-query-lab -target rookery -create-consumer-fixture -analyze-fixture`. Enroll the resulting statement IDs in the detector and start the scripted consumer with `-analyze-staging-fixture`, the exact returned project and fixture ID, and the pinned Rookery lab connection. Use bound reads with `FROM ONLY`, lookup value 43, and both returned table names to produce the same traffic mix. Stop the temporary processes and remove the fixture with `pooltune-query-lab -target rookery -remove-consumer-fixture <fixture-id>`.

The end-to-end sample is from the staged build used for job 27. After the run, the code added a returned-ID check and a final plan check under the action lock. Local PostgreSQL tests verified those checks, confirmed that an interrupted pending action cannot trigger a second refresh, and confirmed that a concurrent statistics refresh invalidates the original proposal. The final report schema omits fields for measurements this comparison does not collect. The timing above comes from the single requested hosted run.
