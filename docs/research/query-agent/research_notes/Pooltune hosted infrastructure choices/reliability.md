# Hosted Pooltune reliability, scalability, and agent dependence

Research date: November 12, 2026. Hosted deployment and six repair types are accepted product scope. This is research and a proposed infrastructure baseline, not an approved technical plan, deployed service, capacity benchmark, or integration test. No implementation or workflow-mode selection occurred.

## How much infrastructure does a good agent actually depend on?

### Takeaway
The model decides which evidence to investigate and proposes a repair. Pooltune's infrastructure determines whether evidence is fresh, permissions hold, work survives a restart, competing actions are excluded, and the measured result is believable. A fast implementation language cannot substitute for those guarantees.

### Cited Findings
- PostgreSQL exposes cumulative execution counters, query identities, shared/temporary block activity, resets, and deallocation information. The identity includes database/user/query/top-level context; a query hash alone is insufficient for isolation. It can return counters without repeatedly returning query text. Planning instrumentation may itself add overhead. — [PostgreSQL pg_stat_statements](https://www.postgresql.org/docs/17/pgstatstatements.html)
- Supabase's current Metrics API documentation describes CPU, I/O, WAL, connection and query series; advises once-per-minute scraping of the complete endpoint; and labels the endpoint beta, with names/labels subject to change. Availability and permissions still require an actual project capability check. — [Metrics API](https://supabase.com/docs/guides/observability/metrics)
- River's queue documentation distinguishes job insertion uniqueness from execution uniqueness: a job can be re-executed after its successful work was not recorded as complete. — [Unique jobs](https://riverqueue.com/docs/unique-jobs)
- Concurrent PostgreSQL index creation performs multiple phases and cannot be inside a transaction block. It may leave an invalid index after failure; that artifact can still impose update overhead. IF NOT EXISTS does not establish that an existing index matches the requested definition. — [CREATE INDEX](https://www.postgresql.org/docs/17/sql-createindex.html)
- PostgreSQL exposes progress views for ANALYZE, CREATE INDEX, VACUUM and selected other operations. — [Progress reporting](https://www.postgresql.org/docs/17/progress-reporting.html)

### Inferences
- Maintain a durable investigation record: queued, collecting, investigating, validating, authorized, applying, reconciling, verifying, completed/inconclusive/failed. Exact state model remains to be designed. A model conversation alone is not the source of truth.
- Snapshot counters with timestamps and freshness; compute interval deltas, handling reset/disappearance/version changes. Missing evidence is unknown, never healthy zero. Separate query execution time from CPU and application p95.
- Required checks run as ordinary code independently of model confidence. Associate all evidence/actions with the authenticated tenant and project on the server, not a model-supplied ID.
- Retry reads and transient requests with bounded backoff. Do not put arbitrary repair SQL into a generic auto-retry loop. After a lost response, reconcile actual database state before deciding whether another action is permitted.
- Unique operation keys and worker leases reduce duplicates, but a lease expiring does not undo or stop SQL already submitted to an external database. Fencing tokens prevent stale workers from updating our own state only where enforced. They do not automatically fence a running CREATE INDEX. Record uncertainty, inspect catalogs/progress/activity with suitable privileges, and stop conflicting new repairs until resolved. No exactly-once external mutation promise.
- If Pooltune loses authoritative permissions/action history/locking state, stop admitting new mutations. Already submitted operations may continue and require reconciliation after recovery. Dashboard unavailability alone need not stop an independent healthy worker.
- Dogfood first against a noncritical project. Keep Pooltune's job/permission/control tables out of automatic self-repair initially, and retain an independent operator stop. Otherwise the agent can damage the infrastructure needed to control or recover it.

### Gaps
- None of these recovery paths has been fault-tested. Need worker-kill, network-partition, stale-lease, lost-response, duplicate-delivery and partial-index scenarios.
- A measured overhead budget and exact freshness thresholds require representative Supabase projects and permission probes.

## What is a simple hosted baseline that can serve multiple customers?

### Takeaway
Start with a web/API process, one or a few worker processes, and managed PostgreSQL for control records and a durable job queue. This is a proposed minimal shape, not a claim about an exact customer count it can support.

### Cited Findings
- River supports PostgreSQL as its primary queue store and integrates with Go/pgx. Its documentation provides transactional job insertion. Saving application state and scheduling associated work in one PostgreSQL transaction avoids the gap between committing state and publishing to a separate queue. — [Getting started](https://riverqueue.com/docs), [Transactional enqueueing](https://riverqueue.com/docs/transactional-enqueueing)
- River supports separate queues and worker limits, allowing long jobs to be separated from short jobs. Some features, including particular concurrency-limit and durable-periodic-job features, are labeled Pro; do not assume every documented feature is in the free edition. — [Multiple queues](https://riverqueue.com/docs/multiple-queues), [Feature catalog](https://riverqueue.com/docs)
- River retries failed jobs by default with increasing delays and jitter, with configurable attempt limits. Pooltune must override inappropriate defaults for mutation/reconciliation jobs. — [Job retries](https://riverqueue.com/docs/job-retries)
- Anthropic imposes organization-level spend limits and model-class limits on requests, input tokens, and output tokens. Workspace controls do not remove organization ceilings. Rate-limit responses can advise retry-after, while spend-limit failures have different recovery requirements. — [Anthropic rate limits](https://platform.claude.com/docs/en/api/rate-limits)

### Inferences
- Suggested baseline if Go is chosen: Go API plus Go worker entrypoints from one codebase; PostgreSQL with relational tables and JSONB for bounded variable-shaped evidence; PostgreSQL-backed queue such as River after license/feature review; existing React/TypeScript UI; Anthropic API; secret manager; normal hosted container platform. No Kubernetes, Kafka, vector database, Redis, agent swarm, or per-customer permanent process is justified yet.
- Three logical work classes: frequent bounded collection/detection, less frequent model investigations, and scarce experiments/repairs. Separate their concurrency pools so long repairs do not starve collection. These can start within one worker deployment and split only when measured needs justify it.
- Schedule fairly across tenants. Bound in-flight jobs, connections, query time, model turns, tool calls, experiment work and spending per project/customer, with a global ceiling. One noisy project cannot consume all capacity. Start conservatively with one active modifying repair per project; this is a proposal, not an accepted policy.
- Do not allocate a model or server per customer. Durable job records identify whose task a shared worker is processing. More workers can serve independent projects, while per-project mutation coordination remains shared across replicas.
- Avoid a permanent large database connection pool for every customer. Use bounded connections with capped idle retention, reuse only under bounded caches, stagger polling, and constrain total fan-out. Long-running operations need their own explicit execution lifecycle. This trades connection setup latency against aggregate connection/resource usage and must be measured.
- Centrally budget Anthropic request/token admission across all workers. Reserve estimated usage before concurrent calls, enforce max output tokens and maximum turns, reconcile reported actual use, and treat uncertain charged calls conservatively. Per-customer accounting is not achieved by handing everyone the same provider key or blindly retrying it.
- All external waits need deadlines. API failures should delay one project's investigation, not block every tenant. Circuit breakers/backoff and visible stale status matter more than running the language faster.

### Gaps
- River is a researched candidate, not selected. Confirm free-edition capabilities, connection/pool mode requirements and scheduler catch-up behavior before adopting it. Own persisted due-times can be needed if durable periodic scheduling is not available in the selected edition.
- No hosting provider, database tier, replica count, exact deployment price, latency target or service-level commitment was selected.

## What grows with customers, and when should the architecture grow?

### Takeaway
The main scaling dimensions are connected projects, retained query patterns, polling frequency, concurrent investigations, and repairs. Customer application row count does not mean Pooltune must copy all those rows, although it can increase diagnostic/repair cost at the source.

### Cited Findings
- PostgreSQL can omit query text on repeated statistics calls and retrieve it when necessary; tracked entries can be evicted, so collection is not a complete event log. — [pg_stat_statements](https://www.postgresql.org/docs/17/pgstatstatements.html)
- Queue separation reserves worker capacity for different workloads. It is not by itself a guarantee of tenant fairness or safe external mutations. — [Multiple queues](https://riverqueue.com/docs/multiple-queues)

### Inferences
- Illustrative arithmetic only: P projects × Q retained query-pattern rows per sample × (1,440 / I-minute interval) yields daily query-snapshot rows. For P=100, Q=100, I=5: 2,880,000 rows/day before project metrics, indexes, WAL and backups. This is not a proposed default, byte/RAM estimate, or proven supported load. It shows why retaining every sample forever is a bad default.
- Retain short-resolution history briefly, roll up older numeric measurements, cache stable metadata with change detection, cap query text and plan sizes, expire experiment/model artifacts, and enforce per-project cardinality budgets. Required evidence supporting an action gets a separate retention policy from routine telemetry. Sampling/top-N selection must remain explicit because it can miss rare expensive or important queries.
- Monitor oldest job age by class and tenant, collection freshness, execution duration, provider throttling, active DB connections, database write latency/storage growth, memory/CPU, model cost, repair reconciliation backlog, and outcomes. Queue length alone is not enough: a long list of cheap samples differs from long-running repairs.
- Add worker replicas when healthy downstream systems have spare capacity and queue delay is violating the intended service target. If PostgreSQL or Anthropic is already saturated, more workers can worsen pressure. Apply backpressure or increase downstream capacity appropriately.
- Separate telemetry from control/job storage when measured ingestion/storage/maintenance pressure harms critical operation latency. Add object storage for large bounded evidence artifacts if it simplifies retention/cost; do not put a separate analytics system in V1 solely because customers vary.
- Main agent-quality dependency is fresh evidence + durable execution + verified results. Rust/Go/C++ cannot remove network/API latency, model reasoning quality issues, or the database I/O needed to test/build an index. Profile language/runtime costs once the workflow is functioning.

### Gaps
- Capacity needs load tests with realistic evidence sizes and stalled external services. No claim that Go/Rust/Python can serve a particular number of customers is supported by this research.
- Exact retention, telemetry granularity and experiment policies require the product's privacy expectations and measurement goals; proposals should not be reported as settled user decisions.
