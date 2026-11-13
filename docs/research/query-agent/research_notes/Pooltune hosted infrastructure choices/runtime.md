# Hosted Pooltune runtime, queue, and deployment

## Which initial stack fits the approved hosted service?

### Takeaway
Recommendation, not an approved implementation decision: keep React/TypeScript for the dashboard and Go for the API and workers, use Anthropic's official Go client, and introduce managed PostgreSQL plus River for durable jobs. Start with one web/API service and one always-on worker service, potentially built from the same repository/image with different entrypoints. Customer Supabase remains the live source; Pooltune PostgreSQL contains selected telemetry and operational memory, not a copy of customer applications.

### Cited Findings
- Anthropic lists official client SDKs for Go, TypeScript, Python, Java, C#, PHP, and Ruby. Its general client SDKs handle requests/responses and features such as retries and streaming; it distinguishes those from higher-level products that supply agent execution/runtime. Rust and C++ are not in this official SDK list. [Official SDK overview](https://platform.claude.com/docs/en/cli-sdks-libraries/overview)
- River is a database-backed queue with worker definitions, migrations, transactional insertion, retries, scheduled work, and unique jobs. Some advanced features, including workflows, durable periodic jobs, and concurrency limits, are explicitly marked Pro; do not assume every desired control is free functionality. [River documentation](https://riverqueue.com/docs)
- River documents at-least-once execution: jobs can run again after a crash or failure to save completion. Unique insertion does not make the effects of a job execute exactly once. [Reliable workers](https://riverqueue.com/docs/reliable-workers), [unique jobs](https://riverqueue.com/docs/unique-jobs)
- Read-only local inspection found pgx, SQLite, JWT and Go dependencies in [go.mod](/Users/jshum/Desktop/code-folders/pooltune-learning/go.mod). [agent/client.go](/Users/jshum/Desktop/code-folders/pooltune-learning/internal/agent/client.go) currently talks to an installation-token gateway. [agent/run.go](/Users/jshum/Desktop/code-folders/pooltune-learning/internal/agent/run.go) contains a bounded tool loop with evidence freshness, budgets and authority interfaces. These are reuse candidates, not proof the legacy design safely implements the new six repair families.

### Inferences
- Prefer Go because useful code and PostgreSQL integration already exist, not because Python/TypeScript are incapable of scaling. Requesting a model, waiting for databases, and collecting measurements are largely network/database coordination work; changing language does not increase customer database capacity or provider rate limits.
- Add explicit durable investigation states and action journals around the client SDK. An SDK alone is not the complete agent harness. River coordinates jobs; it does not validate database diagnosis or repair safety.
- A job record in our database and an index creation in a customer database cannot be assumed to commit atomically. After uncertain completion, inspect the target state before replaying a mutation. Per-project repair exclusion and revision checks must be enforced in application/database logic even if queue uniqueness is used.
- Use separate API and worker processes so slow investigations do not hold web requests open. Keep collection lightweight and model calls conditional on an actionable investigation. Do not introduce Kubernetes, Kafka, sharding, per-customer servers, or multiple backend languages for the initial pilot.

### Gaps
- No runtime benchmark, load test, memory profile, tenant capacity, or cost estimate was performed. No claimed supported customer count is justified yet.
- Precise River version/license-feature requirements, migration permissions, periodic schedule catch-up strategy, and execution recovery need a bounded prototype before adoption.
- Managed PostgreSQL plus managed authentication is the proposal; the identity provider and platform plan are not selected here.

## What are the strongest language and orchestration alternatives?

### Takeaway
TypeScript with LangGraph is the strongest alternative if explicit graph/checkpoint ergonomics outweigh Go reuse. Rust is legitimate if learning it or controlling runtime behavior is a deliberate goal; C++ has no demonstrated workload-driven advantage here. Do not choose a language merely to make HTTP calls faster.

### Cited Findings
- LangGraph JavaScript supports persisted graph checkpoints and longer-lived stores, enabling continuity, interruption recovery and fault tolerance. An in-memory saver is a tutorial starting point, not durable production storage. [LangGraph persistence](https://docs.langchain.com/oss/javascript/langgraph/persistence)
- BullMQ offers queue/worker APIs, and current documentation now describes an optional PostgreSQL backend as well as its default Redis backend. The docs call Redis the more battle-tested option; PostgreSQL avoids a separate Redis service. Its PostgreSQL implementation uses a query pool plus dedicated LISTEN connections. [BullMQ PostgreSQL backend](https://docs.bullmq.io/guide/postgresql)
- Go, TypeScript, Python and Java have official Anthropic client SDKs. Java's JVM is not a barrier to API use. Rust and C++ can use the HTTP API with suitable libraries but do not appear in the official SDK catalog. [SDK/API overview](https://platform.claude.com/docs/en/cli-sdks-libraries/overview)

### Inferences
- Concrete TypeScript alternative: React dashboard + Node API/worker + LangGraph with PostgreSQL checkpoints + BullMQ durable jobs (PostgreSQL backend candidate, or Redis if maturity is prioritized). LangGraph checkpoints track investigation progress; the queue schedules work. Neither alone guarantees a remote database mutation is safe to retry.
- Rust requires selecting/integrating HTTP, async, PostgreSQL and queue libraries and owning provider API compatibility absent an official client. That is extra work relative to the inspected Go code, not proof Rust is inherently difficult or unsuitable.
- C++ similarly needs HTTP/JSON/PostgreSQL and orchestration choices. No CPU-intensive local algorithm has yet been identified to justify a rewrite into C++.
- Python remains technically valid; there is no measured evidence here that Python memory would dominate operating cost. User preference and existing Go investment are sufficient reasons to avoid it without a false performance claim.
- Do not split Go collection and TypeScript agent execution initially. Add that boundary only if a specific framework capability materially reduces work and outweighs two runtimes, cross-service contracts, deployment and tracing complexity.
- Infrastructure governs continuity, fair access, evidence freshness, budgeting and whether verified actions complete. It does not create correct diagnosis by itself. Agent quality additionally depends on representative evidence, tools, constraints, experiments and evaluations.

### Gaps
- No Go versus Rust versus C++ versus TS benchmark was performed. Do not report a ranking for RAM/throughput.
- BullMQ's current PostgreSQL option was verified in official docs, not integration-tested. Version compatibility and feature behavior must be checked before selection.
- No claim that LangGraph is necessary for good agents or that it automatically guarantees effects occur once.

## Where can the services run and what limits make them stable?

### Takeaway
Railway persistent services are a simple pilot candidate; disable serverless sleeping for the worker. Cloud Run service plus worker pool is the strongest named alternative if Google Cloud operations are preferred. Scale based on observed queue delay and budgets rather than the number of accounts.

### Cited Findings
- Railway distinguishes persistent always-running services from scheduled run-to-completion jobs. Its serverless mode sleeps services based on outbound inactivity, so an unattended queue worker should not rely on web visits to wake it. [Service types](https://docs.railway.com/services), [serverless behavior](https://docs.railway.com/deployments/serverless)
- Google Cloud Run supports HTTP services, run-to-completion jobs and always-on worker pools. Worker pools require at least one instance to start/remain active; they do not automatically scale by default. External-metric autoscaling is a separate option. [Cloud Run resource types and worker pools](https://docs.cloud.google.com/run/docs/overview/what-is-cloud-run)
- River recommends a direct or session-pooled connection for running queue clients because runtime services use session-scoped features. Transaction pooling is suitable for insert-only clients; simply disabling notifications is not proof all runtime services work in transaction mode. [River pooling guidance](https://riverqueue.com/docs/pgbouncer)
- Supabase describes direct connections for persistent services and session pooling as the IPv4-compatible alternative. Transaction pooling affects session state, LISTEN/NOTIFY and other features. This matters for OUR queue database separately from customer inspection/repair connections. [Supabase connection modes](https://supabase.com/docs/guides/database/connecting-to-postgres)

### Inferences
- Proposed pilot: React assets/API service + worker service on Railway, with a managed PostgreSQL database (Supabase is a candidate) and managed auth. A single worker can handle many bounded jobs; one permanently dedicated worker per customer is unnecessary.
- Use small connection pools, per-project collection limits, one active repair per project initially, bounded global investigations, timeout budgets, model-token budgets, and backoff for provider limits. Expensive experiments should have a lower concurrency than routine sampling.
- Persist next-due times and investigation/action state. Stagger collection schedules so customers do not all poll simultaneously. Queue/coalesce redundant sampling or investigation jobs and stop adding unbounded duplicate work during outages.
- Add worker replicas only when measured queue wait becomes unacceptable and both our database/provider budgets and customer-project limits have headroom. More workers without those limits can amplify problems.
- Measure our queue age, missed collection intervals, worker crashes/retries, model errors/rate limiting, connection usage, and retention/storage growth. Customer query performance and Pooltune service health are different metrics.
- Local container disk is not the canonical queue/history store. Persistent managed PostgreSQL permits recovery when containers are replaced. This still requires backup/restore verification and controlled schema migrations.

### Gaps
- No hosting purchases, deployment, network allowlist/static-egress validation, exact region selection, or project connection probes were performed. Hosting recommendation remains provisional until customer connectivity requirements are known.
- Exact collection cadence, retention, model budgets and worker limits are design/prototype inputs, not established production-safe constants.
