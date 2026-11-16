# Google sign-in Task 1 implementation report

Status: Implementation complete. Independent review is clean. Live Google signup and session reload verified.
Branch: feat/pooltune-revamp-dev.
Base: 227757f310c4a6e2f7930a7a86df6a23b142a479.
Head: 497c70c4fa7204ac7917f234f92606e599f2cb36.
Commit: feat(auth): add secure Google sign-in and explicit account linking.
No push or deployment performed by implementer.

## Delivered

- Google authorization-code flow using go-oidc v3.21.0 and oauth2 v0.38.0.
- Only openid and email scopes, fixed callback /auth/google/callback.
- PKCE S256, random state and nonce, separate secure HttpOnly SameSite=Lax browser cookie.
- PostgreSQL flow records expire after ten minutes and are consumed atomically once. Invalid browser binding cannot consume another browser's flow. Expired records cleaned at start.
- ID-token signature, issuer, audience, expiry, nonce, subject and verified email validation. Authorized party checked when supplied and required for multiple audiences.
- Provider responses/errors are not logged or reflected. Token exchange and key fetching have bounded HTTP timeouts.
- Stable Google subject mapping, atomic concurrent signup, invitation required before signup redirect. Existing identities sign in without invite.
- Existing password account email never silently links. Collision explains sign-in and Account > Link Google account path.
- Explicit linking requires matching verified email and same initiating live account session. Session is revalidated inside the linking transaction; revoked or replaced sessions fail.
- Nullable password migration with all-or-none constraint; password authentication, throttling, sessions and ownership remain covered.
- Separate Google forms on signup/signin. Account settings links Google, cancellation/error pages provide local navigation. Arbitrary return URLs not supported.
- Google auth form redirect allowed in CSP only for https://accounts.google.com.

## Environment contract

- POOLTUNE_GOOGLE_CLIENT_ID
- POOLTUNE_GOOGLE_CLIENT_SECRET
- Existing POOLTUNE_APP_ORIGIN=https://pooltune.up.railway.app
- Existing POOLTUNE_SIGNUP_CODE remains required for new registrations.
- Both Google vars unset disables Google buttons/routes. Only one set fails startup with a safe configuration message.
- Exact redirect URI derived from configured origin: https://pooltune.up.railway.app/auth/google/callback.
- No credentials in source, tests or report.

## Verification

PATH=/Users/jshum/.local/share/pooltune-toolchains/go1.27.4/bin:/opt/homebrew/opt/postgresql@17/bin:$PATH

New store tests were written first and failed because GoogleAccount/BeginGoogleFlow/ConsumeGoogleFlow and error types were absent. New HTTP tests failed because Google config and implementation were absent. Implementation followed these failures.

Final command:
`go test -race -tags integration ./internal/hostedapp/auth ./internal/hostedapp -count=1 -timeout=180s -v`
Log: /tmp/pooltune-google-integration.log
Exit: 0. Real local PostgreSQL testpg instances, no skipped tests.
Final package output:
```
PASS
ok  	pooltune/internal/hostedapp/auth	17.380s
PASS
ok  	pooltune/internal/hostedapp	13.566s
```

Covered: pre-Google NOT NULL migration with a live password account; existing sessions/passwords; passwordless login rejection; partial credential rejection; no email takeover; explicit linking; Google subject stability; revoked session denial; concurrent registration; wrong-browser/expired/replayed/concurrently consumed state; invite enforcement; cross-site initiation; valid HTTP signup/session/dashboard; bad signature, nonce, audience, issuer, expiry, unverified email, empty subject, missing token and failed exchange; unknown identity; cancellation; normal signin; account collision; lost/replaced/revoked linking session; successful linking. Existing ownership, enrollment, repair controls and monitoring tests also pass.

Final command:
`go test ./... -count=1`
Log: /tmp/pooltune-google-unit.log
Exit: 0. All repository Go unit packages pass. Broad hostedquery integration campaign deliberately not run.

Also passed:
`go vet ./internal/hostedapp/... ./cmd/pooltune-web`
`git diff --cached --check`

## Self-review and routine decisions

No approved design conflicts found. Fixed Google endpoint constants follow official Google OIDC documentation and avoid startup network discovery. Stable mapping permits one Google identity per Pooltune user; no unlink feature added. Linking lives on /app/account and rotates through existing session issuance. Expired flow cleanup is indexed and shared across replicas. Secrets/errors do not enter rendered error text.

Sources verified:
- https://developers.google.com/identity/openid-connect/openid-connect
- https://pkg.go.dev/github.com/coreos/go-oidc/v3/oidc
- https://pkg.go.dev/golang.org/x/oauth2

## Remaining verification / preserved state

At implementation handoff, live Google consent/account selection, provider credentials and Railway deployment remained root/user tasks; the completed results are recorded below. HTTP fixtures use real RSA-signed tokens with a local token endpoint and real PostgreSQL, but do not contact Google's production token endpoint. No UI browser screenshot taken by implementer.

Unrelated legacy/.dockerignore and legacy/go.sum deletions, legacy duplicate files and research directory were preserved. Root-owned untracked docs/design/google-signin remains uncommitted by this task.

## Deployment and live verification

Root deployed the exact reviewed source 497c70c through a clean Git archive to
Railway. Deployment 3aa500b4-24fe-41ed-adc0-42b716c03ad7 succeeded. Health,
signup and signin returned 200 with Google buttons. An existing disposable password
account reached its dashboard and Account page and signed out. An invalid callback
returned 400. Jared then completed the real Google signup and reached /app, and
reload retained his session. See live-checks.json and progress.md. Live returning
Google sign-in and explicit linking were not repeated with Jared's account.
