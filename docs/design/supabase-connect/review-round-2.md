# Supabase connection focused re-review

Reviewed fix range `cd2624c158d30ffa9c8e8bc8a768e3cc4cf074ce..7dab55e7f7bc626938ab5f8a3d74b65980885e58`, incorporating the original whole-feature review from `cf693f405fb92033e14264a233aee4765d484bd5`.

Read current canonical progress, the original complete design/approval context, correction report and all eleven changed source/test files. Ask/final_only and all existing authority constraints remain. No checkout, index, branch, external service or customer state changes were made. Reviewer reproduction code exists only in `/tmp/pooltune-supabase-rereview-7dab55e`, a separate exact-head archive.

## Strengths and disposition

- I1 is resolved at this head. ConnectRepair now preserves only the safe OnNotice callback in addition to action RuntimeParams; destination/TLS/password still come from the trusted connector. The regression observes a real PostgreSQL WARNING.
- I2 is resolved at this head. The cron collector replaces closed or soon-expiring verified sessions before collection and each observation. It uses a six-second margin for the bounded observation and timestamp precision. Renewal goes through fresh authorization and installation checks, and denial returns to the supervisor rather than silently continuing. The test observes a replacement native backend and failure on denied renewal. It does not prove a live pg_cron campaign.
- I3 is resolved at the code/contract level. Current grant verification now performs a fixed nonmutating catalog query under that grant before every cache admission and in the existing watchdog. It requires effective nonsuperuser postgres in the postgres database on a writable primary. It retains serialized shared login issuance. Tests cover readable-but-write-denied access, another account's cached-credential attempt, healthy-grant isolation, transient HTTP statuses and unsupported response shapes.
- This authority probe is consistent with Supabase's documented distinction: read-only member SQL executes as supabase_read_only_user. Sources: [Access Control, footnote 7](https://supabase.com/docs/guides/platform/access-control), [Management query API](https://supabase.com/docs/reference/api/v1-run-a-query), [OAuth scopes](https://supabase.com/docs/guides/integrations/build-a-supabase-oauth-integration/oauth-scopes). Actual provider writable response remains a required live verification item. Local SQL/fake-provider tests do not establish that production behavior.
- M1's aggregate setup-budget and CA lock problems are addressed: one 18-second context covers setup, and CA network I/O no longer holds the shared mutex. Refresh row-lock timeout is correctly temporary rather than terminal grant denial. However, the new refresh deadline change introduces I4 below, so the overall correction is not yet technically clean.

## Critical findings

None identified.

## Important finding

### I4. A caller deadline can discard an already-rotated refresh token

Location: `internal/hostedapp/supabase_oauth.go:178-182`, persistence at `:221-228`.

The new detached context adopts the caller's earlier deadline and is used both for token exchange and the database update/commit. An exchange can succeed with little caller budget remaining. If persistence then crosses that deadline, the transaction rolls back after Supabase has already invalidated the old refresh token. The next request retries the stored old token, receives invalid_grant, clears the grant and forces reconnection for every project sharing it. Ordinary caller cancellation is detached, but an imminent caller deadline still destroys the rotation protection.

Independent reproduction in the separate exact-head archive uses a real local PostgreSQL store and fake rotating provider. The caller has 70ms remaining; the provider returns the new token immediately; a 150ms local persistence delay models a slow database update. The first call fails with `timeout: context deadline exceeded`; the next fails with `Supabase authorization expired or was revoked. Connect Supabase again`. This is deterministic loss after a successful exchange, not an assertion about a timed-out response whose token is unknowable.

Evidence: `/tmp/pooltune-supabase-rereview-refresh-deadline.log`; reproduction source `/tmp/pooltune-supabase-rereview-7dab55e/internal/hostedapp/reviewer_refresh_deadline_integration_test.go`.

Next action: preserve a bounded refresh/persistence critical section once rotation can occur, independently from the browser's wait budget. Either decline to start a refresh when insufficient budget remains and reserve persistence time, or let a separately bounded refresh finish while the waiting request returns on its deadline. Retain database serialization, avoid duplicate refreshes, and never classify waiting timeout as revoked. Add a test with successful provider rotation followed by caller deadline/cancellation during persistence; assert the next request uses the new token and the browser budget remains bounded.

Status: unresolved at 7dab55e. This requires a focused fix and re-review.

## Minor finding

### M2. A null authority proof is treated as a permission denial

Location: `internal/hostedapp/supabase_runtime.go:64-78`.

The response is decoded into `[]map[string]bool`, so JSON `[{"authorized":null}]` becomes an existing false boolean. It is treated as errSupabaseProjectAccess, which the watchdog can persist as a blocked project requiring reconnect. This contradicts the stated temporary-unavailable handling for malformed proof. The new shape test only asserts an error, so it does not catch the classification.

Next action: distinguish null from an exact JSON false. Null/invalid shape should be temporary-unavailable; exact false should remain project denial. Add exact error classification assertions.

Status: open minor finding.

## Evidence

Inspected final implementer logs:
- `/tmp/pooltune-supabase-review-final-app-race.log`: hostedapp 36.189s, auth 17.265s, supabaseproject 8.357s, all passing.
- `/tmp/pooltune-supabase-review-final-repair-race.log`: focused repair/recovery passed in 30.177s.
- `/tmp/pooltune-supabase-review-final-authority-race.log`: final authority/deadline suite passed in 24.081s.

Independent passing checks in the exact-head archive, using the specified Go/PostgreSQL PATH:
- `go test -tags=integration -race ./internal/supabaseproject -run 'TestDelegatedConnector|TestOfficialCA' -count=1`: passed 2.307s; `/tmp/pooltune-supabase-rereview-native-ca.log`.
- `go test -tags=integration -race ./internal/hostedquery -run TestDelegatedManaged -count=1`: passed 3.131s; `/tmp/pooltune-supabase-rereview-cron.log`.
- `go test -tags=integration -race ./internal/hostedapp -run 'TestSupabaseWriteAuthority|TestSupabaseOAuthBinding' -count=1`: passed 23.966s; `/tmp/pooltune-supabase-rereview-authority.log`.

Independent failing reproduction: I4, as described above. It adds no production code and leaves the checkout unchanged.

## Declined to judge

- Actual Supabase consent, Management API effective identity response, temporary role issuance and real permission downgrade behavior. Root owns live verification; tests cannot establish these.
- Live results for six repair families, broader qualification, production restore and the seven-day pilot. These retain their original out-of-scope status.
- External app and Railway configuration values. No secrets were inspected or changed.
- Landing/dashboard redesign. Functional new setup and recovery behavior remain within this review; visual redesign remains separate.

## Whole-feature technical assessment

**Unresolved findings.** The three original Important findings have targeted fixes that preserve the approved design. The new deadline regression can still lose a rotating shared grant, so the feature is not technically clean at `7dab55e`. M2 should also be corrected while the parser is being finalized. Human review, integration and live verification remain separate decisions.
