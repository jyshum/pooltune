# Pooltune managed burst gateway

This experimental PostgreSQL route keeps an explicitly budgeted reserve warm and releases admission when requests queue. Applications connect through Pooltune. Provider pool settings stay unchanged during a spike. It is separate from the background-only tuner.

Read [measured provider results](managed-mvp-results.md) for the successful Azure, Google and Supabase spike tests and their exact scope. An equally sized warm fixed pool may perform as well or better. The current path does not reclaim physical reserve connections or promise lower cloud bills.

## Run the private gateway

Install PgBouncer1.26.1 and make it available on PATH. The native package contains `bin/pooltune-burst`; it does not bundle PostgreSQL or PgBouncer. Start with a dedicated private state directory (0700) and mounted credential files (0600). Never place credentials inside a website, package output or repository.

Create a JSON profile using schema `burst-gateway-1`. Required fields are listed below. All file paths must be absolute. Supply existing credentials through files; the profile itself contains no passwords.

```json
{
  "schema": "burst-gateway-1",
  "target": "my-supabase",
  "provider": "supabase",
  "project": "YOUR_PROJECT_REF",
  "directory": "/absolute/private/pooltune-state",
  "secret_root": "/absolute/private/credentials",
  "backend_dsn_file": "/absolute/private/credentials/application.dsn",
  "monitor_dsn_file": "/absolute/private/credentials/monitor.dsn",
  "provider_token_file": "/absolute/private/credentials/provider.token",
  "listen": "127.0.0.1:6439",
  "certificate_file": "/absolute/private/credentials/gateway.crt",
  "key_file": "/absolute/private/credentials/gateway.key",
  "upstream_ca": "/absolute/private/credentials/provider-ca.crt",
  "sql_role": "YOUR_APPLICATION_SQL_ROLE",
  "base": 5,
  "ceiling": 8,
  "queue": 64,
  "queue_wait_ms": 2000,
  "other_reserve": 20,
  "provider_limit": 8,
  "exclusive_budget": true
}
```

The numbers above reproduce the small experiment's allowance. They are not universal recommendations. Account for provider services, other applications, every pool/user/database and the monitor connection. `exclusive_budget` is an operator assertion backed by a same-host resource lock; it is not distributed fencing.

Both DSN files must use verified TLS with the correct hostname and CA, no insecure fallback, and the same database resource. Use a dedicated application SQL role with the permissions your application needs. The monitor needs complete activity visibility for inventory checks. Supabase's shared session endpoint uses a tenant-qualified login such as `app_role.PROJECT_REF`, while `sql_role` is the actual PostgreSQL role. The PostgreSQL transaction path does not replace Supabase Auth, Realtime, Storage or REST SDK endpoints.

The gateway certificate must cover127.0.0.1, and clients must trust its CA. `key_file` must be within `secret_root`. This MVP listener is loopback-only. Remote applications need a separately designed private deployment and verified routing; do not expose this port publicly as a shortcut.

```sh
bin/pooltune-burst -config /absolute/private/profile.json -resume
```

Startup verifies authentication, distinct warm PostgreSQL backends, inventory, complete connection budget and provider pressure. `-resume` explicitly enables admission after those checks. Without it, startup remains frozen. Point the application's PostgreSQL configuration to127.0.0.1:6439, preserving its database/user/password and requiring hostname-verified gateway TLS. No SQL is replayed after an uncertain outcome.

## Provider differences

- Supabase: use the stable session endpoint when direct IPv6 connectivity is unavailable. Use a management API token file for metrics. No live tenant resize is performed.
- Azure: set `provider` to `azure`, plus `subscription`, `resource_group`, and `instance`; use the verified direct PostgreSQL hostname. Supply an Azure management access token in the private token file and refresh that file externally before expiry.
- Google: set `provider` to `google`, `project` and `instance`. Use the exact certificate DNS name from the instance metadata. If it lacks public DNS resolution, `backend_ip` can pin its verified IP through a private single-name resolver while TLS still checks the original hostname. Supply a Google access token file and refresh it externally. Current verified TLS setup uses Google's shared-CA mode. The native managed pool's dynamic resize is not certified by this route.

Google host-pressure samples are delayed, with a240-second maximum source age. Azure uses135seconds and Supabase75seconds. Azure is polled every5seconds. These are coarse pressure signals, not instantaneous CPU-overload protection. SQL inventory is independently checked on a short cadence. A failed metrics refresh retains the last sample only until its original source expiration.

## Operating limits

The CLI emits redacted current state. Effective admission, warm backend count and queue occupancy are different quantities. The managed gateway has its own authenticated dashboard with durable admission history and reviewed restore controls. The older background-tuner dashboard remains a separate profile.

SIGINT/SIGTERM after startup freezes new admission and waits for active transactions. Unknown outcomes retain their reservations and require operator reconciliation. No automatic bypass or backup route exists. A crash is not transparent: retain state and reconcile old backend work before restart. Do not delete the ownership lock or edit lifecycle records to force recovery.

A clean drained shutdown can restart in the same state directory. Backend replacement or restart causes a safety hold; exact PID retention currently requires reconciliation, including connection replacement after the configured one-hour lifetime. Startup signal handling remains a known limitation. COPY and suspended portals are outside the narrow PostgreSQL protocol profile. AI and business-priority scheduling are deferred.

The latest [three-provider result](managed-mvp-results.md) supersedes the earlier failed confirmation. Used/tested PgBouncer backends remain established capacity, with normal liveness checks before reuse. They are not new logins or a guarantee of zero check latency.

## Open the private service UI

From the extracted package, supply the absolute UI path:

```sh
bin/pooltune-burst -config /absolute/private/profile.json -resume \
  -dashboard-listen 127.0.0.1:8081 -assets /absolute/pooltune/ui
```

Open http://127.0.0.1:8081. Read `claim.token` from your private state directory and paste it into the installation claim form. Choose an administrator password of12 to72characters. Keep this token and database credentials out of URLs and screenshots. Subsequent visits use the administrator password.

The UI shows live admission, established connections, active/queued work, uncertain outcomes, provider CPU/memory and source age. It marks stale or lost observations unavailable. It does not claim query speed or money saved from connection counts. Startup `-resume` enables admission; Ctrl+C permanently freezes this process while active work drains. The authenticated dashboard can freeze admission, resume automation, or restore a confirmed historical admission limit as a new revision. Restore holds the selected limit while safety checks remain active. See [managed history and restore](managed-restore.md) for persistence, bounds and recovery limits. Browser/server clock skew can produce a false stale display over a remote tunnel.

The UI folder must be separate from state and credential folders. Dashboard bind addresses are loopback-only. No public managed service or automatic backup route is included.
