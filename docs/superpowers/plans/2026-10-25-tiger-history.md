# Tiger Data history and Mana demo

Approved scope: optional telemetry history, all existing provider paths, preserve tuner logic and Claude's styling. Factory/Moss deferred. Execute inline using Superpowers.

Architecture: an admin-backend decorator polls existing redacted state once per second, independently writes allowlisted measurements to a dedicated Tiger Data hypertable, and serves an authenticated last-hour history endpoint. No changes to scheduler/controller/provider code. Private credential-file and stable stream alias opt in. Exporter errors remain diagnostic only. Persistent storage is optional and outside request handling. Demo fixtures retain explicit simulated labels.

Tasks:
- [x] Test mapping, counter resets/gaps/duplicates and source allowlist; implement internal/telemetry recorder and pgx store. Use exact uint64 counts, TLS verification, 2-second network timeouts, fixed queries and one writer connection. Real Tiger integration: extension, schema, idempotency, time_bucket aggregation, reconnect persistence, isolation between streams, error redaction. Only known disposable test rows deleted.
- [x] Wire decorator at dashboard construction and /api/telemetry under existing authentication. Add optional history panel via TrafficTrend using existing design classes. Verify unauthorized access, disabled state, historical data after reload, mobile layout and no tuner/style diff.
- [x] Run a separate Mana interactive demo using public product artifacts and measured evidence. No credentials or private database access. Keep simulated animation distinct from recorded provider trials. Verify working interactions and shareable URL.
- [x] Fresh focused review; fix important findings, document exact verification and update handoff. Preserve preexisting dirty work; no mixed backend commit.

Review focus: history outage must not freeze admission; no credential or target leakage; stream isolation; reset/gap handling; simulated history must remain labeled. Live provider trials are not rerun.


## Completion ledger

- Live Tiger Data integration passed with full TLS certificate and hostname verification, explicit system CA bundle, Timescale hypertable, idempotency, weighted buckets, fresh-connection persistence and stream isolation. Four provider fixture labels remain simulated.
- History browser test passed in 7.0 seconds, including both dashboard types, persisted rows after reload and mobile overflow checks. First attempt exposed a test-only login race: isVisible ran before claim state loaded and skipped the token. Replaced it with Playwright's awaited fill on the fresh fixture.
- Existing managed/local browser tests passed in 16.3 seconds, including stale response handling.
- Fresh review tiger_final_review found one important cleanup deadline issue. Fixed the history query's connection close with an independent one-second timeout. TestHistoryDisconnectHasDeadline RED (missing helper) then GREEN; final race suite telemetry/admin/tuner/burst all passed. Live Tiger was included in that run.
- Native archive allowlist, manifest and private startup verification passed in 18.599 seconds. Package includes traffic-history.md. SHA256 1c728a8d4ec4b5777de99939c8f1e3069b19e23b1707dce4ba881861ba63bebf.
- Mana demo shared at https://www.mana.space/a/pooltune. Five scenes, four provider choices, explicit simulated animation/recorded results, real dashboard recording, optional narration, corrected managed setup URL requirement and optional Tiger history. Verified all three managed result values, scene navigation, setup text, and active audio/video playback (readyState 4, no media error). Shared short URL redirects correctly. Mana reports sign-in is required for viewers.
- Ruling: keep the existing dirty backend branch and public website unchanged. Deliver the verified new package separately rather than combine preexisting uncommitted work into an unrelated commit or deployment. Cost: the existing website download remains the earlier package until deliberately refreshed.
- Ruling: use the user-approved architecture and concise ledger instead of adding planning approval or full-branch test work. Verification is focused on changed components and delivery. Cost: unrelated preexisting changes are outside this review.
- Controller, runtime, local tuner and Claude CSS SHA256 values match pre-change values exactly. No live managed-provider benchmark reruns. No secrets sent to Mana.
- Raw history retention is owner-managed, not automatically deleted. Last-hour view is fixed. History export runs separately and drops unavailable intervals.
