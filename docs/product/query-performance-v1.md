# Pooltune V1 product plan

Status: product scope, fully hosted deployment, one shared investigator, and the Go + TypeScript/LangGraph + PostgreSQL division accepted on November 12, 2026. Go replaces the earlier Rust backend decision. Detailed technical design remains open.

This records the six-repair proposal accepted by Jared with “Yep, that makes sense” and his request to save the product plan separately from technical planning. It is a target product definition, not a claim that the new agent has been built or validated. It does not replace historical documentation of the existing traffic-admission MVP.

## Purpose and customer

Pooltune maintains important Supabase query performance as data, schemas, and workloads change. It detects problems, investigates their causes, applies permitted repairs, and measures the outcome.

The initial customer is a small startup team running a Supabase-backed application without a dedicated database specialist. The product reduces recurring investigation and repair work. It does not promise unlimited scale, guaranteed cost savings, or avoidance of every infrastructure upgrade.

Hosted Supabase/PostgreSQL is the initial supported environment. The customer's application continues using its existing database connection path. Pooltune is not a replacement pooler, traffic proxy, or application request queue.

## Six repair types in V1

| Problem | Permitted repair scope | Intended outcome |
| --- | --- | --- |
| 1. Queries search unnecessarily large amounts of data | Create tested nonunique indexes, including compound indexes. Do not delete customer-created indexes. | Reduce unnecessary access work and improve relevant query execution without unacceptable write or storage overhead. |
| 2. PostgreSQL has outdated information about data | Targeted ANALYZE on eligible tables or columns. | Restore useful planner information and improve affected execution. |
| 3. Fresh statistics miss uneven or related data | Suitable extended statistics or a bounded column statistics-target adjustment, followed by analysis. | Improve execution across representative inputs, including uneven customer sizes or correlated columns. |
| 4. Cleanup problems cause unnecessary work | Bounded plain VACUUM; selected table cleanup-threshold adjustments when late triggering is the demonstrated cause. | Reduce avoidable table reads or recurring cleanup backlog without excessive maintenance interference. |
| 5. An eligible database function spills necessary work to disk | Function-scoped work_mem adjustment within a tested memory and concurrency budget. | Reduce temporary disk work and improve execution without creating memory pressure. |
| 6. Background jobs repeatedly interfere with important queries | Shift future schedules of enrolled Cron jobs within owner-approved windows. | Improve foreground query behavior while jobs still meet their deadlines. |

These are six problem types drawn from five families in the earlier research catalog. Statistics refresh and richer statistics belong to the same family. Each individual operation must pass its own evidence, permission, workload, and recovery qualification before unattended release. Inclusion in this plan does not mean every project or query is eligible.

Functions and scheduled jobs require explicit enrollment. Indexes, tables, maintenance actions, and other mutable targets must also fall within the owner's grants. Routine eligible repairs can proceed without asking the owner to approve every action once those grants and limits are configured.

## Observation and investigation

Observe both sustained regressions and persistent inefficiency. A sudden spike is not required.

- Resource context: CPU, available memory information, disk activity and capacity, and connection pressure.
- Query behavior: execution frequency and duration, database-page access, temporary disk work, and available errors/timeouts.
- Database context: table growth and modifications, existing indexes, planner statistics, cleanup history, blockers, and enrolled job history.
- Representative execution context: query inputs, roles, and relevant access rules when necessary to investigate or verify a repair.

Discover the project's actual telemetry, extensions, versions, and permissions. Missing evidence is unknown, not zero. Query statistics do not automatically supply request parameters, per-query CPU, or application latency percentiles. SQL-query improvements must not be presented as measured page improvements without application measurements.

### Evidence reuse and investigation cost

Accepted by Jared on November 12, 2026: “if we have exisitng evidence, dont create extra tests that would take up memory.” Reuse sufficient, valid evidence instead of repeating database work. This applies to CPU, memory, disk activity, and connection use, not only memory.

- Check existing measurements, plans, and prior investigation results before requesting another experiment. Reuse requires relevant query inputs and role context, compatible schema/settings, valid counter intervals, sufficient coverage, and appropriate freshness; mere existence is not enough.
- Prefer observing ordinary workload for before/after verification when it supplies comparable, sufficient evidence. Verification is required, but an extra synthetic query execution is not required for every repair.
- Every additional experiment must identify the missing evidence, the decision it can change, and why existing observations cannot answer it. Go enforces permission and execution budgets; LangGraph reuses recorded results and requests only justified additional evidence.
- Defer or report an inconclusive outcome when evidence is insufficient and a permitted, proportionate experiment is unavailable. Do not substitute an estimated plan for measured improvement or treat pre-change evidence as proof of a new repair's outcome.
- Continue necessary lightweight collection and current permission/target checks. Collection itself has overhead and remains bounded. Evidence reuse does not waive pre-execution checks or grant new repair authority.

This policy does not assume that a hypothetical index can demonstrate actual execution speed. Physical index builds and other repairs still consume resources even when redundant test queries are avoided. Exact freshness rules, experiment budgets, and controlled live-trial conditions remain technical design work.

## Product behavior

### Diagnostic capability and customer data

Direction accepted on November 12, 2026. Jared clarified: “i want the agent to perform at full capacity” and “that dosnet mean no effort though, just a compromise”. The earlier proposal to exclude real customer values from model evidence by default was not accepted as a fixed restriction.

Design access around effective investigation of the six repair scopes. Disclose what Pooltune reads, stores, sends to its model provider and changes; obtain the relevant customer authorization up front. Within those configured grants, routine evidence requests and eligible repairs proceed without repeated human permission prompts. Useful query inputs, plans, statistics and bounded samples may be included when needed for diagnosis or verification; do not remove relevant evidence merely to claim a stricter privacy posture.

Protect that workflow with encrypted transport and storage, account isolation, controlled credential access, action history and explicit retention. Encryption does not conceal readable evidence from a model provider processing it. Credentials and unrelated records are not useful diagnostic context. More evidence is not automatically better; collect relevant evidence and avoid unnecessary volume or database work.

Exact evidence categories, sample limits, retention periods and provider disclosures remain implementation-design decisions. Privacy filtering and permission checks must be evaluated for diagnostic accuracy and latency, not assumed to be free or harmless. Customer restrictions must be reflected honestly in capability eligibility; missing authority cannot be bypassed or represented as full coverage. The service still does not replicate customer application tables or gain repair powers beyond the six scopes.

### Execution sequence

1. Collect measurements and maintain history.
2. Identify a worthwhile investigation and avoid duplicate work.
3. Investigate competing explanations using appropriate evidence.
4. Propose and evaluate an eligible repair, reusing valid evidence and performing additional experiments only for decision-relevant gaps.
5. Enforce the owner's permissions and resource limits.
6. Execute the repair while recording durable action state.
7. Verify the actual outcome and continue monitoring.

Possible outcomes are improved, regressed, inconclusive, failed, or no permitted repair. The product must explain missing evidence and unsupported causes rather than invent a successful repair.

Expose action history, evidence, observed before/after measurements, and a stop control. Revert supported changes when appropriate. VACUUM and ANALYZE do not have an exact ordinary undo; recovery and residual effects must be described accurately. Removal of a qualifying Pooltune-created index is distinct from permission to delete customer indexes.

## Unattended service requirement

### Automatic scheduling default

On November 12, 2026, Jared delegated the scheduling choice with “whatveer allows for best performance and least amount of customer work”. Selected approach: adaptive automatic execution within upfront owner grants and operation-specific limits. This is the assistant's selection under that stated preference, not approval of untested numerical thresholds.

- Collect lightweight evidence continuously on a bounded schedule. Investigate and perform eligible work automatically when current conditions support it.
- Schedule heavier repairs using observed workload and available headroom. Customer-configured maintenance windows are optional overrides, not required onboarding work. Enrolled Cron jobs still require the business constraints needed to preserve deadlines and frequency; the agent cannot infer arbitrary permission to delay a job.
- Ship tested defaults. Customers should not need to choose PostgreSQL memory settings, collection intervals or lock timeouts to start. Advanced controls may narrow scope, specify windows, or pause future actions.
- Defer expensive work when capacity is inadequate or essential evidence is missing. Track and surface persistent deferral with its reason and the smallest necessary next step; do not silently wait forever or expand capacity purchases beyond scope.
- During execution, monitor progress and application impact. An unsafe operation may require canceling Pooltune's own work and reconciling its state; do not describe every operation as pausable or instantly reversible.

The strongest alternative was requiring a customer-selected maintenance window for every heavy repair. That gives predictable timing but adds setup and can delay useful repairs. Adaptive scheduling accepts more engineering and qualification work to reduce customer effort. It does not guarantee zero interference or the best possible performance. Exact thresholds and per-repair contracts remain to be calibrated and tested.

### Hosted operation

Confirmed by Jared on November 12, 2026: monitoring and permitted repairs must continue when the customer's browser is closed or laptop is turned off. The dashboard is for configuration, checkups, action history, and intervention; opening it must not be required to start or sustain background work.

Pooltune supplies access to the intended Anthropic model. Customers are not required to bring their own Anthropic API key. The provider credential must not be shipped inside a customer-controlled application or container.

Fully hosted deployment was accepted by Jared on November 12, 2026 with “okay nail this down as the deployment.” Pooltune operates the backend, background workers, its own application database, and model calls. Customers connect their existing Supabase projects; they do not need to install software, rent another server, or keep a browser or laptop running. Their application records remain in their existing database. This does not promise that no diagnostic evidence or sensitive information reaches Pooltune; the technical design must define and enforce that boundary.

Customer-managed and hybrid execution were considered as alternatives for keeping database credentials and execution in the customer's environment. They are not the selected V1 deployment because they introduce customer infrastructure setup and operation. Exact hosting provider, worker placement, authentication, storage, and any optional future download remain technical design decisions.

Customer connection choice accepted on November 12, 2026: “for the supabase connecting, yes connect supabase is right here”. Use a Connect Supabase integration entry point. Whether database access provisioning is automated through approved provider APIs or completed through a guided SQL setup remains undecided. OAuth authorization and native PostgreSQL credentials/permissions are distinct. The same user-facing connection concept may extend to other providers, but their authentication, permissions, networking and supported operations require separate integrations; V1 remains Supabase/PostgreSQL only.

Continuous service is a requirement, not a claim of guaranteed uptime or uninterrupted execution during failures. After a failure, execution must account for uncertain prior actions before retrying.

## Boundaries

Outside V1 automatic repair scope:

- Pool resizing and WAL/checkpoint tuning.
- Index rebuilding, arbitrary customer-index deletion, and table-rewriting maintenance.
- Application-code rewrites, access-policy rewrites, and broader data-model redesign.
- Cancelling or terminating customer transactions or active jobs.
- Buying compute, increasing paid capacity, adding replicas, or migrating to another database.
- Changes outside explicitly enrolled targets and owner grants.

Pooltune may diagnose these situations and explain the necessary next step. Such a diagnosis does not count as a completed automatic repair.

## What counts as success

Measure useful query/workload outcomes under comparable conditions. Include read performance, writes, errors, completed work, resource use, and storage costs as appropriate to the repair. A lower planner estimate, successful SQL command, or lower CPU caused by less completed work is not sufficient.

Evaluation must include unfamiliar workloads, representative inputs and roles, misleading symptoms, healthy databases, already-correct indexes, incomplete evidence, failed actions, and repeat runs. The system must demonstrate both justified repairs and justified decisions to leave the database unchanged.

No autonomous repair or customer savings have been demonstrated by the research alone. Product qualification remains required for each operation.

## Accepted lessons from comparable products

Accepted on November 12, 2026: Jared requested “save these lessons into our plan and keep the current scope.” These requirements reinforce the existing six repairs; they add no new repair family or proxy.

1. **Separate observation from change authority.** Connecting a project and diagnosing a problem must not implicitly authorize experiments or repairs. Code-enforced owner grants and resource limits govern each operation; model recommendations cannot expand them.
2. **Separate predictions from measured outcomes.** Show planner estimates, observed baselines, and post-change measurements as different evidence. Reuse sufficient valid evidence and normal traffic before adding experiments. A successful SQL command or cheaper estimated plan is not proof of improvement.
3. **Treat justified inaction as success.** Healthy databases, insufficient evidence, and unsupported causes must produce an explained no-change or inconclusive result. Avoid universal targets such as requiring every query to finish below 100 ms or every workload to use indexes at a fixed percentage.
4. **Record actions and recover before retrying.** Persist intended operations and their outcomes. After interruption, inspect actual database state before repeating an uncertain action. Provide action history and a stop control; describe repair-specific recovery honestly because ANALYZE and VACUUM have no exact ordinary undo.
5. **Keep diagnosis broad and repair authority narrow.** Consider competing causes, including application and capacity problems. Explain causes outside the six permitted repairs without improvising new changes.

Research basis: [Cast Performance Advisor](https://docs.cast.ai/docs/dbo-performance-advisor), [Cast Kubernetes permission stages](https://docs.cast.ai/docs/kubernetes-permissions), [Cast DBO architecture](https://docs.cast.ai/docs/dbo-how-does-it-work), and the [inspected VoltAgent definition](https://github.com/VoltAgent/awesome-claude-code-subagents/blob/8ce183f5900c718e5a139982c910039d54017fbd/categories/05-data-ai/database-optimizer.md). Cast's advisor overlaps query diagnosis; its proxy is a different intervention. The Markdown definition is useful agent guidance, not evidence of measured outcomes or a complete hosted service. Pooltune's proposed differentiation remains permitted repairs with measured outcomes, not demonstrated superiority over either product.

## Accepted technical decisions and remaining design

An evolving [interactive architecture map](../design/hosted-query-agent/design.html) and [design notes with open decisions](../design/hosted-query-agent/design.md) now accompany this plan. The map shows intended responsibilities and explicit approval history; it is neither a running LangGraph agent nor a customer-dashboard prototype. Proposed entries remain unapproved.

Jared accepted one shared investigator with structured, code-enforced tools for V1, rather than separate investigators or subagents for each repair family. The six repair modules retain their own evidence, permission, execution, and verification requirements. The implementation must preserve exactly the six repair scopes and exclusions above. Inspection and validation tools support those repairs; they do not confer arbitrary SQL execution, new repair powers, or authority outside owner grants.

Go is selected for the hosted backend and workers. Jared approved replacing Rust on November 12, 2026: “okay change it to go, and document everything, this is good progress towards implementation”. This supersedes his earlier Rust selection (“lets go rust, docuemnt this in the overral plan like the last section and lets move on”) while preserving the subsequently accepted separate TypeScript/LangGraph investigator. This records an approved architecture revision and documentation work, not authorization to implement or deploy the hosted service. See [the backend language decision](backend-language-decision.md) for rationale, alternatives, scaling requirements, parser considerations, and remaining decisions.

| Component | Accepted responsibility |
| --- | --- |
| Go backend and workers | Account/API services, durable scheduling, bounded collection, deterministic detection, and controlled database inspection, experiment, execution, and recovery tools. Enforce permissions and resource limits at the tool boundary. |
| TypeScript + LangGraph investigator | One agent service defining investigation nodes, conditional branches, Anthropic calls, the model/tool loop, and checkpointed investigation state. TypeScript is the implementation language; LangGraph is its workflow framework. |
| PostgreSQL owned by Pooltune | Selected measurements, operational history, permissions, scheduled work, investigation checkpoints, and authoritative action records. The managed provider and exact schema remain open. |

LangGraph reduces custom workflow plumbing and supports inspecting graph structure and saved execution state. Visual debugging was an explicit reason for choosing it. It does not supply Pooltune's diagnosis rules, tool authorization, repair qualification, or exactly-once external effects. Those checks remain application code. PostgreSQL-backed checkpoints require configuration; durable scheduling and reconciliation of uncertain customer database operations remain explicit design work. LangSmith Studio is the researched visual-debugging option, but its deployment, accounts, tracing/export settings, and any paid services are not selected by choosing LangGraph.

An all-Rust backend and investigator was previously considered as an alternative to the split architecture. The current strongest backend-language alternative is Rust plus the same TypeScript/LangGraph investigator. A single-language Go backend and investigator would remove one runtime but require replacing the accepted LangGraph workflow and visual-debugging approach; that change is not selected. The selected split accepts a second runtime and a narrow internal tool API in exchange for LangGraph's workflow and debugging support. Routine monitoring and numerical detection do not require model calls. Detailed trigger thresholds, budgets, tool contracts, and verification timing are still proposals to be designed and tested.

Treat the hosted service as a new architecture. The existing local Go backend, SQLite state, and dashboard do not constitute the planned hosted service. Evaluate any reuse separately; preserving that implementation is not the reason for selecting a language.

The selected trade-off favors Go's simpler development and concurrency model, faster expected iteration for Jared, and established PostgreSQL tooling. It accepts garbage-collection overhead and fewer compile-time ownership and shared-memory checks than safe Rust. Both support concurrent network work. Pooltune coordinates remote database operations and model calls; no measured Pooltune benchmark establishes that either language will be faster, cheaper, or support more customers. Rust remains a defensible alternative for stronger compile-time guarantees and tighter memory control, but no demonstrated V1 requirement outweighs its additional learning and implementation complexity for the current deadline.

Scaling depends on bounded workers, fair per-customer admission, shared model-provider limits, database connection limits, durable job state, repair coordination, and recovery. Goroutines enable concurrent work but do not persist progress or prevent duplicate external effects. These requirements apply regardless of language. SQL parsing is a bounded inspection concern, not a reason to introduce a separate Rust service or collect all customer query traffic.

The technical design will decide Go libraries, hosting provider, account authentication, credentials, model integration, exact LangGraph nodes and state, PostgreSQL schemas and persistence adapters, collection schedules, experiments, recovery, and release operations. pgx is a database-client candidate, and pg_query_go is a parser candidate where structural SQL inspection is needed; neither is selected by this language decision. Any earlier Go-specific queue suggestion must still be evaluated against the current hosted design. No separate Rust component is planned.

The data boundary remains: customer Supabase supplies current database evidence and executes permitted repairs; Pooltune stores selected measurements, permissions, investigation history, and job progress. The service does not require a replica of customer application tables. Recheck current customer database conditions before changes; stored history is supporting evidence, not a substitute for live checks.

Anthropic is the user's intended initial model provider, with access supplied by Pooltune. Exact model selection, inference budgets, customer pricing, source licensing, and distribution terms are not settled by this product plan. No publication or repository visibility change is authorized by it.

Next brainstorming topic: define the tool contracts for the six accepted repair types. For each, specify inputs, evidence sources and freshness, required permissions, permitted effects, resource limits, structured results, verification, and recovery. Distinguish passive inspection from experiments that execute customer queries. Then use those contracts to settle detection policies, the investigation graph and durable recovery, followed by customer connection/onboarding and remaining infrastructure choices. This is the proposed discussion order, not an implementation plan.
