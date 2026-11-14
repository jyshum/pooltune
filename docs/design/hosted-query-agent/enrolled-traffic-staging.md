# Enrolled Rookery traffic in hosted staging

The Go detector samples PostgreSQL's statement counters in Rookery. When a query repeatedly misses its latency target, the detector writes an investigation to Pooltune's separate database. The Railway consumer claims the job, checks that it still has access, and saves the estimated plan and index check.

## What we tested

We used a temporary dedicated login and two tables with 100,000 synthetic rows each. Both queries read one row by an integer lookup. One table had a B-tree index on the lookup column. The other had no index. Both queries had a 1 ms mean execution target. The test driver ran ordinary parameterized SELECTs over Rookery's verified session pooler. Only the detector could create a job during this experiment.

The detector sampled every 2 seconds. A decision required five valid windows of at least 4 seconds each, with at least three above the target to investigate. The shorter windows exercised the normal rule quickly. The launcher's default configuration uses a 1-minute sampling interval and 5-minute windows.

## Results

Railway created exactly one investigation from traffic. Both the project and its queue started empty. We sent 240 paired reads in 48.316 seconds. Including eight warmup reads per lookup, Rookery counted exactly 248 executions of each query. The consumer did not add an execution.

| Lookup | Mean execution time across 248 reads | Last window at the decision | Detector decision | Jobs |
| --- | ---: | ---: | --- | ---: |
| Unindexed | 8.131 ms | 8.796 ms | `investigate` | 1 |
| Indexed | 0.319 ms | 0.446 ms | `no_change` | 0 |

All five unindexed decision windows averaged 8.733–8.877 ms. The indexed windows averaged 0.284–0.622 ms. The five-window sequence began just before the first read, and job 11 appeared **18.494 seconds after the driver started**. The consumer finished its first attempt **2.841 seconds after queue creation**, with `needs_measured_evidence`, an estimated sequential scan, and no covering index. There was no graph invocation, model call, or repair. The median Rookery counter read took 144.278 ms from Railway across 25 logged polls. The read time includes network time and does not measure database load.

Both Railway services returned to the measured fixture configuration with successful deployments. The temporary Rookery role and schema were removed, and the public index fingerprint matched its pre-test value. Job 11 remains in Pooltune staging as a test record.

The race-enabled Go suite with real local PostgreSQL, investigator tests, TypeScript checks, launcher syntax, and independent review all passed. The dedicated-role regression test covers accepting its own role and rejecting another role's enrollment.

## Design trade-offs

- **Prove the connection with a narrow login.** The detector sees counters for queries run by its own authenticated role and filters them to the explicit enrollment. The driver used that same role. Monitoring traffic from other application logins will need a separate access and isolation test.
- **Use counters for detection and a fresh plan for diagnosis.** Sampling reads PostgreSQL's existing counters instead of replaying monitored queries. The consumer asks the planner how it would run the lookup and checks current indexes and grants on a fresh connection. This adds database round trips when a job exists and keeps the foreground traffic independent.
- **Wait for repeated evidence.** Requiring several windows filters out one-off slow calls. The window length sets how quickly Pooltune responds. The staging run used steady synthetic traffic and short windows, so its elapsed time does not represent production detection speed.
- **Keep estimates separate from measured evidence.** This mode saves the sequential-scan estimate and index check as `needs_measured_evidence`. The shared investigator and automatic index action require the measured evidence defined in the design. [Architecture and remaining work](roadmap.md)

## Reproduce in staging

1. Use an isolated synthetic schema and temporary dedicated Rookery login. Give the login read access to the two fixture tables and access to the statistics schema. Allow session capacity for the detector, traffic driver, and consumer.
2. Run parameterized lookups as the dedicated login. Use PostgreSQL to identify the database, role, and statement IDs. Enroll both IDs in the detector and bind the consumer to the exact slow lookup's identity, table, and representative input. Enrollment uses numeric object IDs and a string for the signed query ID.
3. Configure the detector with `POOLTUNE_STAGING_ROLE=enrolled-detector` and `POOLTUNE_STAGING_ENROLLMENT`. Configure the consumer with `POOLTUNE_STAGING_ROLE=enrolled-consumer`, `POOLTUNE_STAGING_PROJECT`, and `POOLTUNE_STAGING_LOOKUP`. Use a matching project ID and private state database. Supply the dedicated DSN in `POOLTUNE_WORKER_TARGET_DSN` and the Supabase CA in `POOLTUNE_STAGING_ROOKERY_CA`.
4. Start with a fresh project ID and an empty queue. Send bounded, paired reads long enough to fill the five windows. Compare the detector's decisions with the saved job, and check statement counts to confirm that the consumer made no extra lookup.
5. Disable the temporary credential and restore both services to the measured fixture. Remove the temporary variables, role, and schema. Verify the existing public indexes still match.
