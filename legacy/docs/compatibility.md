# PostgreSQL ingress compatibility

Development profile, October 24, 2026. The explicit local PostgreSQL CLI profile
is runnable; see local-runtime.md. Unconfigured modes remain health-only.

## Verified local profile

PostgreSQL 17.11 with pgx v5.11.0, PostgreSQL protocol 3.0, TLS 1.2 or newer on both legs with certificate and hostname validation. SCRAM-SHA-256 relays the original application's authentication. Configure `channel_binding=disable` explicitly. Required SCRAM-SHA-256-PLUS is rejected; there is no silent downgrade or elevated database login. Plaintext/trust exists only in the explicit literal-loopback test profile.

Supported exchanges: simple query batches, BEGIN/COMMIT/ROLLBACK, failed transactions, unnamed extended Parse/Bind/Describe/Execute/Close/Sync, and Flush responses before Sync. The pgx fixture uses Exec or DescribeExec mode; pgx's default named statement cache is outside this profile. Admission begins before preparation work and stays bound through ReadyForQuery T/E. Only confirmed ReadyForQuery I releases a permit. SQL batches are never split or replayed.

One frontend owns one upstream session. This is not transaction multiplexing. A configured external pooler may own multiplexing only after its own compatibility tests. MaxSessions bounds upstream sessions independently of active transaction permits. MaxHandshakes, message bounds and explicit timeouts limit connection setup and buffering. The scheduler target/path must match the configured upstream owner.

## Cancellation and failures

Virtual random cancellation credentials map to a single frontend/upstream lease. Cancel retires that lease, closes both connections and attempts upstream cancellation over a separate connection. Queued requests are removed before forwarding. A retired key cannot cancel a new lease. PostgreSQL cancel packets have no operation generation: an arbitrarily delayed cancel on the same still-open lease cannot distinguish a previous query from its successor.

A disconnect during COMMIT or other admitted work retains both the transaction permit and physical session reservation as uncertain. No write is replayed and no successful rollback is invented. Cancellation has no completion acknowledgement. This package intentionally has no public API to clear uncertainty without evidence; B09 must supply backend reconciliation and durable restart recovery. Repeated failures can exhaust capacity until that integration exists. Restarting the process is not proof that old database work ended.

Timeouts are explicit operator settings. An idle-in-transaction timeout closes and quarantines the lease; it does not free its reservation. Listener failure and shutdown close connections and wait for readers. Server instances are single-use.

## Unsupported or unverified

Named statements/portals, pipelining, suspended portals/cursors, frontend COPY, replication startup, MD5/cleartext/GSS/OAuth authentication and channel-bound SCRAM are rejected. Backend COPY responses close the lease with an uncertain outcome because its initiating SQL may already have run. No SQL feature scanner is claimed: LISTEN/NOTIFY, temporary tables, settings and advisory locks are not verified with managed transaction poolers.

Managed Azure, Google Cloud and Supabase/Supavisor behavior remains unverified by these local tests. No cloud credentials or cloud resources are used. Production readiness, orphan reconciliation and restart recovery remain blocked by G1/G3. Arrival-window telemetry is wired for the local profile. Ordered capacity telemetry
and calibration remain pending; preparation time must not be used as a transaction latency baseline.

## Reproduce

`go test -race ./internal/pgwire` exercises controlled wire faults, queue cancellation, retired cancellation keys, unknown COMMIT and shutdown.

`go test -race -tags=integration ./internal/pgwire -run TestPG -v` starts an isolated temporary PostgreSQL cluster with generated credentials and certificates. It verifies TLS, original role, SCRAM, simple batches, failed transaction rollback, unnamed extended queries, DescribeExec, Flush-before-Sync, running cancellation, invalid credentials and certificate rejection. It skips explicitly if PostgreSQL binaries are absent. No existing databases are touched.

## REST and browser development profile

The Go REST handler accepts only `/rest/v1` on one administrator-configured HTTPS project origin. It preserves API key, bearer token, method, query, body and PostgREST headers, while removing hop-by-hop and untrusted forwarding headers. It neither replaces user credentials nor follows redirects or retries requests. Completed upstream responses release admission; interrupted responses retain uncertain occupancy. API errors pass through; local errors use `POOLTUNE_*` JSON codes and `X-Pooltune-Error`, with 429 for tenant rate limits and 503 for unavailable admission.

Exact-origin CORS allows only configured origins and explicit request headers, exposes count/preference/overload headers, and handles preflight locally. Non-test ingress requires actual TLS. Deploy with bounded HTTP server header/read/write timeouts as well as handler limits. TLS termination in a separate reverse proxy needs a separately authenticated deployment profile; forwarded headers alone do not enable it.

Scheduling identity uses an expiring, immutable ES256 key snapshot with exact issuer/audience, signature and required expiry checks. Only verified subject identity can enter classification; unverified or unsupported tokens get no scheduling privileges and still face upstream authorization. Credentials that expire in the queue are rejected before forwarding. Public-key refresh and rotation distribution belong to control-plane integration. No per-request key network lookup is made.

`createPooltuneFetch(projectURL, gatewayURL, originalFetch)` rewrites only the exact project's REST URLs. Auth refresh, Storage, Functions, GraphQL and Realtime bypass this adapter unchanged. URL, Request and init overrides preserve body bytes, method, headers and AbortSignal. Routed requests omit browser cookies and use manual redirects. REST uploads are buffered with a 16 MiB SDK ceiling and configurable stricter gateway limit; large Storage uploads stay on their original service. Browser testing caught Chromium's streamed-request-body HTTP/1 restriction, so bounded buffering is intentional. Responses remain streamed.

The first upstream HTTP transport uses fresh connections to avoid Go Transport's automatic retries on reused connections, including side-effecting GET RPCs. This adds connection/TLS overhead. It is a correctness profile, not a measured low-latency production profile; G4 must measure it before making speed claims. A verified persistent transport is follow-up performance work, not an implicit promise.

`npm run test:sdk` tests routing and Request semantics. `npm run build:sdk` checks TypeScript. `npm run test:browser` starts a controlled Go fixture and real Chromium; set `POOLTUNE_GO` if Go is outside PATH. The fixture demonstrates reads, writes, RPC, authorization errors, cross-tenant denial responses, refresh bypass, CORS, count headers, streaming, abort and overload. It models permission responses; **it does not prove real Supabase RLS isolation or SDK-wide compatibility**. Live Supabase verification, durable unknown-request recovery and operational key refresh remain G1/G2/G3 work. The main CLI still does not expose REST traffic.
