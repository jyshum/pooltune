# Isolated local REST runtime

`pooltune traffic -local-rest-runtime /absolute/path/rest.json -listen 127.0.0.1:8080`
starts the opt-in `local-rest-1` development profile. It runs a TLS REST frontend,
a private control socket, loopback health, and an owned PostgREST child process.
It is not a hosted Supabase profile or a customer release. The original browser
fixture remains a separate test and supplies no real provider evidence.

## Configuration and ownership

`internal/runtime.RESTConfig` is the JSON schema. Unknown fields and trailing JSON
reject. Durations are integer nanoseconds and timestamps are RFC3339. The required
fields are:

- `Schema`, `Target`, `Database`, literal-loopback PostgreSQL `Upstream`, and
  literal-loopback TLS frontend `Listen`.
- Existing private `Directory` and `SecretRoot`; absolute `CertificateFile`,
  `PrivateKeyFile`, `CAFile`, `CohortKeyFile`, `MonitorFile`, `PostgRESTDatabaseFile`,
  and `JWKSFile` references beneath the secret root. Files must be regular, private,
  and at most 64 KiB. The cohort key is 32 random bytes encoded as hex.
- Absolute `PostgRESTExecutable`, `DatabaseSchema`, and `AnonymousRole`.
  PostgREST and its system libpq dependency must be installed separately. The
  executable must not be writable by group or others.
- Explicit `Issuer`, `Audience`, and `KeysValidUntil`. The JWKS accepts at most
  16 unique public ES256/P-256 signing keys with `kid`, `kty`, `crv`, `alg`, `use`,
  `x`, and `y`. Private fields and unsupported key types reject.
- `Epoch`, HTTP `Capacity`, PostgreSQL `PhysicalAllocation`, `MaxRequests`,
  `MaxHeaderBytes`, `MaxBodyBytes`, `Timeout`, and optional exact `AllowedOrigins`.
  Request timeout is at most one minute; other limits are checked at startup.
- Optional absolute private `RecoveryDirectory`, separately retained from traffic
  and control state, configured at initial installation. See recovery.md.
- Owner-approved `Guardrails`, `Registry`, `Stable`, and `Scheduler`. Registry
  selectors must name this target and REST path; scheduler maximum active requests
  must equal `Capacity`. No calibrated defaults are supplied.

Monitor and PostgREST credential files contain connection strings for exactly the
configured database endpoint. Both connections require verified PostgreSQL TLS.
The monitor needs complete activity visibility and access to read the cluster
identity through `pg_control_system()`. A shared same-host OS-owner lock excludes
other local traffic profiles in the same physical PostgreSQL cluster, even with
different state directories or database names; see `local-runtime.md`. The PostgREST login must be
NOINHERIT, non-superuser, without BYPASSRLS or replication privilege, and have a
positive connection limit no greater than `PhysicalAllocation`. Grant it only the
intended application/anonymous roles. The runtime checks available server slots,
rejects unrelated client backends and active old work, and configures the child's
pool to the fixed allocation. HTTP concurrency is a separate unit; 20 admitted
HTTP requests do not claim 20 warm database connections.

Pooltune owns the child configuration, database URI and 0600 Unix socket under the
0700 traffic directory. Inherited PGRST/PG settings cannot override that binding.
The DB URI stays in the child environment, never command arguments, logs or UI.
Only this local socket path strips `/rest/v1`; hosted HTTPS ingress retains the
original path. Authorization, API key, query, method and body still reach
PostgREST. There is no automatic upstream replay. See the official
[PostgREST configuration](https://docs.postgrest.org/en/stable/references/configuration.html)
and [database authorization](https://docs.postgrest.org/en/stable/references/auth.html).

## Control and measurement

Use the existing `local-control-1` profile with `Service.Path` set to `rest`, the
same registry/guardrails and this traffic directory's `control.sock`. Control and
admin remain separate from the TLS REST listener. Observation can run without
managed AI or an evaluation profile. Automatic promotion still requires reviewed
calibration evidence; these correctness tests supply none.

A verified JWT subject supplies cohort identity. Unsupported, expired, unknown-key
or invalid JWTs receive bounded normal-class fallback scheduling. It uses stable
during split canaries and candidate during full observation, when stable admission
is closed; after acceptance it uses the new stable policy. Claimed identity is
cleared and cannot select a privileged workload. Original authorization is
still forwarded and PostgREST remains the authorization boundary. Key snapshots
are immutable and expire. Online key refresh remains open.

One measured unit begins after bounded header/body validation and identity
verification, before scheduler admission. Queue and total histograms cover
admission through upstream completion, not frontend header/body upload time.
HTTP errors are failures, queue expirations are timeouts, and an interrupted
upstream response retains uncertain occupancy. Success histograms never include
those outcomes. Request-slot overload marks the window incomplete because a
verified cohort is not yet available. Unknown workloads also invalidate evidence.
Do not describe these histograms as full browser latency or G4 performance proof.

Optional `CapacitySafety` uses measured local-host pressure and the deterministic
controller to reduce or stop admission. Its ceiling and HTTP envelope must equal
the configured HTTP capacity. `Start` supplies initial/restart admission. Optional
control capacity configuration enables measured bounded HTTP growth; see
controller.md. The physical PostgREST pool stays fixed. A latched safety ceiling
cannot be raised automatically. The separate owner safety-stop
resume action requires verified healthy evidence; see operations.md. Host metrics describe the local host, including
other processes. They are not managed database telemetry.

Shutdown fences admissions and waits a bounded interval for handlers. Unresolved
requests produce an error. Child-process death also fences admission and stops the
frontend. Anchored traffic restart stays quarantined until independent control history
and actual backend reconciliation pass. Set `RecoverLocalREST` on control and
use a strictly newer traffic epoch; see recovery.md. Unanchored snapshots remain
quarantined. Do not erase state or reuse an old epoch to bypass recovery. Run one local traffic profile per isolated
database; combined PG/REST allocation and provider recovery are still gated.

## Evidence

`go test -race -tags=integration ./internal/runtime -run TestREST -v -count=1`
requires `initdb`, `postgres`, and `postgrest` on PATH. Without these binaries the
real integration skips; a skip is not evidence. Tests create a disposable database
with real RLS, generated TLS/JWT keys and a limited authenticator role.

`docs/evidence/local-rest-runtime.txt` records actual PostgreSQL/PostgREST reads,
allowed/denied writes, signature rejection, stable/candidate outcome windows,
control/admin state, rollback, full-stage anonymous/expired-snapshot fallback,
same-host ownership, graceful shutdown and child-process death. Pressure
stop is explicitly injected fault coverage. Hosted Supabase compatibility,
calibrated benefit, public browser TLS deployment, distributed/correlated-restore
recovery and container execution remain unverified.

New local tests verify actual parent SIGKILL, child-held ownership even after the
old socket is removed, explicit child termination, fresh PostgREST, independent
SQLite confirmation and preserved anonymous RLS. The child inherits the cluster
lease, so replacement refuses while it lives. A surviving child requires operator
termination; Pooltune does not guess ownership from a PID or kill unknown work.

The private state API now distinguishes `safety.restricted` from
`safety.stopped`. **Release safety ceiling** recovers a nonzero restriction after
fresh health/idle-backend proof while retaining the current admission limit.
**Resume traffic safely** handles a full stop at the approved starting bound.
Both use the existing authenticated `safety-resume` action and immutable history.
