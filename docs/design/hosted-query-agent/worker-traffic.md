# Background worker traffic test

The traffic driver plays the part of an application. It asks PostgreSQL for specific records at a steady pace. The worker runs on a separate database connection and reads the counters PostgreSQL already keeps for those requests. Go compares the averages with each enrolled query's target and saves an investigation when enough windows breach it.

## What we tested

We ran the same test on local PostgreSQL and on Rookery, the Supabase test project. Each run seeded two private tables with 100,000 synthetic rows each. The driver looked up the same records on both tables. The healthy table had an index on the lookup column, so PostgreSQL could go straight to the matching row. The other table required a scan. Both queries had a **1 ms mean execution target** for this test.

One driver connection sent up to one pair of reads every 100 ms for seven seconds. The worker checked the counters every 500 ms and used one-second windows to exercise the same decision rule as the planned longer intervals. It waited for five comparable windows and required three breaches. The fixture stayed inside a transaction and rolled back at the end. An independent check confirmed the test schema was gone and the public index definitions matched their pre-test fingerprint.

## Results

All six runs completed 70 reads per query. The indexed query created **zero jobs**, and the unindexed query created **one job** per run. Each job was read back through a fresh connection to Pooltune's local PostgreSQL.

| Run | Indexed database mean (ms) | Unindexed database mean (ms) | Client p95 indexed / unindexed (ms) | Target snapshot / full poll p95 (ms) | First saved job |
| --- | ---: | ---: | ---: | ---: | ---: |
| Local 1 | 0.040 | 5.317 | 0.85 / 7.09 | 0.64 / 3.93 | 5.005 s |
| Local 2 | 0.041 | 5.132 | 0.92 / 6.89 | 0.63 / 3.47 | 5.005 s |
| Local 3 | 0.051 | 5.725 | 0.89 / 7.31 | 0.86 / 4.13 | 5.005 s |
| Rookery 1 | 0.087 | 10.536 | 79.66 / 84.06 | 75.68 / 77.08 | 5.024 s |
| Rookery 2 | 0.064 | 10.787 | 79.80 / 84.34 | 81.03 / 82.81 | 5.028 s |
| Rookery 3 | 0.726 | 10.691 | 79.84 / 94.58 | 80.15 / 82.85 | 5.045 s |

The investigation time starts when the driver begins sending requests. The unindexed lookup was already slow at that point. The measurement shows how quickly a new monitor can gather evidence and save its first job. Query execution time comes from PostgreSQL. Client p95 is the time within which 95% of the driver's reads completed, including the network. Worker poll p95 includes reading the counters and updating Pooltune's state. A comparison with the observer switched off would be needed to measure how much latency the observer itself adds to requests.

## Engineering decisions

- **Observe normal reads.** Reading existing counters in one query per tick limits extra work on the target. Replaying queries would give more control over inputs and could generate extra load or require wider permissions. A counter average tells us when a query needs attention. It takes query-plan and index evidence to explain why.
- **Wait for repeated evidence.** Faster sampling can surface a problem sooner and increases database reads. A gap or reset discards the incomplete comparison. Healthy readings and one-off spikes do not open investigations. The command's v0 defaults are one-minute polling and five-minute windows. The accelerated test establishes correctness at the test cadence. Real workloads still need calibration.
- **Save the evidence and job together.** Pooltune's PostgreSQL makes the two writes atomic and keeps one open investigation per enrolled query even when two workers overlap. A separate queue could scale job delivery independently. It would add another service and a handoff to keep consistent. This trial proves the PostgreSQL producer with a local state database.

## Reproduce

From the repository root, with the PostgreSQL 17 binaries on PATH:

```sh
go test -tags=integration -count=1 -v ./internal/hostedquery -run '^TestWorker'
```

The integration tests start disposable databases and check real traffic, restart, simultaneous polling, and duplicate suppression. To print timing JSON using the lab command, provide separate target and local state connections through `POOLTUNE_LAB_TARGET_DSN` and `POOLTUNE_LAB_STATE_DSN`. The target must already have `pg_stat_statements` enabled. Use the verified session-pooler connection and `POOLTUNE_LAB_ROOKERY_CA_FILE` for Rookery. Use `-target local` for a disposable loopback target. [PostgreSQL statement statistics](https://www.postgresql.org/docs/17/pgstatstatements.html) · [Supabase SSL configuration](https://supabase.com/docs/guides/platform/ssl-enforcement)

```sh
go run ./cmd/pooltune-query-lab -target rookery -worker-traffic
```

The standalone observer is `go run ./cmd/pooltune-worker -target local -enrollment enrollment.json`. It uses `POOLTUNE_WORKER_TARGET_DSN` and `POOLTUNE_WORKER_STATE_DSN`. Enrollment identifies one authorized project, a database and role by PostgreSQL ID, each statement by query ID, and its mean execution target in milliseconds. Use a string for the signed 64-bit query ID.

```json
{
  "project": "local-demo",
  "queries": [
    {
      "database_oid": 16384,
      "role_oid": 10,
      "query_id": "-842203731215680091",
      "mean_execution_ms": 1
    }
  ]
}
```

Replace the example IDs with those for your own disposable target. The command stops on a connection or polling error. A supervisor can restart it against saved state. A gap beyond the sampling tolerance starts a fresh evidence window.

The next step is a queue consumer that gathers a measured plan and the existing indexes for LangGraph. The worker implemented here ends at a saved investigation. Studio will show that later graph run once the handoff is connected.
