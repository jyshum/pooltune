# Supabase connection progress

November 16, 2026. Base cf693f4, branch feat/pooltune-revamp-dev.
Ask/final_only inherited from docs/design/hosted-query-agent/customer-walkthrough.md.
Google signup is deployed and verified. Jared's account has no projects yet.

User approved the proposed OAuth/project-picker flow and explicitly requested removal
of password and certificate steps. Scope and exact statement are in design.md and
approvals.json. Native permission checks and prior repair safety remain constraints.
Next human action boundary is external OAuth app creation/security consent, followed
by Jared's real Supabase connection. No routine implementation stop required.

## Research and preflight

Read-only research found documented beta CLI login credentials, fixed effective role
switch used by Supabase's own CLI, and official CA download. No graph decision locks
the exact native-login equality, but many code guards depend on it. A narrow verified
connection contract is required; global substitution is prohibited.

| Producer/interface | Consumer | Check and consequence |
| --- | --- | --- |
| OAuth encrypted grant | picker, setup, log/metrics reads | Existing token fields assume <=512 bytes and indefinite validity; must adapt bounded OAuth tokens and refresh. |
| Temporary native login | managed repair/recovery and secondary pgx connections | Existing Config cloning/equality guards reject delegated identity and can reuse expired passwords; migrate through one verified connector. |
| Official CA retrieval | monitor and repair DirectConfig | Preserve hostname verification and no fallbacks; malformed/expired CA blocks activation. |
| Selected project + installation epoch | all workers | Re-fetch access, bind ownership, stop revoked grants without erasing recovery. |
| Normal UI routes | legacy manual connections | New three-step flow must not alter existing saved projects or Google sessions. |

Task 1 implementation in progress with a dedicated implementer. A fresh read-only
builder check found no important unanswered design questions. Provider registration
form is prepared in the browser but has not been submitted.
Requested scopes: Analytics Read, Database Read/Write, Projects Read, Organizations
Read. Provider grant is organization-wide; Pooltune must limit work to selected
projects and disclose the actual authorization scope. No credentials created yet.
No unresolved source-level finding beyond the lifecycle work included in the design.

The consent preview confirms Database Read/Write and Organizations, Projects and
Analytics Read. All other scopes are disabled. Screenshot saved locally for the
action-time approval. No customer grant has been requested or created yet.

## Implementation checkpoint

First real PostgreSQL OAuth test passed after a missing-feature compile failure.
Evidence logs: /tmp/pooltune-oauth-red.log and /tmp/pooltune-oauth-green.log.
Root inspected the outputs. Coverage includes PKCE, rejecting an account swap
before exchange, replay rejection, authorized project selection, ignoring forged
metadata, encrypted grant storage, and eight concurrent callers causing one token
refresh. This is partial local evidence, not complete feature or live verification.
Delegated native sessions and secondary connection renewal remain in progress.

The native delegated connector and CA tests are reported green against local
PostgreSQL. Worker lifecycle integration and targeted race tests remain in progress.
An action-time request is pending to create the prepared OAuth app and store its
credentials in the scoped Railway web service. This is required by the browser
confirmation policy for creating security-sensitive access. No approval or external
credential creation is assumed from the pending request.

Jared answered the action-time request: "Yes, create and configure it". Created the
Pooltune OAuth app in Jared's Work with the prepared scopes and callback. Captured
the one-time credentials into an ignored 0600 local file and saved both variables
to the exact Railway web service using stdin and --skip-deploys. Read-back equality
checks passed without exposing values. No new deployment has occurred yet.
Evidence: /tmp/pooltune-supabase-evidence/app-created.jpg.

Root inspected /tmp/pooltune-supabase-full-unit.log and
/tmp/pooltune-supabase-app-integration-race.log: full Go unit suite and complete
hostedapp/auth/supabaseproject real PostgreSQL race suites passed. Implementer is
finishing repair/recovery race checks, reconnect/disconnect test coverage and
self-review. Independent whole-feature review and live verification remain pending.
Railway normal cadence read back as POOLTUNE_STAGING_INTERVAL=1m and
POOLTUNE_STAGING_WINDOW=5m; neither was changed by credential configuration.

Task 1 committed as cd2624c158d30ffa9c8e8bc8a768e3cc4cf074ce. Root inspected the
implementation report and saved its full content to implementation-report.md.
The targeted native repair/recovery race suite passed in 29.010s, final setup/OAuth
race rerun passed in 4.367s, and focused vet/whitespace passed. Test coverage includes
real delegated concurrent index execution with a secondary session; this does not
prove live Supabase or all repair families. Normal cadence remains unchanged.
Independent whole-feature read-only review is running against cf693f4..cd2624c.
No deployment or real customer OAuth grant has occurred for this feature yet.

## Independent review in progress

Reviewer is validating two candidate correctness findings on cd2624c:
- Delegated ConnectRepair may drop the maintenance OnNotice warning guard.
- The long-running managed cron observer may retain a connection past its bound
  credential TTL and silently ignore subsequent observation failures.
Implementer is investigating both without source edits pending confirmation. These
are not accepted risks or resolved findings. Deployment remains pending review.

A third candidate concerns authorization of a cached temporary login when the
account can still list a project but no longer has database-write authority. The
reviewer is checking provider semantics; implementer is assessing an explicit
write-authority check before cache use. Project visibility alone must not be
assumed equivalent to permission for native repairs. Deployment remains pending.

Reviewer confirmed two Important findings with independent disposable PostgreSQL
reproductions (/tmp/pooltune-supabase-review-repro.log):
1. delegated.go:73-77 omits caller OnNotice, bypassing the existing maintenance
   warning abort safeguard. Fix owner: implementer; preserve the explicit notice
   callback while keeping connector-owned destination/TLS, add native regression.
2. managed_cron_service.go:24 retains the same delegated owner session past its
   bound TTL, then ignores ObserveManagedCron errors at 77-82. Scheduling observation
   stops until restart. Fix owner: implementer; renew each bounded collection pass,
   including witness cleanup, and test expiry/renewal. Both remain unresolved until
   fixed, tested and independently re-reviewed. No approved-design change needed.

Reviewer confirmed a third Important finding by reproducing a readable project and
cached native credential while login-role issuance is denied
(/tmp/pooltune-supabase-review-write-authority.log). Projects Read is insufficient
proof of Database Write. Implementer is verifying a catalog-only Management SQL
probe with read_only=false that requires exact postgres, writable-database and
non-superuser owner identity before using cached credentials. Plain SELECT 1 is
insufficient because the endpoint accepts read scopes too. The exact identity
response must distinguish the documented supabase_read_only_user path. This is a
fix to uphold existing authority requirements; no permission weakening is allowed.
Live writable response and remaining limitations must be recorded before completion.

Full initial review is preserved in review-initial.md. No other material call-site
finding was identified. Minor M1 concerns the aggregate setup timeout exceeding the
HTTP response deadline. Root directed a coherent overall setup budget and a delayed
provider/retry regression in the same fix round, since this affects the requested
simple customer flow. No durable job redesign is requested. All four findings
remain open until the fix commit and focused review. Review declined-to-judge live
provider behavior, real six-family repairs, visual redesign and external secret
configuration; these retain their original verification scope and limitations.

Fix commit 7dab55e7f7bc626938ab5f8a3d74b65980885e58 is stable for focused re-review.
Implementer reports I1/I2/I3/M1 fixed. Root inspected passing real PostgreSQL race
logs: hostedapp36.189s/auth17.265s/supabaseproject8.357s; focused native
repair/recovery30.177s; final authority/deadline correction24.081s. Source remains
unmodified while reviewer checks the exact fix range. The fix also ensures local
refresh-lock deadlines remain temporary failures, not grant revocation. Findings
await independent disposition; live provider evidence is still pending deployment.

Focused re-review of 7dab55e confirms I1/I2/I3 source fixes but found Important R1:
the new parent refresh deadline can expire after provider rotation but before local
save/commit, rolling back to the old refresh token. Reviewer reproduced with a 70ms
caller budget and150ms local persistence delay; next refresh was invalid_grant.
Evidence: /tmp/pooltune-supabase-rereview-refresh-deadline.log. Implementer must give
post-exchange persistence its own bounded cancellation-independent reserve while
keeping external calls within request budget and total response below22s. Tests
must preserve the replacement token and avoid false shared-grant revocation.
Minor R2: JSON authorized:null currently decodes to false and causes project denial.
Strict response decoding must keep malformed/null proof temporary-unavailable while
exact false remains denial. Both fixes assigned to implementer. No deployment yet.

Full second review is preserved in review-round-2.md. It confirms original I1/I2/I3
resolved at code/contract level and found no additional material issue beyond R1
(Important I4 in that report) and R2 (Minor M2). Independent focused race suites
passed for delegated/native/CA, collector renewal and OAuth/authority. The reviewer
will check only the remaining correction and its integration when a stable commit
is available; no new broad campaign or customer permission downgrade is authorized.

Final correction commit 6c1487f19f2acca1c07f78a3d6ccb48956a4594e is frozen for
focused re-review. The implementation gives local rotated-token UPDATE/COMMIT a
maximum2s independent persistence reserve capped at work deadline+2s; external
HTTP remains under the original deadline. Strict nullable proof decoding keeps
null/malformed temporary and exact false denied. Real-PG regression covers delayed
UPDATE and deferred COMMIT, slow provider HTTP, and a3s local stall bounded near2s.
Root inspected focused race pass27.631s and hostedapp unit pass; vet reported clean.
This does not promise persistence during a longer database outage. Live checks and
final independent disposition remain pending. No deployment or grant yet.

Final focused reviewer message confirms exact6c1487f has no new material finding.
Independent refresh-loss reproduction, UPDATE/COMMIT reserve boundaries, HTTP
deadline and null/false classification passed under race in6.539s;3s local stall
returned near2.03s. Formal final report is being written. Root started deployment
6189cea1-f85c-4317-9308-1faf2d400cd5 from a clean git archive of exact source
6c1487f19f2acca1c07f78a3d6ccb48956a4594e. Railway reports BUILDING. No other
service or cadence changed. Live Supabase setup remains unverified.

Deployment6189cea1-f85c-4317-9308-1faf2d400cd5 succeeded at2026-11-17T00:14:27Z
(Nov16 local), source6c1487f. Health, existing password signin/correctaccount, Google
button, default ConnectSupabase/no-manual-fields, fixed authorization callback and
PKCES256 checks passed. Jared's Google session remained signed in after reload.
Live browser then found L1: ConnectSupabase POST returns303 but browser remains on
page. Root verified the official API authorize endpoint returns303 to
https://supabase.com/dashboard/authorize, while CSP form-action permits only
api.supabase.com for Supabase. No actual customer authorization has occurred.
Implementer is adding only the official consent destination when Supabaseenabled,
with provider-combination CSP regression. Independent focused review and redeploy
required. Original code review was clean locally; browser flow remains unverified.
Evidence: /tmp/pooltune-supabase-evidence/connect-live.jpg and ignored live-checks.json.

CSP fix2b8154b951464854920133e5b46602be5d4279d4 passed focused independent review
and all four provider-combination race checks. Deployment
ac473f92-9bc5-428e-80ec-5b25f1922eb9 succeeded at2026-11-17T00:19:38Z. Repeating
the same browser button now reaches the official AuthorizePooltune screen for
Jared's Work with the expected scopes and callback. The signed-in Google account
survives reload; existing password account and all HTTP/form-policy checks pass.
Requested Jared perform actual consent, Rookery selection, repair choices and Start
monitoring in the open tab. No permission from this pending handoff is assumed.
Customer native setup and strict provider identity proof remain pending. Screenshots:
/tmp/pooltune-supabase-evidence/connect-live.jpg and authorize-live.jpg.

Jared clicked AuthorizePooltune and reported loading. Server evidence showed callback
303 in2292ms and project picker200 in175ms; browser screenshot subsequently showed
Rookery Ready. Customer completed selection and all six repair choices. Setup POST
for new projectf172d6191001eaf6 returned303 in14338ms; dashboard shows Monitoring.
The code cannot activate this OAuth setup without the strict writable-provider
probe, temporary native credential acquisition, verified TLS/role/installation and
monitor checks passing. This establishes the positive live path, not a real
permission-downgrade test. Screenshots show completed monitor checks at00:25:03,
00:26:03 and00:27:03 UTC. No query windows/investigations yet; no repair success
claimed. Exact function/job enrollment is still required and not automatically
broadened. Evidence saved in live-checks.json and local monitoring screenshots.

A final walkthrough copy issue was found: existing availability text said managed
memory/cron were unavailable and cleanup-setting changes were disconnected, despite
existing managed implementations. Implementer is correcting only these descriptions
to show their actual target/evidence prerequisites, without changing permissions.

Final readiness copy commit 08d6fd3b0499d31478298a51c3e5583ad712a3aa passed
independent review and existing readiness checks without behavior changes.
Deployment 2068557d-35b2-4fe9-a5b8-1db757ecd0cf succeeded. Final dashboard shows
all six selected, accurate target prerequisites and a new monitor poll at
2026-11-17T00:32:42Z after the deployment. Final health passed. No new repairs or
query evidence is claimed. The requested connection flow is implemented, deployed
and positively verified with Jared's real account/project. Broader repair coverage,
real permission downgrade, long-duration renewal and frontend design remain outside
this completion claim. Human final review is being presented, not pre-accepted.
