# Pooltune resume bullets backed by saved results

Suggested bullets. These describe controlled engineering tests, not production customer outcomes.

- Validated six PostgreSQL repair families on controlled Supabase workloads by connecting Claude Sonnet diagnosis to permission-checked Go tools, durable job state, and measured before-and-after verification.
- Reduced measured lookup execution time by 99.6% (16.605 ms to 0.070 ms) in a controlled Supabase test by generating and verifying a concurrent compound index with Go.
- Prevented duplicate index creation during a live Railway restart test, retaining one completed database schema operation, by persisting repair intent and reconciling PostgreSQL catalog state.
- Restored prior statistics settings after detecting a 30% latency regression in a Supabase trial by comparing measured performance before and after the change and recording recovery.

## Evidence and scope

- Six families: index, statistics refresh, statistics detail, cleanup, function memory, scheduling. See `evidence/mvp-simple-*-railway.json`. Prepared private workloads and prior authorized fixture workers; no claim of general customer readiness.
- Index: `evidence/mvp-simple-index-railway.json`, measured single-input database execution 16.605 to 0.070 ms, 866 to 4 block accesses. Percent reduction 99.5784%. Separate client request timing was 41.851 to 24.357 ms and must not be conflated.
- Restart: `evidence/mvp-owned-index-restart-railway.json`, completed_ddl_calls=1, identical index OID before/after, exact_index_present, no performance verification claim.
- Regression recovery: `evidence/mvp-simple-column-target-railway.json`, 22.1785 to 28.8261 ms (29.9725% worse), prior target1 and planner sample restored.

The original CalHacks project and later development should be dated accurately. Do not attribute the later agent and Railway work to the hackathon weekend unless that is supported separately.


## New managed customer-flow result

On November15, the ordinary Railway customer connection selected statistics refresh and discovered the query without a manual ID or fixture-worker configuration. Job318 measured14.503ms before and0.252ms after, a98.2624% reduction for one sampled input. The driver completed269 requests without an error. Protected completed-query means stayed within the configured limits. This is a controlled Supabase workload, not an application-wide production benchmark.

Suggested replacement for the older benchmark bullet:

- Reduced a measured PostgreSQL lookup by **98.3% (14.503ms to0.252ms)** in a controlled Supabase test by building an automatic Go repair flow on Railway that discovers queries, refreshes statistics and verifies results without manual query IDs.

Evidence: [Managed statistics-refresh proof](evidence/managed-analyze-railway.json). Keep the existing99.6% compound-index result only if its controlled-test scope is retained. The new managed index attempt was rolled back after protected traffic regressed and must not be described as a retained99.7% improvement.
