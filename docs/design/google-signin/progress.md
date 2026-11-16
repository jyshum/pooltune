# Google sign-in progress

November 16, 2026. Ask / final_only inherited from customer-walkthrough.md.
User requested Google sign-in and explicitly approved reuse of pooltuner-probe.
Design records direct Google OIDC integrated with current Go accounts. Routine
security choices include one-time state, nonce, PKCE and explicit same-email linking.
Task 1 implementation is committed as `497c70c4fa7204ac7917f234f92606e599f2cb36`. Google Auth was
unconfigured before this work.
Current deployed build is 497c70c at https://pooltune.up.railway.app.
Railway deployment 3aa500b4-24fe-41ed-adc0-42b716c03ad7 is SUCCESS.

## Provider configuration

The user explicitly answered "Yes, accept and configure Google sign-in" to the
action-time request covering Google's API Services User Data Policy, Gmail support
contact and the web client restricted to Pooltune's callback.

Created Google Auth branding named Pooltune in project
`pooltuner-probe-20261023`. Audience is External / Testing, with
`jaredshum101@gmail.com` as the sole test user and support/developer contact.
Created web client `Pooltune web sign-in`, with exactly one redirect URI,
`https://pooltune.up.railway.app/auth/google/callback`, and no JavaScript origins.
Both `POOLTUNE_GOOGLE_CLIENT_ID` and `POOLTUNE_GOOGLE_CLIENT_SECRET` were saved
to the existing Railway Web Preview service with deployments skipped. Credentials
are excluded from source and evidence. No public publishing step was taken.

Task 1 implementation and automated testing complete. The full Go unit suite and
real PostgreSQL auth/app integration suites with race detection passed. See
verification.md for commands, scope and evidence. Task 2 provider setup complete;
independent read-only review is clean and deployment succeeded. See review.md. Jared completed Google account selection and consent. The live signup callback
created his account and reached /app; the session survived a page reload.

## Live checks

The deployed signup and signin pages show Continue with Google. The signup button
opened Google's real account chooser with the exact callback, openid/email scopes,
PKCE S256, state and nonce. Jared was asked to select his Google account and
complete consent in the browser and completed it successfully. The existing
disposable password account still reached its dashboard and
Account page, then signed out. An invalid callback returned HTTP 400. Health and
auth pages returned HTTP 200. See live-checks.json.

## Live Google result

Jared reported "Yes, I’m on the dashboard" after completing Google's consent.
Browser verification showed /app signed in as jaredshum101@gmail.com, with no
projects yet. Reload preserved the authenticated session. The reported delay was
at Google's consent screen; the page subsequently loaded and completed normally.
No Pooltune callback error was observed. Real returning Google sign-in and linking
an existing password account were covered by automated tests, not repeated with
Jared's live account. Next customer step is Connect Supabase.
