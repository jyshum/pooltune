> Superseded outcome labels: 216 application queue deadline expirations were recorded as rejections. Success counts, measured latency and deadline-miss counts remain valid. Preserved for audit.

# Local warm-pool comparison

Profile `local-rest-warm-v1`, October 24, 2026. **Diagnostic evidence only. G4 stays open.**

36 real PostgreSQL/PostgREST trials completed in 61.204 seconds: four modes, three traffic patterns, three repetitions. Fixed four-connection allocation, verified warm capacity, TLS and real row-level authorization. No model calls, resizing or automatic policy promotion.

Measured on Apple M5 Pro, 15 logical CPUs, darwin/arm64, Go 1.27.3, PostgreSQL 17.11, PostgREST 16.4. Source base `dba1df66f0df4a84275668ba4ad510fd74222252`, dirty workspace. See the source manifest for context.

## Results

Success counts are summed across three repetitions. Latencies are the median of the three success-only p95 values, in milliseconds. Never interpret latency without the success counts.

| Traffic | Mode | Important success | Important p95 | Background success | Background p95 |
| --- | --- | ---: | ---: | ---: | ---: |
| steady | warm_pool | 90/90 | 7.5 | 90/90 | 8.0 |
| steady | application_priority | 90/90 | 8.0 | 90/90 | 8.1 |
| steady | pooltune_equal | 90/90 | 8.4 | 90/90 | 8.6 |
| steady | pooltune_priority | 90/90 | 7.9 | 90/90 | 8.4 |
| burst | warm_pool | 8/24 | 186.2 | 120/120 | 524.7 |
| burst | application_priority | 24/24 | 113.4 | 102/120 | 559.0 |
| burst | pooltune_equal | 24/24 | 186.2 | 120/120 | 520.7 |
| burst | pooltune_priority | 24/24 | 114.9 | 102/120 | 561.0 |
| sustained | warm_pool | 34/120 | 185.2 | 199/360 | 517.6 |
| sustained | application_priority | 120/120 | 74.7 | 81/360 | 591.2 |
| sustained | pooltune_equal | 86/120 | 191.0 | 102/360 | 513.8 |
| sustained | pooltune_priority | 120/120 | 68.9 | 81/360 | 588.2 |

## Interpretation and limits

- Priority helps the important workload in these contention traces. It transfers waiting and failure to background work. It does not create capacity or eliminate overload.
- Application-side priority achieves similar results. This run does not establish a unique performance benefit for placing the scheduler in Pooltune. The application reference uses the same scheduler implementation, so this isolates placement rather than comparing independent algorithms.
- At low load, the Pooltune path is slightly slower in these samples. The small median p95 differences are not a statistically established overhead bound. They do not prove the provisional under-1-ms p99 target.
- Equal-priority Pooltune still applies fairness across identities, unlike the provider queue. Its improvement in burst outcomes is not evidence of AI benefit.
- After a client timeout, Pooltune and the application reference conservatively retain uncertain in-flight reservations. The provider-only path can continue using its own pool. This is part of the observed overload behavior, so results cannot be attributed to priority alone. No unknown query is replayed.
- The workload uses fixed SQL sleeps plus real authorized reads. It models connection occupancy, not CPU-bound queries or customer production traffic. All components share one machine. No hosted-provider claim follows.
- Raw samples include timeouts, rejections, failures, deadline misses and generator lag. Maximum observed generator lag was 6.3 ms. Whole-host CPU/memory is included when available and is not isolated database resource consumption.
- Application queue waiting is recorded separately. Pooltune queue and downstream execution are not separately instrumented by this profile. End-to-end low-load differences are only a path-cost estimate.

## Reproduce and evidence

Use [bench/README.md](../../bench/README.md) and `bench/run.sh /absolute/new/result.json` with the documented binaries in PATH. No success-margin threshold selects a winner. All 36 trials verified four warmed connections and complete request accounting; successful responses retained the original authenticated identity.

- [Raw observations, gzip JSON](warm-pool-v1-original.json.gz)
- [Workspace source hashes](warm-pool-v1-original-source.json)
- Raw JSON SHA-256: `e349e1940685e677cab1ebee198567123c81160b5a0041e403b944625f5f0fdb`

## Release gaps retained

Backup routing and uninterrupted failover remain unimplemented/unverified. Crash recovery evidence does not establish availability during a traffic-process failure. A backup requires independent health/routing, exclusive budget ownership, reconciliation of old work and no replay of uncertain writes.

AI can propose changes inside customer-approved classes, locks and numeric limits. Discovery needs owner review; the local compiler enforces authority and the evaluator governs rollout. This comparison makes no AI-benefit claim and changes no customer boundary. Production calibration, managed providers, containers, full onboarding and broader G1-G5 gates remain open. The physical-pool work in this checkout is a separate unfinished checkpoint, not a feature certified by this comparison.
