# Supabase onboarding final independent review

## Verdict

**Technically clean for the reviewed code and local test contracts at `6c1487f19f2acca1c07f78a3d6ccb48956a4594e`. No unresolved Critical, Important or Minor finding identified.** All six findings from the original review and correction review are resolved. This is not live Supabase verification, deployment approval, or human acceptance of the milestone.

Whole-feature range: `cf693f405fb92033e14264a233aee4765d484bd5..6c1487f19f2acca1c07f78a3d6ccb48956a4594e`.
Original implementation: `cd2624c158d30ffa9c8e8bc8a768e3cc4cf074ce`.
First correction: `7dab55e7f7bc626938ab5f8a3d74b65980885e58`.
Final focused correction: `7dab55e7f7bc626938ab5f8a3d74b65980885e58..6c1487f19f2acca1c07f78a3d6ccb48956a4594e`.

The review incorporates the complete assembled context, canonical design/implementation/progress/approvals, original feature review, focused correction review, and implementation report. It follows inherited ask/final_only and existing authority decisions. No checkout, index, branch, external service or customer state was changed. Tests used a separate exact-head archive and disposable local PostgreSQL. No agents were spawned and no broad qualification campaign was run.

## Strengths

- OAuth state, PKCE and account/session binding prevent callback reassignment and replay. Encrypted grants remain bound to their account and immutable connection identity. Database row locking serializes rotating refresh across processes.
- Provider-derived project metadata and current grant authority precede native connection admission, including cached temporary credentials. A readable project listing alone is insufficient. Project denial, terminal grant denial and temporary provider failures retain separate handling.
- Delegated identity is accepted only through the server-owned connector and exact verified native connection object. It retains destination/TLS ownership, exact role checks, validity and membership checks, and installation verification. Native owner/target/generation/enrollment/recovery fences remain active. The affected secondary index, VACUUM, memory and measurement connections use fresh resolution.
- Temporary CLI issuance remains serialized and encrypted per project. The implementation avoids rotating the shared CLI login for every parallel connection. Credential expiry and stale-password retry are bounded; project disconnect retains shared grants used by other projects.
- Normal setup contains project selection and repair choices without manual secret fields. Partial setup and retry preserve choices. Existing manual account/project behavior remains supported.
- Real PostgreSQL tests supplement provider fixtures. Coverage includes native delegated identity, warnings, concurrent index behavior, renewal and denial, target/recovery guards, refresh serialization and deadline boundaries.

## Findings and resolution

| Finding | Prior severity | Final disposition |
| --- | --- | --- |
| I1: delegated connector dropped OnNotice, disabling the maintenance warning guard | Important | Resolved by preserving the safe caller callback. Independently checked the real PostgreSQL WARNING regression. Destination, TLS and password still come from the trusted connector. |
| I2: long-lived cron collector retained an expired delegated session | Important | Resolved by renewal before passes and observations, with the six-second validity margin. Renewal rechecks provider and installation authority; denied renewal stops the collector. Real PG evidence observes a replacement backend and denied renewal. |
| I3: a read-only grant could reuse privileged cached CLI credentials | Important | Resolved at the code/contract level by the fixed writable-postgres probe on cache admission and the watchdog. Exact supported identity is required; shared issuance serialization remains. Actual production response remains a live verification item. |
| M1: setup budget excluded provider/CA work and contained deadline escapes | Minor | Resolved by the shared 18-second setup context, no CA mutex held across network I/O, and bounded detached refresh. Final local persistence may use at most two extra seconds. |
| I4 / R1: caller deadline could roll back an already-rotated refresh token | Important | Resolved by independent bounded local UPDATE and COMMIT settlement after the completed exchange. Independent previously failing reproduction now passes. |
| M2 / R2: null authority proof became permanent project denial | Minor | Resolved by nullable-boolean parsing. Null and malformed responses are temporary-unavailable; exact false remains project denial. Exact classification test passes. |

No additional Critical, Important or Minor finding remains from these reviewed ranges.

## Final correction assessment

`internal/hostedapp/supabase_oauth.go:184-196` establishes separate work and settlement contexts. Work ends at the earlier caller deadline or 12 seconds. Settlement is capped at that original work deadline plus two seconds. Both are detached from caller cancellation so a browser disconnect does not abandon an in-flight rotation.

The provider exchange at `internal/hostedapp/supabase_oauth.go:223` uses only the work context. After a completed exchange, `:231-247` gives local UPDATE and COMMIT one shared two-second persistence reserve, bounded by settlement. The transaction keeps the row lock through commit. A local persistence failure returns temporary-unavailable; it does not itself revoke the grant. A genuine terminal provider response is stored as terminal only through the same bounded transaction. Deferred rollback is also bounded.

The shared setup budget therefore permits at most 18 seconds of work plus two seconds of local refresh settlement, leaving a response margin under the existing 22-second HTTP write limit. Cached-token reads do not extend the external-work budget.

`internal/hostedapp/supabase_runtime.go:67-79` now distinguishes an exact JSON false from null. The single-row, single-key shape guard remains. Null/missing/invalid proof cannot become durable permission denial merely through Go's zero value.

Finite durability limit: if local database persistence is unavailable for longer than the two-second reserve after an external rotation, storage can still fail and a later attempt may require reconnect. Likewise, an externally rotated token in a response never received cannot be recovered by these local tests. The change fixes the ordinary completed-response/deadline regression; it does not promise durability through an arbitrary outage. This is an explicit operational limit, not an unresolved version of I4.

## Evidence

Independent final run in `/tmp/pooltune-supabase-final-review-6c1487f`:

`go test -tags=integration -race ./internal/hostedapp -run 'TestReviewerRefreshDeadlineAfterProviderRotation|TestSupabaseRefreshPersistsAfterRequestDeadline|TestSupabaseWriteAuthorityRequiresExactOwnerProof' -count=1 -v`

Passed in **6.539s**. Log: `/tmp/pooltune-supabase-final-review-race.log`.

- Independent reproduction uses a 70ms caller budget and 150ms local UPDATE delay after a successful rotating provider response. It previously failed at 7dab55e; the unchanged assertion that the next request succeeds now passes.
- Deferred COMMIT delay preserves the rotated token and the next request reuses the stored grant.
- Slow HTTP exits at the work deadline without spending local persistence reserve.
- Three-second persistence stall exits in 2.03 seconds, returns temporary-unavailable, and leaves the grant unrevoked.
- Null-versus-false authority error classification passes.

Inspected implementer final logs:

- `/tmp/pooltune-supabase-refresh-reserve-final-race.log`: affected Supabase/setup integration race suite passed in 27.631s.
- `/tmp/pooltune-supabase-refresh-reserve-final-unit.log`: hostedapp unit tests passed.
- `/tmp/pooltune-supabase-refresh-reserve-final-vet.log`: empty successful vet output, corroborated by implementation report.

Earlier independent evidence remains applicable because the final correction touches only four hostedapp files:

- Delegated connector and CA race checks passed in 2.307s: `/tmp/pooltune-supabase-rereview-native-ca.log`.
- Delegated managed/cron race checks passed in 3.131s: `/tmp/pooltune-supabase-rereview-cron.log`.
- Authority and OAuth binding race checks passed in 23.966s: `/tmp/pooltune-supabase-rereview-authority.log`.
- Initial independent native delegated checks passed in 1.977s: `/tmp/pooltune-supabase-review-native.log`.
- Inspected implementer full affected application/native suites and focused repair/recovery suite, including final 30.177s repair run. Exact commands and limits are retained in `/tmp/pooltune-supabase-connect-implementation-report.md`.

Toolchain used: `/Users/jshum/.local/share/pooltune-toolchains/go1.27.4/bin:/opt/homebrew/opt/postgresql@17/bin:$PATH`.

## Provider semantics and remaining verification

The fixed probe is consistent with Supabase's documented access model. The query endpoint allows database read OR write permission, so HTTP success is not sufficient. Access Control footnote 7 states that read-only member SQL runs as `supabase_read_only_user`. Requiring the effective nonsuperuser `postgres` role on a writable primary is therefore a meaningful current-authority check. Sources previously inspected: [Access Control](https://supabase.com/docs/guides/platform/access-control), [Management query API](https://supabase.com/docs/reference/api/v1-run-a-query), and [OAuth scopes](https://supabase.com/docs/guides/integrations/build-a-supabase-oauth-integration/oauth-scopes).

This reasoning and fake-provider/local-SQL evidence do not establish the actual production response. Root still needs to verify the strict writable probe and native setup against Supabase. Unsupported role/response behavior should fail closed; it is not authority to weaken the approved proof. No real customer's permissions should be altered merely to create a downgrade test.

Declined to judge:

- Live OAuth consent/callback behavior, production Management API effective identity, beta temporary-login response and direct connectivity. These remain root-owned live verification.
- Real permission downgrade behavior beyond official documented semantics and local fail-closed tests.
- A new live six-family repair campaign, broad six-hour qualification, production restore and seven-day pilot. The native index proof and focused guard/recovery tests do not substitute for these.
- External app/Railway secret values, deployment readiness/configuration, visual redesign, and human milestone acceptance.

The code and focused local verification support proceeding to the separately authorized live verification step. They do not by themselves establish completion of that step or authorize deployment.
