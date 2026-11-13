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
- Authorized scope is M1 in the existing checkout, on a local branch, with isolated synthetic work in authorized Rookery. Routine internal interfaces and lab parameters are delegated. Approved architecture and customer-facing boundaries require a ruling if they conflict with evidence.
- M1 acceptance is pending. The canceled worktree request stays canceled. No push, merge, publication or customer deployment.

## Human milestones

- [ ] **M1 acceptance.** Review Tasks 1–3, including the isolated local and Rookery setup, healthy/no-change and investigate decisions, actual measurements, state readback, cleanup, source and limitations. Obtain explicit acceptance before enabling agent-driven index changes.
- [ ] **Index slice review.** After M1, demonstrate investigation through verified outcome and recovery, including refusal of unsupported actions. Record actual live model evidence and explicit review before any later integration decision.

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

### Task 4: Investigate and qualify a nonunique index (after M1)

Consume an enrolled query identity, comparable observation IDs, catalog identities and evidence freshness from Go. The investigator has checkpointed LangGraph state and structured evidence/recommendation tools. A candidate carries project and enrollment IDs, table OID, ordered column numbers, intended mechanism and evidence IDs. Go resolves/quotes catalog identifiers and rejects stale, unenrolled, duplicate or unsupported targets. The first contract is an ordinary nonpartitioned table and B-tree over up to three approved plain columns; no raw SQL, unique/partial/expression indexes, or model-provided identifiers. Implement one per-project mutation lease and a durable action ID before execution. Before any implementation here, pin the exact grants, credential lifecycle, pressure/lock/storage limits, timeout, evidence freshness, queue, model budgets, and secure Anthropic configuration. Qualify Go denial independently of model output.

### Task 5: Execute, verify and recover (after M1 and Task 4)

Go admits only a fresh qualified candidate and executes a bounded concurrent build. Record intent, expected catalog definition and backend identity before execution. On uncertain completion, reconcile identity, validity/readiness and ownership before retry or cleanup; collisions and ambiguous ownership require a stop. Cancel only identified Pooltune work. Observe normal traffic first, use a gap-driven experiment only under grant and compare benefit and protection separately. Cover noisy input-mix shifts, reset/missing guards, interrupted action, existing valid/invalid indexes and repeated delivery. Record outcome, measured benefit, write/error/storage guards and recovery behavior. Complete concrete contracts and tests before enabling mutation.

## Candidate test application inspection (historical)


Read-only Supabase inspection on November 12, 2026 listed five projects: vv-dashboard (ACTIVE_HEALTHY), rookery (INACTIVE), reach (INACTIVE), hoppedIn (INACTIVE), and the older account-named project (INACTIVE). Listing vv-dashboard tables failed with `Transport closed`; a smaller catalog query returned `Failed to run sql query: Connection terminated due to connection timeout`. No live table contents or query behavior were verified.

Initially recommended candidate, subsequently selected by Jared: **rookery**, project ref `osdjqwpzgyzwfaxyufze`. Selection on November 12, 2026: “yes i give u full access, u can amke rookery available for a bit. we can use this”. Its local source at /Users/jshum/Desktop/code-folders/rookery has a small five-model Prisma schema on Supabase Postgres and scripts/load-test.mjs. Virtual users create separate annotation projects, sync shapes, reload and export, while recording request timings and comparing expected annotation counts with database counts. This provides an existing route to actual synthetic app traffic. It does not prove a repair is needed or exercise Supabase PostgREST/RLS behavior.

Before running: restore database availability, verify the app and database point to the same intended target, and establish a small bounded trial. The existing script defaults to 10/50/100 concurrent users without user think time, checks its size threshold only after each level, deletes its tracked projects on normal remote cleanup, and has no durable interrupted-run cleanup. It needs a separate qualification pass before reuse; it is not yet a Pooltune evaluation harness. Existing foreign-key indexes mean additional index benefit must be measured, not assumed. No project was resumed, no test data created and no load script executed during this inspection.

Follow-up authorization: Rookery may be brought online for the test work. The earlier sentence describes the initial read-only inspection only. A restore request has now been accepted by Supabase; readiness is tracked in progress.md. No load run is required to check connection and privileges. Later performance qualification needs representative data sizes/distributions and repeated comparable query executions; concurrency testing is a separate stage, especially for memory pressure and interfering jobs. High request volume alone is not an index-repair benchmark. Authorization for this personal test project does not define future customers' default grants or approve new V1 repair powers.
