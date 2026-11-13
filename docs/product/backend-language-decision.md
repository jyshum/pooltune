# Pooltune backend language decision

Decision ID: PT-TECH-001

Status: accepted by Jared on November 12, 2026.

Authority: “okay change it to go, and document everything, this is good progress towards implementation”. This accepts the proposed replacement of Rust with Go while retaining TypeScript/LangGraph and PostgreSQL. It authorizes documenting this decision, not implementing or deploying the service.

Canonical product scope: [Pooltune V1 product plan](query-performance-v1.md).

## Decision and responsibilities

Use Go for the hosted backend and database tools, TypeScript/LangGraph for the investigator, and Pooltune-owned PostgreSQL for operational state and selected evidence.

| Component | Responsibility |
| --- | --- |
| Go API/backend | Accounts, project configuration, owner grants, dashboard API, and access to server-side credentials. Exact auth and secret-storage mechanisms remain open. |
| Go workers and tools | Scheduling, collection, numerical detection, database inspection, bounded experiments, permission enforcement, repair execution, verification measurements, and recovery. |
| TypeScript/LangGraph | One investigator with structured tools, investigation branches, Anthropic calls, and checkpointed investigation progress. No V1 subagent system. |
| Pooltune PostgreSQL | Accounts, permissions, selected telemetry, queued work, run history, checkpoints, and authoritative action records. Exact schemas and adapters remain open. |
| Customer Supabase/PostgreSQL | Source of current database evidence and execution of permitted repairs. Customer application tables remain here. |

The investigator requests tool operations. The Go boundary independently checks whether an operation is permitted and eligible. A model instruction or graph transition cannot grant additional database powers.

The service remains fully hosted. Customers do not install a collector, rent a server, supply an Anthropic key, or keep a browser open. Selected diagnostic evidence can reach Pooltune; this is not a promise that all metadata is non-sensitive or that no customer data leaves its environment.

## Why Go fits the current workload

PostgreSQL performs query execution, index creation, analysis, and maintenance in the customer's database. Anthropic performs model inference. Our backend coordinates these operations, processes selected evidence, and records outcomes. Much of this work is expected to involve waiting for network responses, although no profiling establishes a percentage.

Go's goroutines fit concurrent network work. Rust's asynchronous tasks can also handle this workload well. The decision favors expected development speed and a smaller learning burden for Jared, especially with his upcoming call in a few days. It is not a claim that Go has a higher performance ceiling.

The existing local Go gateway, SQLite state, and dashboard are not the new hosted architecture. Any reuse requires separate evaluation. Avoiding a rewrite of existing infrastructure is not the rationale.

## Alternatives and accepted trade-offs

| Approach | Advantages | Costs and decision |
| --- | --- | --- |
| Go + TypeScript/LangGraph | Simpler backend development; established PostgreSQL tooling; retains graph-based investigation and debugging. | Selected. Accept garbage-collection costs, explicit concurrency discipline, and an internal service boundary. |
| Rust + TypeScript/LangGraph | Ownership and type checking reject many unsafe sharing patterns; tighter memory-management control without a tracing garbage collector. | Superseded. Additional ownership, lifetime, and async learning is not justified by a demonstrated V1 bottleneck. |
| Go for both backend and investigator | One backend language/runtime and fewer service contracts. | Not selected. Would replace the accepted TypeScript/LangGraph approach and require separate workflow/debugging decisions. |

Go's garbage collector consumes resources and can affect latency. Safe Rust's checks do not prevent every concurrency bug, deadlock, authorization error, or incorrect database repair. Neither language removes the need for tenant isolation, durable recovery, bounded execution, and workload verification.

No Pooltune benchmark establishes a memory footprint, maximum customer count, throughput advantage, or operating-cost advantage for either option. Claims such as “95% waiting”, “15–30 MB”, guaranteed sub-millisecond latency, or effortless thousands of model streams are not accepted sizing assumptions.

## What scaling actually requires

These are design requirements, not a selected queue implementation or fixed capacity configuration:

- Persist investigation jobs before dispatch and recover unfinished work after restarts.
- Bound concurrent model calls and database operations. Waiting work stays queued rather than becoming unlimited active work.
- Enforce shared Anthropic request/token limits and per-customer budgets and fairness. Define where admission is coordinated across Go scheduling and TypeScript model calls.
- Bound connection pools and inspection load per customer project as well as globally. More workers must not accidentally multiply database pressure without limits.
- Avoid duplicate investigations and coordinate repairs targeting the same database objects. Recheck current conditions before mutation.
- Persist action intent and observed outcomes. After a timeout or crash, inspect whether a change already happened before retrying. A graph checkpoint alone cannot guarantee exactly-once external changes.
- Use timeouts, appropriate cancellation, and bounded retries. Respect provider retry guidance; do not blindly retry an uncertain repair.
- Monitor queue delay, active work, model limits and cost, connection use, worker resource use, and recovery failures. Increase worker capacity only when the limiting resource and customer impact are understood.

A goroutine is a concurrent in-memory task, not a durable job. PostgreSQL state and explicit recovery make progress survive process failure. TypeScript can handle simultaneous model requests too; concurrency does not require routing those calls through Go.

## SQL parsing and deployment

Parsing turns SQL text into a structured syntax tree. It can support statement classification, structural inspection, normalization, and evidence minimization. It does not execute the query or replace PostgreSQL's execution planner.

V1 inspects selected queries and plans. It does not require parsing every request, intercepting live application traffic, or ingesting gigabytes of query logs. A large individual SQL statement is not evidence that Rust is required.

Consider an existing parser when its correctness benefits are needed. pg_query_go uses PostgreSQL parser source through Go's C integration, cgo. It could be a library dependency in the Go service rather than a new Rust service. Selection requires checking version compatibility, supported inputs, resource limits, deployment packaging, and failure handling.

Important limits:

- A syntax tree does not fully resolve tables, columns, types, roles, or function behavior. Catalog and execution context still matter.
- Successful parsing does not prove a query is authorized, read-only in effect, or safe to run.
- Literal normalization is not a complete privacy boundary. Identifiers, comments, function bodies, plans, errors, and logs require appropriate handling; raw diagnostics should not automatically enter model context.
- Native parser code still allocates memory and has costs. It is not “zero overhead”.
- cgo adds C build tooling. Cross-compilation requires a suitable C cross-compiler; an automatically dependency-free static binary is not guaranteed.

Cross-platform customer distribution is not a decisive benefit for a fully hosted V1. We can select a controlled server deployment target. No custom parser, C/Rust bridge, separate parsing service, or parser package is approved for implementation by this decision.

## Product and investigation boundaries preserved

All six repair types remain unchanged: tested nonunique indexes; targeted statistics refresh; richer statistics; bounded cleanup; scoped function memory tuning; and approved future job scheduling changes. Their permissions, evidence requirements, exclusions, and outcome verification remain binding.

Numerical monitoring runs without continuously invoking the model. LangGraph manages investigation flow and checkpoints; it does not supply diagnosis rules, tool permissions, repair qualification, or durable external-action reconciliation. The investigator's quality depends on evidence, tools, and evaluation, not the backend language.

## History and remaining work

Earlier on November 12, Jared selected Rust, then accepted a Rust + TypeScript/LangGraph + PostgreSQL split. After reviewing Go versus Rust against the actual workload and deadline, he explicitly approved Go. This record supersedes only the backend-language decision and its associated rationale. Earlier research reports remain historical, not competing sources of current approval.

Before implementation, complete or validate:

1. Contracts for each inspection, experiment, repair, verification, and recovery tool, using the existing Supabase evidence research.
2. Detection thresholds, collection cadence, evidence freshness, representative query enrollment, and model/experiment budgets.
3. The Go-to-TypeScript interface, authoritative state ownership, durable scheduling, concurrency admission, and uncertain-action recovery.
4. Customer connection permissions, credential storage, authentication, diagnostic redaction and retention, and tenant isolation.
5. Go web/database libraries, queue approach, LangGraph persistence, hosting, deployment, and observability.
6. A bounded first implementation slice and evaluation workloads. Language selection does not establish that all six repairs are ready for unattended release.

Detailed implementation workflow settings and review milestones have not been selected by this documentation approval. No code, deployment, package installation, or implementation schedule is claimed complete.

## Primary references checked during the decision

- [Go goroutines](https://go.dev/doc/effective_go#goroutines): concurrency primitives.
- [Go garbage collector guide](https://go.dev/doc/gc-guide): CPU and memory trade-offs of automatic memory management.
- [Rust concurrency](https://doc.rust-lang.org/book/ch16-00-concurrency.html): ownership and compile-time concurrency checks.
- [Tokio introduction](https://tokio.rs/tokio/tutorial): Rust support for many concurrent I/O operations.
- [pgx](https://github.com/jackc/pgx): Go PostgreSQL driver and connection-pool tooling; candidate only.
- [pg_query_go](https://github.com/pganalyze/pg_query_go): PostgreSQL-source parser through cgo; candidate only.
- [cgo documentation](https://pkg.go.dev/cmd/cgo): native tooling and cross-compilation constraints.
- [Anthropic rate limits](https://platform.claude.com/docs/en/api/rate-limits): provider capacity constraints independent of local worker concurrency.
