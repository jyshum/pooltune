# Connect Supabase without copied credentials

## Authorized outcome

On November 16, 2026, Jared approved Connect Supabase, project selection and repair
checkboxes, then asked to remove database password and certificate entry too:
"yes lets build that, and figure out how to get rid of the password and certificate
steps too. i want them to just connect supabase, pick their project, check the boxes
and thats it". This authorizes implementing that flow and determining its credential
mechanism. Google login and existing Pooltune accounts remain the account boundary.

## Customer experience

The normal new-project route starts Supabase OAuth from the signed-in account.
Supabase shows its own consent screen, then returns to a project picker in Pooltune.
Show accessible project names and organization context; use only provider-returned
identifiers. Confirm the chosen project before setup. The next screen contains the
existing repair choices and a Start monitoring action. No project reference, setup
token, password or certificate field appears in this normal flow.

The provider grants access at organization scope. State this honestly on the
connection screen; Pooltune stores the customer's chosen project and restricts its
workers to that selection. Never claim the OAuth grant itself is project-scoped.
An empty list, inactive project, missing permissions, cancellation or expired
authorization must offer a useful reconnect/retry path without losing choices.
Existing manual connections continue working; advanced manual setup can remain a
separate fallback, not the default. No silent downgrade to unencrypted access.

Initial setup may finish with monitoring and the selected repair families. Existing
memory/function and scheduling target enrollment remains required before those
specific mutations. Preserve the existing permission semantics and show readiness
honestly; do not infer authorization for arbitrary functions or jobs from this UI
simplification. Broader automatic enrollment is outside this credential change.

## OAuth and project selection

Use Supabase Management OAuth, not Supabase project Auth. Config names are
POOLTUNE_SUPABASE_CLIENT_ID and POOLTUNE_SUPABASE_CLIENT_SECRET. Exact callback is
https://pooltune.up.railway.app/auth/supabase/callback. Use authorization-code flow,
PKCE S256, random short-lived single-use state and a browser cookie. Bind the flow
to the initiating authenticated Pooltune user and session; revalidate at callback.
No supplied redirect URL or provider host. Existing Google login remains intact.

Provider constants: https://api.supabase.com/v1/oauth/authorize and
https://api.supabase.com/v1/oauth/token. Token exchange uses Basic client auth and
form encoding. OAuth scopes are configured on the app: Projects Read,
Organizations Read, Database Read/Write and Analytics Read; no Secrets/API-key,
Auth, Storage or project deletion access. Store access/refresh tokens encrypted
with account/connection binding. Serialize refresh across replicas, persist rotated
refresh tokens atomically, honor expiry and invalidate terminal denial. Do not log
provider bodies, tokens or credential-bearing URLs. API calls have bounded timeouts
and bounded response sizes. Handle OAuth tokens larger than the old 512-byte PAT
limit at every downstream validation boundary without unbounded input.

Re-fetch the authorized project list before accepting a selection or starting a
new installation. Never trust submitted project metadata or cross-account IDs.
Bind selected project, connection and installation to the current Pooltune owner.
Do not allow reconnect under another organization to silently redirect an existing
worker's authority. Connect only to canonical Supabase hosts and the selected ref.

## Automatic trusted connections

Supabase's documented experimental endpoint POST /v1/projects/{ref}/cli/login-role
accepts {"read_only":false} with database:write and returns role/password/ttl_seconds.
Use these provider-issued temporary credentials for native repair connections.
Only accept the expected cli_login_postgres identity; do not accept user-selected
roles or broad prefixes. The official CLI performs fixed SET SESSION ROLE postgres.
Keep native pgx execution for concurrent indexes, VACUUM and transaction locks.

Introduce a server-owned connection provider bound to the selected project and
installation epoch. Before effective-role selection verify authenticated native
backend identity, writable primary, unexpired non-superuser login and appropriate
postgres membership. After fixed role selection verify effective postgres and the
existing installation marker. Carry this verified connection contract through
managed repair checks. Direct postgres connections keep their existing behavior.
Never globally relax current_user/session_user checks or application-role checks.
Preserve owner-OID, exact object/fingerprint/generation, capability/enrollment,
admission, transaction and recovery fences.

Every new repair connection, including secondary measurement locks and recovery,
must acquire usable credentials through this provider. Do not clone an expired
credential indefinitely through db.Config(). Fetch credentials according to their
returned TTL, serialize/cache issuance appropriately, and avoid rotating shared CLI
credentials per parallel connection. Do not delete shared CLI login roles.

Fetch the CA through the fixed official HTTPS URL
https://supabase-downloads.s3-ap-southeast-1.amazonaws.com/prod/ssl/prod-ca-2021.crt.
Validate PEM, CA usage and validity, cache safely and retain hostname verification,
TLS 1.2 minimum and no plaintext fallback. Failure blocks setup with a useful retry.
Never reset the customer's postgres password or change SSL enforcement/JIT settings.

Keep dedicated low-privilege monitor login provisioning. Resolve OAuth tokens
dynamically for setup, logs and memory metrics. Revalidate provider access at bounded
monitoring intervals and before new repair admission. Terminal revoked/denied OAuth
stops new work and closes local sessions via cancellation/epoch changes. Preserve
unfinished repair recovery records and surface reconnect-required state; failed
cleanup is not success. Disconnect must stop only the selected installation, remove
its stored credentials and avoid breaking other selected projects sharing the same
OAuth grant. Shared account grants must not be revoked merely to disconnect one
project. Explicit provider unlink can revoke/remove only when safely implemented.

## Approaches and evidence

Selected implementation approach: OAuth + documented temporary native credentials
and automatic verified CA. This removes copying while retaining native PostgreSQL
semantics. It adds token/credential lifecycle work and depends on a beta endpoint.
The client isolates the endpoint, validates its response and fails closed.

Strongest alternative: Supabase JIT/temporary access can preserve native postgres
identity, but official docs only promise PAT/dashboard JWT, require newer Postgres,
SSL enforcement and user role mappings. OAuth support is unverified and changing
project-wide SSL settings could break existing clients. Not selected.

Management query HTTP alone cannot simply replace all native session/transaction
semantics. Retaining password entry contradicts the requested normal setup. Reading
the existing postgres password is unsupported, and resetting it is not authorized.

Evidence:
- https://supabase.com/docs/guides/integrations/build-a-supabase-oauth-integration
- https://supabase.com/docs/reference/api/v1-create-login-role
- https://github.com/supabase/cli/blob/HEAD/apps/cli/src/command-internal/db-connection.sql-pg.layer.ts
- https://github.com/supabase/supabase/blob/HEAD/apps/studio/components/interfaces/Settings/Database/SSLConfiguration.tsx
- https://github.com/supabase/supabase/blob/HEAD/apps/studio/hooks/custom-content/custom-content.json
- https://supabase.com/docs/guides/platform/temporary-access

No existing graph decision locks current_user=session_user as a product requirement.
These implementation checks must migrate narrowly while retaining actual database
authority. d-onboarding leaves provisioning open. The user's new request replaces
manual credential entry; unrelated repair decisions remain binding.

## Verified implementation details

The selected grant proves current native-write authority before shared credential
cache admission and on the watchdog. Project visibility and a successful SELECT 1
are insufficient. A fixed catalog-only Management query must report the effective
non-superuser postgres owner on a writable primary. Exact false is denial; null or
malformed proof is temporary unavailability. The positive provider response passed
in the live Rookery setup; real permission downgrade was not induced.

Setup uses an 18-second overall work budget. A completed rotating-token response
may use a separate, cancellation-independent two-second local UPDATE/COMMIT reserve,
leaving response margin under the 22-second HTTP limit. External HTTP cannot use
that reserve. Longer persistence outages can still require reconnect. CA download
locks never cover network I/O. Long-lived scheduling observation renews its verified
native session before the next bounded observation can cross credential expiry.

The browser form policy permits the exact Supabase API and dashboard consent
origins when the integration is enabled. Both are needed for the real redirect
chain. Default restrictions and Google’s existing destination remain intact.
See verification.md and live-checks.json for the observed customer flow and limits.
