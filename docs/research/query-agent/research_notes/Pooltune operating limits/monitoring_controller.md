# Pooltune monitoring and controller limits

## What can Pooltune measure and bound?

### Takeaway

Observe pressure and enforce discrete work limits. Do not promise a reserved CPU percentage or instantaneous knowledge of remaining capacity.

### Cited Findings

- Supabase exposes `https://<project-ref>.supabase.co/customer/v1/privileged/metrics`, recommends one scrape per minute, and describes the API as beta with evolving metric names/labels. Discover the actual project's metrics and credentials rather than assuming every series exists. [Metrics API](https://supabase.com/docs/guides/observability/metrics)
- Official example series include `node_cpu_seconds_total`, `node_memory_MemAvailable_bytes`, `node_memory_MemTotal_bytes`, `node_disk_read_bytes_total`, `node_disk_io_now`, and filesystem-space gauges. These are machine-level signals, not query-level CPU or reserved memory. Example zero values are documentation fixtures, not observations. [Exported metrics](https://github.com/supabase/supabase-grafana/blob/main/docs/metrics.md)
- PostgreSQL tracks current sessions/waits separately from cumulative statistics. Cumulative updates can lag; data may remain cached within a transaction, and crashes can reset counters. Collection itself incurs overhead. Short collection transactions avoid carrying a stale statistics snapshot through an investigation. [PG17 statistics](https://www.postgresql.org/docs/17/monitoring-stats.html)
- `pg_stat_statements` supplies calls, total/mean/min/max execution time, block work and temporary-block work. It groups normalized queries by database, user, query ID and top-level status. There is no per-query p95 or CPU-time column. PG17 adds `stats_since`; resets and eviction require baseline invalidation. Calling `pg_stat_statements(false)` avoids repeatedly loading query text. [PG17 statement statistics](https://www.postgresql.org/docs/17/pgstatstatements.html)
- SQL settings provide statement-duration, transaction-duration and lock-wait controls, not a general per-session CPU-share reservation. [PG17 session controls](https://www.postgresql.org/docs/17/runtime-config-client.html)

### Inferences

- Compute interval mean as `delta(total_exec_time) / delta(calls)`, only across compatible, non-reset observations. Compare block work per call as well. Never subtract cumulative means. Missing telemetry means unknown. Enrolled query inputs can differ sharply behind one normalized query.
- A “stop above 80% CPU” rule would be an observed trigger, not proof Pooltune used the extra CPU or a guarantee it never exceeded a budget between samples. Pooltune cannot reserve “5% of Supabase CPU” through these interfaces.
- Use current activity for ongoing blockers and existing recent metrics for capacity. Do not repeatedly rerun a slow query merely to diagnose it. Monitoring-only reads still need a connection limit, timeout and collection-cost measurement.

### Gaps

No project-specific metrics exposure, compute tier, collector overhead or safe saturation threshold was measured here. SQL extension version must also be checked; PostgreSQL major version alone does not establish every extension column.

## How should our hosted controller enforce budgets?

### Takeaway

Maintain two budgets: customer-database work and Pooltune's own model/worker spending. The model can propose more evidence; Go decides whether the request fits the configured envelope.

### Cited Findings

- Supabase currently documents a standard Management API limit of 120 requests/minute per identifier, scope and endpoint. OAuth identifies the app; callers share the applicable bucket. Logs endpoints are limited to 30/minute; database context to 10/minute plus 1/second. Use response headers and backoff, not assumptions of an independent allowance per worker. [Management API limits](https://supabase.com/docs/reference/api/introduction)
- Anthropic enforces organization/model-class request and token rates, with workspace controls. Rate ceilings are not guaranteed throughput. Honor `retry-after` when present and distinguish spend-cap exhaustion from temporary throttling. [Anthropic limits](https://platform.claude.com/docs/en/api/rate-limits)
- LangGraph checkpoints persist graph state, but retries re-execute nodes and external side effects are not exactly-once. A missing acknowledgement is an unknown outcome requiring inspection before another write. [LangGraph fault tolerance](https://docs.langchain.com/oss/javascript/langgraph/fault-tolerance)

### Inferences

- Proposed control path: collector → numerical detector → deduplicated investigation → LangGraph hypothesis/evidence loop → Go admission check → durable action record → repair → observation/verification. Routine collection needs no model call.
- Go should check tenant identity, operation/target grants, remaining database and model budgets, evidence freshness, compatible schema and active repair ownership. Reserve budget before dispatch and reconcile actual usage afterwards. Enforce shared limits across replicas, not only in each process.
- Persist job leases, action IDs and lease generations. Reject stale workers at dispatch. A lease expiring does not stop an already-running SQL command: inspect operation state before handing mutation authority to another worker. Stable IDs plus database reconciliation support safe retries; they do not create magical exactly-once SQL.
- Permit one active mutation per project initially, including controlled index builds. Keep an overlapping-query verification interval before another repair obscures attribution. Unrelated lightweight observation can continue. This bounds Pooltune's activity, not application traffic or concurrent calls into a tuned function.

### Gaps

Queue, credential and action protocols remain unimplemented. Recovery must receive capacity even if a customer's normal investigation allowance is exhausted. A hard model-spend stop must not abandon a running database action.

## Which initial policy preserves capability?

### Takeaway

Recommend adaptive work within preauthorized ceilings, with quiet windows for costly operations. Fixed conservative caps are simpler but can prevent useful repairs; quiet-window-only operation avoids daytime work but can delay fixes indefinitely for always-busy applications.

### Cited Findings

- Supabase's query-monitoring example combines execution count, mean latency and total time spent. Its sample numbers illustrate filtering, not universal production thresholds. [Supabase query monitoring](https://supabase.com/docs/guides/database/extensions/pg_stat_statements)
- LangGraph supports attempt timeouts/retries, but checkpoint persistence needs a durable backend; in-memory checkpoints do not survive restart. [Persistence](https://docs.langchain.com/oss/javascript/langgraph/persistence)

### Inferences

**Proposed starting values for qualification, not accepted or proven defaults:**

| Work | Initial proposal |
| --- | --- |
| Collection | Metrics every 60 seconds; selected query/table deltas every 60 seconds; schema refresh every 15 minutes and before a relevant change. Jitter schedules. |
| Lightweight SQL | One collector connection/project; start with a 2-second statement timeout. Back off on failure. Do not apply that timeout to repairs. |
| Investigation | One active investigator/project; initial allowance of 8 model turns and 12 new evidence requests. Permit another bounded tranche automatically when it answers a named unresolved question, within an upfront project cost ceiling. Stop repeated unchanged requests. |
| Extra trials | One experimental query at a time/project, separately qualified for duration/memory. Reuse ordinary traffic first. |
| Mutations | One active repair/project; operation-specific duration/storage limits, no universal “all repairs finish in 30 seconds.” |

Suggested detector starting rule: three consecutive five-minute windows with at least 100 calls in total, both >50% and >20ms interval-mean increase against a comparable baseline. This is a tunable investigation trigger, not proof or a repair requirement. Also investigate persistent high total work without a regression. Important low-frequency queries need enrolled latency objectives or longer observation, not permanent exclusion.

Immediate severe errors, rapidly growing blockers or disk pressure may trigger diagnosis without the persistence wait. They do not waive repair preconditions; defer expensive experiments during overload.

Verification compares useful completed work, query latency/work, error rates and write/resource overhead under comparable inputs. Lower CPU with fewer completed requests is not improvement. Low traffic can yield inconclusive results. Data size and distribution reveal scan/planner problems; repeated executions build confidence; concurrency tests reveal contention/memory risks. Heavy traffic is therefore unnecessary for the initial access test and insufficient by itself to prove any repair.

### Gaps

The proposed counts and timings need Rookery calibration and varied fixtures before customer defaults. A numeric improvement percentage alone cannot establish confidence or causal attribution.
