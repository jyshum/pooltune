# Richer planner statistics in the hosted repair queue

A later hosted run exercised this repair with a real Claude decision. Its timings, controls and results are recorded in [Live Claude repairs](live-repairs.md).

Ordinary `ANALYZE` tells PostgreSQL how common values are in each column. A query that filters on two related columns can still get a bad estimate. In this fixture, each filter value is common on its own, while the two selected pairs are rare. Pooltune checks both pairs after a recent ordinary analysis, then creates one most-common-values (MCV) statistics object to describe those combinations. [PostgreSQL CREATE STATISTICS](https://www.postgresql.org/docs/17/sql-createstatistics.html)

## Design choices and trade-offs

- **Use two measured inputs.** The fixture returns 100 rows for `(1, 'b')` and 300 for `(2, 'a')`. Both need at least a tenfold estimate error after a recent ordinary analysis. Under the fixture locks, Go verifies the exact synthetic distribution it just analyzed. This gives the lab a direct freshness check instead of relying on PostgreSQL’s approximate modification counter. Measuring both cases costs extra work and helps catch changes that improve one input while harming the other.
- **Add one joint summary.** The existing index stays in place. One Go-controlled `CREATE STATISTICS` and a refresh of the two named columns populate the summary. The queue uses the shared LangGraph and a deterministic decision. Raising a single-column statistics target would give PostgreSQL more detail about each column individually. The information missing here is how often the values occur together.
- **Measure the scope of the change.** The result includes the row-count error, target read latency for each input, control reads and rolled-back updates, and the stored MCV payload size. The same wall-clock boundaries measure ordinary and extended `ANALYZE`. A repeatable maintenance-cost conclusion requires at least two comparable observations, per the design.
- **Check for a regression before retaining the object.** The staging action creates and verifies it in a bounded transaction. The fixture locks allow reads and exclude concurrent writes during comparison. That makes the samples easier to interpret and limits this design to an isolated fixture until we measure write interference. If a guard detects a regression, Go removes the statistics object it just created. The exact old ordinary sample has no guaranteed restoration. Recovery after interruption inspects the marked object and records uncertainty without repeating the action.
- **Hold planner settings constant for the experiment.** The initial Rookery attempt used the default random-page cost of 1.1 and already chose the index. With no slow job, the fixture was cleaned up. The qualifying workload uses 5 for this setting, with bitmap scans and parallel workers disabled in the staged query sessions. Both sides use those same settings. This represents one planner configuration and leaves project-wide settings as they were.

## One Railway run on Rookery

The driver sent 200 pairs of reads to private synthetic tables with 100,000 rows each. The Railway detector queued job 38 for the slow lookup. The healthy indexed lookup recorded `no_change` and opened zero jobs. The consumer saved `improved_tested_inputs` on its first attempt with three LangGraph checkpoints.

| PostgreSQL measurement | Before | After |
| --- | ---: | ---: |
| Estimated rows for input A, 100 actual | 24,912 | 113 |
| Estimated rows for input B, 300 actual | 25,086 | 280 |
| Mean target read for A | 13.709 ms | 0.093 ms |
| Mean target read for B | 13.797 ms | 0.207 ms |
| Shared-page accesses per read for A | 541 | 102 |
| Shared-page accesses per read for B | 541 | 303 |
| Healthy read mean, A / B phases | 0.031 / 0.036 ms | 0.020 / 0.020 ms |
| Key-changing update mean, A / B phases | 0.169 / 0.078 ms | 0.065 / 0.063 ms |

Both inputs went from scanning 100,000 rows to using the existing index. Each input and phase measured ten target reads, ten healthy reads and ten rolled-back updates. The results matched the same expected IDs. Ordinary analysis took 195.958 ms. Analysis with the joint summary took 206.990 ms, about 11 ms more in this one comparison. The summary payload used 172 bytes. Catalog and statement-counter checks confirmed that both existing indexes and the healthy table’s analysis timestamp stayed the same and that the repair ran exactly one additional column-scoped `ANALYZE`.

| Time from first traffic to saved result | Duration |
| --- | ---: |
| Detector collected five windows and queued the job | 20.661 s |
| Queue to saved result | 10.998 s |
| **Total** | **31.659 s** |
| Baseline measurements within queue processing | 2.754 s |
| Shared LangGraph decision within queue processing | 0.389 s |
| Apply and verify transaction within queue processing | 3.827 s |
| Other queue wait, connection, guards and state writes | 4.028 s |

The apply and verify transaction included 0.316 s to create and mark the statistics object, 0.207 s to analyze, and 1.092 s to measure after the change. Transaction setup, identity checks and commit account for the remaining time. The detector polled every 2 seconds and used five windows of at least 4 seconds with a 3 ms objective. The graph used the scripted decision and made no Claude call.

An earlier attempt stopped on a PostgreSQL lock conflict before recording an action. The qualifying run kept the same lock guard. We removed the fixture afterward and verified the fingerprint of all 12 public indexes was unchanged. This run establishes the repair mechanism and speedup on these two inputs. Customer qualification still needs representative inputs, live workload protection and repeated maintenance-cost measurements.

## Reproduce and inspect

Use the private staging route linked below. Create the fixture with `pooltune-query-lab -target rookery -create-consumer-fixture -statistics-fixture`. It returns the two enrolled statements and the ordinary analysis time. Run the scripted consumer with the exact project and fixture and `-statistics-staging-fixture`. In the traffic session explicitly set `enable_bitmapscan=off`, `random_page_cost=5` and `max_parallel_workers_per_gather=0` before sending paired, parameterized reads. Verify the candidate begins with a table scan and that both inputs return their expected rows. Stop the temporary processes and remove the fixture with `pooltune-query-lab -target rookery -remove-consumer-fixture <fixture-id>`. [Private staging route](queued-index-repair.md#how-to-reproduce)
