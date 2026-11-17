# Supabase Connection Implementation Plan

> For agentic workers: use crocking:subagent-driven-development for implementation and review.

Goal: Connect Supabase, choose a project, choose repairs and start without copying
IDs, tokens, database passwords or certificates.
Architecture: Go account-bound Management OAuth, encrypted grant lifecycle and a
server-owned native PostgreSQL connection provider, using the existing worker.
Tech stack: Go, PostgreSQL, pgx, existing oauth2 library, server-rendered HTML.
Spec: docs/design/supabase-connect/design.md.
Canonical records: design.md, approvals.json, implementation.md and progress.md in
this directory. Existing product graph/approvals: docs/design/hosted-query-agent/.

## Workflow settings

Design conflict ask; human review final_only inherited from customer-walkthrough.md.
User explicitly approved this new flow and asked us to determine how to remove
password/certificate entry. Routine implementation choices autonomous. Existing
feat/pooltune-revamp-dev supplies isolation; do not touch main or push without root
direction. No routine human task checkpoint. Real security/design conflict must be
reported before affected edits. Provider security-sensitive creation/consent follows
action-time browser policy. Root configures external apps, deploys and guides live use.

## Global Constraints

Preserve existing Google/password accounts, existing manual projects and project
ownership. Preserve native repair authority, exact target and generation checks,
memory/scheduling enrollment, selected capabilities, rollback/recovery and budgets.
Never reset customer passwords, grant catalog UPDATE, disable TLS, weaken role
checks globally, delete shared CLI roles, or log secrets. No unrelated repair
campaign or benchmark. No fake readiness or claimed live verification from mocks.
No code edits by root while implementer owns code. Local implementation commits
allowed; no push/deployment by implementer. Unrelated legacy/research changes stay.

## Review Focus

Account/session swaps during OAuth and malicious project selection must fail.
Concurrent refresh must retain the current rotating refresh token across replicas.
Expired temporary credentials and secondary connections must use fresh authority.
OAuth revocation stops new mutations and doesn't discard pending recovery records.
Native verified delegated login must preserve all repair and app-role safety checks.
Partial setup, reconnect, shared grants and disconnect must be isolated/idempotent.

### Task 1: Integrated OAuth setup and automatic connection lifecycle

Files: new focused files in internal/hostedapp for OAuth store/client/routes and
automatic setup; internal/supabaseproject for CA and delegated native connection;
existing app.go, projects.go, start.go, supervisor.go, views.go, ui/pages.html,
cmd/pooltune-web/main.go; narrowly affected managed connectors and identity checks
in internal/hostedquery. Existing function/job enrollment semantics stay intact.

Interfaces: Implementer owns the integrated boundary and may choose idiomatic exact
types after source inspection. Expose one verified managed connector used everywhere
the new credential path opens a native session, retaining legacy direct login paths.
Config uses POOLTUNE_SUPABASE_CLIENT_ID/SECRET and /auth/supabase/callback.

- [x] Read complete design, graph, approvals and progress; report any hard conflict.
- [x] Write failing real-PG/HTTP tests for session-bound OAuth, encrypted grants,
refresh serialization, project authorization and rejection of forged provider data.
- [x] Implement provider client/store, refresh and project picker with scoped errors.
- [x] Write failing native tests for delegated identity rejection/acceptance, expiry,
secondary connection handling, permission revocation and direct-login compatibility.
- [x] Implement verified CA retrieval and renewable native connection integration.
- [x] Wire normal UI setup with no manual credential fields; keep legacy setup intact.
- [x] Test revoked grants, token expiry, partial setup/retry and isolated disconnect.
- [x] Run Go unit suite and focused real PostgreSQL integration/race suites for
auth/app/supabaseproject and changed managed repair paths. Do not run the long broad
hostedquery qualification campaign. PATH=/Users/jshum/.local/share/pooltune-toolchains/go1.27.4/bin:/opt/homebrew/opt/postgresql@17/bin:$PATH.
- [x] Run focused go vet and whitespace checks, self-review, commit task code only,
write exact test commands/results and report all limitations for independent review.

### Task 2: Review, configure and verify the customer flow

- [x] Independent read-only whole-feature review, resolve material findings and
perform focused re-review where necessary.
- [x] Finish the prepared Supabase OAuth app in Jared's Work after action-time
approval, securely configure Railway client variables and callback.
- [x] Deploy exact reviewed source from a clean Git archive. Verify existing Google
and password accounts, new picker pages and fixed Supabase authorization redirect.
- [x] Jared authorizes Supabase, chooses Rookery and repair boxes through the normal
flow. Verify TLS, native setup, advancing monitoring, selected capability readiness
and no manual password/certificate/token/ID entry. Do not manufacture repair events.
- [x] Record actual evidence/remaining limits, update existing map and commit docs.
