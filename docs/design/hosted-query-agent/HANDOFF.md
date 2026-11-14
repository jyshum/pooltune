# Pooltune implementation handoff

Prepared November 13, 2026, America/Vancouver. Start here in the implementation chat.

## Request and current authority

Jared's latest instruction: “no if that covers eveyrhting, save evryrhting, and start a new chat for implementation.” Implementation belongs in that new chat. This planning chat did not write product code, install dependencies or create test data. The product direction is sufficiently established to plan and implement the first vertical slice; this is not a claim that all interfaces, credentials or six repair contracts are complete.

Current repository: `/Users/jshum/Desktop/code-folders/pooltune`. The checkout was previously named `pooltune-learning`. This handoff describes the initial state. At that point, the checkout contained the older traffic-admission and pool-tuning product. See [progress.md](progress.md) for the hosted implementation and current layout.

Apply Jared's skills at `/Users/jshum/Desktop/code-folders/jared-skills`, beginning with `crocking/skills/using-superpowers/SKILL.md` and workflow-policy. Continue with writing-plans and the appropriate execution skills. Use plain explanations and concrete progress updates. Do not re-ask already selected stack, deployment or workflow preferences.

## Workflow choices already selected

On November 13, Jared answered the native interactive questions:

- Design conflict mode: **ask**. Exact selection: “Ask me first”. Ask before changing agreed architecture/scope. Routine within-design details remain autonomous.
- Human review mode: **milestones**. Exact selection: “Pause at agreed milestones”. The question explicitly recommended reviewing the working test setup before enabling automatic database changes.
- **M1:** demonstrate the working isolated test setup, healthy comparison case and real measurements; present code, checks and limitations. Pause for acceptance before enabling the agent's automatic index changes.
- After M1, the next deliverable is the complete index flow and recovery evidence for review. Do not invent acceptance of either milestone.

An attempted managed worktree creation was cancelled. Its tool returned: “The worktree request was cancelled. Do not create a worktree or retry this request.” No worktree was created. Honor that dismissal; do not recreate or automatically retry it. The source checkout was on `main`; use an appropriate local feature branch in the existing checkout for product implementation without pushing/merging to main. No publication, remote push or deployment to customers is authorized by this handoff. The final planning-document commit is local.

## Read these records in order

1. `docs/product/query-performance-v1.md`: accepted product, deployment, stack and boundaries, including recent evidence-access and scheduling decisions.
2. `docs/product/backend-language-decision.md`: Go supersedes Rust; do not reopen the language decision.
3. `docs/design/hosted-query-agent/design.md`, `graph.json`, `approvals.json`, `design.html`: responsibilities, decision history and unresolved design details.
4. `docs/design/hosted-query-agent/detection-verification-v0.md`: complete numerical policy proposal and its qualification cases.
5. `docs/design/hosted-query-agent/implementation.md`: current planning status and Rookery findings. **Replace/expand this into an executable task plan before coding. It is presently a planning placeholder.** Preserve the existing findings and constraints.
6. `docs/design/hosted-query-agent/progress.md`: current authority and historical findings. The Current handoff state section supersedes stale historical status entries.
7. `docs/research/query-agent/README.md`: archived research index. Research is evidence, not a competing product specification.

## Accepted product and stack

Pooltune maintains important Supabase query performance as data, schemas and workloads change. It detects worthwhile problems, investigates, makes permitted repairs and measures outcomes. Initial customer: a small startup without a dedicated database specialist. V1 is hosted Supabase/PostgreSQL only.

Exactly six repair types:

1. Qualified nonunique indexes, including compound indexes; no deleting customer-created indexes.
2. Targeted ANALYZE.
3. Suitable extended statistics or bounded column-statistics targets, followed by analysis.
4. Bounded plain VACUUM; selected cleanup-threshold adjustments when late triggering is demonstrated.
5. Enrolled function-scoped work_mem within qualified memory/concurrency budgets.
6. Future scheduling changes for enrolled Cron jobs inside owner-authorized business constraints.

No pool resizing, WAL/checkpoint tuning, index rebuilding, arbitrary SQL/app/RLS rewrites, customer transaction/job killing, compute purchases, replicas or migrations. Diagnosis can identify unsupported causes without claiming an automatic repair.

Architecture:

- **Go:** backend, scheduled collection, deterministic detector, admission/permissions, controlled evidence/experiment/repair tools and recovery.
- **TypeScript + LangGraph:** one investigator with Anthropic calls and structured tools. No product subagent team. Preserve inspectable graph structure; LangGraph Studio is a development viewer, not the customer dashboard. Studio hosting/tracing choices remain open.
- **Pooltune-owned PostgreSQL:** operational history, selected evidence, checkpoints, jobs and action records. Do not store this as a replica of customer application tables or silently replace it with old SQLite architecture.
- Fully hosted monitoring continues with browser/laptop closed. Customer need not rent a server, install a proxy or supply an Anthropic key. Pooltune supplies model access; never distribute its provider secret.

A local development demonstration of these services is appropriate for the first slice, but it must not be described as the completed hosted service.

## Recent product decisions that must survive handoff

- **Capability-first evidence:** collect relevant diagnostics, inputs or bounded samples as needed under disclosed upfront customer grants. Do not impose a blanket redaction rule that destroys necessary diagnostic context. Encrypt stored/transmitted data, isolate accounts, protect credentials and define retention. Encryption does not conceal inputs from the model provider processing them. Rich evidence does not expand repair authority.
- **Minimal customer work:** adaptive scheduling with tested defaults. Run eligible work when conditions support it; maintenance windows are optional overrides. Enrolled jobs still need deadlines/frequency constraints. Defer heavy work without room, then explain persistent deferral. Do not promise fixed CPU reservations, zero impact or universally optimal performance.
- **Evidence reuse:** use valid existing measurements first. Additional experiments must resolve a specific decision gap. Prefer normal-traffic verification; do not generate redundant benchmark work.
- **Measured outcomes:** distinguish plan estimates from execution results. Preserve improved/regressed/inconclusive/failed/no-permitted-repair outcomes. Healthy/no-change is a success case for agent behavior.
- **Recovery:** persist intent, reconcile uncertain completion before retrying, cancel only our identified work, and recover per operation. Not every change can be undone or paused. LangGraph checkpoints do not guarantee exactly-once external changes.

## First vertical slice and tests

Build **collect → detect → investigate → qualified index repair → verify**, including a healthy query that receives no repair. The test harness must supply actual repeatable database work and assert agent behavior; manually running SQL does not demonstrate the finished agent.

M1 should establish a bounded, isolated synthetic fixture and measured healthy/actionable cases, plus relevant detector/tool tests. Keep real application tables/indexes intact. Do not manufacture a live incident by removing a useful application index. Only after M1 acceptance enable automatic index changes. Later qualification must include interrupted/uncertain actions, misleading input-mix improvement, missing telemetry/counter reset, and rejected out-of-scope requests.

The v0 thresholds are explicit calibration candidates, not proven constants or grounds to pretend that 30 minutes of evidence already exists. Historical healthy baseline detects regressions; current pre-repair baseline measures benefit; other-query pre-change baselines protect against harm. Allow the intended repair as the treatment difference but exclude unrelated changes. Guard coverage must actually be observed; missing write/error/cohort evidence means incomplete qualification. Synthetic fixture findings are not production-wide performance proof.

## Rookery and environment

- Selected Supabase project: **rookery**, ref `osdjqwpzgyzwfaxyufze`, region `us-west-2`, PostgreSQL 17.6.
- Jared explicitly authorized using it and bringing it online: “yes i give u full access, u can amke rookery available for a bit. we can use this”. It was restored; latest observed status ACTIVE_HEALTHY. A read-only query through Supabase MCP returned database `postgres`, role `postgres`, version `17.6`.
- **No fake traffic, schema/role changes, index builds or performance experiments have run.** MCP success is not proof of Pooltune OAuth provisioning/native connectivity or least-privilege access.
- Rookery remains online. No timed pause is scheduled or promised. Recheck status before testing.
- App source: `/Users/jshum/Desktop/code-folders/rookery`. Read its AGENTS.md before code changes. It uses Prisma/Next.js on Supabase; its local fallback can run without a DB, so verify actual persistence during tests. It does not exercise PostgREST/RLS by default.
- `scripts/load-test.mjs` exists, but defaults to 10/50/100 concurrent users without think time. Its storage check runs after each level; cleanup lacks durable interrupted-run handling. Qualify/adapt it before use. It is not already the Pooltune harness.
- Rookery `.env` has DATABASE_URL, DIRECT_URL and Supabase variables. Never print, commit or put secret values in prompts, reports or traces. Native connection validity is untested.
- Preflight found no Go executable in PATH or usual install locations. Node v26.3.0 was available. Docker CLI exists but daemon was unavailable. PostgreSQL 17.11 was installed through Homebrew, but the default local server was not responding. Choose/setup development prerequisites deliberately; do not silently change the Go architecture.
- No ANTHROPIC_API_KEY was present in the process environment. Real model execution needs secure credential configuration. Continue independent implementation/test work while arranging it; never claim mocked responses are a real investigator run. Do not ask Jared to paste a secret into chat.

## Technical details still to finish in the implementation plan

1. Fixed bootstrap template and actual monitoring/repair privileges, native reachability and credential lifecycle. Supabase Management API SQL execution offers a route, but exact Pooltune provisioning is untested. Retained broad OAuth tokens remain broad even with a restricted native login.
2. Index tool inputs, eligible objects, evidence freshness, time/lock/storage limits, action identity, durable recording, invalid-index cleanup and uncertain-outcome recovery. No arbitrary model SQL execution.
3. Own PostgreSQL job/checkpoint ownership, per-project concurrency and shared provider budgets; action recovery must survive model-budget exhaustion.
4. Concrete detector interfaces and Rookery calibration plan using the v0 defaults. Broad autonomous release requires per-repair qualification; the index slice does not qualify all six.
5. Hosting/auth/secret storage and evidence retention are not all selected. Choose routine libraries/interfaces within the accepted design; use Ask mode for meaningful design conflicts. Do not turn every package choice into another product brainstorm.

The HTML map is a generated design snapshot, not a running dashboard. Source validation/export passed, but unforced Explore/Back navigation remains incompletely verified from earlier inspection. This does not block backend planning; do not claim that viewer issue fixed.
