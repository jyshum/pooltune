# Pooltune: build status and remaining architecture

Updated November 14, 2026, against source `f3f01c7`. This map now distinguishes implemented staging behavior from the complete V1 design. It is an offline snapshot, not a live agent dashboard. The archived design context below records earlier decisions and open questions at the time; current build status is in this section and `map/status.json`.

## How to read the colors

- **Light green / Verified scope:** implemented and demonstrated for the scope stated on the card (staging, local database testing or replay). This does not mean customer production readiness.
- **Amber / Partly built:** a working piece exists, but the stated responsibility is not fully implemented.
- **Gray dashed / Remaining:** still needs implementation or broader qualification.
- **External:** the owner, customer database or model provider; these are context, not Pooltune completion claims.

Click any card for its current scope, remaining work and source references. Explore Controlled database tools for the six tested repairs and broader qualification. Explore Investigator graph for the existing recommendation path, the newly qualified diagnostic loop and the remaining hosted connection. Explore Hosted API or Customer dashboard for customer connection, credentials, sign-in and visible activity. Build status is available from every view and lists the next work. Existing node IDs and the tools deep link are preserved.

## What is built, and what is not

The Go collector, PostgreSQL queue, continuous consumer and six guarded repair cases have staging evidence. A separate diagnostic LangGraph now compares multiple cause observations and requests named Go evidence. Sonnet passed 15/15 bounded diagnostic cases and a fresh local PostgreSQL test. Go-owned evidence receipts and graph checkpoints survived a process restart without repeating the completed measurement. [Full result and trade-offs](diagnostic-loop.md).

The earlier tests supplied one known cause category. The new graph removes that input restriction and can investigate missing evidence. However, the staged Railway consumer still uses the earlier fixed-evidence path. **Connect the hosted investigator** marks the remaining wiring and live-reader qualification. General customer onboarding, dashboard and sign-in remain unfinished.

Green means the scope stated on that card was verified. The new diagnostic nodes were qualified locally and with retained-evidence replay, not deployed to Railway. The investigator parent remains amber because hosted integration and general customer coverage remain.

## Changes from the earlier map

- Replaced the stale “no running agent” claim with measured staging status.
- Made the actual PostgreSQL queue, continuous consumer and delayed follow-up behavior explicit in the scheduling scope.
- Recorded the single-call model boundary: supplied evidence goes to Claude once; a typed proposal returns to independently checked Go execution.
- Split out diagnostic work and customer-product responsibilities; cause comparison and evidence requests are now locally qualified, with hosted integration still visible as remaining work.
- Kept broader repair variants visible: compound indexes, column statistics targets, cleanup settings, candidate-table writes, real memory budgets and owner schedules.
- Preserved every decision revision and approval record. Build status is a separate evidence annotation and does not approve proposed production contracts.

The Go + TypeScript/LangGraph architecture and six repair families remain consistent with the plan. The implementation is intentionally narrower than the full design. Earlier summaries blurred that distinction; completing the tool cases does not complete the whole agent product.

## Next work and trade-offs

The next sequence is a proposal within the existing roadmap: connect the qualified diagnostic loop to the hosted consumer and live readers; general project connection/enrollment; a simple end-to-end activity view; representative repair coverage; account/credential and production operating controls. The scope of a first pilot must be explicit before customer mutations. A public landing page is not a prerequisite for an internal pilot.

Current assessment: keeping one investigator and deterministic Go execution avoids creating six disconnected agents. The alternative is separate agents per repair; they could specialize prompts but would duplicate evidence, budgets and recovery coordination. The diagnostic loop requests additional calls only for decision-changing gaps, preserving the speed benefit of the existing one-call path when evidence is already sufficient.

The database-backed queue makes evidence updates and job creation transactional. An external queue could offload delivery operations but introduces another service and coordination boundary. Staging uses PostgreSQL successfully; production scale, lifecycle and outage behavior remain open.

## Coverage and reproducibility

Reviewed the consumer's fixture/enrollment routes, graph cause validation and model boundary, the six staging reports, restart/recovery records and roadmap. Current colors follow those bounded claims; this refresh is not a new full source/security audit. Customer permissions, account separation, resource headroom, retention and full production operations are not claimed verified. Existing decision comparisons remain attached to their owning nodes; new unfinished cards expose concrete open work rather than claiming decisions are settled.

`graph.json` remains the canonical architecture and decision source. `map/status.json` stores scoped implementation status and remaining work. `design.html` is generated with `python3 docs/design/hosted-query-agent/map/render.py`; `--check` checks freshness. The small local presentation layer reuses Jared's shared renderer and adds status colors and an always-available status panel without changing the shared skill installation. No approval file is modified.


---

# Historical design and approval context

Status: evolving design snapshot, not a completed implementation design. The canonical product scope remains ../../product/query-performance-v1.md. Backend decision PT-TECH-001 remains in ../../product/backend-language-decision.md.

Implementation continuation: [HANDOFF.md](HANDOFF.md) records the November 13 handoff authorization, selected workflow modes, first milestone, test project and environment gaps. Product scope is ready for implementation planning; unresolved technical contracts remain explicitly listed below.

## How to use this map

Click a component to inspect its purpose, stack, decisions and trade-offs. Explore Controlled database tools to see evidence collection, justified experiments, all six repair capabilities, and verification/recovery. New means a planned addition, not implemented code. Proposed decisions have not been approved. The map is separate from future LangGraph Studio and the customer dashboard.

## Accepted boundaries

- Go backend and database tools: d-language revision 2 supersedes Rust revision 1.
- One TypeScript/LangGraph investigator and PostgreSQL operational state: d-workflow.
- Fully hosted; no customer installation or laptop dependency: d-hosting.
- Connect Supabase is the accepted onboarding entry point: d-onboarding. Manual versus automated database-access setup remains open; other providers are future integrations, not added V1 scope.
- Reuse sufficient valid evidence; avoid redundant tests: d-reuse.
- Adaptive scheduling is selected under Jared's preference for performance and minimal customer work: see the product plan's Automatic scheduling default. Maintenance windows are optional overrides; exact limits remain open under d-trials.
- Exactly six repair scopes and the exclusions in the canonical product plan. Each repair's complete tool contract remains unfinished.
- Customer Supabase executes database work; Pooltune stores selected evidence, not a copy of application tables.
- No arbitrary model SQL authority, customer transaction killing, customer-index deletion, automatic compute purchases, or application/RLS rewrites.

## Decisions still needed, in useful order

1. Customer connection and grants: Connect Supabase is selected (d-onboarding); provisioning method, monitoring identity, repair identity, credential lifecycle, representative query enrollment, redaction and retention remain open (d-identity).
2. Common tool contract and six operation-specific contracts: permitted inputs/effects, evidence freshness, timeouts, preconditions, result states and recovery.
3. Controlled trial qualification: allowed targets and sizes, windows, concurrency, resource headroom, timeout/cleanup policy and evidence of benefit (d-trials). Existing support for live trials is not acceptance of unspecified limits.
4. Detection and verification: [v0 numerical proposal](detection-verification-v0.md) now specifies meaningful slowdown/expense triggers, minimum observations, per-repair benefit criteria, regression protection and inconclusive outcomes. Calibrate on Rookery before accepting release defaults; do not treat proposed values as established guarantees.
5. Durable jobs and graph state: leases, duplicate suppression, model budgets, state ownership and uncertain database-action reconciliation (d-durability).
6. Deployment and onboarding: providers, Go libraries, auth, secrets, account isolation and first development Studio configuration (d-studio).
7. First implementation slice and evaluation: demonstrate one query from detection through repair outcome and failure recovery on an authorized test project. All six remain V1 scope; qualification can be sequential.

## Evidence and authority

Current approval records import real statements from this conversation. Their timestamps record import time, not an invented original utterance timestamp. Source documents describe intended behavior; no live Supabase integration or autonomous repair is claimed verified. This map does not grant implementation/deployment authority or choose missing workflow settings.

## Accepted research lessons and implementation consequences

The November 12, 2026 request to save comparison lessons and preserve scope is recorded in the product plan's “Accepted lessons from comparable products” section. Existing component responsibilities, decision IDs and six repair scopes are unchanged; no new graph decision or approval is inferred.

- **Go tool boundary:** check operation-specific owner grants, current target state and execution budgets independently of the investigator. Inspection access does not imply trial or repair access.
- **Evidence and dashboard:** distinguish estimates, observed baseline, and measured outcome. Preserve the evidence-reuse policy and explicitly represent unknown or inconclusive findings.
- **Investigator:** consider alternative explanations, including unsupported causes. A reasoned no-change result is valid; do not optimize toward an arbitrary universal index-use or latency target.
- **Action journal and workers:** persist intent, identify uncertain execution, inspect customer state before retrying, and use repair-specific recovery. Stopping future work is different from reversing completed work.
- **Qualification:** evaluate healthy and misleading cases, incomplete evidence, interrupted actions, repeat runs and regressions as well as successful repairs. These requirements do not establish that the service already works.

## Handoff readiness

The project can be handed off for implementation planning now. It is not yet a complete specification for unattended repair execution. The seven design topics above remain the detailed checklist. Group the decisions that affect customer promises into these boundaries:

| Boundary to settle | Required decision or evidence | Recommendation for the next planning chat, not yet accepted |
| --- | --- | --- |
| Access and authorization | Prove the connection/bootstrap route, actual privileges for each operation, credential revocation and enrolled targets. | Probe automatic setup on an identified, authorized test project; retain guided SQL as a fallback. Do not assume a narrow database login narrows a retained broad Management API token. |
| Evidence and privacy | Capability-first direction accepted; specify evidence categories, retention, sample limits and what reaches Anthropic and optional tracing. | Give the investigator relevant evidence under upfront customer grants, including values when necessary. Encrypt retained data and transport; remove secrets and irrelevant sensitive fields without discarding diagnostic meaning. Measure accuracy and latency impacts. No application-table replica. |
| Unattended operating limits | Adaptive scheduling selected; specify timeouts, concurrency, storage and memory headroom, optional windows, and per-project model and investigation budgets. | Ship tested defaults; execute eligible repairs when conditions permit, defer when necessary, and surface persistent inability to proceed. One mutating repair at a time remains an initial proposal, not an approved numeric limit. |
| Evidence of benefit | Review and calibrate detection-verification-v0.md, including distinct detection/benefit/protection baselines, missing guard coverage and fast protective intervention. | Observe ordinary traffic first; add a bounded experiment only for a decision-changing gap. Proposed numerical defaults are not release-qualified. |
| Service and recovery | Select hosting, authentication, secrets, durable job ownership, account isolation and disconnect/stop behavior. | Specify restart and uncertain-action recovery before unattended writes; choose routine libraries within these requirements during implementation planning. |
| First qualification slice | Rookery selected and its MCP SQL connection verified; select one representative query and specify expected behavior and failure cases. | Start with observation through one eligible index repair and measured outcome, including interruption recovery and a healthy no-change case. All six repairs remain V1 scope and qualify individually. |

Exact Go packages, route names, SQL schema layout and LangGraph node names can be chosen by the implementation planner within accepted boundaries. They do not each require another product discussion. Concrete tool contracts, numeric defaults and their evaluation cases must be written before the affected operations are implemented, rather than left for the model to invent at runtime.

Implementation workflow settings were selected on November 13: design conflict mode ask; human review mode milestones. Review the working isolated test setup before enabling automatic database changes. Final document commit and a new implementation chat are now authorized by Jared; see HANDOFF.md and progress.md. Exact thresholds remain calibration proposals, not claimed production guarantees.

## Research context

November 12, 2026 clarification: the product plan's “Diagnostic capability and customer data” section records Jared's capability-first compromise. The initial blanket redacted-evidence proposal was not accepted. This clarifies d-identity's requirements without approving an exact credential, retention or redaction design. Permission enforcement belongs in code and should not cause repeated human prompts within existing grants. Rich evidence access does not expand mutation authority.

Existing reports in /Users/jshum/Desktop/Tech Stuff/Resumes/softEng/reports/ cover Supabase evidence contracts and representative query validation. Historical Rust references in those reports are superseded by d-language revision 2. Exact API permissions, response contracts and deployed extension capabilities still require a real-project probe.
