# Always-on investigation consumer

The consumer is a separate Go process. When it has no work, it checks Pooltune's own PostgreSQL queue every two seconds. It connects to the monitored database only after claiming an enrolled job. The detector can keep sampling on its own schedule while the consumer is gathering evidence or waiting for Claude.

## Restart test

The test started the real consumer binary and killed it after it claimed a job, with evidence collection held open by a database lock. A replacement process started with the same settings. It respected the original 30-second claim and completed the same job on attempt two after that claim expired.

The test added an index to its own synthetic table while the process was offline. The replacement measured an index scan, saw the existing index, and saved `no_change`. The graph's evidence check stopped before the model. The test replaces the process explicitly. A deployment host would handle starting a replacement and stopping any child Node processes. A crash during the model request could result in a repeated call.

## What Rookery measured

We ran three matched pairs of traffic phases on the verified Rookery session connection. Each phase used the same two 100,000-row synthetic tables and sent 28 indexed and 28 unindexed lookups, targeting one pair every 150 ms. The order was off/on, on/off, off/on to vary which condition came first. In each on phase, one queued job prompted one extra measured scan. The detector was outside this comparison. The investigator was scripted so the test could focus on database impact.

Across two rounds of 336 foreground reads each, the active phases each added one measured scan. It took **9.857–11.557 ms** and touched **1,334 shared buffer blocks** each time. The paired change in client median ranged from **−0.209 to +0.852 ms** for the unindexed lookup and **−0.445 to +1.785 ms** for the indexed lookup. PostgreSQL counters confirmed exactly 28 foreground executions of each lookup in every phase.

Final round after the queue coordination fix:

| Phase | Consumer | Indexed client p50 / p95 | Unindexed client p50 / p95 | PostgreSQL mean indexed / unindexed | Extra measured scan |
| --- | --- | ---: | ---: | ---: | ---: |
| 1 | off | 18.052 / 82.281 ms | 26.976 / 36.500 ms | 0.042 / 10.151 ms | 0.000 ms |
| 2 | on | 17.607 / 92.139 ms | 27.828 / 84.052 ms | 0.024 / 10.169 ms | 11.557 ms |
| 3 | on | 18.830 / 60.736 ms | 27.626 / 101.136 ms | 0.024 / 10.032 ms | 9.885 ms |
| 4 | off | 17.045 / 85.812 ms | 27.162 / 42.546 ms | 0.024 / 9.945 ms | 0.000 ms |
| 5 | off | 16.679 / 92.223 ms | 27.682 / 79.248 ms | 0.024 / 9.919 ms | 0.000 ms |
| 6 | on | 17.546 / 81.099 ms | 27.571 / 95.260 ms | 0.024 / 9.924 ms | 9.901 ms |

These are short, low-concurrency trials with one job per active phase. Client timings include the network and varied much more than the server's measured lookup time. They cannot establish a reliable p95 penalty or a production overhead ceiling. The extra plan scan time measures execution inside PostgreSQL. The catalog checks, connection setup, and network also contribute to the consumer's elapsed time. Buffer accesses are recorded separately from physical reads or CPU time.

The benchmark verified that the test schema was removed and the public index fingerprint matched. It compared only the test statements' counters. It did not read application rows or run the repair loop.

## Trade-offs

| Choice | Benefit | Cost |
| --- | --- | --- |
| Separate detector and consumer | A slow diagnosis leaves the sampling schedule free to continue. | Hosting runs two processes and needs a supervisor to replace either after a crash. |
| Poll Pooltune's queue | An idle consumer generates no connections or statements on Rookery. A durable queue survives process loss. | An idle poll adds up to one interval before a fresh job starts. Notifications could reduce that delay and the number of idle checks. The queue would still need a way to recover expired claims. |
| One active job per project | Overlapping consumers cannot pile diagnostic scans onto one target. The state lock is held only while choosing or finishing jobs. | A slow job or an unexpired claim can delay the next job for that project. If a model request finishes after a process crash, retrying can repeat a billable call. |
| One fresh target connection per investigation | Verification uses current catalog and statement data, and idle time consumes no target session. | Connecting adds network latency. A small warm pool would trade idle connections for faster investigation startup. |
| Measure only the journaled fixture lookup | The consumer matches the original objects, statement, role and counter generation before executing a bounded read. It uses one measured plan and sends a structured summary. | This adapter handles the synthetic lookup. Customer enrollment, least-privilege roles and representative plans still need qualification. |

## Reproduce

Use Go 1.27.3 or newer, PostgreSQL 17 binaries on PATH, and the investigator's locked Node dependencies installed.

```sh
go test -tags=integration -count=1 -v ./cmd/pooltune-consumer -run TestConsumerRestartsAndCollectsFreshEvidence
```

For the Rookery benchmark, provide `POOLTUNE_BENCH_TARGET_DSN` and `POOLTUNE_LAB_ROOKERY_CA_FILE` privately, using the previously verified Rookery session route. [Supabase SSL configuration](https://supabase.com/docs/guides/platform/ssl-enforcement)

```sh
go test -tags=integration -count=1 -v ./cmd/pooltune-consumer -run TestConsumerRookeryLoad
```

For a manual run, use a persistent, separate local state database. Set `POOLTUNE_LAB_TARGET_DSN` and `POOLTUNE_LAB_STATE_DSN` as described in the [traffic test](worker-traffic.md). Use the same values for the worker and consumer through `POOLTUNE_WORKER_TARGET_DSN` and `POOLTUNE_WORKER_STATE_DSN`.

1. Run `go run ./cmd/pooltune-query-lab -target local -create-consumer-fixture` and save the fixture ID and enrollment object from its JSON.
2. Start the [detector](worker-traffic.md#reproduce) using that enrollment and start the consumer with the same project and fixture.
3. When finished, stop both processes and run `go run ./cmd/pooltune-query-lab -target local -remove-consumer-fixture <fixture-id>`. Keep the state journal until cleanup succeeds. The automated benchmark cleans up on normal exit. A hard kill of the entire test harness can strand the synthetic schema if its temporary state directory is later discarded.

```sh
go run ./cmd/pooltune-consumer -target local -project <enrollment-project> -fixture <fixture-id>
```
