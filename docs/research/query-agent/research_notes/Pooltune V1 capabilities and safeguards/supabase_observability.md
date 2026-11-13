# Supabase observability, credentials, and deployment capabilities

Research checked November 11, 2026 (America/Vancouver). Primary public documentation and official source only. No customer project was accessed. All recommendations below are proposals for the hosted Supabase pilot, not accepted design changes or implementation authorization. Existing action families remain pool tuning, eligible index changes, and targeted ANALYZE.

## 1. What telemetry can Pooltune actually collect?

### Takeaway

There is enough documented telemetry for useful diagnoses, especially PostgREST connection pressure and expensive SQL. Build a capability inventory for each project; the documentation does not justify treating every metric, field, privilege, or plan as universally available.

### Cited Findings

**Metrics access and cadence**

- The direct endpoint is `https://<project-ref>.supabase.co/customer/v1/privileged/metrics`, HTTP Basic Auth with username `username` and a secret API key as password. The documented scrape interval is 60 seconds. [Vendor-agnostic Metrics setup](https://supabase.com/docs/guides/observability/metrics/vendor-agnostic).
- An additional Management API route is now documented: `GET https://api.supabase.com/v1/projects/{ref}/analytics/endpoints/metrics`. It emits Prometheus exposition and accepts OAuth `analytics:read` or fine-grained token permission `analytics_logs_read`. This can avoid collecting the project's data-access secret solely to scrape metrics. [Metrics API reference](https://supabase.com/docs/reference/api/v1-scrape-project-metrics).
- The main Metrics guide describes approximately 200 series, beta status, and possible metric/label changes. It says every project has an endpoint and excludes self-hosted instances. [Metrics guide](https://supabase.com/docs/guides/observability/metrics).
- **Availability conflict:** pricing marks the Metrics endpoint unavailable on Free and available on paid plans. The Metrics guide says every project, while a Supabase partner page explicitly claims Free availability. Do not resolve this by guessing or make payment a blanket prerequisite. Test access during authorized onboarding and record plan/endpoint/response. [Pricing](https://supabase.com/pricing), [Metrics guide](https://supabase.com/docs/guides/observability/metrics), [Bronto partner listing](https://supabase.com/partners/catalog/bronto).

**Verified metric examples**

The following names appear in Supabase's official example exposition, not an invented metric catalog. This is documentation evidence, not a successful live project scrape. [Official metrics sample](https://raw.githubusercontent.com/supabase/supabase-grafana/main/metrics.md).

| Export name | Kind/unit | Diagnostic use |
|---|---|---|
| `node_cpu_seconds_total` | Counter, CPU-seconds by CPU/mode | CPU and iowait rates |
| `node_memory_MemAvailable_bytes`, `node_memory_MemTotal_bytes` | Gauges, bytes | Available memory |
| `node_disk_read_bytes_total`, `node_disk_written_bytes_total` | Counters, bytes/device | Read/write throughput |
| `node_disk_io_time_seconds_total` | Counter, seconds/device | Disk busy-time trend |
| `pg_stat_database_num_backends` | Gauge, connections | Database connection count |
| `pg_stat_database_temp_bytes_total` | Counter, bytes | Temporary-file growth |
| `pg_stat_database_deadlocks_total` | Counter, events | Deadlock frequency |
| `pg_stat_statements_total_queries` | Counter, executions | Aggregate SQL throughput |
| `pg_stat_statements_total_time_seconds` | Counter, seconds | Aggregate execution work |
| `pgrst_db_pool_max`, `pgrst_db_pool_available` | Gauges, connections | PostgREST pool capacity |
| `pgrst_db_pool_waiting` | Gauge, requests | PostgREST queue snapshot |
| `pgrst_db_pool_timeouts_total` | Counter, timeouts | PostgREST acquisition failures |

Sample labels include `supabase_project_ref`, `supabase_identifier`, and `service_type`; disk and CPU series add their dimensions. `pgrst_*` samples carry `service_type="postgrest"`. The sample query totals have no query fingerprint label. Auth's `db_sql_connection_wait_duration_milliseconds_total` has `service_type="gotrue"`; it is not Supavisor wait time. [Official metrics sample](https://raw.githubusercontent.com/supabase/supabase-grafana/main/metrics.md).

- Supabase's actual dashboard queries `supavisor_connections_active` and `supavisor_db_handler_db_connection_count`. The latter groups by `user`, `mode`, and `supabase_identifier`. These are counts, not queue-latency distributions. They are absent from the example metrics file checked above, which demonstrates why a sample is not a universal contract. [Official dashboard JSON](https://raw.githubusercontent.com/supabase/supabase-grafana/main/dashboard.json).
- Upstream PostgREST independently documents `pgrst_db_pool_timeouts_total` as a counter and the three pool-state metrics as gauges. Its admin endpoint existence alone would not establish hosted access; the hosted sample above is the stronger Supabase evidence. [PostgREST observability](https://docs.postgrest.org/en/v16/references/observability.html).
- Supavisor serves ORM/driver connections; PostgREST, Auth, and Realtime use their own internal pools. Supavisor's server limit applies per user/database/mode combination, and it grows backend connections on demand. A higher ceiling is not a new adaptive-pooling invention. [Supavisor FAQ](https://supabase.com/kb/troubleshooting/supavisor-faq-YyP5tI).

**SQL diagnostic fields, distinct from exported metrics**

- `pg_stat_activity`: `pid`, `usename`, `application_name`, `state`, `query_start`, `xact_start`, `wait_event_type`, `wait_event`, `query_id`. These are current-session observations.
- `pg_stat_user_tables`: `n_live_tup`, `n_dead_tup`, `n_mod_since_analyze` are estimates; `last_analyze`, `last_autoanalyze` are timestamps; `seq_scan`, `idx_scan` are counters.
- `pg_stat_user_indexes`: `idx_scan`, `idx_tup_read`, `idx_tup_fetch` describe index activity, not exact request attribution.
- `pg_stat_database`: `temp_bytes`, `deadlocks`, `xact_commit`, `xact_rollback`, `stats_reset`.
- `pg_stat_io`: operations, context/backend type, `op_bytes`, and timing in milliseconds. Time columns require `track_io_timing`; zero can mean disabled. Statistics may be cached within a transaction; fresh polling needs a fresh transaction/snapshot. [PostgreSQL 17 statistics](https://www.postgresql.org/docs/17/monitoring-stats.html).
- `pg_stat_io` arrived in PostgreSQL 16. Do not issue its query blindly against older databases. It does not distinguish physical-disk reads from the kernel page cache. [PostgreSQL 16 release](https://www.postgresql.org/docs/16/release-16.html), [PostgreSQL 16 statistics](https://www.postgresql.org/docs/16/monitoring-stats.html).
- `pg_stat_statements` groups `(dbid, userid, queryid, toplevel)`. `calls`, `total_exec_time`, `mean_exec_time`, min/max/stddev execution times are aggregates; execution times use milliseconds. Track `pg_stat_statements_info.stats_reset` and `dealloc`; newer entries expose `stats_since`/`minmax_stats_since`. Successful executions contribute execution statistics, so this is not a complete failure ledger. SQL text/query IDs for other users need `pg_read_all_stats`. Extension setup requires preload and query-ID computation; inspect existing state before proposing installation. [PostgreSQL 17 pg_stat_statements](https://www.postgresql.org/docs/17/pgstatstatements.html).
- Supabase documents enabling `pg_stat_statements` via its extension settings and querying those execution fields. [Supabase query statistics](https://supabase.com/docs/guides/database/extensions/pg_stat_statements).

### Inferences

- Proposed collector: persist raw counters and timestamps every 60 seconds, calculate deltas only across matching labels and uninterrupted reset epochs, and store capability/source version with observations. A missing series is unknown, not zero. A negative counter delta or changed source identity ends the comparison window.
- Preserve `supabase_identifier` and service identity. Aggregate for display only after determining whether identifiers mean database, replica, or service instance. Do not add unrelated pools together to decide one pool's ceiling.
- One-minute gauges miss brief queues. A timeout counter captures failures between samples better, but cannot reveal their latency distribution. A provider-only detector can investigate recurring pressure; it is not a millisecond incident rescuer.
- Compute interval SQL mean as `Δtotal_exec_time / Δcalls`, with compatible reset markers and positive calls. Never call the all-time mean an interval mean, subtract lifetime maxima, or derive p95 from mean/stddev. Use logs/traces with individual duration observations for percentiles.
- SQL inspection should fetch metadata/stats first. Capture bounded query text only when necessary and redact it because normalization is not a guarantee that all literals disappeared.

### Gaps

- No hosted Supavisor queue-length or checkout-latency histogram contract was verified. Do not invent `supavisor_queue_depth` or promise exact wait p95. The dashboard's two names need live capability checks.
- Per-project metric retention, scrape-staleness guarantees, complete instance-label meaning, and a consistent PostgREST process-restart marker were not established. Pooltune needs its own history and explicit unknown/reset handling.
- The API reference establishes Management metrics authentication but not feature parity for every plan with the direct endpoint.

## 2. What do logs and traces actually establish, and what do they cost?

### Takeaway

Current Supabase logs use a unified ClickHouse model. Use metrics for continuous detection and narrow logs queries during investigation; app-visible success and deadline failures still require app-side signals.

### Cited Findings

- `GET /v1/projects/{ref}/analytics/endpoints/logs` accepts `sql`, `iso_timestamp_start`, and `iso_timestamp_end`; use Bearer Management credentials. Current SQL uses `logs` plus `source`, not the older source-as-table/BigQuery model. Each API time window must be 24 hours or less. The guide was updated November 9, 2026. Without `sql`, the endpoint queries API Gateway events only. [Query logs with SQL](https://supabase.com/docs/guides/observability/advanced-log-filtering).
- Logs access requires OAuth `analytics:read` or PAT `analytics_logs_read`. [Logs endpoint reference](https://supabase.com/docs/reference/api/v1-get-project-logs).
- Useful sources are `edge_logs`, `postgres_logs`, `postgrest_logs`, `supavisor_logs`, and `pgbouncer_logs`. Top-level event fields include timestamp, ID, source, message, severity. `log_attributes` stores string values even for numeric fields. Documented gateway keys include `response.status_code`, `request.path`, `response.origin_time`, and `response.headers.x_kong_proxy_latency`. Not every field exists on every event. Internal Supabase-service connection events are excluded from hosted Postgres logs. [Log field reference](https://supabase.com/docs/guides/observability/log-field-reference).
- Postgres SQLSTATE is accessible at `log_attributes['parsed.sql_state_code']`. A shared timestamp is insufficient to associate two events; missing results may reflect source/filter/retention problems. [Log querying](https://supabase.com/docs/guides/observability/advanced-log-filtering).
- PostgREST `PGRST003` means failure acquiring a pool connection and returns HTTP 504. A raw 504 alone is less specific. [PostgREST pool behavior](https://docs.postgrest.org/en/latest/references/connection_pool.html).
- API/database log retention: Free 1 day, Pro 7, Team 28, Enterprise 90. This is separate from some Auth audit retention. [Pricing](https://supabase.com/pricing).
- Query allowance depends on data scanned, not returned rows. Documentation discourages scripted repeated scans and recommends drains for continuous streams. Future overage enforcement can degrade to 10 queries/minute and shorter retention, then cut off access after repeated overage. [Logs Query usage](https://supabase.com/docs/guides/platform/manage-your-usage/logs-query).
- The September 25, 2026 changelog says the limits are in soft launch; existing organizations receive grace through early 2027, while new organizations' grace can end earlier upon announcement. Do not present all future restrictions as already enforced on every project. [Logs pricing changelog](https://supabase.com/changelog/50925-logs-usage-based-pricing).
- Log drains require Pro or above, cost $0.0822/hour (about $60/month) per drain, plus $0.20/million events and egress, and are outside the spend cap. [Drain usage](https://supabase.com/docs/guides/platform/manage-your-usage/log-drains).
- Management API standard limit is 120 requests/minute per user, per project/organization, per endpoint. Exceeding it returns 429. [Management API introduction](https://supabase.com/docs/reference/api/introduction).
- Supabase client tracing propagates W3C context into API Gateway and Edge Function logs. It needs an existing tracing provider and exporter. JS versions 2.112.0+ require `import '@supabase/supabase-js/tracing'`; versions 2.106.0–2.111.x have different setup, and the CDN build does not support it. [Client tracing](https://supabase.com/docs/guides/observability/client-side-tracing).

### Inferences

- Proposed V1: continuous metrics plus budgeted, incident-triggered, small log windows with deduplication by event ID and overlap for late arrival. Read response/query errors before classifying zero errors. Make scan-budget exhaustion and delayed ingestion visible, not silently healthy.
- Do not require a paid drain for the first pilot. Offer it later only if continuous log detection is worth its explicit cost. Select and aggregate server-side, but do not assume a returned-row LIMIT bounds scanned bytes.
- Keep three distinct outcome labels: database execution, provider request, application operation. `pg_stat_statements` cannot measure client cancellation; gateway response duration excludes the app's full lifecycle. A trace ID may join gateway/app observations but does not automatically identify the exact Postgres session.
- The useful default product promise is improved provider-visible failures/query behavior at comparable traffic. Claims about user deadlines, completed business operations, and app p95 need existing traces or a tiny optional app integration.

### Gaps

- The field reference confirms `response.origin_time` exists but does not define its unit or exact timing boundaries in the retrieved text. Do not hard-code milliseconds solely from its name. Validate source schema and a controlled request before using it for latency claims.
- No query-body capture guarantee was verified. Gateway logs can identify routes/statuses without supplying the exact generated PostgREST SQL or per-request parameters.
- Project-specific ingestion delay, query-scan usage visibility, and telemetry completeness require later read-only pilot checks.

## 3. What is the smallest honest onboarding and permission model?

### Takeaway

The handoff's broad-PAT assumption is now stale: scoped PATs provide a practical pilot path, and the Management metrics route can eliminate a separate service-role key. SQL monitoring and SQL maintenance still need independently verified privileges and execution routes.

### Cited Findings

- Supabase's November 2, 2026 PAT documentation says newly created tokens are scoped by default to selected organizations, projects, and permissions; scoped tokens begin `sbp_fc`. Legacy tokens retain all the user's present and future project authority. Grants cannot exceed the creating user's role. [Personal Access Tokens](https://supabase.com/docs/guides/platform/personal-access-tokens).
- Fine-grained permission categories separate Logs Read, Database Read, Connection Pooling Read/Write, and Data API Config Read/Write. These are meaningfully narrower than one unrestricted project secret. [PAT permission map](https://supabase.com/docs/guides/platform/personal-access-tokens).
- OAuth still exists for polished third-party integration; scopes limit Management endpoints, and changing app scopes requires user reauthorization. The general scopes page omits some newer endpoints, so use endpoint references for exact scopes. [OAuth scopes](https://supabase.com/docs/guides/integrations/build-a-supabase-oauth-integration/oauth-scopes).

| Capability | Verified interface | OAuth / fine-grained permission |
|---|---|---|
| Metrics and logs | Management analytics routes | `analytics:read` / `analytics_logs_read` |
| Read-only SQL | `POST /v1/projects/{ref}/database/query/read-only` | `database:read` / `database_read` |
| Supavisor pool configuration | `PATCH /v1/projects/{ref}/config/database/pooler` | `database:write` / `database_pooling_config_write` |
| PostgREST configuration | `PATCH /v1/projects/{ref}/postgrest` | `rest:write` / `data_api_config_write` |

Sources: [metrics](https://supabase.com/docs/reference/api/v1-scrape-project-metrics), [read-only SQL](https://supabase.com/docs/reference/api/v1-read-only-query), [Supavisor PATCH](https://supabase.com/docs/reference/api/v1-update-pooler-config), [PostgREST PATCH](https://supabase.com/docs/reference/api/v1-update-postgrest-service-config).

- Read-only query is beta/experimental, runs as `supabase_read_only_user`, and requires schema-qualified references. Its docs do not promise every diagnostic privilege. [Read-only query reference](https://supabase.com/docs/reference/api/v1-read-only-query).
- Secret API keys provide elevated data access and bypass RLS. A dedicated secret's name or purpose does not prove metrics-only authorization, despite wording in the metrics setup guide about dedicated metrics automation. Prefer the narrower documented Management endpoint. [API key authority](https://supabase.com/docs/guides/getting-started/api-keys), [metrics setup](https://supabase.com/docs/guides/observability/metrics/vendor-agnostic).
- PostgreSQL `pg_read_all_stats` exposes system statistics; `pg_monitor` includes it plus all-settings access and scan functions that can take locks. Neither description grants index ownership/DDL. [Predefined roles](https://www.postgresql.org/docs/17/predefined-roles.html).
- Supabase documents that `postgres` is not a superuser, broad inherited role/schema privileges can matter, and `pg_stat_statements` visibility changes with `pg_read_all_stats`. Avoid assuming a newly created role is isolated simply because few explicit grants are visible. [Supabase privilege caveats](https://supabase.com/kb/troubleshooting/revoking-privileges-on-storage-pg_cron-vault-and-extension-objects-has-no-effect-916576).
- Direct connections normally require IPv6 or the IPv4 add-on. Shared session pooling provides IPv4; dedicated PgBouncer and Supavisor are different services. All services consume the underlying Postgres connection budget. [Connection methods and limits](https://supabase.com/docs/guides/database/connecting-to-postgres/pooling-and-limits).

### Inferences

- Proposed pilot setup: select one hosted Supabase project; provide a project-scoped PAT through secure onboarding; probe metrics/log/config/SQL capabilities; choose the three accepted action categories; add a maintenance connection only when SQL changes are enabled; optionally connect app traces. No credential should be pasted in chat.
- Default to Data API diagnoses where PostgREST is observed, while detecting existing ORM/Supavisor paths. Asking the customer to understand every internal service up front is unnecessary.
- Keep read credentials separate from mutation execution where practical. Provider scopes cap overall access; Pooltune's executor must further enforce exact allowed fields, object allowlists, budgets, and action categories. A pool-config scope still permits more fields than a narrowly allowed ceiling change.
- An API-only monitoring pilot may work if the read-only SQL role exposes needed views. Preflight it. If insufficient, offer a dedicated login with `CONNECT`, minimal statistics access, and extension-schema access. Never silently promote monitoring to the database-owner credential.
- The SQL mutation route must be settled with the action-contract researcher. A direct/session administrative connection remains outside the application traffic path. The HTTP read-only endpoint is not evidence that long concurrent index builds can run safely over a mutation API.
- Capability checks should record server version, extension versions/schema, relevant settings, read privileges, reachable connection paths, metric labels, retention tier, and action availability. Missing requirements disable only the dependent action and produce a concrete explanation.

### Gaps

- Exact `supabase_read_only_user` privileges for all needed views, connection credentials provisioned by OAuth, SQL API timeout/transaction semantics, and a safe ownership delegation mechanism need a disposable-project integration spike.
- No product source changes, infrastructure provisioning, app instrumentation, credential generation, or live query execution were authorized or attempted in this research.
