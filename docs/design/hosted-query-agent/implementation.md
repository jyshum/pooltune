# Implementation planning status

Not yet an executable implementation plan. Product scope and language responsibilities are accepted; tool contracts, connection authority, trial budgets and recovery protocols remain open in design.md.

## Implementation handoff authority

Read [HANDOFF.md](HANDOFF.md) first. On November 13, 2026 Jared explicitly requested a new chat for implementation. Before code work, expand this file into the executable plan under Jared's writing-plans skill; do not pretend the work below is already implemented.

Workflow settings are selected: design conflict mode **ask** (“Ask me first”); human review mode **milestones** (“Pause at agreed milestones”). M1 is the working isolated test setup with healthy comparison and real measurements, reviewed before enabling automatic index changes. No M1 acceptance exists. Do not re-ask these settings. Honor the cancelled worktree request. Product implementation has not begun.

## Global constraints

Preserve the six repair scopes, owner grants, customer data boundary, evidence-reuse policy and Go/TypeScript/LangGraph/PostgreSQL split. Do not treat a design-map approval as deployment permission. Restore the selected workflow settings and milestone boundary before code work.

Proposed first slice: the selected Rookery test project and one enrolled query, with observation, evidence inspection, a qualified index trial, verification and uncertain-action recovery. Rookery is online and a read-only SQL check through Supabase MCP succeeded. Select the representative query, concrete interfaces and evaluation criteria before writing tasks. Pooltune's own OAuth/native connection setup is not yet verified.

Carry forward the five accepted comparison lessons in the product plan: separate observation and authority; separate estimates and measured outcomes; accept justified inaction; reconcile interrupted actions before retry; diagnose broadly while keeping repair authority within the six scopes. Use design.md's handoff-readiness table to settle missing boundaries. Routine package and internal naming choices belong to implementation planning and do not reopen the product scope.

Scheduling direction selected under Jared's preference for performance and minimal customer effort: adaptive execution with tested defaults and optional maintenance-window overrides. Concrete operation budgets, pressure thresholds, deferral escalation and cancellation/reconciliation rules must be specified and qualified. Do not require customers to configure low-level database limits during ordinary onboarding. Preserve job-specific business deadlines and all existing scope grants.

Numerical detection and verification proposal: [detection-verification-v0.md](detection-verification-v0.md). It defines initial triggers, benefit routes, minimum evidence, per-repair mechanism tests, regression thresholds and Rookery calibration cases. It is a test candidate, not an approved production policy. The builder must distinguish historical detection baseline from current pre-repair benefit baseline; permit exactly the intended repair as a comparison difference; preserve fast intervention independently of longer outcome windows; and treat missing required guard coverage as inconclusive. No load tests or database repairs were performed to produce the proposal.

## Candidate test application inspection

Read-only Supabase inspection on November 12, 2026 listed five projects: vv-dashboard (ACTIVE_HEALTHY), rookery (INACTIVE), reach (INACTIVE), hoppedIn (INACTIVE), and the older account-named project (INACTIVE). Listing vv-dashboard tables failed with `Transport closed`; a smaller catalog query returned `Failed to run sql query: Connection terminated due to connection timeout`. No live table contents or query behavior were verified.

Initially recommended candidate, subsequently selected by Jared: **rookery**, project ref `osdjqwpzgyzwfaxyufze`. Selection on November 12, 2026: “yes i give u full access, u can amke rookery available for a bit. we can use this”. Its local source at /Users/jshum/Desktop/code-folders/rookery has a small five-model Prisma schema on Supabase Postgres and scripts/load-test.mjs. Virtual users create separate annotation projects, sync shapes, reload and export, while recording request timings and comparing expected annotation counts with database counts. This provides an existing route to actual synthetic app traffic. It does not prove a repair is needed or exercise Supabase PostgREST/RLS behavior.

Before running: restore database availability, verify the app and database point to the same intended target, and establish a small bounded trial. The existing script defaults to 10/50/100 concurrent users without user think time, checks its size threshold only after each level, deletes its tracked projects on normal remote cleanup, and has no durable interrupted-run cleanup. It needs a separate qualification pass before reuse; it is not yet a Pooltune evaluation harness. Existing foreign-key indexes mean additional index benefit must be measured, not assumed. No project was resumed, no test data created and no load script executed during this inspection.

Follow-up authorization: Rookery may be brought online for the test work. The earlier sentence describes the initial read-only inspection only. A restore request has now been accepted by Supabase; readiness is tracked in progress.md. No load run is required to check connection and privileges. Later performance qualification needs representative data sizes/distributions and repeated comparable query executions; concurrency testing is a separate stage, especially for memory pressure and interfering jobs. High request volume alone is not an index-repair benchmark. Authorization for this personal test project does not define future customers' default grants or approve new V1 repair powers.
