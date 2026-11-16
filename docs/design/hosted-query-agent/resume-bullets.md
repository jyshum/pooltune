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
