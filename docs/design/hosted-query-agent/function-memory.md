# Enrolled function memory qualification

This is repair family five in the accepted plan. The problem is a function whose sort writes temporary files because its memory allowance is too small. The repair changes only that enrolled function's work_mem. It does not change the database default, rewrite customer code, or claim memory is free.

## Follow-up passed with less diagnostic work

The separately recorded follow-up passed on Railway deployment `49ead71c-37b4-4c16-ae7a-ee82d1271402`, job **96**, first attempt. Ongoing query execution fell from **214.902 to 148.868 ms**, a **30.7% reduction**. The controlled ten-call comparison fell from **220.696 to 162.805 ms**. Its 57.891 ms gain exceeded the unchanged 22.880 ms noise threshold, as well as the 20% and 5 ms requirements. Both the actual function and the matched internal diagnostic went from **14,688,256 temporary-write bytes per call to zero**. Go kept the **32 MiB function setting** until owned fixture cleanup. [Full follow-up evidence](evidence/mixed-traffic/final-memory.json)

| Stage | Seconds |
| --- | ---: |
| Sustained slowdown to queued investigation | 12.832 |
| Queue to saved verified outcome | 17.565 |
| Total slowdown to verified outcome | 30.397 |
| Before measurement, inside the outcome time | 4.994 |
| Claude request, inside the graph | 2.609 |
| Graph including Claude | 3.061 |
| Setting action and guards | 1.255 |
| After measurement | 4.185 |

All **987 offered requests** completed without errors or missed slots. The brief spike crossed the fixed 180.979 ms objective but created no job. The two monitoring-on phases used **2.580 and 1.622 ms of collector database execution per elapsed second**; these are not CPU percentages. Target client p95 was **308.02/309.69 ms** with monitoring off and **311.33/310.75 ms** with it on. These short comparisons do not establish a universal overhead bound. Fixture cleanup passed and the 12 public index definitions retained their prior fingerprint.

The source was the reviewed pre-Cron service at `0fcbed1` plus only the reviewed memory sampler from `fe86aa4`. This kept unfinished scheduling code outside the memory deployment. Its five-repair prompt made the real decision; [six-repair direct controls](evidence/mixed-traffic/six-repair-live-controls.json) separately checked healthy/unsupported behavior after adding Cron. An initial SSH launch timed out; read-only inspection found no output or fixture. The successful retry launched one trial. Monitoring connection delays are not included in the saved database/worker timings.

The optimization removed nine repeated internal diagnostic sorts per side while retaining ten actual function samples. Before/after measurement time was 9.178 seconds here versus 13.405 seconds in the initial trial. That comparison also includes workload variation; the old trial restored its setting and this one retained it, so the shorter total outcome time is not a controlled estimate of the optimization alone. The regression test directly establishes the reduced diagnostic execution count. No benefit or noise threshold was relaxed.

**Assessment:** sufficient to continue the remaining bounded repair qualification. This proves one enrolled function with one sort, admitted concurrency and a stated test allowance. Production memory headroom, unbounded callers and more complicated functions still require qualification. The initial inconclusive result below remains part of the record.

## First hosted observation, November 14

The full live flow ran, but benefit qualification was **inconclusive**. Railway deployment `b4db8387-ce51-49fb-a800-68caf1ef0fae`, Rookery job **89**, used one Claude call (**3.272 seconds**). Go applied 32 MiB and measured temporary writes falling from **14,688,256 bytes per call to zero**. Controlled mean latency fell from **268.210 to 158.488 ms**.

That 109.722 ms gain did not exceed the unchanged noise threshold of **146.336 ms** (three times the larger median absolute deviation). Pooltune therefore restored **4 MiB**. The subsequent ongoing traffic correctly remained slow. This is evidence of a working decision/action/verification/restore path, not a qualified durable performance win. All **1,057 offered requests** completed without errors or missed slots; the brief spike created no job, and fixture cleanup passed. [Full initial evidence](evidence/mixed-traffic/initial-memory-inconclusive.json)

Detection took **9.040 seconds**, and queue-to-saved-outcome took **24.185 seconds**. Inside that outcome time: before measurement **7.541 seconds**, graph including Claude **3.815 seconds**, setting action **1.295 seconds**, and after measurement **5.864 seconds**. Other time includes connection/catalog/state work and restoration. Do not count this failed qualification in a successful-repair speed table.

The five-repair prompt separately declined a healthy function and a four-call request exceeding the 96 MiB allowance in **1.831/1.998 seconds**. The graph screened both without a provider call; these direct synthetic provider controls checked the prompt itself. [Control evidence](evidence/mixed-traffic/memory-live-controls.json)

The initial measurement loop repeats the identical internal diagnostic sort ten times alongside ten actual function samples on each side. The follow-up above tested reusing one internal plan per side under the same pinned definition, settings, data guard and input. The ten actual function latency/spill measurements and all benefit/noise gates stay unchanged. This reduces diagnostic work; it does not assume the original noisy result becomes a success. Record any follow-up as a separate experiment and retain this result.

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

## Implemented local contract

The fixed padding is `repeat(md5(n::text),4)` for `n=1..100000`. The internal query is `SELECT sum(lookup) FROM (SELECT lookup FROM ONLY <owned candidate> ORDER BY padding,lookup OFFSET 0) AS sorted`. The private zero-argument PL/pgSQL function is security-invoker, parallel-unsafe, and has pinned `search_path=pg_catalog`, `max_parallel_workers_per_gather=0` and `jit=off`. These setup choices remain fixed during repair. Only its `work_mem` changes.

Three transaction advisory slots, keyed to the private schema OID, admit calls before the sort. The internal diagnostic acquires the same slot; one verifier runs its actual calls and diagnostics sequentially. Slots are held until transaction end, so an idle caller can conservatively occupy a slot after its sort finishes. A fourth caller fails before sorting. This is a maximum allowance for the enrolled sort nodes, not a bound on all PostgreSQL memory, and changing work_mem does not reclaim memory from already-running calls.

The journal stores function OID, owner, full definition, argument signature, original work_mem, other settings and private execution status separately from the repair report. Sampling checks the measured actual call and the matched internal plan: exactly one fully consumed serial Sort over 100,000 candidate rows, ten measured reads, ten healthy reads and ten value-changing healthy writes on each side. Actual function execution and the internal diagnostic each report temporary writes; both must satisfy the reduction gate. Warmup and final result calls check `5000050000` and are excluded from measured latency. The diagnostic uses transaction-local matching settings only for inspection; the repair is exclusively `ALTER FUNCTION ... SET work_mem`.

The diagnostic-reuse follow-up now executes one internal diagnostic per before/after phase, then reuses that exact plan only inside the same guarded sampling transaction. It runs after the warmup and before the ten measured actual calls. Function identity/settings are checked before and after the phase, candidate data remain protected by the existing table lock, and input and sort settings remain fixed. `internal_temp_bytes` is the single diagnostic observation; `temp_bytes` remains the mean of ten actual function observations. Both still have to satisfy the existing spill gates. The ten actual latency samples, ten healthy reads, ten healthy writes, result checks, admission and noise thresholds are unchanged. PostgreSQL execution-counter regressions verify one diagnostic and ten measured calls at both 4 MiB and 32 MiB. This removes nine redundant diagnostics per side, at the cost of no longer repeating the internal plan observation. It does not imply less actual-call variance or overturn the initial inconclusive hosted result. The separately recorded hosted follow-up above supplies the required new qualification evidence.

Pending intent precedes the action. Definition, identity and settings are checked immediately before the fixed alteration. Interrupted verification attempts conditional restoration on a fresh bounded connection. Queue recovery never reapplies the increase: it restores only the exact still-applied setting with matching identity, preserves independent changes, and records interruption. Verified reports replay. Failed latency, spill or protection gates restore the applied value and retain the inconclusive/regressed result.

Preparation uses durable `memory_preparing` state. Failed setup cleans through independent connections and startup recovers only unfinished memory preparation. Cleanup verifies every function in the private schema, removes only the bound unchanged function before its tables, and uses RESTRICT throughout. Changed or foreign functions prevent removal. As with the existing private fixture, hostile concurrent same-owner catalog mutation is outside this staging contract; the code rechecks immediately before action but does not establish production DDL coordination.

The shared graph accepts exactly one causal evidence object and the typed `memory` selection. The shared traffic driver runs one target call per second across two sessions, with four healthy reads and two healthy writes per second, and records phase-level temporary-write counter deltas. The ten-call 32 MiB objective calibration happens before either fault and remains fixed. Live Claude, hosted concurrent traffic, source review and their outcomes are separately required; local checks alone do not qualify deployment.
