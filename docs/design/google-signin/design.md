# Google sign-in for Pooltune

Jared requested a Google sign-in button and approved reuse of Google Cloud project
pooltuner-probe-20261023 on November 16, 2026. Google augments the existing Go
account store and sessions. The deployed origin is https://pooltune.up.railway.app.

Use the authorization-code flow, PKCE S256, state bound to an HttpOnly browser
cookie, a nonce, short expiration and one-time consumption. Verify Google's signed
ID token with a maintained OIDC library, audience, issuer, expiry, nonce and
email_verified. Request only openid and email. Use Google's subject as the stable
identity. Reuse existing session issuance and account/project ownership.

New Google accounts still require the existing preview invite. Existing Google
users can sign in without an invite. A Google identity must never silently take
over a pre-existing password account based only on email. If that email exists,
require the existing account session to explicitly link Google, validating the
current session again at callback and matching the verified email. Show a clear
sign-in-first message for collisions. Allow passwordless Google users by a
backward-compatible nullable credential migration with an all-or-none constraint.

Render Continue with Google on signup and sign-in when configuration exists.
Use separate forms so email/password validation does not block Google. Signup
requires invite before redirect, and sign-in by an unknown Google identity returns
a clear create-account path. Errors and cancellation return useful local feedback.
No arbitrary return URLs. Callback is /auth/google/callback. State stores flow
and optional linking user identity. Secrets stay in Railway environment settings.

Alternative considered: Supabase Auth would require moving the current account and
session boundary. Direct Google OIDC fits the requested addition and preserves the
existing ownership identifiers. Existing password functionality remains covered.
