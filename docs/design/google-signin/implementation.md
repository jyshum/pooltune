# Google Sign-in Implementation Plan

> For agentic workers: use crocking:subagent-driven-development for implementation and review.

Goal: working Google account creation, sign-in and explicit linking in Pooltune.
Architecture: Google OIDC authorization code flow in Go, existing PostgreSQL account
store and session cookies. Maintained OIDC and OAuth libraries.
Spec: docs/design/google-signin/design.md.
Canonical records: design.md, implementation.md, progress.md in this directory.
Existing product map: docs/design/hosted-query-agent/{graph.json,approvals.json,map/status.json}.

## Workflow settings

Design conflict mode ask. Human review final_only, restored from
../hosted-query-agent/customer-walkthrough.md. Native project branch
feat/pooltune-revamp-dev already supplies isolation. User authorized Google sign-in
and reuse of pooltuner-probe with "yes, use that ... turn it for google auth sign in
for this new version" on November 16. Routine implementation decisions autonomous.
No further routine human review stop. Ask before altering other approved decisions.

## Global constraints

Preserve existing accounts, sessions, project ownership, repair permissions and
unrelated working tree changes. No secret logging. No database-wide repairs or
benchmark campaigns. Changes target only authentication and its deployed settings.
Root handles Google Cloud and Railway configuration. Implementer handles code.
Local commits allowed on revamp; push/default branch integration requires current
root direction. Real Google consent/account selection remains user-driven.

## Review focus

OAuth request forgery, callback replay, bad tokens, expired flows, session loss,
account pre-hijacking, concurrent registration, prior password account compatibility,
unknown identities and cancellation. Cover these with focused real-DB and HTTP tests.

### Task 1: Google identity and account flow

Files: internal/hostedapp/auth/store.go plus focused Google identity/flow files and
tests; internal/hostedapp/app.go, views.go, ui/pages.html; cmd/pooltune-web/main.go;
go.mod and go.sum. Keep files focused.

- [x] Write meaningful failing auth-store and HTTP flow tests.
- [x] Implement Google identity storage, explicit account linking and one-time flow state.
- [x] Implement OIDC request, callback verification and existing session issuance.
- [x] Add usable buttons, collision/cancellation handling and environment configuration.
- [x] Run focused race/integration tests and Go unit suite. Tests use local PostgreSQL.
- [x] Self-review, document commands/results and commit only task files.

### Task 2: Configure, review and deploy

- [x] Configure Google Auth in approved existing project, obtain action-time confirmation
where browser policy requires credentials creation or legal acceptance.
- [x] Review complete Task 1 diff for correctness, security and requested behavior.
- [x] Configure Railway and deploy the reviewed source, verify pages and OAuth redirect.
- [x] Have Jared select his Google account for live end-to-end verification.
- [x] Record final evidence, remaining gaps and update existing auth map status.
