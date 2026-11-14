# Pooltune V1 build sequence

The [V1 product plan](../../product/query-performance-v1.md) defines what we are building. The [interactive design map](design.html) shows how the pieces fit, while the [implementation plan](implementation.md) and [progress log](progress.md) show what has been built and tested. The map is a design snapshot from before the staged implementation and Studio shows actual graph runs or replays.

## Where we are

The Go detector, durable queue and consumer run on Railway staging. The TypeScript/LangGraph investigator uses Claude to propose a repair; Go checks, applies and verifies it. A dedicated Rookery login passed evidence and revocation tests. Queued work survived a hosted restart.

Four bounded repair cases have passed live Claude and mixed-traffic qualification: a single-column index, targeted ANALYZE, joint statistics and plain VACUUM. Function-scoped memory is implemented and reviewed; its first hosted trial eliminated temporary writes but failed the latency noise gate, so Pooltune restored the old setting. Enrolled Cron scheduling is being implemented and requires actual scheduled cycles before it can qualify. These are synthetic staging cases, not general customer rollout.

[Plain-English overview](overview.md) · [Mixed-traffic results](mixed-traffic.md) · [Cleanup results](bounded-vacuum.md) · [Function memory](function-memory.md) · [Enrolled scheduling](enrolled-cron.md)

## What remains

The current work completes and evaluates the remaining repair families with the same harness. Each needs real Claude decisions, short-spike rejection, before/after traffic measurements, recovery and owned cleanup. A repair that executes successfully can still fail benefit verification; the memory trial is an example. Broader diagnosis across competing causes and concurrent committed writes to the repaired table remain open.

1. **Connect and disconnect a project.** Build the Connect Supabase flow. Provision and store a dedicated credential, enroll representative queries and scopes, verify permissions, rotate credentials, and make disconnect stop jobs and close sessions.
2. **Complete the diagnostic workflow.** Turn queued observations into investigations, reuse fresh evidence, and distinguish estimates from measured results. Give the shared graph structured tools for working through plausible causes and a way to explain insufficient evidence.
3. **Qualify the full index path.** Expand beyond one synthetic equality lookup to compound indexes and representative traffic. Verify owner grants, current load, execution, protected reads and writes, rollback, and recovery before allowing automatic customer changes.
4. **Complete the V1 repairs.** Finish hosted qualification for function-scoped memory and enrolled Cron scheduling, then qualify cleanup settings beyond the bounded plain VACUUM case. Expand targeted ANALYZE beyond the first measured-error case. Qualify the richer statistics repair on representative traffic and cover the separate column statistics-target operation. Each repair needs its own evidence, grants, resource limits, verification, and recovery. The same investigator uses those tools.
5. **Build the hosted product.** Add the landing page, account sign-in, project dashboard, connection setup, investigation and repair history, and a pause control. The dashboard configures and explains a service that runs in the background.
6. **Qualify operations and rollout.** Verify account isolation, retention, monitoring, backups and restore, queue and model budgets, and behavior during deploys and outages. Start with pilot projects and measure workload benefit and interference before expanding.

These are build stages within V1. The product plan defines the six capabilities and boundaries. It leaves V2 open.

## Hosting

Railway remains the staging provider. It already runs the two worker processes and Pooltune PostgreSQL and has verified restart recovery. An always-running process and durable state matter because deployments and crashes can interrupt work. [Railway restart policy](https://docs.railway.com/deployments/restart-policy)

Render is a close alternative with continuous background workers and private access to Render Postgres. AWS ECS on Fargate provides more control over networking and deployment behavior, at the cost of more cloud setup and operations. [Render background workers](https://render.com/docs/background-workers) · [Render private networking](https://render.com/docs/private-network) · [AWS ECS services](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/ecs_services.html)

Before selecting a production host, compare cost for two always-on workers plus PostgreSQL, latency to the Supabase regions we serve, backup and restore, and the effort of operating the platform. The staging test proves job recovery on Railway. Production uptime depends on the database, workers and recovery setup together.
