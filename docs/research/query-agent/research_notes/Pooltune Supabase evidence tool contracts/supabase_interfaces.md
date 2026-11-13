# Exact Supabase evidence interfaces for Pooltune

Research November 12, 2026. Public API specifications and first-party source/configuration only. No customer project, credentials, live metric scrape or SQL connection was used. Rust collection/execution and TypeScript LangGraph investigation are context; no implementation is performed here. A public OpenAPI excerpt is saved alongside this note as `management-api-evidence.json` with retrieval date and full-spec SHA256.

## Which API can inspect PostgreSQL, and what does its contract actually guarantee?

### Takeaway
There is a real Management API read-only SQL route, but its beta contract is weaker than a normal typed database client. Its role does not establish full statistical visibility. Treat access as a capability to test, not permission to assume.

### Cited Findings
- Base `https://api.supabase.com`; `POST /v1/projects/{ref}/database/query/read-only`. Uses Bearer authorization, OAuth `database:read` or fine-grained permission `database_read`; executes as `supabase_read_only_user`. All entity references must be schema qualified. Marked beta/experimental. — [API reference](https://supabase.com/docs/reference/api/v1-read-only-query), [live OpenAPI](https://api.supabase.com/api/v1-json)
- Required JSON body: `{"query":"...","parameters":[...]}` where query is a nonempty string and parameters is optional with unconstrained item schema. No timeout, cursor, replica selector, or row limit appears in this operation's schema. `ref` is 20 lowercase letters. Success is documented as **201** with no response content schema. Listed failures: 401 unauthorized, 403 forbidden, 429 rate limited, 500 failed query. Do not invent a guaranteed typed row response or additional documented limits. — [OpenAPI](https://api.supabase.com/api/v1-json)
- Supabase distinguishes frontend Data API access from native backend PostgreSQL connectivity. Persistent backends can use a direct connection or the shared session pooler depending on network reachability. A publishable/anon app key does not supply generic administrative SQL execution. — [Connection methods](https://supabase.com/docs/guides/database/connecting-to-postgres)

### Inferences
- Rust can implement vetted evidence queries over this HTTP route if role visibility and limits suffice, or use a deliberately granted PostgreSQL role via a PostgreSQL driver. No need to route through a browser or invent an app-row REST endpoint.
- First probe `current_user`, server version, extension schema locations, required catalog access, visible query text/IDs, null/redacted activity fields, and capability for each targeted query. Do not assume `supabase_read_only_user` has `pg_read_all_stats` merely because it can SELECT a view.
- Use schema-qualified objects, explicit selected columns, bounded rows, parameterized values, request deadlines and tenant/project authorization. Read-only does not mean cheap: a read query can still consume large resources. Public endpoint lacks documented per-call statement-timeout field; actual DB limits and safe query shapes need testing.
- Do not send arbitrary model SQL directly to this route. Tool input should be project ID plus allowed evidence request, with generated SQL owned by the collector.
- Example probe body is `{"query":"select current_user, pg_catalog.current_setting('server_version') as server_version"}`. This is illustrative, not executed. A qualified activity query can inspect catalog data without copying customer application rows.

### Gaps
- Response body, exact row/byte cap, SQLSTATE error envelope, endpoint execution timeout and query cancellation behavior are not specified by reviewed OpenAPI. Verify before relying on them.
- Exact read-only role memberships and visibility across versions/projects are not established by this API spec. Direct SQL role grants remain separate.

## Where do CPU, memory, disk and connection measurements come from?

### Takeaway
Metrics have an authenticated Management API scrape route. Host metrics are node-exporter measurements, not PostgreSQL query-statistics columns. A scrape is a snapshot; rates and historical comparisons are calculated from repeated samples.

### Cited Findings
- `GET https://api.supabase.com/v1/projects/{ref}/analytics/endpoints/metrics` uses Management Bearer auth, OAuth `analytics:read` or fine-grained `analytics_logs_read`. 200 returns `text/plain` or `application/openmetrics-text`. Errors: 400 inactive/unhealthy/unavailable metrics, 401, 403, 429, 500 fetch failure. No time-range/history parameters are defined. — [API reference](https://supabase.com/docs/reference/api/v1-scrape-project-metrics), [OpenAPI](https://api.supabase.com/api/v1-json)
- Supabase's pinned Prometheus configuration also shows direct project scraping at `https://<ref>.supabase.co/customer/v1/privileged/metrics`, Basic auth username `service_role`, password service-role key. This is a distinct authentication route, not proof that a Management token or modern secret key works as that password. Prefer the documented Management route for scoped integration access. — [Pinned configuration](https://github.com/supabase/supabase-grafana/blob/04469b485b758e3a28bdea5239f540a4c3604603/prometheus/prometheus.target.yml.tpl)
- Official sample exposition contains `node_cpu_seconds_total` counter with cpu/mode labels, `node_memory_MemAvailable_bytes` and `node_memory_MemTotal_bytes` gauges, disk byte counters, filesystem gauges, plus Supabase project/service labels. These are source-backed examples with placeholder zero values, not observed project readings. — [Pinned sample](https://github.com/supabase/supabase-grafana/blob/04469b485b758e3a28bdea5239f540a4c3604603/metrics.md)
- Host measurements are produced by node-exporter collectors, with CPU mode handling, diskstats and memory collectors implemented separately. — [CPU source](https://github.com/prometheus/node_exporter/blob/master/collector/cpu_linux.go), [disk source](https://github.com/prometheus/node_exporter/blob/master/collector/diskstats_linux.go), [memory source](https://github.com/prometheus/node_exporter/blob/master/collector/meminfo_linux.go)
- Supabase's PostgreSQL exporter service disables default metric collectors and loads `/opt/postgres_exporter/queries.yml`. Its custom query configuration is not present in the inspected public tree. Thus generic upstream PostgreSQL-exporter metric names cannot be assumed. — [Pinned service definition](https://github.com/supabase/postgres/blob/16f89701ccd464d80204b7718a0914d04a67c666/ansible/files/postgres_exporter.service.j2)
- Supabase's pinned Grafana dashboard references `pg_stat_database_num_backends` (with underscore), `supavisor_connections_active`, `supavisor_pool_connections_checked_out`, and `supavisor_pool_connections_idle`. These represent different connection layers; dashboard source is evidence of intended metric use, not universal availability or a TYPE declaration. — [Pinned dashboard](https://github.com/supabase/supabase-grafana/blob/04469b485b758e3a28bdea5239f540a4c3604603/grafana/dashboard.json)

### Inferences

Proposed `get_resource_context(project_id, window)` returns timestamped source measurements, derived values and coverage flags. Rust collector periodically scrapes current metrics and stores bounded samples; the tool obtains a fresh sample if needed and compares with retained samples. This is polling, not an automatic webhook on CPU spikes. One minute is a candidate collection period, not an API requirement or proven budget.

| Raw metric example | Type and unit | Derived interpretation |
| --- | --- | --- |
| `node_cpu_seconds_total{cpu="0",mode="idle",service_type="db",...}` | counter, seconds per CPU/mode | `100 × (1 − mean_cpu(Δidle_seconds / Δwall_seconds))` is non-idle percentage; includes iowait/steal, not query CPU. Show modes separately where useful. |
| `node_memory_MemAvailable_bytes{service_type="db",...}` / `node_memory_MemTotal_bytes` | gauge, bytes | `100 × (1 − available/total)` estimates non-available memory, not PostgreSQL allocated memory or permission to raise work_mem by all available bytes. |
| `node_disk_read_bytes_total{device="nvme1n1",...}` and `node_disk_written_bytes_total` | counters, bytes | delta bytes / elapsed seconds gives bytes/s; cannot infer disk saturation from bytes/s without limits/other evidence. |
| `node_filesystem_avail_bytes{mountpoint="/data",...}` / `node_filesystem_size_bytes` | gauges, bytes | available capacity and `100 × (1 − avail/size)`; identify the data mount rather than summing duplicate mount/device layers. `/data` is sample configuration, not a universal hard-coded assumption. |
| `supavisor_connections_active{db_name,mode,user,...}` | sample declares untyped; current connection count | Preserve dimensions and treat as instantaneous count, not query rate or direct DB backend count. |
| `pg_stat_database_num_backends` | dashboard references current backend count; TYPE not verified here | Prefer direct `pg_stat_database.numbackends` if exporter field missing and SQL access works; keep database/instance identity. |

Metric output must carry exact source names, labels, units, sample times, reset/missing status, calculation window, and derivation. Do not join all instances/replicas into one fake series. Discard rate intervals spanning resets or changed identity. Record unavailable, denied, stale, parse_failed and insufficient_samples distinctly from numeric zero. Discover metric names/types/labels from the actual response, and use `node_scrape_collector_success` if exposed to help detect partial scrape failures.

Connection/context measurements support diagnosis, not V1 pool resizing. Resource budgets must also protect the customer's DB and our polling/API allowance. Back off on 429, do not automatically enable paid features, and avoid provisioning another exporter on customers' servers.

### Gaps
- No live project metric availability was checked. Public source version and a project's hosted deployment may differ. The Management API explicitly allows metrics-unavailable responses.
- No source-backed numeric global Management API rate limit, max scrape body or required minimum scrape interval was established. Implement configurable quotas and probe responses rather than invent limits.
- Public SQL exporter query config was not located; exact pg metric TYPE/label map remains a live-discovery issue. Source samples include identifiers/devices that should not be forwarded indiscriminately to the model.

## How can the agent obtain errors and log context reliably?

### Takeaway
Use the current public unified logs endpoint, not Dashboard-internal routes or old BigQuery examples. Fetch a bounded time window and selected fields, recording gaps and truncation. Empty results are not proof of no errors.

### Cited Findings
- `GET /v1/projects/{ref}/analytics/endpoints/logs` accepts query parameters `sql`, `iso_timestamp_start`, `iso_timestamp_end`. API description requires ClickHouse SQL, unified log stream and filtering `source` for `postgres_logs`, `edge_logs`, etc. With both timestamps omitted it queries the last minute; supplied intervals are at most 24 hours and rounded to minutes. — [API reference](https://supabase.com/docs/reference/api/v1-get-project-logs), [OpenAPI](https://api.supabase.com/api/v1-json)
- Auth is Bearer with OAuth `analytics:read` / `analytics_logs_read`. 200 JSON uses `result` array and optional `error` string/object; inspect the body as well as HTTP status. Listed failures include 401, 402 usage exceeded, 403, 429. No cursor/page parameter is declared. `/logs.all` is removed and returns 410. — [OpenAPI](https://api.supabase.com/api/v1-json)
- Current Studio source queries `log_attributes['parsed.sql_state_code']` for PostgreSQL status; uses top-level `severity_text`, `event_message`, `source`, `timestamp`, `id`, and `log_attributes`. HTTP status uses `log_attributes['response.status_code']`; it is not interchangeable with SQLSTATE. — [Pinned query source](https://github.com/supabase/supabase/blob/b6fdee5f5437e19b2d2b14c83e9d61eb4039b529/apps/studio/components/interfaces/UnifiedLogs/UnifiedLogs.queries.ts)
- Source field inventory includes PostgreSQL parsed `query_id`, `process_id`, `database_name`, `user_name`, `error_severity`, `sql_state_code`, and session fields. This inventory is not proof every event contains every field. — [Pinned field inventory](https://github.com/supabase/supabase/blob/b6fdee5f5437e19b2d2b14c83e9d61eb4039b529/packages/shared-data/log-constants.ts)
- Studio implements bounded pagination using timestamp and id ordering, while its internal endpoint paths differ from the public API. Copy algorithm ideas, not internal URLs. — [Pinned pagination source](https://github.com/supabase/supabase/blob/b6fdee5f5437e19b2d2b14c83e9d61eb4039b529/apps/studio/data/logs/unified-logs-infinite-query.ts)
- Current source lists 7-day Pro and 28-day Team log retention. No exhaustive all-plan retention contract was established in this source-only investigation. — [Pinned plans source](https://github.com/supabase/supabase/blob/b6fdee5f5437e19b2d2b14c83e9d61eb4039b529/packages/shared-data/plans.ts)

### Inferences

Proposed `get_error_context(project_id, start, end, sources, max_rows)` compiles a fixed ClickHouse query selecting `id`, `timestamp`, `source`, `severity_text`, and `log_attributes['parsed.sql_state_code'] AS sqlstate` for `source='postgres_logs'`, filtered to error/fatal/panic severity, ordered by timestamp/id with explicit LIMIT. Add narrowly selected sanitized message text only when needed. Query ID/process correlation requires checking field presence and identity; never assert full mapping to pg_stat_statements from timing proximity alone.

Implement response validation, per-request payload caps, bounded subwindows, stable timestamp/id paging where verified, overlap/dedup for late arrivals, ingestion-lag allowance, and coverage metadata. Return counts by SQLSTATE plus limited redacted examples; no unrestricted log export. SQLSTATE is a string (e.g. `40P01`, `57014`), not a numeric HTTP status. The HTTP error `402` concerns API usage rather than a database query's SQLSTATE.

Persist last successful cursor/window and fetch new evidence from Supabase; own PostgreSQL retains selected earlier samples/history, not a replacement customer database. Offline access does not require mirroring their application records. To validate query-specific repairs, representative parameter/access-role context is still a separate evidence gap; resource metrics/logs cannot supply it automatically.

### Gaps
- Retention depends on plan/contract; collector must not assume old missing logs never existed. Exact maximum row count, query runtime, ingestion delay and completeness are not guaranteed by reviewed public specification.
- `log_attributes` field presence, parsed query-ID behavior, source availability and severities need a live capability probe. No actual log response was obtained.
- Source says queries can be SQL or LQL but the documented parameter is `sql`; recommend the explicitly documented ClickHouse SQL contract first.
