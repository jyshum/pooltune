# Local agent loop

The control process owns one bounded loop per target/path. It sends generic
metrics and opaque workload IDs to the owned gateway. Selectors, principals,
routes, raw SQL, rows and request bodies stay local. Each inference step uses the
same account budget accounting as other calls. There is no inference in admission.

The gateway sees the current declarative policy, approved numeric bounds and class
choices, evidence ID, aggregate workload counts/latencies and a bounded trigger
category. The local runner translates opaque IDs back to the verified registry.
Optional descriptions require approval of an exact preview hash, including policy,
boundaries, local registry and disclosed descriptions. Changing these invalidates
consent; ordinary new metric windows do not. Descriptions are never authority.

A trigger gate requires continuous complete measurements, configured minimum
samples, a sustain window and cooldown. Missing evidence resets that window. The
runner independently enforces persisted cooldown/hourly limits, evidence freshness,
safety state, a configured wall-clock deadline and at most eight model steps. It
allows only one active run. Production signal thresholds require calibrated setup;
the gate does not invent an SLO or infer business importance from latency.

Tools read bounded state, known-workload aggregates and revision outcomes. Candidate
submission checks current predecessor and guardrail versions, known IDs and fresh
complete evidence, then runs the real policy compiler. The initial profile allows
one parameter family per candidate. The candidate is durable and inactive before
acknowledgement. A human edit or freeze invalidates a pending proposal.

StartCanary writes a durable intent before requesting the rollout coordinator. That
coordinator must atomically check expected versions, safety and its one-experiment
constraint and deduplicate the request. Acceptance ends inference immediately;
the deterministic evaluator owns subsequent decisions. Unknown acknowledgement
blocks subsequent agent runs, survives restart, and is never replayed automatically.
Coordinator-backed reconciliation is required before clearing it. The agent cannot
install a stable policy or change provider settings.

Discovery suggests classes only for observed registered workloads and labels them
as model hypotheses requiring owner review. It cannot enable an initial policy or
change business boundaries. The dashboard must escape reasons as untrusted text.
Opaque metrics alone cannot establish the business meaning of a request.

History is a private, fsynced local journal: trigger/version/evidence IDs, bounded
tool names and outcome codes, token usage, inactive candidates, canary request IDs,
and discovery hypotheses. Raw model arguments and summaries are not logged. The
initial journal stops after 256 runs instead of silently deleting history; an
explicit export/archive lifecycle is required for longer operation. Process restarts
never resume an inference step or replay an activation automatically.

## Evidence and remaining integration

Fixture tests cover candidate compilation, no action, invented workloads, hostile
approved descriptions, forbidden tools, stale edits/evidence, eight-step exhaustion,
budget denial, persisted cooldown, failed storage, unknown canary restart, discovery
review and actual gateway HTTP accounting. These are executable local tests, not
proof of a real model or real canary. The local continuous control service now supplies measured runtime snapshots,
sustained trigger signals and the real rollout authority. See control-runtime.md.
Production calibration and hosted deployment remain gated.

`go test -tags=model ./tests -run TestRealPolicyRun -v` is opt-in and skips by default.
To enable it later, configure POOLTUNE_MODEL_TEST=1 plus absolute paths for
POOLTUNE_MODEL_PROFILE, POOLTUNE_MODEL_KEY_FILE, POOLTUNE_INSTALL_TOKEN_FILE and
POOLTUNE_GATEWAY_DB. The ledger must already exist with owner-approved access and
exactly the $5 lifetime global cap; the test does not reset accounting. The reviewed
profile must match verified provider pricing and ceilings. Its test authority cannot
activate traffic. Success means a real model produced a locally validated candidate
against clearly synthetic inputs, not that rollout or provider compatibility passed.

One actual configured-model run now produced a locally validated candidate after an earlier failed attempt. See evidence/model-run.md. Hosted deployment security and actual canary benefit remain pending, as does final branch review.

## Customer authority and measured value

The customer defines business goals, allowed classes, locked priorities and
numeric queue/concurrency/weight/reserve bounds. Priorities need not be fixed
forever: the agent may propose a different allowed class or a bounded policy
adjustment for measured evaluation. It cannot change the registry, unlock a
priority, invent an objective, resize a provider, or install a stable policy.
Discovery remains a suggestion requiring owner review. Full interactive editing
of these boundaries remains an administration release gap.

`TestCustomerLimitsBoundDynamicAgentPriorities` exercises the real local candidate
compiler from scripted model tool output: an allowed class change can request a
canary; locked/unapproved classes and excessive wait, concurrency, reserve or
weight cannot. This is authority enforcement evidence, not model intelligence.
No additional paid call is needed to verify these deterministic checks.

The fixed-policy warm-pool comparison in `benchmark-results.md` deliberately
excludes AI. Prove queue/pool value first, then compare bounded AI proposals with
simple rules and account for model cost. AI has no performance credit until that
separate comparison exists. The existing $5 lifetime accounting cap stays intact.
