# Supabase connect implementation report

Repository: /Users/jshum/Desktop/code-folders/pooltune
Branch: feat/pooltune-revamp-dev
Base: cf693f4
Implementation commit: cd2624c158d30ffa9c8e8bc8a768e3cc4cf074ce

Task 1 implementation is ready for independent review. No push, deployment, real customer database mutation, provider role deletion or password reset was performed by the implementer. Root owns the external OAuth app and deployment. Ask/final_only preserved; no hard approved-design conflict was found. Human final review remains pending.

## Delivered behavior

- Supabase Management OAuth with fixed authorize/token hosts, Basic token authentication, S256 PKCE, ten-minute single-use state, browser cookie, owner/session binding and post-exchange live session revalidation. Exact callback /auth/supabase/callback; config POOLTUNE_SUPABASE_CLIENT_ID and POOLTUNE_SUPABASE_CLIENT_SECRET.
- Encrypted account/connection-bound access and rotating refresh tokens. PostgreSQL row locking serializes refresh across processes. A bounded detached refresh context preserves a rotated token if the browser disconnects during exchange. Terminal token denial removes that grant's credential; project denial and transient/rate-limit errors remain distinct.
- Fresh provider project/organization lookup before selection and each new native admission. Canonical ref/host validation; form-supplied metadata is ignored. Organization scope is disclosed. Existing healthy grant reused when adding another project. Immutable connection IDs and saved organization/ref prevent silent worker redirection.
- Normal project pages contain repair choices and Start monitoring without ref/token/password/certificate inputs. Manual setup remains separately accessible. Failed setup choices persist across reload and reconnect. Readiness continues to require exact function/job enrollment.
- Fixed HTTPS official CA download, bounded response, PEM/CA/validity verification and cached validity. Native TLS hostname verification and TLS 1.2 remain intact.
- Encrypted per-database temporary CLI credential cache, serialized by a PostgreSQL advisory transaction lock. Exact cli_login_postgres only. Returned TTL governs reuse; one stale-password invalidation/remint/retry handles external CLI rotations. No shared role deletion.
- Server-owned context connector verifies native backend PID, writable primary, non-superuser temporary login, validity and postgres SET membership before fixed SET SESSION ROLE postgres, then verifies the installation marker. Narrow native repair catalog fences recognize only verified connection objects in that context, rechecking role membership and TTL. Legacy direct-login and application-role checks retain their behavior.
- Managed action/recovery/measurement sessions use the resolver, including secondary index pins, VACUUM locks and memory measurement locks. Weak connection references avoid a process-global trust registry. Cleanup contexts preserve the connector where recovery needs it.
- Workers recheck provider access every 30 seconds and before new admission; denied projects leave the active set and local sessions cancel. Reconnect preserves secrets, choices, target enrollment and recovery records while advancing the worker epoch. Disconnect clears the selected installation's credentials and its per-ref cache when no active or pending installation still needs it, while retaining shared account grants.

## Verification

All commands ran with:
PATH=/Users/jshum/.local/share/pooltune-toolchains/go1.27.4/bin:/opt/homebrew/opt/postgresql@17/bin:$PATH

1. `go test ./...` passed. Log: /tmp/pooltune-supabase-full-unit.log
2. `go test -tags=integration -race ./internal/hostedapp/... ./internal/supabaseproject` passed. Latest log: /tmp/pooltune-supabase-app-integration-race.log (hostedapp 17.200s; auth and supabaseproject cached from prior passing runs; earlier fresh runs 17.134s and 7.898s).
3. `go test -tags=integration -race ./internal/hostedquery -run 'Test(DelegatedManaged|ApplyProject|ProjectRepairAccess|ProjectFunctionMemory|ProjectCronChanges|ProjectCleanupSettings|ManagedMemory(NativeOwnerFence|FencePreserves|ActionExpiry|Fallback)|ManagedIndex(Rollback|Recovery)|ManagedCron(Witness|History|Store)|CommittedStatistics|ManagedMaintenanceRecovery|ManagedStatisticsRecovery)' -count=1 -timeout=10m` passed in 29.010s. Log: /tmp/pooltune-supabase-repair-integration-race.log
4. Final affected HTTP/setup rerun: `go test -tags=integration -race ./internal/hostedapp -run 'TestSupabaseOAuth|TestAutomaticSetup' -count=1` passed in 4.367s. Log: /tmp/pooltune-supabase-final-focus.log
5. `go vet ./internal/hostedapp/... ./internal/supabaseproject ./internal/hostedquery ./cmd/pooltune-web` passed. Log: /tmp/pooltune-supabase-vet.log
6. `git diff --check` passed before commit.

Initial missing-feature test failures and subsequent passes are recorded in /tmp/pooltune-oauth-red.log, /tmp/pooltune-oauth-green.log, /tmp/pooltune-native-red.log, /tmp/pooltune-native-green.log, /tmp/pooltune-ca-red.log, /tmp/pooltune-readers-red.log and /tmp/pooltune-readers-green.log. The concrete retry/reload choice-loss regression failed before its fix, then passed with race: /tmp/pooltune-choice-red.log and /tmp/pooltune-choice-green.log.

New integration coverage includes account swap, same-account session swap, expired/cancelled/replayed callback, PKCE, cross-owner grant/project selection, ignored forged metadata, encrypted grants, serialized rotating refresh, browser cancellation during refresh, dynamic long OAuth tokens, serialized shared CLI issuance, stale-password remint deduplication, cached-login denial after provider revocation, invalid role responses, stale installation epoch, failed setup without manual fields, saved choices after reload, reconnect preserving state, and shared-grant disconnect isolation. Native tests cover exact delegated identity, no acceptance outside its connector, secondary fresh sessions, real concurrent native index creation with a separate pin, table/function/statistics/maintenance catalog gates, revoked membership and expired role rejection. Existing direct-owner and recovery/race suites cover the affected repair families.

## Evidence and limits

Provider schemas independently read from https://api.supabase.com/api/v1-json (local research copy /tmp/pooltune-supabase-openapi.json), https://supabase.com/docs/reference/api/v1-create-login-role, https://supabase.com/docs/guides/integrations/build-a-supabase-oauth-integration, and the official CLI source https://raw.githubusercontent.com/supabase/cli/HEAD/apps/cli/src/command-internal/db-connection.sql-pg.layer.ts.

The temporary login endpoint is beta and can change. Tests use a fake HTTP provider plus disposable local PostgreSQL, not real Supabase OAuth consent or live temporary credentials. The fresh delegated mutation proof is a native index action; other affected families have focused catalog/legacy guard/recovery tests, not a new live six-family repair campaign. No broad hostedquery qualification run was performed. Normal direct Supabase connectivity is still required. Existing short setup/server request timeouts remain, so slow provider/setup attempts may require retry; pending installation state and choices are preserved. Explicit account-level provider unlink is not introduced.

Root-owned design documents and unrelated legacy/research changes remain outside the implementation commit. No product-code changes remain uncommitted at report time.

## Independent review correction round

Fix commit: `7dab55e7f7bc626938ab5f8a3d74b65980885e58`.
Fix base: `cd2624c158d30ffa9c8e8bc8a768e3cc4cf074ce`.
Original implementation base: `cf693f405fb92033e14264a233aee4765d484bd5`.
Branch remains `feat/pooltune-revamp-dev`. All eleven changed files in this correction commit are product code or tests. Root-owned docs and unrelated files remain untouched. No push, deployment, external credential changes, or customer database operations were performed.

I1: The delegated connector now explicitly preserves the caller's OnNotice callback in addition to RuntimeParams. It continues to own destination, TLS and password configuration. A real PostgreSQL WARNING now reaches the caller's warning guard. Regression evidence: `/tmp/pooltune-supabase-review-fixes-red.log` failed before the fix; `/tmp/pooltune-supabase-review-fixes-green.log` passed afterward. This is direct connector callback coverage, not a synthesized maintenance warning during an actual VACUUM.

I2: The scheduling collector checks the exact verified native session lifetime before a collection pass and before each observation. It replaces sessions that are closed or have fewer than six seconds left, preserving the five-second observation budget and one-second SQL timestamp precision margin. The replacement revalidates the installation and provider authority. No existing-session expiry predicate is relaxed. The real PostgreSQL collector regression observes replacement backend identity and then denies the renewal resolver, proving the collector stops on denied authority. This exercises the long-lived collector lifecycle; it does not fabricate a running pg_cron scheduler or claim a new end-to-end scheduling campaign.

I3: Every projectSupabaseToken check, including native admission and the existing 30-second watchdog, now performs a fixed catalog-only Management API query under the requesting grant. It requests read_only:false and disable_statement_timeout:false, and requires exactly one boolean authorized response proving effective postgres, postgres database, writable primary, and live non-superuser owner role. A successful HTTP response or SELECT 1 is insufficient. No mutation is performed by this probe. Per-ref encrypted cache and issuance serialization remain, and denied grants cannot use another account's cached credential. Empty/malformed proof is temporary-unavailable; an exact false proof is project denial; 429/503 remain temporary. The existing grant-refresh terminal handling remains separate.

Official API reference confirms database/query permits database_read OR database_write; access-control footnote 7 says read-only member SQL runs as supabase_read_only_user. Sources: https://supabase.com/docs/reference/api/v1-run-a-query and https://supabase.com/docs/guides/platform/access-control. This supports checking the actual execution role, rather than treating query success as permission. The probe's actual Supabase writable response and downgrade behavior still need live verification. Local tests establish SQL behavior and fail-closed handling, not provider production semantics. If the provider returns an unsupported identity/shape, setup will refuse it; do not weaken the proof to obtain a successful setup.

I3 tests cover database-write downgrade with project visibility retained, another account's read-only grant for the same project/cache, healthy grant isolation, 429/503, malformed responses and real PostgreSQL rejection of a superuser, read-only role and read-only transaction while accepting the supported nonsuperuser owner. Red logs: `/tmp/pooltune-supabase-authority-red.log`, `/tmp/pooltune-supabase-authority-shape-red.log`. Green: `/tmp/pooltune-supabase-authority-green.log` and final authority race log below.

M1: The 18-second setup context now starts before project loading, provider authorization and CA retrieval and is inherited by installation. The original request context remains available for rendering the response. Detached token refresh still survives caller cancellation, but respects the earlier request deadline. CA downloads no longer hold the cache mutex across network I/O, so another request can obey its own deadline. Refresh-lock timeout is classified as temporary-unavailable, not grant revocation. Pending installation state, repair choices and existing retry behavior remain.

The delayed-provider regression reproduced the original 19.014-second attempt and now returns the simple retry screen within the shared budget, retaining selected memory permission and allowing retry without manual fields. Additional failures proved the detached refresh and CA lock budget escapes, and incorrect refresh-lock revocation classification. Red logs: `/tmp/pooltune-supabase-setup-budget-red.log`, `/tmp/pooltune-supabase-budget-edges-red.log`, `/tmp/pooltune-supabase-lock-classification-red.log`. Green: `/tmp/pooltune-supabase-budget-edges-green.log` and final authority race log. The fixture corrections during development are visible in intermediate logs; only the final successful runs below are completion evidence.

Final verification:

- Full Go units: `/tmp/pooltune-supabase-review-final-unit.log`.
- `go test -tags=integration -race ./internal/hostedapp/... ./internal/supabaseproject -count=1`: passed, hostedapp36.189s, auth17.265s, supabaseproject8.357s. Log `/tmp/pooltune-supabase-review-final-app-race.log`.
- The same focused hostedquery native repair/recovery command from the original report: passed30.177s. Log `/tmp/pooltune-supabase-review-final-repair-race.log`.
- After the final refresh-lock error classification correction, `go test -tags=integration -race ./internal/hostedapp -run 'TestSupabase|TestAutomaticSetup' -count=1`: passed24.081s. Log `/tmp/pooltune-supabase-review-final-authority-race.log`.
- Focused vet: `/tmp/pooltune-supabase-review-final-vet.log`; final hostedapp vet `/tmp/pooltune-supabase-review-final-authority-vet.log`, both successful.
- `git diff --check`: passed. Product tree clean after fix commit.

Technical implementation status: the four reported findings have local fixes and regression evidence. Independent rereview and root-owned live provider verification remain. The original report's note that setup timeouts remained unchanged is superseded by M1 above. No broader qualification claims are added.

## Refresh deadline rereview correction

Correction commit: `6c1487f19f2acca1c07f78a3d6ccb48956a4594e`.
Base: `7dab55e7f7bc626938ab5f8a3d74b65980885e58`.
Four hostedapp code/test files changed; no external or documentation changes in the commit.

R1 was reproduced: the earlier deadline fix also expired the context pgx.BeginFunc used to save and commit a token that Supabase had already rotated. The local reproduction failed with a 70ms caller budget and a 150ms persistence delay; retry then received invalid_grant. The corrected implementation explicitly owns the transaction. Row-lock acquisition and external token exchange retain the earlier caller/setup deadline or 12-second refresh limit. After the provider responds, local UPDATE and COMMIT share a separate cancellation-independent two-second persistence budget, also capped at the original work deadline plus two seconds. No HTTP call can use this reserve. The row lock remains held through commit, preserving cross-replica refresh serialization. Rollback is bounded by the same overall settlement deadline. Healthy cached-token reads do not receive an extended external-work budget.

The complete intended setup work limit is 18 seconds plus at most two seconds of local refresh settlement, leaving time before the 22-second HTTP write limit. The separate persistence budget prevents ordinary caller deadline expiry from discarding a successful token rotation. It is still a finite budget: a database outage or delay exceeding that reserve can prevent persistence and require reconnect. No bounded local implementation can guarantee storage through an arbitrarily long database outage after an external rotation. A local persistence timeout is temporary-unavailable and does not itself mark the grant revoked. Provider terminal invalid_grant remains terminal on a later actual exchange.

Real PostgreSQL tests cover both a 150ms UPDATE delay and a deferred trigger delaying COMMIT after a 70ms caller deadline. Both preserve the new token and the next attempt uses the stored result. A slow HTTP refresh stops at the caller deadline without consuming the persistence reserve. A three-second local trigger is interrupted near the two-second reserve and returns temporary-unavailable without marking the grant revoked. Existing tests still cover serialized refresh, browser cancellation during exchange, the whole-setup delayed-provider retry, and refresh-lock timeout classification.

R2: Permission proof now decodes a nullable boolean. Missing, null and malformed values are temporary-unavailable. Exactly false remains project denial. Exactly true is accepted only in the existing single-row, single-key response. The regression asserts the exact error classification, rather than merely asserting an error.

Verification:

- Expected failures: `/tmp/pooltune-supabase-refresh-reserve-red.log`.
- Initial corrected regressions: `/tmp/pooltune-supabase-refresh-reserve-green.log`.
- UPDATE/COMMIT/network/persistence-budget boundary tests: `/tmp/pooltune-supabase-refresh-reserve-boundaries.log`, passed5.209s.
- `go test -tags=integration -race ./internal/hostedapp -run 'TestSupabase|TestAutomaticSetup' -count=1`: passed27.631s. Log `/tmp/pooltune-supabase-refresh-reserve-final-race.log`.
- `go test ./internal/hostedapp/...`: passed. Log `/tmp/pooltune-supabase-refresh-reserve-final-unit.log`.
- `go vet ./internal/hostedapp/...`: passed. Log `/tmp/pooltune-supabase-refresh-reserve-final-vet.log`.
- `git diff --check`: passed; product tree clean after commit.

This correction supersedes the previous report's description of applying the caller deadline to the complete refresh transaction. Independent rereview and live Supabase verification remain root-owned. No push, deploy, provider changes, or customer database operations occurred.
