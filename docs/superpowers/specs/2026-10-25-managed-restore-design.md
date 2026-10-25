# Managed admission history and restore

User requested the local tuner's Git-style restore experience for all three managed providers, on a separate branch. Implementation is inline. The live judging process, its ui/dist, website, published download and main remain unchanged.

The shared managed gateway controls transaction admission for Azure, Google and Supabase. Restore selects a confirmed historical admission limit within the current approved bounds, creates a new revision, and holds that limit until Resume automation. Safety still overrides the held limit: moderate pressure reduces to base, expired/missing evidence stops admission, and high pressure or uncertain work latches a stop. No provider settings, SQL writes, physical reserve, ownership, or profile bounds are restored.

Reuse the authenticated admin API (session, origin, CSRF). Freeze admission, Resume automation and Restore require expected revision and idempotency key under the controller lock. Confirm with an explicit review step in the UI. Concurrent stale requests fail, duplicate identical requests return their original outcome, and changed requests cannot reuse a key. Missing durable storage means read-only capability.

Persist a versioned, scope-bound admission journal with intent before runtime change and confirmation afterward using existing private fsynced snapshots. Keep unknown outcomes distinct and fail closed on storage failure. Restart remains frozen and never replays a command. Clean restart retains confirmed history but gives the new process a distinct revision. Reject incompatible scopes, malformed journals and unresolved intents. Legacy pre-journal state is preserved but not invented into history. Journal capacity is bounded at 4096 records; latest 200 shown, older history retained on disk; a full journal stops admission pending operator maintenance.

Use actual profile identity (provider/source, target, bounds plus backend host/port/database/role and budget) to bind history. Store only an opaque scope hash, not DSNs or credentials. Changing profile requires a separately reviewed state directory, not automatic adoption of old history.

Existing managed admission algorithm is retained except for explicit owner hold mode. Tests cover all three provider labels without paid cloud benchmarks, real PostgreSQL transaction preservation, persistence/restart, storage failure, duplicate/stale requests and HTTP security. Build UI into a separate ignored directory. The native demo's UI files must remain byte-identical.
