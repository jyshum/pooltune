# Pooltune V1 build sequence

The [V1 product plan](../../product/query-performance-v1.md) defines what we are building. The [interactive design map](design.html) shows how the pieces fit, while the [implementation plan](implementation.md) and [progress log](progress.md) show what has been built and tested. The map now shows verified staging scope, partial work and remaining nodes. It is a source snapshot; Studio shows investigator graph runs or replays.

## Where we are

The Go detector, durable queue and consumer run on Railway staging. The TypeScript/LangGraph investigator uses Claude to propose a repair; Go checks, applies and verifies it. A dedicated Rookery login passed evidence and revocation tests. Queued work survived a hosted restart.

Six bounded repair cases have passed live Claude and mixed-traffic qualification: a single-column index, targeted ANALYZE, joint statistics, plain VACUUM, function-scoped memory and enrolled Cron scheduling. The first memory trial was inconclusive and restored the old setting; a separately recorded follow-up passed unchanged benefit gates. Scheduling required provider, timing/readiness and model-response corrections before its complete run passed: one real model decision, one allowed schedule change, consumer restart, three future on-time executions and owned cleanup. All earlier failures remain recorded. These are synthetic staging cases, not general customer rollout.

[Plain-English overview](overview.md) · [Mixed-traffic results](mixed-traffic.md) · [Cleanup results](bounded-vacuum.md) · [Function memory](function-memory.md) · [Scheduling staging status](cron-staging.md)

The invite-only preview adds account creation, sign-in, manual Supabase setup, and an owner dashboard with saved poll, query, and diagnosis history. It runs observation and Sonnet diagnosis from the same worker as the operator command. The practical next step is the manual connection and seven-day read-only pilot on the intended project. [Preview signup and pilot guide](hosted-preview.md)

## Scope clarification

Compound indexes have always been accepted V1 scope. The bounded two-column case now passed real Sonnet and ongoing Rookery traffic with ordered evidence/proposal keys and exact recovery. The map separates this tested case from broader compound query shapes beneath an amber index family. Richer statistics and cleanup likewise separate tested operations from their remaining variants. All five diagnostic connections are implemented around existing tested repair tools; all five have passed the new hosted workflow. Jared authorized the compound-plus-connections batch. The list below remains the wider scope inventory; it does not imply customer rollout approval.

## What remains

The six-family evaluation batch is technically complete, with combined human review pending. Each has real Claude decisions, short-spike rejection, before/after traffic measurements, recovery checks and owned cleanup. A repair that executes successfully can still fail benefit verification; the memory trial is an example. The diagnostic graph compares causes and requests evidence. Sonnet now passed 18 bounded replay cases. Its index connection passed a hosted repair and queued-job restart. The bounded compound case, ANALYZE, joint statistics, cleanup, function memory and scheduling now passed the diagnostic workflow with real Sonnet and ongoing Rookery traffic. A separate hosted requested-evidence probe exposed a network round-trip deadline. The reviewed fix then passed live: Sonnet requested ANALYZE evidence, Go read it within 1.761 s, and the investigation finished in 8.338 s. Broader workload qualification and concurrent committed writes to the repaired table remain open. [Hosted result](hosted-diagnostic.md).

1. **Connect the pilot Supabase project.** The hosted dashboard guides the owner through direct TLS and exact query enrollment. Verify the intended project's app roles and traffic through a seven-day history, including monitoring gaps and diagnosis outcomes. One [owner-captured plan](enrolled-plans.md) and two [ordinary-traffic plan shapes](traffic-plans.md) reach Sonnet on enrolled staging traffic. Broaden the supported roles and query shapes using pilot evidence. [Preview guide](hosted-preview.md)
2. **Finish diagnostic qualification.** Qualify requested evidence across the remaining families, representative mixed causes and hosted mid-model recovery. All six bounded action routes passed; this does not close broader workload gaps. The separate ANALYZE requested-evidence probe passed. Existing execution guards remain unchanged. [Current expansion](repair-expansion.md).
3. **Qualify the full index path.** Expand beyond the tested single-column and two-equality compound cases to representative enrolled queries. Verify owner grants, current load, execution, protected reads and writes, rollback, and recovery before allowing automatic customer changes.
4. **Complete the V1 repairs.** Broaden scheduling beyond the one owner-approved quiet-window fixture and qualify cleanup settings beyond the bounded plain VACUUM case. Expand targeted ANALYZE beyond the first measured-error case. Qualify the richer statistics repair on representative traffic and cover the separate column statistics-target operation. Each repair needs its own evidence, grants, resource limits, verification, and recovery. The same investigator uses those tools.
5. **Finish production onboarding and the hosted product.** The preview already provides account sign-in, owner project access, manual setup, investigation history, and disconnect. Widen onboarding with email verification and account recovery. Expand the dashboard to longer-term reporting and the approved repair lifecycle when those routes are qualified.
6. **Qualify operations and rollout.** Preview isolation, bounded history, and diagnosis admission were tested. Qualify backup and restore, credential rotation, the behavior of connected projects during deploys and outages, and the full pilot week before broad rollout.

These are build stages within V1. The product plan defines the six capabilities and boundaries. It leaves V2 open.

## Hosting

Railway remains the staging provider. It already runs the two worker processes and Pooltune PostgreSQL and has verified restart recovery. An always-running process and durable state matter because deployments and crashes can interrupt work. [Railway restart policy](https://docs.railway.com/deployments/restart-policy)

Render is a close alternative with continuous background workers and private access to Render Postgres. AWS ECS on Fargate provides more control over networking and deployment behavior, at the cost of more cloud setup and operations. [Render background workers](https://render.com/docs/background-workers) · [Render private networking](https://render.com/docs/private-network) · [AWS ECS services](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/ecs_services.html)

Before selecting a production host, compare cost for two always-on workers plus PostgreSQL, latency to the Supabase regions we serve, backup and restore, and the effort of operating the platform. The staging test proves job recovery on Railway. Production uptime depends on the database, workers and recovery setup together.
