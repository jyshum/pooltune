# Background pool tuning: experimental local profile

This profile is under development and final review. It is not hosted-provider
support or production release approval. Azure, Google Cloud and Supabase remain
the intended product targets. Live compatibility is required before enabling
those paths. AI and business-priority scheduling are deferred.

## Route and authority

Applications retain their normal TLS connection to a separately running
PgBouncer. The tuner has no SQL ingress, owns no pooler child process and never
replays application work. It observes and adjusts one transaction-mode pool for
one database/role, on the same host and under the same OS owner. Loopback tunnels do not satisfy this requirement. Process and socket visibility must be available; container and remote profiles are not verified. It shares the
existing cluster ownership lock with the legacy proxy profiles, so they cannot
operate competing local budgets. Multiple hosts/OS accounts and out-of-band
administrative writers are unsupported. The operator must establish exclusive
management; local locks cannot fence remote administrators.

The adapter checks the pooler's configured topology, actual PostgreSQL backend
identities, owned local processes and exact TCP socket pairs, monitor visibility, absent prepared transactions, no foreign cluster
clients, and the full nonreserved connection budget including its monitor. It
rejects multiple scopes, configuration drift, unknown server identities and
partial or uncertain writes. It keeps a warm application-role client so the
configured minimum is eligible. The local profile sets minimum and maximum
together; it does not claim that every hosted pooler exposes this mechanism.

## Deterministic decisions

Every second, fresh actual pool counts and same-host CPU/memory feed a bounded
occupancy controller. Five continuous seconds of waiting clients, a fully busy
established pool and low resource pressure permit at most 25% growth. Thirty
continuous idle seconds permit one bounded decrement. Five seconds separate
changes. Missing/stale observations, sampling gaps, pending operations and
unknown outcomes reset those windows and prevent compounding changes.

Owner-approved minimum/maximum and CPU/memory growth/backoff thresholds are
mandatory. No threshold is supplied as a production recommendation. High
pressure reduces the configured ceiling by 20%, no lower than the approved
minimum, even when elective automation is frozen. Existing transactions drain;
the tuner cannot immediately stop overload, remove database work or enforce
request deadlines. PgBouncer's ordinary queues and timeouts still apply. These
occupancy rules are experimental, not an optimal-concurrency or latency claim.
They deliberately do not fabricate per-query latency/error evidence.

Intent is persisted before any setting operation. The provider journal retains
intent, acknowledgement and readback separately. Confirmation requires the
expected setting and actual established count. An ambiguous or partially applied
operation holds further changes. The tuner never retries an uncertain operation
to discover whether it happened. Owner history and freeze state are retained in
private fsynced snapshots; the physical operation journal is append-only SQLite.
The owner history is bounded at 1,000 records and refuses further changes at its
limit. Archive/recovery tooling for that limit is not yet implemented.

Every process start freezes elective automation. It must reconcile current
settings against retained state and pending provider history before the owner
can resume. Credentials are private referenced files. Administrator access is
literal loopback with the existing claim/password/session/origin/CSRF controls.
Static assets may not overlap state or credential directories and are served through an opened filesystem root that rejects escaping symlinks. Retained authority binds the database cluster and database/monitor identities plus the pooler process identity. Pooler replacement or restart conservatively requires reconciliation; an automated migration/rebinding tool is not implemented. No public
administrator deployment is authorized by this profile.

## Verification to date

`go test -race -tags=integration ./cmd/pooltune-tuner -run TestBackgroundTunerCrashLeavesDirectRoute -v`
uses real disposable PostgreSQL and PgBouncer, four direct pooled transactions,
actual host pressure and a separate tuner OS process. Observed physical growth
was 2 -> 3 -> 4. SIGKILL of the tuner preserved all four transactions and a fresh
connection. Independent restart retained the setting and stayed frozen. After explicit resume, a real quiet window reclaimed one established connection, from 4 to 3. This
proves the local route survives tuner failure, not pooler/database HA, backup
routing, hosted compatibility or performance benefit.

`go test -race -tags=integration ./internal/provider -run TestLocalPool -v`
verifies physical growth/drain with active transactions, fresh waiting-client
measurement, foreign-work/configuration/budget refusal, retained intents, distinct resource identities and loopback-forwarder rejection.
Browser tests use real private authentication and the tuning service with
explicitly controlled pool/host observations. They cover independently expiring freshness when a state request hangs and bounded request timeouts. They are UI evidence only.

## Build and launch

Build `go build -o pooltune-tuner ./cmd/pooltune-tuner` and
`npm --prefix ui run build`. Supply the JSON schema in `tuning.Config` through
`pooltune-tuner -config /absolute/path/profile.json`. Create the state directory
as 0700 first. Required fields:

- `schema`: `local-tuner-1`; `target`: a bounded identifier.
- `directory`, `secret_root`, `assets`: absolute directories; assets points at
  the built UI and must be disjoint from both private directories.
- `admin_dsn_file`, `application_dsn_file`, `monitor_dsn_file`: private 0600 files
  under `secret_root`. TLS verify-full, literal loopback targets and the exact
  one-scope adapter checks are required. Credentials never enter the browser.
- `listen`: a literal loopback IP and nonzero port.
- `exclusive_local_pool`: explicit operator acknowledgement of the above scope.
- `bounds`: minimum, maximum, cpu_grow, cpu_backoff, memory_grow,
  memory_backoff. Fractions must be finite, positive, below 1 and ordered.

The private `claim.token` file is created at first start. Use it in the local
claim screen and choose an administrator password. Claim tokens expire after
24 hours. Offline password recovery uses the existing `pooltune-owner` tool and retains the control database. For unclaimed token replacement, stop the tuner, use `replace-claim`, then install the replacement token in `directory/claim.token` without a trailing newline and with mode 0600 before restarting. Keep the state and provider journal intact.
Review verified observations and bounds before pressing Resume automation.
Freeze is always available. Restoring a confirmed capacity creates a new
intent under current bounds and freezes elective automation. Restoring the current capacity is refused before creating intent. A repeated idempotency key with a different restore target is rejected.

## Provider discovery

`go run ./cmd/pooltune-provider -config /absolute/path/provider.json` runs actual
read-only provider adapter requests, with no inventory or write authority.
Fields: `provider` (`azure`, `google`, `supabase`), `target`, and exact provider
resource identifiers (`subscription`, `resource_group`, `instance` for Azure;
`project`, `instance` for Google; `project` for Supabase). Azure/Google use their
existing authenticated CLI to obtain a short-lived token in memory. An optional
private absolute `token_file` is supported and required for Supabase. No token
is written to evidence. This command never calls Apply.

Configured settings do not establish working connections. Google discovery
reports omitted max/min flags as incomplete rather than inventing implicit
values. Azure's actual Go reader now confirms default_pool_size=5 and min_pool_size=0; the supplied password authenticates both direct and built-in pooled SQL routes with verified client TLS. The
Google test instance is stopped with managed pooling enabled and one pooler,
but has no explicit max/min flags. Supabase's prior test project is active;
its management token now passes the actual Go reader and reports a transaction pool size of 10. The supplied database password authenticates through both shared session and transaction poolers with verified client TLS. Direct hostname resolution is unavailable on this workstation. The backend session reports ssl=false. This does not describe the verified client-to-pooler TLS connection; local socket versus network transport was not established. No SSL setting was changed. Azure/Google starts were authorized for at most 30 minutes. Google returned ERROR_STOCKOUT and remained stopped; its database password is still unverified. See the continuation handoff for final Azure results and confirmed cleanup.

## Remaining gates

The normal route surviving a local tuner crash does not provide proxy backup
routing or HA for PgBouncer/PostgreSQL. Partial setting failures, correlated
state loss, external writers and customer backup restoration remain limited.
Azure and Google require actual provider-specific pressure/demand cadence,
complete affected-scope budgets, scoped changes and readback. Supabase cold
mutation remains disabled; Supavisor is separate from PostgREST. The previous
fixed-versus-auto diagnostic did not demonstrate a tuning advantage. No speed,
timeout, cost or hosted-support claim follows from the local correctness tests.
