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

- Exactly six V1 repairs. The first slice qualifies nonunique indexes, including compound indexes. The other V1 operations remain targeted ANALYZE, extended/column statistics, bounded plain VACUUM and qualified cleanup settings, enrolled function-scoped work_mem, and enrolled future Cron scheduling.
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

- [ ] Write tests for healthy queries, one spike, sustained breaches, no calls and counter resets. Include distinct query objectives and missing samples.
- [ ] Add a bounded, cancellable polling loop. Read only enrolled top-level PostgreSQL counter entries and their generation, with a timeout. Calculate interval deltas and combine them into comparable windows by total execution time and total calls. Share counter validation and `DetectObjective` with the lab while preserving the fixture's exact-count test. The worker emits a result for each finished window, including insufficient evidence when readings cannot be compared.
- [ ] Verify with `go test -count=1 ./internal/hostedquery ./cmd/pooltune-worker` and commit the standalone sampler.

### Task 7: Persist evidence and queue one investigation

Files: `internal/hostedquery/worker_store.go`, `internal/hostedquery/worker_store_integration_test.go` and the worker command. Depends on Task 6. Decisions: d-hosting r1, d-reuse r1 and the proposed d-durability r1.

- [ ] Write PostgreSQL tests for overlapping worker ticks, replay of the same window, separate project identities and restart with a partially collected history.
- [ ] Store the latest counter, partial window and recent completed windows in Pooltune's PostgreSQL. Commit each evidence update and its investigation insert together. Use a database-backed uniqueness rule for one open investigation per project, database, role and query. The proposed local implementation uses PostgreSQL for both jobs and evidence. That keeps intake consistent in one transaction and makes Pooltune responsible for the later job claim and recovery logic. The production queue decision remains open.
- [ ] Run the local integration scenario with healthy traffic and sustained breaches. Verify the healthy query creates zero jobs and the slow query creates one job even after a worker restart. Run `go test -tags=integration -count=1 ./internal/hostedquery ./cmd/pooltune-worker` and commit the durable queue producer.

### Investigation handoff after worker intake

A follow-up consumer will claim jobs with an expiring reservation and bounded retries. It will send validated query-plan and existing-index evidence to the current one-call LangGraph investigator, or record that evidence is missing. An integration test will show a queued investigation reaching the graph with measured evidence from an isolated fixture. The automatic index repair remains on the qualified fixture path until target-specific evidence collection, grants and capacity checks are ready.

## Candidate test application inspection (historical)


Read-only Supabase inspection on November 12, 2026 listed five projects: vv-dashboard (ACTIVE_HEALTHY), rookery (INACTIVE), reach (INACTIVE), hoppedIn (INACTIVE), and the older account-named project (INACTIVE). Listing vv-dashboard tables failed with `Transport closed`; a smaller catalog query returned `Failed to run sql query: Connection terminated due to connection timeout`. No live table contents or query behavior were verified.

Initially recommended candidate, subsequently selected by Jared: **rookery**, project ref `osdjqwpzgyzwfaxyufze`. Selection on November 12, 2026: “yes i give u full access, u can amke rookery available for a bit. we can use this”. Its local source at /Users/jshum/Desktop/code-folders/rookery has a small five-model Prisma schema on Supabase Postgres and scripts/load-test.mjs. Virtual users create separate annotation projects, sync shapes, reload and export, while recording request timings and comparing expected annotation counts with database counts. This provides an existing route to actual synthetic app traffic. It does not prove a repair is needed or exercise Supabase PostgREST/RLS behavior.

Before running: restore database availability, verify the app and database point to the same intended target, and establish a small bounded trial. The existing script defaults to 10/50/100 concurrent users without user think time, checks its size threshold only after each level, deletes its tracked projects on normal remote cleanup, and has no durable interrupted-run cleanup. It needs a separate qualification pass before reuse; it is not yet a Pooltune evaluation harness. Existing foreign-key indexes mean additional index benefit must be measured, not assumed. No project was resumed, no test data created and no load script executed during this inspection.

Follow-up authorization: Rookery may be brought online for the test work. The earlier sentence describes the initial read-only inspection only. A restore request has now been accepted by Supabase; readiness is tracked in progress.md. No load run is required to check connection and privileges. Later performance qualification needs representative data sizes/distributions and repeated comparable query executions; concurrency testing is a separate stage, especially for memory pressure and interfering jobs. High request volume alone is not an index-repair benchmark. Authorization for this personal test project does not define future customers' default grants or approve new V1 repair powers.
