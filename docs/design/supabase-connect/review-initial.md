# Supabase connection independent review

Reviewed base `cf693f405fb92033e14264a233aee4765d484bd5` through exact head `cd2624c158d30ffa9c8e8bc8a768e3cc4cf074ce`.
Repository: `/Users/jshum/Desktop/code-folders/pooltune`.
Review date: November 16, 2026.

The checkout, index, HEAD, branch, deployment and customer state were not modified by this reviewer. Reproduction changes exist only in a separate Git archive at `/tmp/pooltune-supabase-review-cd2624c`.

Read the assembled review brief, complete existing product graph/approvals, canonical Supabase design/implementation/progress/approvals and verification record, implementation report, applicable AGENTS, workflow policy and requesting-code-review instructions. Ask/final_only remains inherited from customer-walkthrough. This is technical advice, not human acceptance or deployment authority.

## Strengths

- OAuth uses S256 PKCE, random ten-minute single-use state, a separate browser cookie, account and exact session binding, and a second live session check after token exchange. Invalid callback paths are tested before exchange.
- Access and rotating refresh tokens are encrypted with owner/connection binding. PostgreSQL row locking serializes refresh, and the bounded detached refresh context addresses browser cancellation after rotation. Provider errors and response sizes are bounded and secret bodies are not echoed.
- Project selection re-fetches provider metadata and rejects forged names/hosts, foreign connections, changed installation epochs, unsupported identities and noncanonical destinations. The UI discloses organization scope and does not expose manual credential fields in the new setup flow.
- The native identity allowance is narrow: exact cli_login_postgres, verified connection object/backend PID, non-superuser membership, validity, fixed postgres role selection and installation marker. The changed action catalog checks retain object ownership, target identity, enrollment and recovery conditions. Direct login and application-role checks remain separate.
- CA retrieval uses a fixed HTTPS endpoint, rejects redirects and invalid/expired/non-CA certificates, and preserves hostname verification and TLS 1.2 minimum. Secondary index, maintenance and memory connections use the resolver.
- Existing local suites are meaningful, including real PostgreSQL and race coverage. The implementation report correctly limits their claims and does not claim a live Supabase result.

## Critical findings

None identified in the reviewed range.

## Important findings

### I1. Delegated connections discard the maintenance warning handler

Location: `internal/supabaseproject/delegated.go:73-77`.
Affected caller: `internal/hostedquery/project_maintenance.go:75-87` and the warning check immediately after maintenance execution.

ConnectRepair creates a fresh configuration from the provider's base and copies only RuntimeParams from the action configuration. ApplyProjectMaintenance installs its OnNotice callback on a copied action configuration. That callback is discarded on the delegated path, so `warned` remains false even when PostgreSQL emits a WARNING. This removes an existing maintenance safety guard: the caller can continue toward confirmation instead of requiring readback when PostgreSQL reports a warning. Counter verification still runs, but it is not equivalent to preserving the explicit warning guard.

Evidence: in the separate archive, connected using a caller configuration with OnNotice, executed a PostgreSQL DO block that raises a WARNING, and asserted the handler ran. It failed with `ConnectRepair discarded caller OnNotice; maintenance warning guard cannot run`. Log: `/tmp/pooltune-supabase-review-repro.log`.

Next action: preserve this safe callback explicitly without accepting caller destination/TLS overrides. Add a delegated regression test that observes a real PostgreSQL warning, preferably covering the maintenance caller as well.
Status: unresolved at reviewed head; implementer is preparing a fix per root.

### I2. Scheduling collection silently stalls after its first temporary session expires

Location: `internal/hostedquery/managed_cron_service.go:24-29`, `:77-82`.
Expiry gate: `internal/supabaseproject/delegated.go:148-152`.

ManagedProjectCron.Run opens its owner session once before an unbounded collection loop. The connector records that session's initial temporary credential expiry. Once it expires, RepairIdentitySQL returns false for this connection, including the identity check in ReadManagedCronHistory. Successful issuance/refresh elsewhere does not extend that recorded connection expiry. Run then ignores each observation error and continues with the same session. With no expired witness forcing an error return, scheduling observations and subsequent repair evidence stop indefinitely, while the independent monitor can still look healthy.

Evidence: source control flow plus a local PostgreSQL fixture check which moves the existing session's registered expiry into the past and confirms VerifyManagedRepair still refuses it after the credential resolver returns usable fresh credentials. This fixture check tests expiration handling, not a long wall-clock wait. Log: `/tmp/pooltune-supabase-review-repro.log`.

Next action: renew/reopen the collector session before its authority expires, or surface an explicit expired-authority error so the existing outer service retry opens a new session. Do not weaken the expiry guard or blindly retry all SQL errors. Add a long-lived collector/expiry regression proving observation resumes and denied authority still stops it.
Status: unresolved at reviewed head; implementer is preparing a fix per root.

### I3. A shared credential cache hit proves project visibility, not current database-write authority

Location: `internal/hostedapp/supabase_runtime.go:46-69`; preceding authorization at `:18-40`.

Before returning cached write credentials, temporaryLogin only checks the owner/epoch/connection binding and GET /projects plus GET /organizations. The cached credential is keyed by project ref and can have been minted using a different grant. If the current grant can still see the project but is denied database-write access, the cache path never calls an endpoint that proves that write authority. It returns the cached privileged credential anyway. The same incomplete check is used by the periodic watchdog. A permission downgrade can therefore fail to stop new native repair admissions until cache expiry, and another still-authorized installation can keep the shared cache renewed.

This is a structural gap in the current permission check, not a claim that a live permission downgrade was exercised. Official scopes distinguish project metadata read from database write; login-role specifically requires database:write/database_write. Supabase also documents Read-Only organization/project access and SQL execution under supabase_read_only_user. Sources: https://supabase.com/docs/guides/integrations/build-a-supabase-oauth-integration/oauth-scopes, https://supabase.com/docs/reference/api/v1-create-login-role, https://supabase.com/docs/guides/platform/access-control. The local OpenAPI copy confirms database/query accepts database_read OR database_write at the member-permission layer, so a successful SELECT 1 alone is insufficient.

Evidence: extended only the fake provider fixture in the separate archive so project/org listing continued to succeed but the temporary write-login endpoint returned 403. After a valid login was cached, temporaryLogin still returned it. Log: `/tmp/pooltune-supabase-review-write-authority.log`.

Next action: prove the current selected grant's database-write authority before native admission and at the bounded revalidation interval, while retaining serialized shared credential issuance. Do not remint on every parallel connection or delete shared roles. A fixed nonmutating Management query with read_only:false and strict effective postgres/writable-session response validation is a candidate to verify against provider semantics; a plain success status or SELECT 1 is not a proof. If no reliable in-scope proof exists, record an explicit design conflict and apply ask mode before changing the approved authority contract. Reviewer advice does not approve that change.
Status: unresolved at reviewed head; root/implementer are evaluating the authority check.

## Minor findings

### M1. The total setup time budget can exceed the HTTP response deadline

Locations: `internal/hostedapp/start.go:72-78`, `:119-120`; `cmd/pooltune-web/main.go:128-129`.

The initial project authorization and CA fetch occur before the 18-second installation context. Two provider lookups can each consume the 8-second HTTP timeout, followed by an 8-second CA fetch, then the installation's 18-second budget. The server WriteTimeout is 22 seconds. A sufficiently slow but otherwise valid attempt can therefore complete or persist partial setup after the browser has lost the response, instead of returning the intended useful retry screen. Repeated project checks during native setup also consume the 18-second installation budget. This risk already existed in simpler form, but the new flow adds several sequential network calls.

Next action: give the complete request a coherent bounded budget shorter than WriteTimeout, or use a durable setup/progress flow. Verify one delayed-provider attempt and retry, and ensure success cannot be misreported merely because the HTTP response was lost. This is a practical latency/UX risk, not evidence of a live failure or unsafe mutation.
Status: open minor recommendation.

## Verification reviewed and performed

Observed existing evidence logs:
- `/tmp/pooltune-supabase-full-unit.log`: full Go unit suite passed.
- `/tmp/pooltune-supabase-app-integration-race.log`: hostedapp passed in 17.200s; auth and supabaseproject passed from cache.
- `/tmp/pooltune-supabase-repair-integration-race.log`: focused repair/recovery suite passed in 29.010s.
- `/tmp/pooltune-supabase-vet.log`: empty successful vet log, consistent with the implementation report.

Reviewer commands used the specified Go/PostgreSQL PATH and the separate archive:
- `go test -tags=integration -race ./internal/hostedquery -run TestDelegatedManaged -count=1`: passed in 1.977s; `/tmp/pooltune-supabase-review-native.log`.
- Extended delegated fixture reproduction: expected failure for lost OnNotice; passed expiry-rejection demonstration; `/tmp/pooltune-supabase-review-repro.log`.
- Extended OAuth/provider fixture reproduction: expected failure for cached write authority after write endpoint denial; `/tmp/pooltune-supabase-review-write-authority.log`.

Reviewed unchanged connection and identity call sites. Remaining raw pgx connections in the normal supervisor/diagnostic route use the dedicated monitor or Pooltune state configuration; fixture-specific owned and queued paths remain direct-login paths. The missed long-lived delegated case is called out in I2. No broad qualification campaign was run.

## Declined to judge

- Actual Supabase consent, beta login-role response, direct network reachability, live CA handshake and temporary-role behavior. These require the explicit live verification phase; fake HTTP/local PostgreSQL cannot establish them.
- Actual provider behavior for a role downgrade and the proposed write-authority probe. The missing check and local reproduction are established; provider-specific proof for its replacement remains necessary.
- Live repair success across all six families, wider workload support, a seven-day pilot, production restore and the broader qualification campaign. These were explicitly outside this credential feature review and remain tracked limitations.
- Visual polish of the landing page/dashboard. Functional setup fields, scope disclosure, retry and readiness text were reviewed; redesign is separate work.
- External OAuth app registration and Railway secret equality. Root reports those completed; this reviewer did not inspect credential values or mutate external services.

## Technical assessment

**Unresolved findings.** The main implementation follows the approved flow and contains substantial safeguards, but I1-I3 materially affect preserved maintenance safety, continuing scheduling operation and authorization. Do not call this reviewed head technically clean until these findings are resolved and checked. Human final review, live verification and integration/deployment decisions remain separate.
