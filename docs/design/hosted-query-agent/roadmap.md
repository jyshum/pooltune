# Pooltune V1 build sequence

The [V1 product plan](../../product/query-performance-v1.md) defines what we are building. The [interactive design map](design.html) shows how the pieces fit, while the [implementation plan](implementation.md) and [progress log](progress.md) show what has been built and tested. The map is a design snapshot from before the staged implementation and Studio shows actual graph runs or replays.

## Where we are

The Go detector, durable queue and consumer run on Railway staging. The same investigation survived a hosted restart. TypeScript and LangGraph run one shared investigator. The lab exercised Claude and one automatic index repair on a single-column equality lookup. A dedicated Rookery login passed evidence and revocation tests. The Railway detector now queues investigations from enrolled Rookery traffic, and the consumer saves the measured traffic windows, plan estimate, and index check. A bounded staging mode now passes a measured read to the scripted LangGraph workflow and saves an index recommendation. A separate one-shot command from Railway has tested the guarded build, read and write comparison, and recovery on Rookery. The queued staging workflow now detects, recommends, builds, and verifies one single-column index on the same synthetic fixture. Compound indexes and the other five repairs still need qualification. [Hosted traffic test](enrolled-traffic-staging.md) · [Measured traffic reuse](observed-traffic-diagnosis.md) · [Observed page accesses](observed-page-access.md) · [Hosted measured lookup](measured-lookup-staging.md) · [Hosted index repair](hosted-index-repair.md) · [Queued index repair](queued-index-repair.md)

## What remains

1. **Connect and disconnect a project.** Build the Connect Supabase flow. Provision and store a dedicated credential, enroll representative queries and scopes, verify permissions, rotate credentials, and make disconnect stop jobs and close sessions.
2. **Complete the diagnostic workflow.** Turn queued observations into investigations, reuse fresh evidence, and distinguish estimates from measured results. Give the shared graph structured tools for working through plausible causes and a way to explain insufficient evidence.
3. **Qualify the full index path.** Expand beyond one synthetic equality lookup to compound indexes and representative traffic. Verify owner grants, current load, execution, protected reads and writes, rollback, and recovery before allowing automatic customer changes.
4. **Add the other five V1 repairs.** Build targeted ANALYZE, richer planner statistics, bounded VACUUM and cleanup settings, function-scoped memory, and scheduling for enrolled Cron jobs. Each needs its own evidence, grants, resource limits, verification, and recovery. The same investigator uses those tools.
5. **Build the hosted product.** Add the landing page, account sign-in, project dashboard, connection setup, investigation and repair history, and a pause control. The dashboard configures and explains a service that runs in the background.
6. **Qualify operations and rollout.** Verify account isolation, retention, monitoring, backups and restore, queue and model budgets, and behavior during deploys and outages. Start with pilot projects and measure workload benefit and interference before expanding.

These are build stages within V1. The product plan defines the six capabilities and boundaries. It leaves V2 open.

## Hosting

Railway remains the staging provider. It already runs the two worker processes and Pooltune PostgreSQL and has verified restart recovery. An always-running process and durable state matter because deployments and crashes can interrupt work. [Railway restart policy](https://docs.railway.com/deployments/restart-policy)

Render is a close alternative with continuous background workers and private access to Render Postgres. AWS ECS on Fargate provides more control over networking and deployment behavior, at the cost of more cloud setup and operations. [Render background workers](https://render.com/docs/background-workers) · [Render private networking](https://render.com/docs/private-network) · [AWS ECS services](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/ecs_services.html)

Before selecting a production host, compare cost for two always-on workers plus PostgreSQL, latency to the Supabase regions we serve, backup and restore, and the effort of operating the platform. The staging test proves job recovery on Railway. Production uptime depends on the database, workers and recovery setup together.
