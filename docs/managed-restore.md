# Managed admission history and restore

Implemented on `codex/managed-restore`. This feature is not merged into the public release or the running judging demo yet.

The Azure, Google Cloud and Supabase gateway paths share these controls. They change **Pooltune transaction admission**, not provider pool configuration, physical warm connections, database contents or query priority.

## Dashboard

- **Change history** shows the latest 200 records from the local durable journal. Each includes before/after admission, outcome, actor and reason.
- **Restore N** opens a review. **Confirm restore** adds a new revision referencing the old one. History is retained, like a Git revert rather than a rebase.
- Restore holds the reviewed limit until **Resume automation**. A confirmed limit equal to the current limit can still be selected to pause future automatic changes.
- **Freeze admission** opens a review, then stops new transactions and lets active work finish. It is different from holding a restored, nonzero limit.
- **Resume automation** opens a review and checks fresh inventory, provider health, capacity and uncertain work, then returns to the approved base and automatic control.

A restore never interrupts an active transaction, replays SQL or undoes completed writes. It cannot clear a safety stop. Resume after reviewing the current state first. Invalid, unconfirmed, zero, out-of-range and foreign-profile revisions cannot be restored.

## Safety still wins

While a limit is held, moderate pressure reduces admission to the approved base. Missing or expired health stops new work. Fresh healthy evidence can return to the held limit; high pressure and uncertain outcomes latch a stop until a reviewed resume is safe. Physical warm capacity remains unchanged.

All commands require a private authenticated session, same origin, CSRF token, expected current revision and idempotency key. The controller checks and applies them under its existing lock. A stale review is rejected, and a repeated identical request returns its recorded outcome without applying it again. Dashboard controls cannot reopen a process that is shutting down.

## Journal and recovery

`state.json` is a private, bounded, atomically replaced and fsynced snapshot. Each transition records intent before installing admission, then confirmation afterward. Automatic changes are recorded too. The journal contains no SQL or database credentials; its scope is an opaque fingerprint of the approved provider, backend and budget. The dashboard excludes retry keys and private scope identifiers.

Storage failure stops admission and marks the audit outcome uncertain. An uncertain intent is never automatically retried. Shutdown after an audit failure cannot mark the lifecycle clean. Unclean startup, corrupt/incompatible history and unresolved intent require operator review, preserving the existing conservative recovery policy. Do not delete state or ownership locks to bypass a refusal.

A clean restart retains historical records, starts with a new revision and zero admission, and does not replay a held limit. Profiles with a different provider, backend identity or approved budget cannot adopt the journal. The earlier snapshot format is backed up as `state.pre-history.json` when first encountered; no historical changes are invented from it.

The journal retains at most 4096 records. Filling it stops admission for operator maintenance. Automatic archival/rotation is not implemented, so review journal usage before prolonged deployment. The API shows the latest 200 records; older entries remain on disk. History is separate from optional Tiger Data traffic measurements.

## Evidence and limits

Tests exercise the common controller with Azure, Google and Supabase pressure-source labels, including stale/unsafe evidence, concurrent revision checks, idempotency, intent/confirmation failures and private HTTP protections. Real PostgreSQL/PgBouncer integration checks downward restore while an open SQL transaction retains its backend, and retained history after clean restart. These are local correctness checks with controlled pressure. No new live cloud benchmark or production certification is claimed.
