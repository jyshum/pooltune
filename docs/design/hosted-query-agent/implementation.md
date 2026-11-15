# Hosted Query Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use crocking:executing-plans for the authorized inline M1 work. Steps use checkbox syntax for tracking.

**Goal:** Measure a healthy query and a query worth investigating on an isolated Rookery fixture, then qualify the first index repair after M1 acceptance.

**Architecture:** The Go entry point collects PostgreSQL counters and measured plan evidence and applies a deterministic detector. Pooltune-owned PostgreSQL stores its results. After M1, a single TypeScript/LangGraph investigator uses Anthropic and Go-controlled tools; Go owns admission, action state, verification and reconciliation.

**Tech stack:** Go 1.27.3 or newer, the repository's pgx v5, PostgreSQL 17 for the test rig, TypeScript/LangGraph and Anthropic after M1.

**Spec:** [Product](../../product/query-performance-v1.md), [backend decision](../../product/backend-language-decision.md), [v0 detection and verification proposal](detection-verification-v0.md), [handoff](HANDOFF.md).

**Canonical records:** [Design](design.md), [graph](graph.json), [approvals](approvals.json), [plan](implementation.md), [progress](progress.md).

**Decision references:** d-language r2, d-workflow r1, d-hosting r1, d-reuse r1 and d-onboarding r1 match the corresponding approval records. d-identity, d-trials, d-durability and d-studio r1 remain proposals.

## Workflow settings

- Design conflict mode **ask**. Jared selected “Ask me first” in the November 13 interactive reply.
- Human review mode **milestones**. Jared selected “Pause at agreed milestones” in the same reply. [Handoff](HANDOFF.md).
- Authorized scope includes the automatic index slice after the accepted Rookery milestone, on a local branch, with isolated synthetic work in authorized Rookery. Routine internal interfaces and lab parameters are delegated. Approved architecture and customer-facing boundaries require a ruling if they conflict with evidence.
- [Progress](progress.md) records acceptance of M1 and the index milestone. Jared authorized integrating the index work locally into `feat/pooltune-revamp` and starting the background worker on a separate branch. The canceled worktree request stays canceled. Remote publishing and customer deployment are outside this scope.

## Human milestones

- [x] **M1 acceptance.** Review Tasks 1–3, including the isolated local and Rookery setup, healthy/no-change and investigate decisions, actual measurements, state readback, cleanup, source and limitations. Obtain explicit acceptance before enabling agent-driven index changes.
- [x] **Index slice review.** After M1, demonstrate investigation through verified outcome and recovery, including refusal of unsupported actions. Record actual live model evidence and explicit review before any later integration decision.

## Global constraints

- Live Claude qualification is required for every repair capability. Jared explicitly requested this on November 14 after Task 21. Tool tests and scripted runs remain useful checks. Each capability also needs a real model decision through the shared investigator, healthy and unsupported controls, the authorized Go action and verification, and separately reported model and total timings. Record a missing live check as incomplete.
- Exactly six V1 repairs. The first slice qualifies a nonunique index for a single-column equality lookup on synthetic data. Compound indexes remain in V1 scope and need separate qualification. The other V1 operations remain targeted ANALYZE, extended/column statistics, bounded plain VACUUM and qualified cleanup settings, enrolled function-scoped work_mem, and enrolled future Cron scheduling.
- Preserve exclusions for pool and WAL/checkpoint tuning, index rebuilds and customer-index deletion, arbitrary SQL and application/RLS rewrites, killing customer work, compute purchases, replicas and migrations. Restrict operations to authorized projects and enrolled objects.
- Preserve upfront capability grants, relevant evidence access, encrypted production transport/storage, account isolation, retention, and evidence reuse. Avoid redundant experiments and copying application tables. Adapt scheduling to current capacity and optional owner windows; missing guards defer heavy work.
- Keep detection, benefit and protection baselines distinct. Measurements, planner estimates and client timings have different meanings. No resets of shared counters. Missing observations cannot pass a guard. v0 numerical defaults are calibration candidates.
- M1 fixture DDL is developer test setup, with one existing primary key on each synthetic table. The harness has no candidate index execution path. No app-table or role changes on Rookery. Existing native admin credentials are for the fixture only; they establish neither OAuth nor least privilege.
- The old local gateway, SQLite state and dashboard remain outside the hosted implementation. Reuse pgx and the repository's local PostgreSQL test helper.

## Review focus

- Missing, evicted or reset statement counters cannot appear as fast observations (Task 1).
- Equal latencies at a strict objective boundary and mixed contexts cannot manufacture an investigation (Task 1).
- A name collision, competing run or failed statement must preserve pre-existing objects (Task 2).
- A provider connection misrouted to another project or lacking server verification must fail before running the fixture (Task 3).
- A successful DDL command or improved aggregate cannot outrun action recovery or missing guard coverage (Tasks 4–5).

### Task 1: Statement evidence and deterministic objective detection

**Files:** `internal/hostedquery/evidence.go`, `internal/hostedquery/evidence_test.go`.

**Decisions:** d-language r2 and d-reuse r1. No M1 dependency on model access.

**Interfaces:** `Measure(before, after Counter, expectedCalls int64) (Observation, error)` computes calls, mean execution milliseconds and shared blocks per call. Counter carries statement identity (DB, role, query ID, top-level by collector filter), server boot, global stats reset, entry birth, calls, total execution time and combined block count. `DetectObjective(observations []Observation, objectiveMS float64) (Decision, error)` returns investigate, no change or insufficient evidence.

- [x] Write and run tests for counter deltas, wrong identity/reset/rebirth/restart, missing values and no calls, strict objective comparisons, and fewer than five observations.
- [x] Implement deltas and the v0 objective rule of at least 3 breaches among the latest 5 comparable observations. This is the mean-objective route only. M1 uses fixed lab batches with matching data, role and input sequence; no five-minute cadence or historical baseline claim.
- [x] Run `go test ./internal/hostedquery` and commit the verified evidence/detector change locally.

### Task 2: Run a bounded PostgreSQL fixture

**Files:** `internal/hostedquery/lab.go`, `internal/hostedquery/lab_integration_test.go`.

**Decisions:** d-language r2 and d-reuse r1. d-identity and d-trials are tested only within the lab contract.

**Interfaces:** `RunLab(ctx context.Context, target *pgx.Conn) (Report, error)` uses a dedicated native connection. Report records server version, lab policy, case observations/decisions and measured scan evidence. It carries no DSN or application data.

- [x] Write and run an integration test using the existing TLS helper. Load pg_stat_statements in the disposable server. Assert the lab completes, both queries return identical results, the measured plans distinguish scans, and no fixture remains. Verify rollback on a failed run and that an existing fixture name is untouched.
- [x] Implement a single transaction with explicit rollback on every path. Take a transaction advisory lock, verify no prior fixture at `pooltune_m1`, and create only objects under that schema. Reject a standby or observed lock wait and heavy activity at preflight. One connection, a 35-second run deadline, 250 ms lock timeout, 5-second setup statement timeout, and 750 ms per measured statement. Enforce 30 seconds server-side per transaction and 8 seconds idle in transaction. Defer if activity statistics are unavailable. No project setting or extension changes on Rookery.
- [x] Seed two ordinary tables with identical 100,000 deterministic synthetic rows and primary keys. Read the same small set of distinct IDs using parameterized equality. The healthy query uses its primary key; the candidate queries an equally selective unindexed lookup column. Analyze fixture tables once before sampling. Never remove a useful index.
- [x] Obtain query IDs from JSON EXPLAIN VERBOSE. Read pg_stat_statements for the current database, role and exact top-level query IDs. Use a single warmup per case, then interleave five batches of four executions per case with identical inputs and a preset 1 ms database mean objective. Verify returned data and exact observed call deltas. Capture one measured EXPLAIN plan per case for scan type, examined and returned rows, and logical shared blocks. Collect no project-wide query text.
- [x] Run `go test -tags=integration ./internal/hostedquery -v` with PostgreSQL 17 binaries in PATH. Require the integration cases to execute, then commit locally.

### Task 3: Persist and demonstrate M1

**Files:** `cmd/pooltune-query-lab/main.go`, `cmd/pooltune-query-lab/main_test.go`, `internal/hostedquery/store.go`, [progress](progress.md).

**Decisions:** d-language r2, d-workflow r1 and d-onboarding r1. Local PostgreSQL models the Pooltune-owned state boundary.

**Interfaces:** CLI consumes `POOLTUNE_LAB_TARGET_DSN` and `POOLTUNE_LAB_STATE_DSN` from private process configuration, plus explicit `-target rookery` for the live route. `RecordReport(ctx context.Context, state *pgx.Conn, report Report) (int64, error)` appends an observation and returns its durable ID.

- [x] Write and run tests rejecting another project/port, TLS downgrade, alternate/fallback hosts, and a nonlocal state destination. Extend integration verification to persist and read back the report over a separate state connection.
- [x] Implement the CLI with strict local-loopback or exact Rookery session-pooler routes, bounded connections, checked rollback, and sanitized errors. Rookery requires `verify-full` and its CA certificate. Keep bootstrap credentials out of command arguments, output and source. Confirm the state connection is usable before running the target. Store report JSON in a dedicated local PostgreSQL table. No model or repair endpoints in this milestone.
- [x] Verify with `go test ./...`, `go test -tags=integration ./internal/hostedquery -v`, `go vet ./cmd/pooltune-query-lab ./internal/hostedquery`, and `git diff --check`. Document actual limitations of the old gateway suite if any. Run the CLI on Rookery once, independently verify state readback and absence of the fixture and changes to existing app indexes, and preserve measured results in progress. If native access is unavailable, demonstrate the local implementation and report any separately measured Rookery evidence as such.
- [x] Obtain a fresh review, address material findings and commit verified work locally. See the M1 evidence in progress.md. Human acceptance is tracked separately above.

### Task 4: Qualify an index against the enrolled fixture

Files: `internal/hostedquery/index_policy.go`, `index_policy_test.go`, `investigator/`. Decisions: d-language r2, d-workflow r1, d-reuse r1. M1 accepted. The index slice remains restricted to the developer-enrolled synthetic fixture.

- [x] Test `qualifyIndex(Evidence, Proposal, now)` on healthy, skewed, stale, existing equivalent and unknown-authority cases. Proposals carry an evidence ID and catalog table/column numbers. Match a single enrolled plain integer equality column in this slice. V1 compound indexes remain in scope for later qualification. The executor supplies the generated object name and quotes catalog identifiers; the proposal cannot provide SQL.
- [x] Build a single TypeScript LangGraph workflow with an evidence node, one bounded Anthropic selection of plan or catalog tools, and a structured recommendation. Use `langgraph.json` for development inspection in Studio. Pass only synthetic evidence and the Pooltune state connection to the local investigator process. Keep the Anthropic key in its environment; require an explicit model name. Use a scripted investigator only in labeled development evaluation. Test the graph with a deterministic model double and assert evidence access, healthy/no-change and misleading scan behavior. Correlate its checkpoint with the Go run ID.
- [x] Run Go policy tests, TypeScript typecheck/tests and review the cross-language contract.

- [x] Follow-up requested by Jared. Compare one call with two on three new queries, verify the exact index and the zero-call healthy path, then make one call the default. [Results](index-slice.md#default-after-testing-new-queries).

### Task 5: Execute, verify and recover automatically

Files: `internal/hostedquery/index_trial.go`, `index_journal.go`, `index_trial_integration_test.go` and the existing lab entry point. Decisions: d-reuse; implementation details within d-trials and d-durability. Depends on the qualification rules and index scope in Task 4.

- [x] Add `RunIndexTrial(ctx, target, state, investigate, identity)`. Use a dedicated session and project/database advisory lock. Persist a target-scoped run ID before creating a unique private schema. Create fixture and exact ownership marker atomically. Reject collision, standby, missing activity visibility, lock waits and more than two other active client backends. Caps: 100,000 rows per table, 24 MiB relation data per table, 8 MiB index, 16 MiB maintenance memory, 250 ms lock wait and 5-second build timeout. Rookery uses the pinned authenticated route. This bounded envelope is for disposable fixtures; customer disk/pressure checks and credentials remain unqualified.
- [x] Reuse the M1 data pattern and objective. Measure real healthy/candidate plans, a paired write guard, and exact row results using 10 sequential iterations on preselected inputs before and after. Batch round trips; report database and wall timings separately. Require controlled v0 latency route (20% and 5 ms plus noise check), 30% less target shared-block work, improved plan, and measured healthy/read and write/error coverage. Distinguish tested-input improvement from production qualification.
- [x] Journal the authorized target and intended name before issuing one `CREATE INDEX CONCURRENTLY`. Check validity, definition and size afterward. On cancellation or lost acknowledgement, use the durable record and exact ownership marker to inspect target state before cleanup. Restart reconciles unfinished fixture runs with the same supplied target identity. Leave ambiguous/unowned objects intact. Cleanup removes the exact owned fixture objects only. Concurrent duplicate invocation defers under the session lock.
- [x] Test the normal path, healthy no-change, misleading/duplicate proposal, missing verification, interruption after the build, restart reconciliation, refusal of unowned objects, and fixture cleanup. Run `go test -count=1 ./...`, focused PostgreSQL integration, investigator tests, and one bounded Rookery trial. Check the public catalog fingerprint before/after. Document measured timings and distinguish deterministic evaluation from live Anthropic evidence.
- [x] Record the technical choices, strongest alternatives and results. Publish a source-grounded learning view for the slice, review and present the index milestone. Any broader enablement requires a new qualification. Live model evidence is recorded. [Jared accepted the index milestone](progress.md#index-milestone-accepted-november-13-2026). See [measured results and decisions](index-slice.md).

## Background worker

The index milestone proved the repair loop on an isolated fixture. The worker makes the detection loop run on its own. It reads counters PostgreSQL already collects for queries a project has opted to monitor. Go screens those measurements against each query's target. Repeated breaches open one investigation in Pooltune's database, where it survives a restart.

Start with one project and one kind of signal, an enrolled mean-latency objective. The v0 experiment uses a 60-second collection interval, five-minute windows, and three breaches in the latest five comparable windows. Sampling more often can shorten the wait for evidence and adds more database reads. The test will shorten its clock to exercise the same rules quickly. Real workload measurements will determine the release cadence.

### Task 6: Collect and screen normal traffic

Files: `cmd/pooltune-worker/main.go`, `internal/hostedquery/worker.go`, `internal/hostedquery/evidence.go` and their tests. Depends on Task 1. Decisions: d-language r2 and d-reuse r1. Use the objective path in [detection and verification](detection-verification-v0.md).

- [x] Write tests for healthy queries, one spike, sustained breaches, no calls and counter resets. Include distinct query objectives and missing samples.
- [x] Add a bounded, cancellable polling loop. Read only enrolled top-level PostgreSQL counter entries and their generation, with a timeout. Calculate interval deltas and combine them into comparable windows by total execution time and total calls. Share counter validation and `DetectObjective` with the lab while preserving the fixture's exact-count test. The worker emits a result for each finished window, including insufficient evidence when readings cannot be compared.
- [x] Verify with `go test -count=1 ./internal/hostedquery ./cmd/pooltune-worker` on the worker branch. The sampler, state store and command landed together so the command can resume partial windows.

### Task 7: Persist evidence and queue one investigation

Files: `internal/hostedquery/worker_store.go`, `internal/hostedquery/worker_store_integration_test.go` and the worker command. Depends on Task 6. Decisions: d-hosting r1, d-reuse r1 and the proposed d-durability r1.

- [x] Write PostgreSQL tests for overlapping worker ticks, replay of the same window, separate project identities and restart with a partially collected history.
- [x] Store the latest counter, partial window and recent completed windows in Pooltune's PostgreSQL. Commit each evidence update and its investigation insert together. Use a database-backed uniqueness rule for one open investigation per project, database, role and query. The proposed local implementation uses PostgreSQL for both jobs and evidence. That keeps intake consistent in one transaction and makes Pooltune responsible for the later job claim and recovery logic. The production queue decision remains open.
- [x] Run the local integration scenario with healthy traffic and sustained breaches. Verify the healthy query creates zero jobs and the slow query creates one job even after a worker restart. Run `go test -tags=integration -count=1 ./internal/hostedquery ./cmd/pooltune-worker` and save the [local and Rookery traffic timings](worker-traffic.md).

### Task 8: Deliver a queued investigation to LangGraph

Depends on Tasks 4 and 7. Decisions: d-workflow r1, d-reuse r1 and proposed d-durability r1.

The Go consumer uses the same PostgreSQL queue as the worker. It reserves a job briefly, gathers a measured plan and checks existing indexes, then sends that evidence through the existing one-call graph. A finished attempt records the recommendation or why the job could not proceed.

- [x] Add PostgreSQL job claims and guarded completion in `internal/hostedquery/worker_dispatch.go`. Test overlapping consumers, expired claims, stale completions, bounded retries and missing evidence. Reserve for 30 seconds and limit the whole attempt to 25 seconds. Preserve the current producer's job identity and suppress new jobs for an unchanged objective breach during the v0 evidence reuse window.
- [x] In `internal/hostedquery/worker_traffic.go`, pass the existing lab investigator through to the consumer. Collect fresh evidence for the exact synthetic query whose durable job was claimed. Verify its plan query ID and catalog identity. Generic enrolled queries need a qualified evidence source before they can reach the model.
- [x] Measure claim-to-result latency and verify the saved job and LangGraph checkpoints. Keep a record of the fastest production alternatives and their trade-offs in [Investigation handoff](worker-investigation.md).

### Task 9: Run the consumer continuously and measure its load

Depends on Task 8 and reuses d-workflow r1, d-reuse r1 and proposed d-durability r1.

- [x] Add `cmd/pooltune-consumer` and reuse the existing graph runner. Check Pooltune's state on an idle interval, process one job at a time, back off state connection errors, and honor shutdown signals. Serialize active claims per project. Open a target connection only once a job matches explicit enrollment.
- [x] Collect fresh evidence from a journaled synthetic fixture that outlives the consumer. Match the job's database, role and query ID to the enrolled lookup, and match the target objects to the recorded fixture. Use bounded read-only PostgreSQL transactions. No arbitrary SQL or repair execution.
- [x] Kill and replace a real consumer process mid-claim. Verify the replacement respects the lease, reclaims the same job, sees an index added while it was offline, and saves the new outcome.
- [x] Compare matched Rookery traffic with the consumer off and on. Report request latency, server execution times and additional evidence work. Record the trade-offs and reproduction in [Always-on consumer](always-on-consumer.md).

### Task 10: Verify the detector and consumer in hosted staging

- [x] Package the existing binaries and scripted graph in one image. Deploy two private processes and use separate state and synthetic target databases in the dedicated staging PostgreSQL instance.
- [x] Let the hosted detector create a job from bounded synthetic traffic. Hold evidence collection briefly, restart the hosted consumer and verify the same job completes on its second attempt and saves graph checkpoints.
- [x] Record verified service state, restart behavior, cleanup and staging trade-offs in [Hosted staging](hosted-staging.md).

### Task 11: Qualify restricted Supabase evidence

Depends on Task 10 and follows d-onboarding r1, d-reuse r1 and proposed d-identity r1.

- [x] Add a bounded planner and index reader for an explicitly enrolled ordinary table and integer equality lookup. Require exact statement, relation and role identities. Read estimated plans without executing the lookup. Classify estimates separately from measured repair evidence.
- [x] Qualify access under a restricted role using a disposable PostgreSQL fixture and the authorized Rookery route. Check stale counters, denied access, changed indexes and matching statements. Record the results in [Enrolled query evidence](enrolled-evidence.md).

### Task 12: Verify a dedicated Rookery login and revocation

Depends on Task 11. Records evidence for proposed d-identity r1.

- [x] Use a separate, short-lived test login to collect the enrolled estimate and index check through the verified Supabase session pooler. Verify session identity and table grants using the login itself.
- [x] Revoke the enrolled table grant on a separate administrative connection. Verify the open session loses access on its next statement. Disable login and test a new connection. Remove the test schema and role, then record the observed pooler behavior and trade-offs in [Dedicated login](dedicated-login.md).

### Task 13: Connect enrolled Supabase evidence to the hosted consumer

Follows d-workflow r1, d-reuse r1 and d-onboarding r1. The dedicated login qualification informs proposed d-identity r1.

- [x] Run a queued lookup through the consumer using a dedicated login and one explicit enrollment. Save the estimated plan and index check separately from measured evidence. Gate on the enrolled query identity and current grants. Test mismatched jobs and revoked access.
- [x] Verify the path in Railway staging against a bounded synthetic Supabase fixture and record the [hosted test result](enrolled-staging.md). Keep the existing measured fixture path available.

### Task 14: Run enrolled traffic through the hosted detector and consumer

Depends on Tasks 10 and 13. Follows d-workflow r1, d-reuse r1 and d-onboarding r1. This test supplies evidence for proposed d-identity r1.

- [x] Run the Go detector with the dedicated Rookery login and matching query enrollment. Restrict the detector to statements executed by that authenticated role. Keep the existing fixture routes.
- [x] Drive indexed and unindexed synthetic traffic while Railway runs the detector and consumer. Verify the detector alone creates one durable job, the healthy lookup creates zero jobs, and the consumer saves estimate-only evidence. Record checks, cleanup, and trade-offs in [Enrolled Rookery traffic in hosted staging](enrolled-traffic-staging.md).

### Task 15: Carry measured traffic into the enrolled investigation

Depends on Task 14 and follows d-workflow r1 and d-reuse r1.

- [x] Keep the sample counts for the five decision windows already measured by the detector. Read them with the existing queued job and preserve the valid, recent observations separately from the planner estimate.
- [x] Record why the enrolled missing-index investigation stops here. Cover older jobs without sample counts, stale measurements, healthy traffic, and exact reuse of fresh detector output. Verify the consumer makes no extra lookup, and document the [results and trade-offs](observed-traffic-diagnosis.md).

### Task 16: Reuse the detector's measured database page accesses

Depends on Task 15 and follows d-workflow r1 and d-reuse r1.

- [x] Keep shared-page access counts from the same five measured windows. Reuse the detector's existing counter reads. Preserve legacy timings when page data is unavailable or malformed.
- [x] Verify the normal-traffic counts can distinguish the staging fixtures without another query execution. Keep whole-query counters separate from measured plan work and record the remaining gap for the index recommendation in [Observed page accesses](observed-page-access.md).

### Task 17: Qualify one measured lookup and the index investigator in staging

Depends on Task 16 and follows d-workflow r1, d-reuse r1, and the isolated qualification scope of Tasks 4–5.

- [x] Keep the existing estimate-only mode available. Add an explicit staging mode for at most one bounded measured read per job, using the exact authorized synthetic lookup, only when fresh traffic, the current plan estimate and the index check support it. An interrupted attempt must not replay the measurement on retry.
- [x] Pass the measured plan and index check to the existing LangGraph investigator. Save the recommendation and timings without applying an index. Verify healthy, stale, already-indexed, and out-of-scope cases skip new work. Record the [staging result and trade-offs](measured-lookup-staging.md).

### Task 18: Qualify the guarded index repair and recovery from Railway

Depends on Task 17 and reuses the isolated fixture, index guard, and action journal from Tasks 4–5.

- [x] From Railway's staging network, run the existing index trial against a new private Rookery fixture. Record matched target reads, healthy reads, and value-changing updates before and after, index validity and size, timings, and fixture cleanup.
- [x] Use a separate fixture for a real process-stop drill after the concurrent DDL returns and before verification. Run recovery with fresh connections, verify catalog and journal state, run recovery again, and verify cleanup. See [Hosted index repair](hosted-index-repair.md).

### Task 19: Run the queued index repair as one staged workflow

Depends on Tasks 17–18. Reuses one isolated, journaled fixture and the existing investigation and repair contracts.

- [x] Run detector, durable queue, graph, and guarded repair on the same fixture and queued job. Save the result and timings together. Keep a separate staging switch for authorized fixture mutations.
- [x] Verify a healthy control, matched before/after reads and writes, and no duplicate build. Record one traffic-to-result timeline and fixture cleanup in [Queued index repair](queued-index-repair.md).

### Task 20: Run targeted ANALYZE through the shared queue

Depends on Task 19. Follows d-workflow r1, d-reuse r1, and repair 2 of the V1 design.

- [x] Reproduce stale lookup-column statistics on a disposable fixture with an existing index. Qualify a worthwhile query with a measured 10x row estimate error. Use the shared graph and exact Go-gated column-scoped ANALYZE.
- [x] Reuse the matched test-input baseline, measure plan accuracy and reads/writes after, and record a single Railway timeline. Keep pending actions from repeating automatically. Verify a healthy control and fixture cleanup. [Targeted ANALYZE results](targeted-analyze.md)

### Task 21: Qualify richer planner statistics through the shared queue

Depends on Task 20. Follows d-workflow r1, d-reuse r1, and repair 3 of the V1 design.

- [x] Use two representative inputs with a measured row-estimate error after a current ordinary ANALYZE. Verify a relationship between the two predicate columns and check for existing extended statistics before applying a scoped MCV statistics object.
- [x] Verify both inputs, protected reads and writes, added analysis time and statistics storage. Run one Rookery/Railway timeline and retain the outcome and cleanup evidence. [Rookery job 38](richer-statistics.md#one-railway-run-on-rookery)

### Task 22: Qualify live investigator decisions across the three implemented repairs

Next milestone, ahead of another repair type. Jared identified the missing live decision test after Task 21. Tasks 17–21 verified scripted workflows and database operations. Tasks 20–21 return their recommendations in graph screening before invoking the investigator. Their timings establish no Claude decision latency or diagnosis quality.

- [x] Extend the live investigator evidence and structured response to cover the implemented index, targeted ANALYZE and richer-statistics operations. Give it the measured cause evidence and eligible choices. Keep Go responsible for admission, current permissions, bounded execution and verification. Document the prompt, evidence available to the model and which decisions remain deterministic.
- [x] Evaluate live decisions on supported examples, ambiguous causes, misleading evidence and healthy controls. Record which controls screening closes, which cases reach Claude, the actual request count and whether each answer matches the evidence. Verify failure, timeout and unsupported recommendations stop mutation.
- [x] Exercise the hosted path with live Claude on authorized Rookery fixtures. Measure evidence collection, model request, graph overhead, action, verification and total elapsed time. Save model identity and token usage when returned. Verify the proposed action and measured outcome through the same Go executor, then clean up.
- [x] Update the repair reports with the live results and remaining limitations before continuing to bounded VACUUM. A scripted graph run alone does not complete this milestone.

Results and trade-offs are recorded in [Live Claude repairs](live-repairs.md). Live diagnosis remains bounded by the fixture evidence and Go admission checks.

### Task 23: Qualify mixed traffic for the three live repairs

Jared requested sustained-slowdown detection, brief-spike rejection and Pooltune overhead measurement with Claude, before moving to the remaining three repair families. Reuse the authorized private Rookery fixtures, Railway services and bounded repair tools.

- [x] Add a reusable workload with independent sessions, varied enrolled inputs, healthy reads and committed value-changing writes. Record offered requests, completed throughput, failures, missed request slots and client latency including scheduling delay.
- [x] Compare matched monitoring-off/on phases in reversed order. Record database execution time, statement counts and shared-page accesses. Distinguish these measurements from CPU, memory and physical disk load.
- [x] For each implemented repair, establish healthy traffic, induce a short spike, verify zero queued jobs, then induce a sustained slowdown and measure detection, live Claude, execution and verification. Continue traffic through the repair and record interference and cleanup.
- [x] Document the results and limits. Carry the same workload and live-model requirements forward to bounded VACUUM, enrolled function memory and enrolled Cron scheduling. Extend each fixture with that repair's specific evidence and guards.

The bounded traffic qualification passed after fixing healthy-table lock interference. All three final runs used live Claude and completed 5,922 total requests without errors or missed slots. [Results, timings and open production limits](mixed-traffic.md). Same-table committed-write qualification and higher-load calibration remain open.

### Task 24: Bounded plain VACUUM through the same live traffic harness

**Authority and boundary:** Jared accepted continuation after Task 23 with “lets keep building the next repairs with this same harness.” This authorizes the remaining three repair families in their existing order and private staging scope. Existing ask/milestones settings persist. No automatic stop between these already requested repair tasks is introduced. Present the combined evidence for human review, and stop for an actual approved-design conflict. No customer rollout, customer data mutations, public-schema changes or push to GitHub.

**Files:** new `internal/hostedquery/vacuum_*.go` and tests; modify the shared evidence/proposal/dispatch types, fixture lifecycle, consumer/lab CLI, `investigator/graph.ts`, `model.ts`, their tests, and `traffic.ts`.

**Interfaces:** `CreateVacuumFixture(ctx, target, state, route) (ConsumerFixture,error)`; `DispatchFixtureVacuum(ctx,state,config,route,fixture,investigate) (*DispatchResult,error)`; `IndexEvidence.Vacuum *VacuumEvidence`; typed proposal `repair=vacuum`; `DispatchResult.Vacuum *VacuumReport`; `-vacuum-fixture` and `-vacuum-staging-fixture`. The driver command is `node investigator/traffic.ts vacuum`.

- [x] Write and run failing local tests for positive visibility-loss evidence, healthy/no-change, wrong repair/identity, changed objects, active cleanup/blocker deferral, and interrupted-action reconciliation without blind repeat.
- [x] Create an isolated fixture within the existing table/connection/time/space budgets. Use a covering index and a bounded aggregate or range with measured index-only heap fetches. The existing index fixture is the lifecycle base. Plain VACUUM is the only repair power added. No FULL, ANALYZE side effect, threshold tuning, customer transaction cancellation or table rewrite. Detect visibility work using the v0 alternative clue: at least 1,000 heap fetches and at least 0.5 per returned scan row. Record the exact test query, inputs, settings and objective before the hosted run.
- [x] Persist intent before execution, recheck exact enrolled object identities and current conditions, execute bounded plain VACUUM outside a transaction, then verify actual outcome. VACUUM cannot be undone. An uncertain completion must inspect state and report uncertainty rather than automatically repeat. Preserve no-change and inconclusive outcomes when causal or benefit evidence fails. Preserve healthy-write concurrency.
- [x] Extend the shared prompt and graph so qualified cleanup evidence reaches real Claude and unsupported or healthy cases do not. Go must validate the typed recommendation independently. Add tests for weak evidence, declines, provider failure and mismatched proposals.
- [x] Extend the same traffic driver with operation-specific setup/short fault/recovery/sustained fault, expected results and objective. Reuse fixed offered demand, errors/misses, monitoring-off/on comparisons, live model timing and cleanup checks. Faults alter only owned test data. Prespecify benefit gates and do not lower them after observing a disappointing result.
- [x] Run local integration/race tests, TypeScript tests/checks, fresh review, and one final complete Railway/Rookery traffic run. Record before/after, per-stage time, interference, skipped spikes and limits. Commit only this task's files on the revamp branch.

### Task 25: Enrolled function memory with attributed spill evidence

Depends on Task 24's shared graph/driver extension. Build an explicitly enrolled, private, read-only synthetic function with fixed definition/identity, a bounded input set and measured internal sort spill. Adjust only that function's `work_mem`, preserve and restore its prior setting, and verify its definition/settings before action and on recovery. Predeclare tested concurrent calls, sort-node count and aggregate memory budget. Unknown real-project headroom cannot establish production memory safety. Use the same driver and live Claude; require lower latency plus at least 50% less attributed temporary writes, successful healthy traffic, no changed results, rollback/recovery and no-change controls. Record separate implementation details before coding this task. Do not tune a role, session default or whole database as the repair.

**Files:** new `internal/hostedquery/memory_*.go` and tests; shared evidence/dispatch/fixture cleanup, lab and consumer flags, graph/model/tests and traffic driver.

**Interfaces:** `CreateMemoryFixture`, `DispatchFixtureMemory`, `MemoryEvidence`, `MemoryReport`, `IndexEvidence.Memory`, `DispatchResult.Memory`, typed `repair=memory`, `-memory-fixture`, `-memory-staging-fixture`, driver mode `memory`. Exact context/config arguments match the VACUUM adapter. Source of bounded values: [function-memory.md](function-memory.md).

- [x] Test internal spill attribution, result preservation, budget/concurrency refusal, changed function body/owner/settings, invalid model selection, no-change and provider failure before implementing the action.
- [x] Build and enroll a private function with the documented 4 MiB original and 32 MiB candidate, known single-sort body and 96 MiB maximum test allowance for at most three calls. Record exact function identity and original setting durably. Preserve current shared source interfaces and ownership cleanup.
- [x] Implement action, measured verification and conditional restoration/reconciliation. Do not claim production headroom from a fixture allowance. Keep independent newer settings untouched.
- [x] Extend the same graph/Claude prompt and traffic harness with the prescribed one-call-per-second target rate, unchanged healthy traffic, frozen healthy-based objective, short and sustained faults, real model call and before/after temporary-write evidence.
- [x] Run local race/integration/graph tests, fresh review, hosted traffic qualification and documented results. Commit verified local work on revamp. A failed benefit or missing resource observation stays visible.
- [x] Follow up the first inconclusive hosted result by reusing one exact internal diagnostic plan per side under the same pinned data/definition/settings/input guards. Retain ten actual function samples, all latency/spill/protection/noise gates and the same workload/budget. Test actual diagnostic execution count (one per sample phase instead of ten), retain attribution and recovery coverage, obtain narrow review, then run one separately documented hosted follow-up. Do not rerun unchanged trials until one happens to pass.

### Task 26: Enrolled Cron schedule with deadline-preserving traffic verification

Depends on Task 25's shared interfaces. First inspect pg_cron availability and privileges read-only. Use only explicitly created/enrolled test jobs with a stored original schedule, owner-defined allowed windows/frequency/deadline and known command identity. Jared explicitly approved enabling pg_cron on Rookery after the prerequisite probe (see progress). No other extension/server-configuration changes, existing-job rescheduling, target/customer-job cancellation or invented customer deadlines are authorized. Cleanup may unschedule only the exactly identified Pooltune-owned fixed SELECT 1 witness, including its diagnostic execution; this does not authorize canceling the target job or using a canceled witness as evidence. Match at least three affected/control periods and three future executions, show resource/lock contention rather than overlap alone, and preserve required completion counts and deadlines. Reuse the traffic driver and real Claude. If a real prerequisite is missing, implement and test the bounded contract locally, report the hosted block accurately, and continue independent authorized work. Record exact test schedule and safety limits before execution.

**Prespecified contract:** [enrolled-cron.md](enrolled-cron.md). `CreateCronFixture`, `DispatchFixtureCron`, typed `CronEvidence`/`CronReport`, `repair=cron`, `-cron-fixture`, `-cron-staging-fixture`, and driver mode `cron` extend the existing shared interfaces. Record owned procedure/job identity and only the permitted alternate window. Keep the 25-second claim timeout; introduce an immediately-defaulted queue not-before time and an atomic, uniquely linked delayed verification continuation.

- [x] Test job identity, three-period evidence and attribution, frequency/deadline/window refusal, changed schedule/command/owner, declines and provider errors before implementing mutation.
- [x] Implement durable action intent, guarded schedule-only mutation, atomic delayed verification scheduling, restart recovery and conditional reconciliation. Never mark an applied schedule verified before observing its required future executions.
- [x] Extend the shared graph/prompt and traffic harness. Preserve real Claude, short-spike rejection, matched requests, independent healthy writes, actual scheduled execution history and measured contention.
- [x] Qualify the supported Supabase job API under REPEATABLE READ as the target-row concurrency guard, following [cron-provider-guard.md](cron-provider-guard.md). Native direct catalog row-lock authority is unavailable. Preserve identity checks, witnesses, deadlines and pending-action recovery; test concurrent target edits abort without overwrite or blind retry. No privilege grant or new customer power is authorized by this internal implementation choice.
- [x] Verify three future cycles, missed/duplicated execution refusal, owned cleanup and unchanged unrelated jobs. Run local tests, fresh review and one complete hosted qualification with separate action and cycle-wait timings. Commit the measured results and limits on revamp.

Task 26 hosted qualification passed on source `bc833c5`, deployment `753674b3-67f7-4ac3-b581-1f72c0afc2ad`. One Claude request selected Cron, Go applied the allowed schedule, verification child 112 survived consumer restart, and three future runs completed on time. Target database mean 33.641 → 0.0897 ms; 7,663 requests, zero errors/misses; owned cleanup and preservation passed. [Full timings, failed attempts and limits](cron-staging.md). The remaining-repairs technical batch is complete; combined human milestone acceptance and customer readiness remain separate.

## Candidate test application inspection (historical)


Read-only Supabase inspection on November 12, 2026 listed five projects: vv-dashboard (ACTIVE_HEALTHY), rookery (INACTIVE), reach (INACTIVE), hoppedIn (INACTIVE), and the older account-named project (INACTIVE). Listing vv-dashboard tables failed with `Transport closed`; a smaller catalog query returned `Failed to run sql query: Connection terminated due to connection timeout`. No live table contents or query behavior were verified.

Initially recommended candidate, subsequently selected by Jared: **rookery**, project ref `osdjqwpzgyzwfaxyufze`. Selection on November 12, 2026: “yes i give u full access, u can amke rookery available for a bit. we can use this”. Its local source at /Users/jshum/Desktop/code-folders/rookery has a small five-model Prisma schema on Supabase Postgres and scripts/load-test.mjs. Virtual users create separate annotation projects, sync shapes, reload and export, while recording request timings and comparing expected annotation counts with database counts. This provides an existing route to actual synthetic app traffic. It does not prove a repair is needed or exercise Supabase PostgREST/RLS behavior.

Before running: restore database availability, verify the app and database point to the same intended target, and establish a small bounded trial. The existing script defaults to 10/50/100 concurrent users without user think time, checks its size threshold only after each level, deletes its tracked projects on normal remote cleanup, and has no durable interrupted-run cleanup. It needs a separate qualification pass before reuse; it is not yet a Pooltune evaluation harness. Existing foreign-key indexes mean additional index benefit must be measured, not assumed. No project was resumed, no test data created and no load script executed during this inspection.

Follow-up authorization: Rookery may be brought online for the test work. The earlier sentence describes the initial read-only inspection only. A restore request has now been accepted by Supabase; readiness is tracked in progress.md. No load run is required to check connection and privileges. Later performance qualification needs representative data sizes/distributions and repeated comparable query executions; concurrency testing is a separate stage, especially for memory pressure and interfering jobs. High request volume alone is not an index-repair benchmark. Authorization for this personal test project does not define future customers' default grants or approve new V1 repair powers.


### Task 27: Bounded multi-cause investigator graph

**Authority/dependencies:** November 14 explicit build request above, existing architecture d-language r2/d-workflow r1/d-reuse r1 and approvals.json. Ask + milestones persist. No new intermediate human stop was agreed; final review covers this diagnostic slice. Earlier repair qualification is evidence, not customer rollout approval.
**Files:** investigator/diagnostic.ts, diagnostic-model.ts, diagnostic.test.ts; reuse graph.ts evidence field schemas; package.json and langgraph.json registration. Design: diagnostic-loop.md. Keep legacy graph behavior intact.
**Interfaces:** buildDiagnosticGraph(decide, read, checkpointer?, clock?) accepts typed identity, initial named observations and yields a proposal plus per-turn decisions/read timings. decide receives all available evidence and remaining budgets. read accepts only a named tool and an AbortSignal.
- [x] Write and observe failing behavior tests for multi-cause evidence and request→read→compare→proposal, one-call reuse, missing/repeated tools, expiry, identity, model failures and bounded turns.
- [x] Implement explicit screen_evidence, compare_causes, request_evidence and finish nodes with typed structured turns and deterministic final eligibility. Register the diagnostic Studio graph with unavailable tools when no Go reader is attached.
- [x] Run npm test and npm run typecheck; commit scoped implementation.

### Task 28: Go-controlled evidence requests

**Files:** internal/graphrunner/diagnostic.go and tests, investigator/diagnostic-run.ts. Existing Process/qualified repair route remains available.
**Interfaces:** DiagnosticProcess(stateDSN) returns a callable taking diagnostic identity/input and a named Go evidence reader. JSON lines carry one bounded request/response at a time, then a final result. No model SQL or target input. Reuse existing evidence via a snapshot adapter; allow supplied Go callbacks for fresh reads.
- [x] Write failing tests for real subprocess tool routing, repeated/unknown requests, cancellation, oversized/malformed frames, changed identity and proposal guard validation.
- [x] Implement bounded protocol, sanitized failures, inherited deadline and independently checked proposal identity/evidence. Persist graph state using existing Pooltune PostgreSQL route rules.
- [x] Exercise an actual local PostgreSQL reader and replay through the process; run Go tests/vet and TS suites; commit.

### Task 29: Real-Claude diagnostic qualification and map update

**Files:** diagnostic evaluation command/tests and evidence/diagnostic-loop results; diagnostic-loop.md, progress.md, roadmap.md, map/status.json and generated design.html.
- [x] Predeclare positive six-repair replay cases, competing statistics explanations, withheld evidence, healthy, insufficient and unsupported controls. Keep expected answers outside Claude input. Preserve original measurement timestamps/provenance; replay has no mutation authority.
- [x] Use real Claude through the Go request bridge, save decisions and model/tool/total timings, including failed attempts. Demonstrate fresh local PostgreSQL reader separately. No new Rookery mutations or Railway deployment required by this diagnostic-only qualification.
- [x] Obtain fresh code review, fix material issues with regressions, run required suites, update scoped map status and report remaining collector/production gaps.
- [x] Commit on feat/pooltune-revamp-dev; final human review. No push/merge or customer deployment.


Tasks 27–29 completed for the explicitly bounded diagnostic slice. Sonnet passed 15/15 cases and real fresh PostgreSQL evidence collection; Haiku remained 13/15. Public process resume and model-attempt persistence passed real local PostgreSQL tests. All earlier failed model evaluations remain in evidence/diagnostic-loop. Independent review material findings were fixed, and the remaining minor reporting defect was reproduced and fixed. At completion of Tasks 27–29, the deployed Railway consumer was not switched to this loop. Tasks 30–31 below subsequently qualified its owned single-column index route; the other five diagnostic routes remain. Final human review and any future deployment remain separate.

### Task 30: Connect diagnostic investigations to the hosted index consumer

**Authority:** Jared explicitly requested connecting the new investigator to the Railway consumer, testing the full Rookery flow with Sonnet, healthy traffic and restart, and committing on revamp. Ask + milestones and native execution persist. Existing decisions d-language r2, d-workflow r1 and d-reuse r1 apply. No new intermediate human stop; final review of this integration. No customer rollout, remote git push or expanded repair grants.
**Design:** diagnostic-loop.md. First hosted adapter uses the already qualified index fixture. The other five executors retain the legacy path until separately qualified with the diagnostic adapter. Reuse sufficient initial evidence; never hide existing evidence just to force another model call.
**Files:** internal/graphrunner/consumer.go and tests; internal/hostedquery/diagnostic_reader.go and integration tests; index_policy.go; cmd/pooltune-consumer/main.go and tests.
**Interfaces:** AdaptDiagnostic(DiagnosticInvestigator, hostedquery.DiagnosticEvidenceReader) returns IndexInvestigator. FixtureDiagnosticEvidence(state, targetConfig, route, fixture) returns a named, identity-bound reader. Proposal retains diagnostic timings, request history and attempt counts in the existing action report.
**Review focus:** changed enrollment/fixture, unknown evidence tools, incomplete observations, healthy bypass, and interrupted/duplicate execution. Existing action guards, 25-second consumer deadline and 30-second evidence expiry stay in force.
- [x] Write failing tests for adapter evidence reuse, requested live evidence routing, unavailable tools, metadata persistence, errors and identity refusal. Add real PostgreSQL reader tests for exact ownership and read bounds.
- [x] Implement opt-in diagnostic mode for the index fixture only; register bounded plan/index/statistics-inspection readers and retain all independent execution guards.
- [x] Run Go race unit/vet, focused PostgreSQL integration and investigator tests/typecheck. Obtain fresh whole-change review and fix material findings before staging mutation.
- [x] Commit the tested integration on feat/pooltune-revamp-dev.

### Task 31: Hosted Sonnet flow, healthy traffic and restart

**Dependencies:** Task 30 verification. Existing Railway staging and synthetic Rookery scope only. Source upload is authorized; no new provider/service/customer grant.
**Files:** docs/design/hosted-query-agent/hosted-diagnostic.md, evidence/hosted-diagnostic, progress.md, roadmap.md, map/status.json and generated design.html.
- [x] Deploy the reviewed source to existing Consumer staging and verify the exact deployment succeeded. Use real Sonnet 5.5 with tracing disabled.
- [x] Run bounded enrolled candidate and healthy traffic. Preserve a queued, unclaimed job through an actual Railway consumer-service restart, then run the new diagnostic consumer through guarded index application and read/write verification. Record that this tests queued recovery, not an in-flight model resume; local checkpoint-resume proof remains separate.
- [x] Capture detection/window time, restart/queue wait, baseline evidence, model turns/tool requests, index build and verification separately. Assert healthy query creates no job or repair, no duplicate action, exact outcome readback, cleanup and unchanged public indexes. Preserve unsuccessful attempts.
- [x] Document results and limitations in plain English, update map scope truthfully, verify generated map and source checks, commit on revamp.

### Task 32: Ordered compound-index evidence and guarded execution

**Authorization:** Jared's November 14 “yea lets just tyr to build this all out...” follows the proposed compound-plus-five-connections parallel build. Existing ask + milestones settings remain; no new intermediate human stop was agreed. Keep local commits on feat/pooltune-revamp-dev. This batch ends in a review of the completed implementation and honest live qualification; customer rollout is excluded.
**Decisions:** Preserve d-language r2 (Go), d-workflow r1 (one TypeScript/LangGraph investigator), d-hosting r1, d-reuse r1 and all six V1 repair scopes. This adds a bounded two-column nonunique B-tree case within the existing index capability.
**Files:** index policy/trial/catalog/sample and queued fixture code in internal/hostedquery; matching investigator/graph.ts and diagnostic schemas/Go bridge; focused compound files/tests. The compound owner coordinates changes to shared index and schema files. Controller owns consumer CLI and documentation.
**Interfaces:** Preserve the existing scalar column identity for old fixtures; represent an optional, exact ordered list of two enrolled column numbers for compound evidence/proposals. Missing lists retain old behavior. The model cannot supply SQL or arbitrary column names. Carry ordered identity through Go validation, TypeScript, saved evidence, proposal checks and recovery. Expose the new fixture/read/dispatch entry points to the controller before integration.
**Verification:** Tests must reject reversed/missing/mismatched keys, unsupported shapes, stale evidence, equivalent existing indexes, foreign objects and ambiguous recovery. Prove an actual two-column query needs the combined index and verify protected traffic. Local PostgreSQL and real Sonnet qualification are distinct required evidence.
- [x] Add failing identity/policy/catalog/recovery tests, then implement the ordered contract and exact Go construction.
- [x] Add an owned two-column fixture and before/after/recovery tests without broadening privileges or weakening benefit gates.
- [x] Verify Go race/integration and TypeScript tests/typecheck, obtain fresh review, and commit.

### Task 33: Planner repair evidence readers

**Dependencies:** Existing IndexEvidence and DiagnosticEvidenceReader interfaces stay compatible; Task 32's optional compound fields do not affect these scalar fixtures.
**Files:** New internal/hostedquery/diagnostic_planner_readers.go and focused tests only; existing planner helpers may be called only if bounded and read-only. Do not modify shared schemas, factory or consumer CLI.
**Interfaces:** Export AnalyzeDiagnosticEvidence(state *pgx.Conn, config *pgx.ConnConfig, route, fixture string) DiagnosticEvidenceReader and StatisticsDiagnosticEvidence with the same signature. Inputs remain tool name plus already enrolled IndexEvidence. Return unavailable for unsupported tools. Read fresh measured/catalog evidence under strict target/journal/object identity, bounded timeout and read-only transaction; never relabel baseline helpers that perform writes as inspection.
**Verification:** Real local PostgreSQL requests for plan/indexes/analyze/statistics as applicable; revoked/retired/mismatched identity, expired evidence and unsupported tools refuse; existing executor protections remain intact.
- [x] Write and run failing requested-evidence and refusal tests.
- [x] Implement the two readers and pass focused race-enabled integration tests.
- [x] Obtain fresh review and report evidence to controller for integration.

### Task 34: Cleanup, memory and scheduling evidence readers

**Dependencies:** Same stable reader interface as Task 33. Existing bounded executors and grants are unchanged.
**Files:** New internal/hostedquery/diagnostic_maintenance_readers.go and focused tests; family-specific helper changes only after coordinating with controller. No shared factory, schemas or CLI edits.
**Interfaces:** Export VacuumDiagnosticEvidence, MemoryDiagnosticEvidence and CronDiagnosticEvidence with the same factory signature as Task 33. Revalidate exact enrolled table/function/job identity and current journal status before collection. Memory preserves attribution and fixed admitted concurrency/budget. Cron uses exact generation, completed periods, allowed future windows and deadlines. Missing or unsafe observations return unavailable/refusal, never invented healthy evidence.
**Verification:** Real PostgreSQL bounded read requests, identity drift, retired fixture, unavailable tools and stale evidence. Cron local fake catalogs do not prove actual scheduling; retain real hosted scheduler qualification requirement.
- [x] Write failing evidence/refusal tests, implement readers, and run focused race-enabled integration checks.
- [x] Obtain fresh review and report evidence to controller.

### Task 35: Integrate and qualify all diagnostic routes

**Dependencies:** Tasks 32–34 interfaces and fresh source review before affected hosted runs.
**Files:** cmd/pooltune-consumer/main.go and tests; internal/graphrunner/consumer.go/tests if needed; existing staging traffic drivers; design/status/results/progress records.
**Interfaces:** Each fixture selects its exact reader and existing guarded dispatcher; no diagnostic path bypasses permissions, admission or action journal. Exactly one repair route is selected. Reuse sufficient initial evidence. Additional requests use the matching live reader.
**Verification:** Run affected Go/TypeScript suites, then serial Railway/Rookery qualification with real Sonnet for compound index and all five connections. Include healthy/unsupported controls, at least one actually requested live observation, queued restart/recovery, before/after reads/writes, model/reader/action/verification timing, cleanup and public-index preservation. Preserve failures. Missing hosted checks remain incomplete.
- [x] Add failing route-selection/adapter tests; connect each supported reader without weakening fixture scope.
- [x] Run local integration and fresh broad review; fix material findings before deployment.
- [x] Deploy reviewed source to existing Railway staging and run repair trials serially against owned Rookery fixtures.
- [x] Update map with only demonstrated scope, document trade-offs and results, commit locally and present final human review.

### Task 36: Connect Supabase and enroll live queries for monitoring

**Authority:** Jared requested connecting a real Supabase project, enrolling queries, following the design and committing on revamp. Inherited ask + milestones and native execution. Use the existing checkout at 9ffb545. The previously agreed stop before automatic database changes is unaffected. This slice grants no repair, and no intermediate review was agreed for the operator monitor.

**Design:** [Connect Supabase for read-only monitoring](supabase-monitoring.md). Reuse d-onboarding r1, d-language r2, d-hosting r1, d-workflow r1, d-reuse r1 and existing design/approval records.

**Files and interfaces:** `internal/supabaseproject/` derives the dedicated role and direct connection and generates owner setup SQL. `internal/hostedquery/project_monitor.go` reads the project snapshot and joins the existing state-backed detector. `cmd/pooltune-project` exposes setup, connect, run and disconnect. Existing fixture routes retain their contracts. Runbook and map record bounded results.

- [x] Prove the owner setup and dedicated identity with a failing real PostgreSQL test first. Cover distinct app/monitor roles, exact statement identity, absent/unenrolled counters, unauthorized grants, TLS and revocation.
- [x] Prove disconnect/unenroll with a failing queue test first. Implement registration, observation and disconnect using the existing polling lock/state and persist the detector handoff.
- [x] Qualify the normal entry points on local PostgreSQL and synthetic traffic on the real Rookery direct TLS endpoint. Observe a healthy and a slow query, restarts, revoked access and cleanup. Run Go race/vet and affected PostgreSQL tests, review and commit locally.

### Task 37: Diagnose a real enrolled query

**Authority:** Jared asked for the evidence reader next, real Sonnet on Rookery, and a local revamp commit. Inherited ask + milestones and native execution. Existing approval boundaries before automatic changes stay in force. Task 36's connection is the dependency and no further intermediate checkpoint was agreed for read-only diagnosis.

- [x] Build and test the optional owner-installed lookup grant and narrow live catalog reader.
- [x] Connect the existing managed queue to the same LangGraph in diagnosis-only mode. Recheck access during model admission and before saving a result.
- [x] Exercise detector, reader, and real Sonnet on owned Rookery traffic. Verify no unauthorized evidence or repair, cleanup, regression checks and scoped local commits.
