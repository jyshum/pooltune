# Pooltune: contention, maintenance, and resource lifecycle problems

Research checked November 12, 2026. Hosted Supabase first. This is problem and product research, not an approved V1 or permission to access a project. The prior repair catalog is a toolbox, not eight mandatory product pillars. No live sessions were accessed or changed.

## Key question: What does “the database is slow” actually mean?

### Takeaway

A slow request can be waiting to enter the database, waiting for another transaction, consuming too much computation, or waiting for storage. Those are different problems. Raising a connection limit can help the first and worsen the others.

### Cited findings

PostgreSQL exposes current activity and separate wait categories, including `Lock`, `LWLock`, and `IO`. A lock wait is not evidence that the server needs more CPU. A lightweight internal lock is not necessarily a customer transaction blocking another customer transaction. An active session with no reported wait is also not conclusive proof of CPU saturation. [PostgreSQL statistics](https://www.postgresql.org/docs/17/monitoring-stats.html).

Supabase reports CPU categories, disk operations and connection use separately. CPU user time and I/O wait support different investigations; neither identifies the offending business operation alone. [Supabase reports](https://supabase.com/docs/guides/observability/reports).

Supavisor already opens backend connections on demand up to its configured ceiling. PostgREST also maintains its own dynamic pool. These are different routes, and Auth and Realtime have their own pools. [Supavisor FAQ](https://supabase.com/kb/troubleshooting/supavisor-faq-YyP5tI), [PostgREST connection pool](https://docs.postgrest.org/en/latest/references/connection_pool.html).

### Inferences: four concrete stories

| What the person sees | What is happening | Native help and remaining gap | Potential response and proof |
|---|---|---|---|
| An API request times out while the database still has spare resources | The request has not obtained a usable connection | Poolers reuse and grow connections. They do not prove that the configured ceiling fits this workload | Identify the actual service pool, establish queue/timeouts and spare DB headroom, then evaluate a bounded ceiling change. Success is fewer acquisition failures at comparable work, with no new DB overload |
| Updates hang although CPU looks quiet | One transaction is waiting for a conflicting transaction to finish | Locks preserve correctness. The database cannot decide whether an application should still be holding the transaction | Find the blocker chain and owner. Future prevention can shorten the operation or change an approved job schedule. Under the no-cancellation constraint, the agent cannot promise to release today's blocker |
| The dashboard is slow while CPU is busy | The database may examine, join, sort or repeatedly compute far more than the answer needs | The planner chooses among available plans; indexes and statistics determine its options | Establish the query and plan, test an index/statistics/query change. Measure database work per successful operation and app latency, not CPU percentage alone |
| Everything slows during imports, reports or cleanup | Work may be waiting on reads, writes, temporary files or durability flushes | PostgreSQL caches and schedules background work, but all work shares finite storage resources | Attribute pressure and remove avoidable work, schedule competing jobs, or identify genuine capacity need. Measure throughput, waiting and deadlines together |

These stories can overlap. A slow query keeps its connection longer, the pool fills, and later callers queue. Fixing the queue symptom by admitting more work may deepen the original bottleneck.

### Gaps

Supabase's documented sample has PostgREST waiting and timeout metrics. The prior investigation did not verify a hosted Supavisor queue-latency histogram. Sampled activity can miss short contention. One-minute gauges do not establish request p95. Application traces remain necessary when claiming improvement in user journeys rather than only provider-visible work.

## Key question: Why can ordinary application writes create disproportionate cost?

### Takeaway

An application write is more than saving one visible value. The database may update indexes, retain an older row version, write a recovery log and later flush modified pages. The product opportunity is identifying avoidable work while preserving the promised result and durability.

### Cited findings

PostgreSQL uses a write-ahead log, or WAL, to recover changes after a crash. Checkpoints flush modified data pages. PostgreSQL spreads checkpoint writes over time, but frequent checkpoints can increase I/O and full-page WAL records. This is a recovery-time/resource tradeoff, not a free performance switch. [WAL configuration](https://www.postgresql.org/docs/17/wal-configuration.html).

Updates can avoid some index maintenance through HOT when the required conditions hold, including free space on the same table page and suitable unchanged indexed values. Reducing table fillfactor can leave more free space, but consumes more storage and affects future page use. [HOT](https://www.postgresql.org/docs/17/storage-hot.html).

Every additional index has maintenance cost. Covering indexes can help reads but also enlarge indexes; wide included values can cause failures when an index tuple exceeds its limit. [CREATE INDEX](https://www.postgresql.org/docs/17/sql-createindex.html).

### Inferences: a write-heavy application story

An event app repeatedly updates a status row, stores each incoming event, and runs a dashboard query. It begins timing out during imports. The first question is not “which knob should change?” It is “which work increased per accepted event?”

Possible causes include excessive indexes on the hot table, repeated updates that could be avoided, an inefficient trigger, a batch arriving all at once, a report scanning the same data, or genuinely necessary writes exceeding the machine's capacity. These causes imply different products:

- A database-only agent could measure WAL/write rates, index size and activity, HOT ratios and query work. It could test already-authorized index and maintenance changes.
- A code-aware agent could propose reducing redundant updates, changing transaction boundaries, or batching work. These are query/application migrations and remain candidates for the product, not presumed out of scope.
- Changing retention, dropping business events, coalescing updates or accepting delayed writes requires business intent. An event that looks redundant to the database might be an audit record.
- Turning off durability safeguards is not an acceptable generic optimization. A faster commit that weakens the promised persistence is a different product behavior.

Measure useful accepted operations and their latency against WAL bytes, disk writes, CPU time and storage growth over comparable traffic. A decline in writes because requests are failing is not improvement. Per-query WAL in `pg_stat_statements` can improve attribution when available, but aggregate counters do not explain business necessity. [pg_stat_statements](https://www.postgresql.org/docs/17/pgstatstatements.html).

### Recurrence and business intent

The pressure can recur on every import, scheduled batch or state transition. That establishes a mechanism for recurring value, not how many customers have it. A prototype should first find projects with observable repeated episodes and ask whether their current response is manual query work, rescheduling or buying capacity.

## Key question: Why does cleanup fall behind if Supabase already runs it?

### Takeaway

Autovacuum is already the cleanup system. A useful caretaker would detect when it cannot keep up or cannot make progress, then choose an appropriate bounded response. Running VACUUM on a timer is not enough.

### Cited findings

Updates and deletes leave old row versions. Ordinary VACUUM makes eligible space reusable; it generally does not shrink the table file back to the operating system. PostgreSQL automates VACUUM and ANALYZE, but old transactions, prepared transactions or replication horizons can prevent cleanup of otherwise obsolete data. [Routine vacuuming](https://www.postgresql.org/docs/17/routine-vacuuming.html).

Supabase's engineering explanation identifies long transactions, stuck locks and insufficient cleanup resources as reasons bloat can accumulate despite automatic maintenance. [Supabase bloat explanation](https://supabase.com/blog/postgres-bloat).

Index-only queries may still visit table pages to check visibility. When the visibility map does not mark those pages all-visible, a covering index alone does not eliminate those visits. [Index-only scans](https://www.postgresql.org/docs/17/indexes-index-only-scans.html).

### Inferences: a maintenance story

A small queue or session table is updated constantly. A report transaction stays open for hours. The table grows even though the application keeps roughly the same number of live items. Reads touch more pages, and extra cleanup consumes I/O. This does not require a high-volume-company customer: it depends on the relation between churn, available resources and how long old versions remain needed.

The investigation must separate three cases:

1. **Cleanup is eligible but late.** A bounded ordinary VACUUM or per-table trigger adjustment may be useful after verifying resource headroom. Compare future backlog and latency across several churn cycles.
2. **Cleanup is blocked by an old visibility horizon.** Repeating VACUUM is unlikely to fix the cause. Identify the old transaction or slot and its owner. The existing no-cancellation constraint means escalation or an application fix, not automatic interruption.
3. **Space is reusable but not returned to disk.** That may be healthy if the table will grow again. Shrinking it requires a different and potentially disruptive operation; the displayed file size alone is not a reason to act.

Additional useful diagnosis: an index-only plan has many heap fetches because pages cannot yet be marked all-visible. Existing maintenance might help after transactions finish. This is another use of the maintenance toolbox, not a new action family or permission.

For verification, track churn, approximate dead rows, completed maintenance, oldest relevant snapshot, relation growth and query work together. Dead-row estimates alone are not a measured bloat percentage. A successful command is not proof that the intended debt disappeared. For index rebuild proposals, inspect supported page-level measurements and compare with an appropriate baseline. [pgstattuple and pgstatindex](https://www.postgresql.org/docs/17/pgstattuple.html).

### Gaps and limits

Maintenance can compete with live traffic. A manual operation needs an explicit target, resource budget and stop conditions for the agent's own work. Its physical result has no simple rollback. Setting restoration is different from restoring an earlier file layout. Imminent transaction-ID exhaustion can require provider/operator involvement beyond a tenant table maintenance grant.

## Key question: Why is disk growth different from capacity exhaustion?

### Takeaway

“We are not targeting high-volume apps” does not mean capacity disappears. There is a useful distinction between removing waste and providing more resources for legitimate work. Supabase already owns the latter provisioning surface.

### Cited findings

Supabase distinguishes database size from total disk use. Disk includes WAL and system files. Its docs warn that deleting rows need not immediately reduce physical use, and `VACUUM FULL` locks a table. [Database and disk size](https://supabase.com/docs/guides/platform/database-size).

Logical replication slots retain WAL until consumers acknowledge progress. A lagging or abandoned consumer can therefore retain disk space. Limiting retention can break the consumer's ability to catch up and require resynchronization. [Supabase replication](https://supabase.com/docs/guides/database/replication).

Disk throughput and operations-per-second are separate limits. On smaller Supabase compute sizes, prolonged I/O can exhaust burst allowance and fall back to baseline performance; this can affect both foreground queries and background maintenance. [Exhausted disk I/O](https://supabase.com/kb/troubleshooting/exhaust-disk-io).

### Inferences: a growing-disk story

A customer deletes old rows but still sees a growing bill or disk warning. At least four explanations require different decisions:

| Growth source | Useful agent contribution | Decision it cannot infer |
|---|---|---|
| Necessary live records | Measure growth and forecast headroom with uncertainty | Retention period, archival rules, acceptable spend |
| Reusable or bloated table/index space | Distinguish normal reuse from measured waste; evaluate maintenance | Whether a disruptive rewrite or temporary extra space is acceptable |
| Retained WAL | Identify slot lag and responsible integration | Whether the downstream consumer is abandoned, may lose continuity, or should be rebuilt |
| Temporary work and excessive reads/writes | Attribute query/batch causes, test query or schedule changes | Whether throughput/freshness may be reduced |

An unattended agent should not drop a slot just because it is inactive, delete records because they are old, or start a storage rewrite near a hard space limit. More capacity may be the correct answer. With automatic spend excluded, the product can provide a supported recommendation and clear evidence while the customer/provider handles the change.

Success may mean stabilized growth, bounded slot lag or restored throughput, depending on the diagnosed source. It is not always a smaller reported database. Daily billing-size metrics are not sufficient for an acute disk incident.

## Key question: What causes jobs and transactions to interfere?

### Takeaway

Correct operations can still interfere when they run at the same time or hold a transaction open too long. This is partly database behavior and partly application intent.

### Cited findings

Locks remain until the relevant transaction ends, with savepoint and advisory-lock qualifications. PostgreSQL detects deadlocks and aborts a participant; consistent lock ordering helps prevent them. The database cannot choose the application's intended order of business work. [Explicit locking](https://www.postgresql.org/docs/17/explicit-locking.html).

Supabase Cron stores schedules in `cron.job` and execution history in `cron.job_run_details`. It recommends no more than eight jobs concurrently and job durations under ten minutes. These are recommendations, not proof every project observes them. [Supabase Cron](https://supabase.com/docs/guides/cron).

`pg_cron` already runs only one instance of the same job at a time and queues the next occurrence. Different jobs can still compete. [pg_cron scheduling behavior](https://github.com/citusdata/pg_cron#how-pg_cron-works).

A real GitLab incident recorded failed migrations first due to long autovacuum activity and then lock timeouts; responders waited/retried or skipped the affected migration. This demonstrates maintenance/deployment interference, not demand frequency among Supabase users. [GitLab April 2026 incident](https://gitlab.com/gitlab-com/gl-infra/production/-/work_items/21698).

### Inferences: an interference story

An hourly aggregation, an import and a cleanup job all begin on the hour. Checkout updates slow down even though each background job is acceptable in isolation. Alternatively, application code opens a transaction, updates a row and waits for an external API before committing. The database is following the app's instructions while later requests wait.

Potential work differs by cause:

- **Preauthorized schedule flexibility:** stagger different jobs within stated completion/freshness windows; compare overlap, lock waits, customer latency and job completion. This can be genuinely unattended after the customer defines which jobs may move.
- **Slow operation within a transaction:** an eligible index, including an appropriate foreign-key-side index, may reduce work and lock duration. It will not make simultaneous writes to the same row independent.
- **Incorrect transaction boundaries or lock order:** propose a reviewed application/query migration and test concurrency behavior. A database knob does not express the missing business intent.
- **A blocker already running:** identify it and explain the owner/action needed. Future schedule changes or future-session timeouts do not release its current locks.

Timeout policies trade waiting for errors; they need an application error/retry contract. Changing a setting in the agent's session does not reconfigure other sessions. Schedule changes must also preserve active work: `pg_cron` version 1.6.4 can cancel work when a job is deactivated, so “pause job” is not a harmless synonym for “change its next start.” [pg_cron cancellation source](https://github.com/citusdata/pg_cron/blob/v1.6.4/src/pg_cron.c#L1674-L1784), [role defaults](https://www.postgresql.org/docs/17/sql-alterrole.html).

### Recurrence and business intent

Schedule collisions can repeat predictably; bad transaction paths can recur with each affected request. But a weekly financial job, a payment settlement and a disposable cache refresh have different deadlines. An agent needs that policy before it changes timing, even if SQL access is technically sufficient.

## Key question: Which product niches follow from these problems?

### Takeaway

Several viable hypotheses remain. None is established as the winner by documentation. A niche should specify the recurring customer problem, evidence available and interventions the customer actually permits.

### Inferences: candidate comparison

| Candidate | Problem promise | Plausible unattended work | Main limit / evidence to seek |
|---|---|---|---|
| Maintenance caretaker | Keep frequently changing tables healthy without continual manual investigation | Monitor cleanup progress, detect blocked cleanup, bounded table maintenance and approved per-table policy adjustment, verify several cycles | Native autovacuum already handles healthy projects. Need projects where meaningful intervention recurs; old snapshots and provider-owned maintenance can require escalation |
| Write-heavy/event-app efficiency | Reduce database work per accepted event or state change | Attribute write/WAL growth, validate eligible index changes and maintenance, watch regression | Strongest improvements may require query/application migration, retention intent or downstream-consumer decisions. Measure useful writes, not just lower resource use |
| Workload/job interference manager | Keep background work from delaying customer operations or deployments | Correlate schedules and contention; stagger distinct jobs within granted windows; bounded future prevention | Requires deadlines and job ownership. Same-job serialization already exists. Cannot immediately rescue a blocked customer transaction under current constraints |
| Database incident investigator with bounded rescue | Explain which layer is failing and perform proven eligible repairs | Detect recurrence, collect evidence, apply already-granted index/statistics/maintenance/config actions only when causal case and headroom support them | Acute rescue often needs cancellation, app rollback, spend or provider repair. One-minute telemetry and missing app traces limit urgency claims; a broad “self-healing database” promise overreaches |

These can share a substrate without being one product pitch. Query migrations remain a possible delivery mode within several niches. Generated SQL still needs privilege boundaries, semantic/concurrency tests and staged rollout; generation by AI is not evidence of safety.

### Gaps

We have mechanism evidence and real incidents, not a prevalence study, willingness-to-pay result or comparative intervention success rate. Next research should examine the user's projects read-only after authorization: actual route, repeated symptoms, existing workarounds and permitted intervention. The result could favor a niche or show that native tooling already solves the observed problems.

## Key question: What does Supabase actually expose for these products?

### Takeaway

Supabase exposes useful provider metrics and management APIs plus standard PostgreSQL diagnostics. It does not expose a single “fix database” SDK. Read access, maintenance authority, application context and provider control are different capabilities.

### Cited findings: concise interface appendix

| Interface | Useful capability and important limit |
|---|---|
| `GET /v1/projects/{ref}/analytics/endpoints/metrics` | Prometheus metrics via OAuth `analytics:read` or scoped PAT `analytics_logs_read`. Capability probe required; do not infer every sample metric exists on every project. [Reference](https://supabase.com/docs/reference/api/v1-scrape-project-metrics) |
| `GET /v1/projects/{ref}/analytics/endpoints/logs` | Narrow time-window log investigation. Current guide uses ClickHouse `logs` with `source`; scans have usage costs and limits. Logs complement counters, not a guaranteed complete trace. [Logs guide](https://supabase.com/docs/guides/observability/advanced-log-filtering) |
| `POST /v1/projects/{ref}/database/query/read-only` | Diagnostic SQL as `supabase_read_only_user`, beta/experimental. Actual catalog/statistics visibility must be tested; endpoint access does not imply all monitoring privileges. [Reference](https://supabase.com/docs/reference/api/v1-read-only-query) |
| SQL activity/locking views | `pg_stat_activity`, `pg_locks`, `pg_blocking_pids()` explain current blockers. Snapshot sampling and role visibility limit completeness. [Lock view](https://www.postgresql.org/docs/17/view-pg-locks.html) |
| SQL maintenance/resource views | `pg_stat_user_tables`, `pg_stat_progress_vacuum`, `pg_stat_wal`, `pg_replication_slots`; `pg_stat_io` on PG16+, `pg_stat_checkpointer` on PG17+. Version and timing settings matter. [Statistics](https://www.postgresql.org/docs/17/monitoring-stats.html), [vacuum progress](https://www.postgresql.org/docs/17/progress-reporting.html), [replication slots](https://www.postgresql.org/docs/17/view-pg-replication-slots.html) |
| `pg_stat_statements` | Aggregated query work, calls and WAL fields when available. Not app p95 or a full failed-request ledger. [Reference](https://www.postgresql.org/docs/17/pgstatstatements.html) |
| `cron.job`, `cron.job_run_details`, `cron.alter_job` | Schedule/history and bounded schedule changes with job privileges and business windows. No proof an HTTP-triggered downstream task has finished merely from the SQL dispatch result. [Cron](https://supabase.com/docs/guides/cron), [pg_cron](https://github.com/citusdata/pg_cron) |
| `PATCH /v1/projects/{ref}/config/database/pooler` | Changes Supavisor `default_pool_size`; routing and all consumers' DB budgets matter. [Reference](https://supabase.com/docs/reference/api/v1-update-pooler-config) |
| `PATCH /v1/projects/{ref}/postgrest` | Changes `db_pool`; GET null is documented as automatic compute-size configuration. Hosted rollout and reset-to-auto behavior require testing. [Reference](https://supabase.com/docs/reference/api/v1-update-postgrest-service-config), [OpenAPI](https://api.supabase.com/api/v1-json) |

Dedicated SQL execution is needed for maintenance contracts. `VACUUM`, concurrent index creation and concurrent reindexing cannot simply be wrapped in an arbitrary application RPC transaction. Use a validated direct or session-affine maintenance route, independently verify privileges and bound the agent's own work. PostgreSQL 17 `MAINTAIN` can authorize certain maintenance; it does not grant index creation or arbitrary index deletion. [VACUUM](https://www.postgresql.org/docs/17/sql-vacuum.html), [REINDEX](https://www.postgresql.org/docs/17/sql-reindex.html), [connection options](https://supabase.com/docs/guides/database/connecting-to-postgres).

### Inferences: useful capability classes

- **Detect and explain:** blocker chains, suspected cleanup lag, query/WAL increases, repeated job overlap, resource headroom and likely provider incidents.
- **Prevent future recurrence:** approved schedule windows, eligible query/index migrations, validated per-table maintenance policies and bounded timeout contracts.
- **Bounded repair:** targeted eligible maintenance or index/statistics action when evidence supports it, privileges exist and the workload/resource budget permits it.
- **Requires application or business decision:** transaction order, retries, retention, event coalescing, freshness, slot retirement, cancellation or purchases. Current cancellation and automatic-spend restrictions remain in force.
- **Requires provider action:** provider network/service outage, platform-owned faults or inaccessible system-level maintenance. Do not keep modifying customer settings while the underlying provider is failing.

A real Supabase February 2026 incident was caused by regional network configuration, not customer query plans. The correct response was provider rollback. This is why a database agent needs a “this is outside this project's repair authority” outcome. [Supabase incident report](https://supabase.com/blog/supabase-incident-on-february-12-2026).

### Remaining uncertainty

No live project confirmed metric availability, permissions, PostgREST rollout behavior or intervention effectiveness in this study. SDK access does not erase those gaps. The strongest next step is observing representative problems and testing a small set of candidate repairs on a disposable project before defining which niche earns an unattended product promise.
