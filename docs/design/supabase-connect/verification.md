# Supabase connection verification

## Observed result

Jared completed the real flow: Google account session → Supabase consent → Rookery
selection → all six repair choices → Monitoring. No project reference, setup token,
database password or certificate was entered in the normal setup flow.

Positive native setup was verified on source 2b8154b951464854920133e5b46602be5d4279d4,
Railway deployment ac473f92-9bc5-428e-80ec-5b25f1922eb9. A final wording-only release
08d6fd3b0499d31478298a51c3e5583ad712a3aa corrects stale readiness descriptions.
Deployment 2068557d-35b2-4fe9-a5b8-1db757ecd0cf succeeded, and the dashboard showed a new monitor poll at 00:32:42 UTC. Final health and corrected readiness text passed. Exact results are in live-checks.json and progress.md.

The selected project is rookery (osdjqwpzgyzwfaxyufze), installation
f172d6191001eaf6. Setup completed with HTTP 303 in 14,338 ms. Activation requires the strict
writable-postgres Management API proof, provider-issued temporary native login,
TLS/role/installation checks, memory metrics access and dedicated monitor checks.
These prerequisites passed on the actual provider; they were not bypassed.

The dashboard showed completed monitoring checks at 00:25:03, 00:26:03 and 00:27:03 UTC
on November 17 (November16 locally), at the unchanged one-minute cadence. No query
windows or investigations were displayed. No repair execution or improvement is
claimed from this onboarding walkthrough.

Function-memory and scheduling repairs still need specific functions/jobs, limits
and permitted windows selected. The permission checkboxes do not silently enroll
arbitrary targets or establish readiness without evidence.

## Local checks and review

Full Go units, auth/application/Supabase real PostgreSQL race suites, affected native
repair/recovery race suites, focused vet and whitespace checks passed. Independent
review and regression reproductions resolved warning handling, collector expiry,
cached write authority, aggregate setup deadlines, post-rotation persistence, null
proof classification and the live browser consent redirect.

Exact commands, timings and limits: implementation-report.md, review-initial.md,
review-round-2.md, review-final.md, review-csp.md and review-copy.md.
No broad six-hour qualification campaign was run. The new delegated mutation proof
was a local native concurrent index action; other families retain focused native
and recovery coverage, not new live repair-success claims.

## Live account and browser checks

- The new Supabase OAuth app uses only Database Read/Write and Analytics, Projects,
  Organizations Read, with callback
  https://pooltune.up.railway.app/auth/supabase/callback.
- Jared explicitly approved registration and saving credentials in the exact
  Railway web service. Credentials were transferred through stdin, saved only in
  private runtime configuration/ignored 0600 backup, and equality-checked without
  exposing values. No other service or monitor cadence changed.
- Existing password sign-in reached the correct account. Google sign-in remained
  available, and Jared’s existing Google session survived deployment/reload.
- Default connection screen, fixed Supabase redirect/callback, S256 PKCE and exact
  provider form-action policy passed. The live browser reached Supabase consent.
- Actual callback 303, provider project picker 200 and selected-project 303 passed.
  Native setup activated successfully and monitoring advanced as recorded above.
- A CSP redirect-chain issue found only in the browser was fixed by allowing the
  exact official consent origin; CSP was not disabled or widened to a wildcard.

Machine-readable evidence: live-checks.json.
Local screenshots: /tmp/pooltune-supabase-evidence/connect-live.jpg,
authorize-live.jpg, rookery-monitoring.jpg and rookery-monitoring-advancing.jpg.
Final dashboard proof: /tmp/pooltune-supabase-evidence/rookery-final-dashboard.jpg.

## Limits

The temporary login endpoint is documented Beta. Its positive response and the
strict writable-provider proof worked in this walkthrough. Real customer permission
downgrade/revocation and long-duration token/credential renewal were not induced;
their evidence is official provider semantics, local provider fixtures and real
PostgreSQL checks. No customer permissions were altered just to create a test.

After a received rotating-token response, local persistence has a bounded two-second
reserve. A longer database outage or an external rotation whose response is never
received can still require reconnect. These are explicit operational limits, not
claims of infinite durability. Encryption-key rotation, account-level provider
unlink, broader repair qualification and frontend redesign remain separate work.
