# Enrolled function memory qualification

This is repair family five in the accepted plan. The problem is a function whose sort writes temporary files because its memory allowance is too small. The repair changes only that enrolled function's work_mem. It does not change the database default, rewrite customer code, or claim memory is free.

## Prespecified fixture contract

A local calibration probe used 100,000 rows with lookup=id and 128-byte deterministic padding. The table occupied 17,432,576 bytes, within the existing 24 MiB table bound. A pinned, security-invoker function computes sum(lookup) through a single explicit padding/lookup sort. The result must always be 5000050000. Function-level settings prevent SQL inlining. Measure the exact internal sort under matching settings as well as the actual function call. Check the stored definition, owner, signature and settings before trusting the attribution.

Calibration compared 64 KiB, 2 MiB, 4 MiB and 32 MiB allowances. The smallest setting wrote about 59 MiB per call. The chosen initial test setting is 4 MiB: it wrote 1,793 eight-KiB blocks (14.008 MiB), while 32 MiB wrote zero. Three 4 MiB calls took 29.761–30.083 ms locally, and three 32 MiB calls took 20.541–21.108 ms. These are fixture-design observations, not hosted or concurrent qualification. Rookery's actual database default was separately observed as 2184 kB and will not be changed.

Declare a 32 MiB candidate per sort, one sort per call, no parallel query workers, at most two traffic calls plus one verifier call, and a 96 MiB aggregate fixture allowance. Use bounded concurrency admission for this private function. Production headroom and arbitrary application concurrency remain unknown. This budget qualifies only the controlled test, not a durable change to a customer function with unbounded callers.

The target traffic rate is one function call per second spread across two sessions. Healthy reads and healthy-table writes retain four and two calls per second. Lower target frequency bounds the intentionally induced temporary writes. All comparison phases keep those rates and inputs unchanged. Declare and freeze the detector objective from ten clean 32 MiB pre-fault calls as max(3 ms,1.25 times their measured database mean). A short fault temporarily restores 4 MiB, then returns to 32 MiB; the sustained fault returns to 4 MiB until the agent decides. These fixture setup changes are separate from the actual repair. The initial draft reused the cleanup fixture's 1.5 multiplier. Before implementation or hosted qualification, the local design probe showed that this would miss its roughly 44% slowdown. This fixture therefore declares 1.25 before the run, matching the ratio needed for a 20% return-to-healthy improvement. The repair benefit gates remain unchanged; this is an enrolled test objective, not a production default.

## Evidence and action

Require at least 8 MiB of attributed temporary writes per call, a fully consumed internal sort, the enrolled function identity, exact current settings, known call concurrency and the explicit candidate budget. Claude may recommend the offered function-memory repair or decline. It supplies no SQL. Go independently checks the recommendation and resolves the owned function name from enrollment.

Persist the original function setting and the intended 32 MiB setting before ALTER FUNCTION. Recheck definition, ownership and current setting immediately before mutation. Preserve all other settings. Verification requires at least 50% less attributed temporary writes and the existing tested-input latency route: at least 20% and 5 ms, exceeding three times the larger median absolute deviation. Measure protected traffic and result correctness. If settings or identity change independently, defer or report inconclusive rather than overwrite them.

Recovery inspects the actual setting before any retry. A verified outcome replays. An interrupted unverified change restores only an exactly matching Pooltune-applied setting to its recorded prior value, with identity checks. It must never overwrite an unrelated newer setting or claim existing calls have relinquished their memory immediately. Cleanup drops only the owned test function and fixture. A real provider request and the shared traffic/error/cleanup measurements are required for completion.

## Trade-off and remaining scope

A function-scoped setting confines the change better than changing every database session. It still multiplies across concurrent calls and sort/hash nodes, so fewer temporary writes alone cannot establish safety. The strongest alternative is leaving the setting unchanged and reducing concurrent work. That can protect memory but may defer or slow requests. This fixture tests one bounded sort and known demand; broader function structures and production resource telemetry remain separate qualification.

Reference: [PostgreSQL ALTER FUNCTION](https://www.postgresql.org/docs/17/sql-alterfunction.html). The implementation must preserve the distinction between an enrolled function setting and a session/database default.
