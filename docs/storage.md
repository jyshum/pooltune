# Local history and secret files

B02 adds internal libraries. The executable still serves health checks only. It does not activate a policy, reconcile backend state, or expose these libraries through an API.

## Revision lifecycle

`store.OpenStore(absolutePath)` opens a restricted SQLite file. A caller appends an `intent` event with a new revision ID and the currently active predecessor. The intent remains pending across close and reopen. An `uncertain` event records that external work may have happened, while leaving it pending. Only a `confirmed` event for that recorded intent advances the active revision. The store checks the predecessor in the same immediate transaction as the journal insert. A competing confirmation loses and rolls back both event and projection changes. Reverts use a new revision ID and an intent pointing at the current revision.

The `events` table is the authoritative append-only journal. SQL triggers reject event UPDATE and DELETE. `Rebuild` clears derived tables and replays events in sequence. `Pending` exposes unresolved intent and uncertainty. `ActiveRevision` reports only confirmed state. The store does not perform external work and never replays uncertain operations. B09 must reconcile backend state before it confirms, replaces, or abandons such work.

Events accept only bounded identifiers: ID, idempotency key, target, actor, kind, revision and predecessor. There is no arbitrary payload or config field. `Events` returns these fields plus a store-recorded UTC timestamp and monotonic sequence. The call rejects results above 1000 rows rather than truncating them. A later paginated export is needed for larger histories. A retry with identical key and content is a no-op; changing the mutation under the same key is a conflict. An empty predecessor means no active revision. The identifier format prevents many accidental malformed values, but cannot tell whether a caller has placed credential material in an otherwise valid identifier. Callers must never do so.

SQLite uses WAL, `synchronous=FULL`, foreign keys and a five-second busy timeout on every driver connection. The pool has one connection and writes use `BEGIN IMMEDIATE`. The current schema is version 1. Policies, guardrails, experiments, agent runs and setup have reserved typed projection tables for their owning tasks. B02 uses revision and operation projections. Do not place secrets in SQLite.

`Backup(absolutePath)` uses SQLite `VACUUM INTO` in a private staging directory, then links the finished snapshot at the requested destination without overwriting an existing file. Opening a backup restores committed journal history and projections, including pending work. A stale backup cannot prove what happened in the external backend after that snapshot. B09 owns that recovery decision. Database files must have no group/world permission bits; backups are published at mode 0600. Parent directories must not be writable by group or others. Store paths should be on trusted local storage; SQLite opens by path, so hostile concurrent replacement of a database path is outside this boundary.

## Secret file boundary

`secrets.Open(absoluteRoot)` pins one allowlisted directory using `os.Root`. `Reader.Read(absoluteReference)` accepts existing references lexically beneath that directory and opens through the rooted descriptor. Parent symlinks cannot escape it. The root itself must be a directory without group/world write permission. Referenced files must be regular, nonempty, at most 64 KiB, and inaccessible to group and others. Mode 0600 and read-only 0400 files are supported. FIFO reads use a nonblocking open and are rejected by type. Errors contain no secret bytes. The reader does not modify permissions.

These file semantics target Unix-like mounts with standard mode bits and nonblocking file opens. Keep the root and its parent on trusted local/mounted storage. Secret bytes remain in the caller process; the journal and event export have no field for them. No browser or ledger API is added by B02.

## Verification limits

Tests cover duplicate and concurrent appends, stale predecessor rollback after event insertion, readonly SQLite write injection, reopen, projection rebuild, backup restore, safe event export, and secret file boundaries. The readonly injection is a deterministic SQLite fault, not proof of behavior under physical disk exhaustion. There are no cloud or model calls. Recovery against real backend work, customer compatibility, and release gates G1 through G5 remain open.
