# Investigator: compare causes and request evidence

**Current follow-up:** the diagnostic graph is now connected to the Railway index consumer and passed a real Sonnet repair run plus queued-job restart. [Hosted results](hosted-diagnostic.md). The local/replay results below describe the original diagnostic qualification. The other five diagnostic execution adapters remain.

## Problem and intended behavior

The six repair tools work on bounded cases, but the old input admits one cause category. A slow query may have several plausible explanations. The investigator needs to compare evidence, request a missing measurement, and decline unsupported changes. Jared explicitly requested this implementation on November 14: “build that investigator next so Claude can compare causes and request more evidence. Follow the design docs, test it with real Claude”.

Map nodes: `diagnose-causes` and `request-evidence` under `investigator`, connected to `inspection` under `tools`. Existing Go + TypeScript/LangGraph, one investigator, six repairs and independent Go action guards remain unchanged (d-language r2, d-workflow r1, d-reuse r1).

## Implementation decisions

Use a separate diagnostic graph with visible screen, compare, request and finish nodes. Keep the existing qualified single-call path intact while qualifying the new loop. Go supplies named, typed readers; the model never supplies SQL, credentials, object names or a different target. Each request is bound to the same run, table and column. Existing valid measurements can be supplied together, allowing the one-call fast path. Several cause observations may coexist.

A structured model turn either requests up to three named evidence tools, proposes one repair, or declines. It includes short evidence-based assessments of plausible alternatives, not private chain-of-thought. The tools are plan, indexes, analyze, statistics, vacuum, memory and cron. Missing/denied evidence is explicitly unavailable, never a zero or a negative result. The request router supports caller-provided Go readers; readers remain responsible for their own project grants and bounded database queries. The existing fixture evidence can be reused without repeating database work.

Initial local implementation limits: three durably reserved model attempts (including interrupted requests), six requested measurements, no repeated requests, 30-second evidence freshness and 22-second whole Go process deadline. These are engineering limits for this qualification, not universal production defaults. Expiry, budget exhaustion, malformed output or tool failure cannot produce a repair. Every emitted proposal must have available fresh plan/index evidence and any selected repair-specific evidence. Each short cause assessment explicitly states whether unresolved uncertainty blocks the decision. Both Go and TypeScript veto blocking alternatives. Diagnostic ANALYZE additionally requires a measured distribution contradiction; estimate error alone is insufficient. Go independently checks the selected proposal. Existing fixture executors retain their exact-family and permission checks; this feature does not authorize arbitrary customer repairs.

Alternative considered: collect all possible measurements before every call. It simplifies orchestration but adds unnecessary database work and model input. The selected loop reuses sufficient evidence and asks only for a stated decision gap. Additional model calls cost time, so evaluate one-call and missing-evidence cases separately.

## Qualification and honest limits

Unit tests cover mixed causes, fresh-evidence reuse, missing evidence, identity changes, invalid tool names, repeated requests, budgets, expiry, malformed model output and interrupted/resumed graph execution. Go bridge tests exercise actual subprocess requests and callback routing. Real-Claude evaluation uses retained Rookery measurements with their provenance preserved as replay, plus ambiguous/healthy/unsupported controls. A real local PostgreSQL reader test must prove that requested evidence can come from a bounded query, not only preloaded objects. Record all attempts, model/tool/total times and outcomes.

This builds and qualifies the investigator mechanism. Replaying Rookery measurements is not a new Rookery repair trial or proof of general customer collectors. The existing staged repair tests retain their own evidence. New customer enrollment, broad workload collectors, all repair variants, dashboard and production operation remain separate work.

## Implementation and evidence

See Tasks 27–29 in implementation.md and progress.md. Results will be appended after execution. LangGraph loop reference: https://docs.langchain.com/oss/javascript/langgraph/thinking-in-langgraph .


## What was built

The new graph has **screen_evidence → compare_causes → request_evidence → compare_causes → finish**. It reuses supplied measurements when enough evidence exists. Claude chooses which named measurement to request, Go supplies it, and the loop stops with a supported proposal or an honest no-change result. The original qualification was separate from staged execution; the hosted index adapter is now qualified in the follow-up linked above.

Go saves requested evidence before sending it to the graph. After interruption, the public protocol restores the same checkpoint and retrieves the same Go-owned evidence receipt. It rejects a changed enrollment and prevents two processes from owning the same investigation. A persisted reservation caps real provider attempts at three across restarts, including a call whose completion is unknown. A graph can report two completed turns while Go reports three reserved attempts after a crash. Failed attempts retain their elapsed time.

The new Go API is `graphrunner.DiagnosticProcess(stateDSN)`. Its caller supplies the enrolled identity, initial observations and a named `DiagnosticReader`. The model cannot provide SQL or expand the target. A reader still must implement its own grants, exact-object checks and bounded database query. `DiagnosticReplayProcess()` is explicitly disconnected from durable jobs and repair execution.

## Results in plain English

**The investigator can now ask for useful evidence and compare explanations.** Sonnet chose the expected outcome in all 15 bounded evaluation cases. This is enough to move to hosted integration; it is not evidence of universal diagnostic accuracy.

| Check | Result | What it establishes |
| --- | --- | --- |
| Claude Sonnet 5.5, native structured output | 15/15 cases passed | Six repair decisions, competing statistics explanations, missing evidence, contradiction and no-change controls |
| Claude Haiku 5.5, same final prompt and output mode | 13/15 passed | Incorrect index decline and invalid Cron output remain; prefer Sonnet for this diagnostic path |
| Sonnet, sufficient evidence | 11 one-call cases; total median 3.639 s, range 3.012–6.501 s | Preserve the fast path when no additional measurement is needed |
| Sonnet, requested evidence | 3 two-call cases; total median 8.691 s, range 6.928–10.673 s | Extra decision rounds cost seconds; ask only for a meaningful gap |
| Healthy detector control | 0 model calls; 0.264 s process total | Healthy work can end before spending model tokens |
| Fresh local PostgreSQL + real Sonnet, final source | 2 model calls; 9.008 s total | Claude requested evidence, Go actually queried PostgreSQL, and Claude proposed ANALYZE |
| Interrupted process and durable restart | Passed; one physical evidence collection | Checkpoint recovery reuses the Go receipt instead of repeating the database read |

The final fresh run spent **8.670 s in Claude**, **4.015 ms handling two evidence requests** (one real catalog read, one unavailable statistics capability), and about **0.333 s** in process/framework overhead. PostgreSQL estimated **19,778 rows** while the executed query returned **1**. Its stored lookup frequency was 1, meaning the old statistics predicted every row would match. That is why refreshing statistics was justified. These timings start when the investigation runs; they exclude detector windows, queue waiting, repair application and benefit verification. No index or statistics repair was applied in this diagnostic test.

The model recommendation is **Sonnet for the new diagnostic loop**. The strongest alternative is retaining Haiku for speed/cost continuity with the earlier single-call repair tests. Haiku was less reliable here and was not faster under the same final native-output mode: one-call median 4.945 s and two-call median 9.092 s. This small sequential test is not a broad model benchmark or pricing comparison. No Railway setting or existing consumer model was changed.

## What the evaluation supplied

`evidence/diagnostic-loop/cases.json` contains the reproducible inputs and expected answers. Expected answers, case names and source paths are kept outside Claude's input. Positive cases reuse the actual measured plan, index checks and repair evidence from the six retained Rookery reports. The replay preserves the original measurement timestamp in its result and explicitly rebases only the diagnostic freshness clock; it has no database or repair connection.

The additional controls are deliberately constructed, not fresh Rookery measurements: withhold ANALYZE until requested; combine stale-distribution evidence with weak relationship evidence; combine accurate single-column estimates with strong relationship evidence; combine contradictory stale/fresh statistics claims; mark a detector-healthy query; make a scan return 90% of examined rows; supply a healthy-looking indexed plan for an unexplained slowdown. Two held-out withheld-evidence variants use 25,000 and 250,000 examined rows. These test the decision behavior, not workload throughput.

`sonnet-native.jsonl` scores both the model's decision and the final guarded outcome. A wrong proposal vetoed by Go is not a correct model answer. All earlier attempts remain recorded: initial incorrect decisions, overly cautious declines, output failures, the forced-tool Sonnet API incompatibility and the Haiku comparison. The cause of the initial two transient parse failures was not established; do not relabel them as proven length failures. Native structured output is now covered at the actual SDK request boundary.

## Verification and trade-offs

86 TypeScript tests and type checking passed. Go race unit tests, vet and the new race-enabled local PostgreSQL integration suite passed. An independent reviewer identified two Important issues (public resume and unresolved alternatives) and one Minor timing issue; regression tests reproduced them before fixes. Follow-up review cleared the material findings. A remaining attempt-count reporting edge was then reproduced and fixed with interrupted/completed-checkpoint assertions.

Three model attempts, six reads, 30-second observation freshness, a 20-second graph budget and a 22-second Go deadline are qualification limits. Long Cron observation windows still belong in Go's durable scheduling/evidence layer, not a model waiting for minutes inside this loop. Saving evidence and attempt reservations adds state writes; it avoids repeated database work and unbounded model spending after a restart. Additional model calls dominate these diagnostic timings, so collecting all possible evidence up front remains an expensive alternative rather than the default.

## What remains

**Connect the other five repairs** is the next investigator map node. The new graph now runs through the Railway index consumer and existing guarded index executor. Its current reader registers bounded plan, index and lookup-statistics inspection for the owned fixture. The one-call live run reused initial evidence; it did not need extra reads. Qualify the other five diagnostic execution adapters and their live readers with real Sonnet, healthy controls and recovery before enabling them. Do not confuse the earlier six-family legacy qualification with new-adapter qualification.

Other remaining work is unchanged: broader repair variants, customer project connection, account isolation, auth/dashboard, retention and operational rollout. This implementation does not make the whole product production-ready.

## Inspect and reproduce

- `investigator/diagnostic.ts`: graph states, budgets and proposal eligibility.
- `investigator/diagnostic-model.ts`: prompt, factual summaries and native structured output.
- `internal/graphrunner/diagnostic.go`: named evidence protocol and independent Go checks.
- `internal/graphrunner/diagnostic_journal.go`: durable evidence and attempt reservations.
- `investigator/langgraph.json`: `pooltune_diagnostic` Studio registration. Studio can inspect the graph and supplied evidence; live Go reads require the process bridge. No cloud tracing is enabled.
- `evidence/diagnostic-loop/summary.json`: machine-readable results and source hashes.

With model credentials supplied privately and `POOLTUNE_ANTHROPIC_MODEL=claude-sonnet-5-5`, run `go run ./cmd/pooltune-diagnostic-eval < docs/design/hosted-query-agent/evidence/diagnostic-loop/cases.json`. The ordinary test command never calls paid models. The explicit live local-database test additionally requires `POOLTUNE_LIVE_DIAGNOSTIC=1` and uses `go test -tags=integration ./internal/graphrunner -run TestDiagnosticGraphReadsFreshPostgresEvidence -v -count=1`.
