# Local warm-pool comparison

Profile `local-rest-warm-v1`, October 24, 2026. **Diagnostic evidence only. G4 stays open.**

36 real PostgreSQL/PostgREST trials completed in 59.989 seconds: four modes, three traffic patterns, three repetitions. Fixed four-connection allocation, verified warm capacity, TLS and real row-level authorization. No model calls, resizing or automatic policy promotion.

Measured on Apple M5 Pro, 15 logical CPUs, darwin/arm64, Go 1.27.3, PostgreSQL 17.11, PostgREST 16.4. Source base `dba1df66f0df4a84275668ba4ad510fd74222252`, dirty workspace. See the source manifest for context.

## Results

Success counts are summed across three repetitions. Latencies are the median of the three success-only p95 values, in milliseconds. Never interpret latency without the success counts.

| Traffic | Mode | Important success | Important p95 | Background success | Background p95 |
| --- | --- | ---: | ---: | ---: | ---: |
| steady | warm_pool | 90/90 | 7.9 | 90/90 | 7.9 |
| steady | application_priority | 90/90 | 7.5 | 90/90 | 7.7 |
| steady | pooltune_equal | 90/90 | 8.2 | 90/90 | 8.3 |
| steady | pooltune_priority | 90/90 | 8.1 | 90/90 | 8.4 |
| burst | warm_pool | 9/24 | 180.5 | 120/120 | 518.7 |
| burst | application_priority | 24/24 | 114.0 | 102/120 | 559.7 |
| burst | pooltune_equal | 24/24 | 188.0 | 120/120 | 520.4 |
| burst | pooltune_priority | 24/24 | 115.0 | 102/120 | 560.2 |
| sustained | warm_pool | 33/120 | 183.3 | 200/360 | 529.7 |
| sustained | application_priority | 120/120 | 73.2 | 81/360 | 592.0 |
| sustained | pooltune_equal | 85/120 | 190.4 | 102/360 | 515.8 |
| sustained | pooltune_priority | 120/120 | 68.9 | 81/360 | 586.9 |

## Interpretation and limits

- Priority helps the important workload in these contention traces. It transfers waiting and failure to background work. It does not create capacity or eliminate overload.
- Application-side priority achieves similar results. This run does not establish a unique performance benefit for placing the scheduler in Pooltune. The application reference uses the same scheduler implementation, so this compares the current paths rather than independent algorithms. The reference reuses upstream HTTP connections while current Pooltune ingress does not; the path difference includes this transport behavior.
- At low load, the Pooltune path is slightly slower in these samples. The small median p95 differences are not a statistically established overhead bound. They do not prove the provisional under-1-ms p99 target.
- Equal-priority Pooltune still applies fairness across identities, unlike the provider queue. Its improvement in burst outcomes is not evidence of AI benefit.
- After a client timeout, Pooltune and the application reference conservatively retain uncertain in-flight reservations. The provider-only path can continue using its own pool. This is part of the observed overload behavior, so results cannot be attributed to priority alone. No unknown query is replayed.
- The workload uses fixed SQL sleeps plus real authorized reads. It models connection occupancy, not CPU-bound queries or customer production traffic. All components share one machine. No hosted-provider claim follows.
- Raw samples include timeouts, rejections, failures, deadline misses and generator lag. Maximum observed generator lag was 7.5 ms. Whole-host CPU/memory is included when available and is not isolated database resource consumption.
- Application queue waiting is recorded separately. Pooltune queue and downstream execution are not separately instrumented by this profile. End-to-end low-load differences are only a path-cost estimate.

## Reproduce and evidence

Use [bench/README.md](../bench/README.md) and `bench/run.sh /absolute/new/result.json` with the documented binaries in PATH. No success-margin threshold selects a winner. All 36 trials verified four warmed connections and complete request accounting; successful responses retained the original authenticated identity.

- [Raw observations, gzip JSON](evidence/warm-pool-v1.json.gz)
- [Workspace source hashes](evidence/warm-pool-v1-source.json)
- Raw JSON SHA-256: `ff6a328d27162aeeb2382074ec6a2888d592d30a48a2dd094bd309c5320aef05`

## Release gaps retained

Backup routing and uninterrupted failover remain unimplemented/unverified. Crash recovery evidence does not establish availability during a traffic-process failure. A backup requires independent health/routing, exclusive budget ownership, reconciliation of old work and no replay of uncertain writes.

AI can propose changes inside customer-approved classes, locks and numeric limits. Discovery needs owner review; the local compiler enforces authority and the evaluator governs rollout. This comparison makes no AI-benefit claim and changes no customer boundary. Production calibration, managed providers, containers, full onboarding and broader G1-G5 gates remain open. The physical-pool work in this checkout is a separate unfinished checkpoint, not a feature certified by this comparison.

For a customer who already has effective application-side priority, this test
provides no measured latency reason to switch. Coordinating several applications
that share a database is a possible reason for a central service, but this
single-driver comparison does not establish that benefit. Preserve the approved
features while requiring evidence for each advantage claimed.

## Review and final verification

Independent review found a deadline-label race, reproduced by a failing test and fixed. The corrected run above has 36 trials/3216 samples, zero generic failures and zero rejections; all unsuccessful requests are timeouts. The original run, including its 216 mislabeled application-queue timeouts, is preserved in [original evidence](evidence/warm-pool-v1-original-report.md). Counts from the original commentary can differ slightly because the corrected evidence is a fresh run.

The full ordinary source-root race/integration suite passed (runtime174.359s), including physical growth/reclamation correctness after a UTF8 test-client correction. Physical features remain outside this checkpoint pending independent review. The reporting fix was subsequently checked with the full ordinary race suite and corrected real comparisons. Vet/diff checks pass. Browser/container/package checks were not rerun for this diagnostic-only change.

[Independent review and scoped rulings](evidence/warm-comparison-review.md). [Exact source delta from the recorded base, gzip patch](evidence/warm-pool-v1-workspace.patch.gz) preserves the dirty Go/benchmark context, including unfinished physical changes; it is evidence, not a released artifact. The benchmark itself and policy boundaries remain unchanged after the deadline-label correction.
