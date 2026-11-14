# One queue-driven index repair

The earlier work established two separate paths. The Railway detector and consumer could save an index recommendation. The one-shot lab command could build and verify an index from Railway and recover an interrupted build on a separate fixture. This slice connects those pieces so one job follows one physical Rookery fixture through detection, recommendation, build, and verification. [Hosted recommendation](measured-lookup-staging.md) · [Hosted repair and interruption drill](hosted-index-repair.md)

The detector reads normal statement counters. For the slow lookup, the consumer uses the same matched read and write baseline to make the recommendation and verify the repair. Go checks the recommendation against the journaled fixture and builds the index. The queue result stores the graph thread, the action report, and the outcome together. The healthy indexed lookup acts as a control. Recovery uses the same ownership checks and inspects only the interrupted queued fixture. A retry can reuse the journaled result if the repair already finished.

## One Railway timeline

The final run sent 210 pairs of lookups to two 100,000-row synthetic Rookery tables. The detector recorded `no_change` for the healthy lookup at 0.047 ms in the decision window and created exactly one job for the unindexed lookup. Job 23 saved the verified repair on its first attempt with four graph checkpoints.

| Elapsed step | Time |
| --- | ---: |
| Traffic start to queue | 20.540 s |
| Queue to verified result | 14.845 s |
| Traffic start to verified result | **35.385 s** |
| Baseline measurement inside queue processing | 3.879 s |
| Scripted LangGraph run inside queue processing | 0.415 s |
| Concurrent index build inside queue processing | 1.569 s |
| Follow-up measurement inside queue processing | 3.613 s |

Queue processing also includes waiting for the consumer, database connections, permission and identity checks, and saving the result. The substeps are already included in the end-to-end time. The earlier combined run finished in 36.041 s. Two staging runs can identify likely latency targets. They do not establish a production latency percentile.

| Mean PostgreSQL execution time | Before | After |
| --- | ---: | ---: |
| Target lookup | 10.489 ms | 0.026 ms |
| Healthy lookup | 0.032 ms | 0.022 ms |
| Value-changing update | 0.063 ms | 0.059 ms |

Each phase measured 40 of each operation with zero errors. The target scanned 100,000 rows before the change and used the index to examine one row afterward. Shared-page accesses fell from 1,334 to 3. The writes stayed inside the fixture regression guard. The valid 2,260,992-byte index was independently visible before cleanup. The fixture was removed and all 12 public index definitions matched their pre-run fingerprint.

## How to reproduce

In the private staging container, create a journaled fixture with `pooltune-query-lab -target rookery -create-consumer-fixture`. Start a temporary detector with that enrollment and a temporary consumer with `-target rookery -project <enrolled-project> -fixture <fixture-id> -mode scripted -repair-staging-fixture`. Use the secured Rookery lab connection and Railway state, as in the [one-shot runbook](hosted-index-repair.md#reproduce-from-staging).

Run paired parameterized candidate and healthy reads long enough to fill five windows. The driver uses the fixture's two query IDs. This test used a 3 ms detector objective, a 2-second poll, and windows of at least 4 seconds. The repair comparison uses the existing 1 ms fixture threshold. Read the terminal queue row and check the graph thread and action journal agree with it. Stop the temporary processes, then remove the fixture with `pooltune-query-lab -target rookery -remove-consumer-fixture <fixture-id>`. Verify the public index definitions match their starting state.

## Trade-offs

- **One report with timings that can be compared.** Traffic-to-queue, queue-to-result, and traffic-to-result use the same run. The baseline, graph, build, and verification timings are nested within queue-to-result. PostgreSQL's per-query execution times measure database work and are a different unit of comparison.
- **One set of measured reads for diagnosis and verification.** The baseline provides the plan for the recommendation as well as the before sample. That avoids a separate diagnosis read or graph replay. Measuring value-changing writes adds work. The samples run on two journaled synthetic tables only, and the write transactions roll back.
- **An explicitly enabled Railway lab mode.** The live check starts temporary detector and consumer processes from the staging image. The repair consumer requires the pinned Rookery connection, private Railway state, a scripted graph, and one journaled fixture. The regular staging workers continue their normal roles. The owner login used here leaves customer repair credentials and grants for separate qualification.
- **A distinct journal state for queued repair.** A standalone lab recovery should leave queued repairs alone. A queue retry can restore the outcome of a verified repair. If a build was interrupted, recovery checks the catalog and removes only that fixture. It does not repeat the build. If the recovery attempt also fails and exhausts the queue retry budget, an operator can use the lab’s fixture-specific removal command to inspect and clean up the pending fixture. The shared cleanup path refuses a fixture with inheritance links. Concurrent DDL assumes exclusive operator control of this private schema, as in the original lab.
