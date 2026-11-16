# Ordinary-project function memory: approved fallback design

Status: **Fallback design approved; implementation and local qualification in progress. Hosted qualification remains outstanding.**

## Approval and superseded proposal

On November 15, Jared approved the recommendation with “i approve, i thoguht this was all already set up- though”. Task 62 in progress.md records that approval. It covers automatic extra memory within an enforced allowance and **original-memory fallback**, not new busy errors. The later token decision authorizes retaining the encrypted project token with Analytics Read so the Go worker can read actual memory availability. Ask for new design conflicts and final-only review remain active.

The original proposal below is preserved as history. Its suggested wait/busy behavior is superseded. The implementation must not reject an ordinary request merely because the extra-memory allowance is full.

## Approved runtime behavior

The supported function keeps its original function-level `work_mem`. At each call, a small fixed helper returns an allowance only while its provider observation is fresh and the installed function has the exact recorded catalog generation and definition. A call that acquires one of at most three database-wide transaction advisory slots temporarily uses up to 32 MiB for its one admitted sort. Other calls execute the original query with the original memory allowance. PostgreSQL's function setting frame restores the caller's setting on return or error. Slots last until transaction end, so an idle transaction can conservatively reduce boost availability; it does not cause busy errors.

The extra-memory policy expires after 60 seconds. The worker renews only verified repairs whose owner selection and installation epoch remain active. Missing telemetry, a stopped worker, an expired token, or a changed function stops renewal. The function then continues at its original memory. This is a bound on Pooltune's added allowance, not a bound on the application's original baseline memory use.

The initial supported class is a zero-argument security-invoker PL/pgSQL function returning bigint, with one built-in sum over one fully consumed serial sort of at most 100,000 rows in a relation no larger than 24 MiB. It must already pin its baseline work_mem, search_path, parallelism and JIT settings. No arbitrary functions are replayed. Read-only measurements use the observed app role and match the actual function-query identifier; protected traffic comes from real counters, with no generated customer writes.

Actual headroom comes from `GET /v1/projects/{ref}/analytics/endpoints/metrics` using the existing encrypted project Analytics Read token. The parser requires exact project/database labels and finite available/total RAM metrics. Memory above max(128 MiB, 25% of total) is only half allocatable; the resulting allowance is capped at three 32 MiB slots. Missing or stale metrics refuse the boost. This does not derive free RAM from shared_buffers or import the fixture's 96 MiB budget as customer headroom. [Supabase Management metrics endpoint](https://supabase.com/docs/reference/api/v1-scrape-project-metrics)

Intent precedes a rewrite. The exact committed catalog generation is recorded after commit. Restoration requires that exact generation and definition; independent owner changes remain untouched. If acknowledgement is lost, recovery does not guess ownership or blindly rewrite the function. It reports uncertainty, and the short allowance expires independently.

The later [native-fence approval](managed-memory-native-fence-proposal.md) permits an identical-content owner save during native lock acquisition. A short transactional rename takes PostgreSQL’s real function tuple lock, compares every catalog value and restores the original name before proceeding. Any changed content or object identity is refused. The lock transaction used for sampling is separate from the read-only app-role measurement and always rolls back. Renewal and recovery retain exact installed-generation checks. No system-catalog UPDATE privilege is required.

## Original proposal and evidence, retained for history

Original status: pending proposal before Task 62 approval.

Scope: the existing V1 function-memory node. Railway execution and encrypted owner credentials are approved. Changing a customer's function body, adding a wrapper, changing application concurrency, or weakening the existing memory budget is not included in that approval. Ask for design conflicts and final-only review still apply.

## What works already

The native Go action can resolve an exact owned function and change only its `work_mem`. It preserves the other function settings, checks its definition and catalog generation, journals the action, and can conditionally restore the exact change. The fixed Rookery function has already demonstrated measured benefit. Those results remain valid for that enrolled, bounded test.

A signed-in catalog picker can now record a specific customer function and its identity. Selection supplies owner intent. It does not establish the missing runtime evidence below.

## What prevents an ordinary selected function from qualifying

1. **The maximum call count is enforced only inside the test function.** `OwnedMemoryFunctionBody` contains three transaction advisory-lock slots. `exactOwnedMemory` requires that exact body before sampling. The ordinary native action accepts a caller-supplied `MaxConcurrentCalls`; it does not independently impose that bound on application traffic. Merely observing three or fewer calls is not a bound on tomorrow's callers.
2. **The current sampler knows the test's exact inner query and result.** `sampleOwnedMemory` uses `memoryInternalQuery`, one serial sort of 100,000 rows, and result `5000050000`. It also generates writes to the test's protected table. Those assumptions cannot become evidence for a selected ordinary function. An arbitrary function may perform writes, invoke another function, or depend on session state. Its body must not be replayed solely because the owner selected it.
3. **The normal-traffic plan reader does not attribute nested function statements.** `TrafficPlan` currently keeps the outer root and one child name, not the complete inner plan, temporary-write attribution, function call identity, or matching settings. PostgreSQL can log nested statements using `auto_explain.log_nested_statements`, but that feature still needs provider availability, bounded collection, and reliable call attribution. [PostgreSQL auto_explain](https://www.postgresql.org/docs/17/auto-explain.html)
4. **The test's 96 MiB allowance is not measured customer headroom.** The generic capacity guard checks connection count. Its optional memory-capacity callback has no ordinary-project provider telemetry behind it today. `work_mem` can multiply across operations and concurrent sessions. [PostgreSQL resource settings](https://www.postgresql.org/docs/17/runtime-config-resource.html)

Evidence sources: `internal/hostedquery/project_function_memory.go`, `owned_memory_repair.go`, `memory_fixture.go`, `memory_samples.go`, `traffic_plan.go`, and `owned_project_repair.go`. The accepted limits and original qualification are in [function-memory.md](function-memory.md). This assessment changes none of those conditions.

## Recommended next design decision

Authorize an **explicitly opted-in admission guard for a narrow selected function shape**, with its behavior shown before installation. Pooltune would preserve the selected function's signature and existing operation, and insert a bounded call-admission guard only after proving the function is a supported, read-only single-sort function. Calls beyond the selected fixed allowance would wait for a bounded interval or return a clear busy result. The waiting/error behavior must be chosen before implementation. This is a customer-code change and therefore requires approval.

The initial supported class should be zero-argument, security-invoker PL/pgSQL functions with one read-only aggregate over one fully consumed serial sort, catalog-resolved built-in operations, bounded relation size, and no dynamic SQL, nested function calls, writes, recursion, request-dependent RLS, or external effects. This remains a narrow eligibility class; it must not be described as support for arbitrary functions.

Implementation after approval:

1. Record and compare the complete original function definition, owner, settings, argument signature, execution grants and catalog generation. Add tests proving that unsupported bodies are refused before execution.
2. Install the approved admission guard while preserving identity and permissions. Persist the original body first. Recheck exact definition and permission epoch. Do not modify a function changed independently by its owner.
3. Collect an attributed internal sort plan and actual function measurements using the observed application role, the same setting, and a stable data/input guard. Reuse sufficient valid normal-traffic evidence. Where a bounded experiment is necessary, execute only the parsed supported read-only shape. Use real protected traffic counters rather than synthetic production writes.
4. Connect a verified provider memory-headroom signal and establish the explicit aggregate allowance. Missing headroom stays a refusal; neither current connection count nor the Rookery allowance substitutes for it.
5. Reuse the Go action, existing latency/spill/result/protected-traffic gates, durable journal and conditional restoration. Test fourth-call admission, changed definitions, restarted actions, setting restoration and ordinary app requests before qualifying a live customer function.
6. Show the enrolled function, admission behavior, observed evidence, changed setting and verified result in the dashboard. Keep the map yellow until this ordinary-project path is demonstrated.

The strongest alternative is **an existing application-enforced limit supplied through a verifiable integration**, plus nested-plan evidence and provider headroom. That preserves the function body but requires customer application integration and a proof that every caller obeys the limit. A checkbox or a declared numeric limit alone is not sufficient. This alternative is less automatic but stays closer to the no-code-rewrite constraint.

A passive-only recommendation remains available without either change, but it is not completion of V1 automatic function-memory repair. Raising a session setting only for a worker's test also does not repair ordinary app calls.

## Current implementation boundary

Continue the approved catalog enrollment work and all independent repair families. Do not route ordinary selected functions into the fixture sampler, fabricate a concurrency cap, remove the matched internal-plan requirement, install wrappers, or claim this node is green. The pending decision is the runtime admission/evidence design, not whether the Go action can issue `ALTER FUNCTION`.

## Verification of the existing boundary

Fresh local PostgreSQL integration checks with the race detector passed on November 15, 2026:

- The native ordinary-overload action and the owned function spill/identity path passed in **6.388 seconds** together with matching memory recovery tests. The ordinary-overload test supplies its bounded call allowance explicitly; it proves native action/restoration, not automatic admission on a customer application.
- `TestMemoryAdmissionCapsCallsAndInternalDiagnostic` and `TestQueuedMemoryRejectsUnsafeOutcomesAndRestoresPendingChange` passed in **18.402 seconds**. These demonstrate the existing fixture's enforced limit and restoration behavior, which the ordinary-project route must preserve.

No additional Rookery function was called or modified for this assessment.
