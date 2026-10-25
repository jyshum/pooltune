# Managed restore implementation

> Execute inline with Superpowers executing-plans and TDD.

**Goal:** Add durable managed admission history and safe restore for Azure, Google and Supabase on codex/managed-restore.
**Spec:** ../specs/2026-10-25-managed-restore-design.md
**Architecture:** Shared Go admission controller, scope-bound durable journal, existing authenticated admin API, React history and reviewed controls.

## Constraints / review focus
No cloud mutation, SQL replay, physical reserve resize or safety bypass. No changes to judging process or ui/dist. No main merge/deployment. Preserve unknown state and active permits. Handle stale tabs, reused keys, failed intent/confirmation writes, clean restart, mismatched profile and journal bounds.

### 1. Controller and persistence
- [x] Add failing tests for three-provider restore/hold/safety, idempotency/stale requests and durable journal/restart/failure cases.
- [x] Implement versioned scoped journal and transactional intent/confirmation around transitions; retain conservative lifecycle rules.
- [x] Wire history/commands into dashboard backend; test auth/origin/CSRF with real controller.

### 2. Dashboard
- [x] Add Overview/History tabs and restore review UI, freeze/resume controls, stale/capability gates and clear semantic copy.
- [x] Build to isolated output, check preview without touching native UI assets.

### 3. Verification and delivery
- [x] Extend existing real PostgreSQL test to restore downward with an active transaction and preserve history over clean restart.
- [x] Focused race tests, typecheck/UI build, fresh review and fixes. Commit branch only and document verified limits.
