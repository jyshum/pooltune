# Reliable agent engineering for Pooltune V1

Research prepared November 11, 2026. Scope: engineering research and design proposals only. The pasted handoff is the accepted product context, not an implementation approval. Existing accepted families remain existing-pool tuning, narrowly eligible index additions and their supported removal, and opt-in targeted ANALYZE. Exact contracts, readiness and launch policies below are proposals. No live database actions or product implementation occurred.

## What should the agent be, and what needs to be added to V1?

### Takeaway

Keep the three accepted action families. Expand V1's ability to establish eligibility, recover uncertain work, measure outcomes and safely abstain. A single investigator inside a durable, deterministic controller is a stronger starting point than a team of autonomous database operators.

### Cited Findings

- Anthropic distinguishes predefined workflows from agents that choose their own tool sequence, recommends beginning with the simplest effective design, and identifies programmatic checks between model steps as a useful pattern. The article itself warns that its tooling landscape has changed since 2024; these architectural principles should not be mistaken for a current framework recommendation. [Building effective agents](https://www.anthropic.com/research/building-effective-agents)
- Anthropic's 2026 Managed Agents design separates the model loop, tool execution and durable event log. It keeps credentials outside the environment where model-generated code executes and uses a credential-holding proxy for custom tools. Session storage is separate from the model's limited context. [Scaling Managed Agents](https://www.anthropic.com/research/managed-agents)
- Tool guidance recommends clear boundaries, strict input models, meaningful results, bounded retrieval and useful validation errors. More tools are not automatically better. [Writing effective tools](https://www.anthropic.com/research/writing-tools-for-agents)
- Context guidance favors a small, relevant working context, unambiguous tools and retrieving further evidence when needed, rather than filling every prompt with accumulated material. [Effective context engineering](https://www.anthropic.com/research/effective-context-engineering-for-ai-agents)
- Supabase currently documents an hourly, read-only performance agent using advisors, SQL diagnostics and saved snapshots. It investigates slow queries and blockers, explicitly does not create indexes or cancel sessions, and distinguishes findings from inability to assess. [Supabase performance agent](https://supabase.com/docs/guides/observability/automate-with-agents/performance)
- Supabase's October 20, 2026 launch also includes agent prompts, read-only log queries, debugging skills, health advisors and a connection dashboard. [Operate with confidence](https://supabase.com/blog/operate-with-confidence-check-production-with-your-coding-agent)

### Inferences

**Proposed product promise:** Pooltune investigates recurring Supabase database problems, performs a small set of preauthorized repairs when eligibility can be demonstrated, and records measured effects and remaining uncertainty. This is a proposed value proposition, not evidence that customers will buy it or that it is superior to Supabase.

**Add these capabilities to V1:**

1. **Capability discovery.** Before enabling an action, identify project and database instance, PostgreSQL version, extensions, available metric columns, grants, reachable connection route, provider API capabilities and tested configuration behavior. Persist the result and recheck after version or infrastructure changes. Do not assume that `MAINTAIN`, `pg_stat_io`, a pool-setting reload, or a particular telemetry endpoint exists on every project. A missing capability disables only dependent checks/actions.
2. **Evidence-backed action eligibility.** An action must have observed prerequisites, a plausible mechanism, current object/config identity, permission, execution limits, an outcome metric, regression guardrails and a recovery contract. A model saying it is 95% confident satisfies none of these conditions.
3. **Durable execution and reconciliation.** Preserve exact intent before dispatch, distinguish known failure from unknown outcome, and inspect the external system before retrying.
4. **Outcome verification.** Confirm both that the technical change landed and that the relevant workload improved without a known guardrail regression. These are different checks.
5. **Explicit inability to fix.** Lock incidents, insufficient hardware, unsupported configurations and uncertain query identities still receive useful diagnoses. The agent must not substitute an allowed but irrelevant action just to appear autonomous.
6. **Drift and stop controls.** Manual changes, schema migrations, credential revocation, stale evidence and user stop must invalidate outstanding authority to dispatch.

This is a meaningful scope addition, but mostly product reliability rather than new mutation categories. Keep arbitrary query rewriting, RLS changes, session termination, global memory tuning, VACUUM/REINDEX, compute purchases and index removal outside the accepted narrow scope.

**Minimal proposed architecture, logical boundaries rather than six microservices:**

| Component | Responsibility | Must not decide |
|---|---|---|
| Collectors and normalized evidence store | Bounded provider reads; record source, instance, time, units, completeness, reset epoch and redaction | Whether a missing sample means healthy |
| Incident controller | Group symptoms, detect persistence, schedule bounded investigations, enforce leases/budgets/cooldowns | Invent a cause from a resource threshold |
| One investigator model | Request diagnostic tools, compare hypotheses, propose one candidate and explain abstention | Grant itself rights or directly execute arbitrary SQL |
| Policy and contract validator | Resolve real objects; enforce grants, current state, bounds, freshness and supported action type | Accept model prose as authorization |
| Executor plus recovery worker | Apply typed actions; inspect state; reconcile and perform supported compensation | Blindly retry ambiguous mutations |
| Verifier plus action history | Compute registered before/after metrics, guardrails and outcome labels; publish evidence | Let the investigator grade its own success from its narrative |

Use one database for incident/action state and one scheduler/worker deployment initially if adequate. A managed durable-workflow engine is an option, not a V1 requirement. A queue and a job table alone do not provide safe external side effects: the action adapters still need their own reconciliation contracts.

**Where the model is actually useful:** competing causes need investigation. For example, elevated CPU plus acquisition errors could reflect missing indexes, an unexpectedly broad RLS query, excessive concurrency, or legitimate workload growth. The model chooses useful next reads and compares evidence. Deterministic code checks rates, permission scopes and numeric limits. If a simple rule handles a case reliably, retain the rule; compare the agent against that baseline.

**Avoid a default multi-agent product architecture.** Separate investigator, executor and verifier by authority and code, not necessarily by separate LLMs. A second model can critique a complex diagnosis later, but does not independently prove safety. Parallel independent evidence reads are appropriate; simultaneous competing mutations are not.

**Proposed tool surface:** `get_instance_capabilities`, `get_incident_metrics`, `get_query_deltas`, `get_blocking_graph`, `get_relation_health`, `get_existing_indexes`, `get_plan_estimate`, `get_index_advisor_candidates`, `get_pool_configuration`, `propose_action`. The executor receives validated action records rather than arbitrary model-generated database statements. A production `execute_any_sql` tool is unnecessary, even if labeled read-only. Functions can have effects; broad reads can expose customer records. Implement fixed collection templates and narrow parameter validation, returning summaries rather than row contents.

Each result should carry an evidence identifier, project/instance/object identity, observation time, interval or snapshot, units, source, reset marker where relevant, `complete|partial|unavailable`, and explicit reason. Bounded top-N output must state it is truncated. Keep durable raw evidence references so compressed context cannot erase the facts behind an earlier action.

### Gaps

- These sources do not establish Pooltune demand, willingness to grant credentials, expected incident frequency, or cost savings. Interview users and evaluate actual recurring incidents.
- The appropriate model, prompt, investigation budget, collection cadence and numerical eligibility thresholds require measured experiments. No universal 80%-CPU or confidence-score rule is proposed.
- Per-family launch readiness depends on the other researchers' actual Supabase API, privilege and runtime findings. In particular, accepted PostgREST pool tuning must remain validation-gated if reload/disruption semantics are unproven.

## How should authority, recovery, verification and stop work?

### Takeaway

Permissions and action safety belong outside the LLM. Every action needs a bounded contract and durable lifecycle that distinguishes “sent,” “observed applied,” and “measured beneficial.” Stop, cancellation and revert are separate operations.

### Cited Findings

- OpenAI documents prompt injection and private-data leakage risks, recommends keeping untrusted text out of developer instructions, using structured fields and layered mitigations, and cautions that those measures do not eliminate risk. Its Agent Builder guidance additionally recommends per-call approvals. That product-specific recommendation is not evidence that preauthorized operation is safe without an independent policy design. [Safety in building agents](https://developers.openai.com/api/docs/guides/agent-builder-safety)
- Temporal explains that activities can be retried and external effects are not atomic with workflow history. Idempotent operations or stable idempotency keys are needed to avoid duplicates; default retries are not an exactly-once external-write guarantee. [Idempotency and durable execution](https://temporal.io/blog/idempotency-and-durable-execution)
- Temporal distinguishes cooperative cancellation, which can run cleanup, from termination, which closes the workflow without cleanup. Activity cancellation depends on receiving and handling the signal. [Temporal cancellation and termination source documentation](https://github.com/temporalio/documentation/blob/main/docs/encyclopedia/workflow/cancellation-and-termination.mdx)
- PostgreSQL says `EXPLAIN ANALYZE` actually executes its statement and ordinary effects occur. Planning costs are estimates, not measured latency. [EXPLAIN](https://www.postgresql.org/docs/current/sql-explain.html)
- PostgreSQL row-security rules depend on role. Owners usually bypass policies, and superusers or `BYPASSRLS` roles bypass them; policies can apply differently to different commands and roles. [Row security policies](https://www.postgresql.org/docs/current/ddl-rowsecurity.html)
- PostgreSQL documents that inappropriate indexes can slow workloads. Concurrent builds have additional work and failure handling, including potentially invalid indexes. `IF NOT EXISTS` alone does not establish that the existing index is the intended one. [CREATE INDEX](https://www.postgresql.org/docs/current/sql-createindex.html)

### Inferences

**Proposed action contract, stored before dispatch:**

- `action_id`, incident and project/instance IDs, contract/model/prompt versions.
- Exact action family and normalized arguments. Bind table/index identity to catalog identity plus definition, not a display name alone; recreated objects can reuse names.
- Evidence IDs and freshness deadline; observed prerequisites and explicit rejected alternatives.
- Customer grant version, object scope, allowed limits, revocation/stop generation.
- Exact prior state and expected current state; expected desired state; supported reconciliation function.
- Maximum runtime, lock wait/resource limits, cooldown, concurrency lease and retry policy.
- Primary outcome metric, guardrails, baseline window, minimum samples, observation limit and comparability checks.
- Revert contract and caveats, or explicit `not_reversible` for ANALYZE.

**Eligibility is an evidence checklist, not an LLM percentage:**

| Gate | Example reason to decline |
|---|---|
| Correct path and target | A Supavisor proposal for a PostgREST-only incident |
| Supported intervention mechanism | CPU is high, but the proposed pool increase only admits more work |
| Valid evidence | Counter reset, stale sample, missing role context or unknown bind parameters |
| Scope and permission | Customer enabled ANALYZE but not indexes |
| Operational headroom | Index build competes with already saturated I/O or storage is insufficient |
| Testable outcome | No relevant requests occur after the change, or the configured value cannot be shown effective |
| Known recovery | Prior config unknown, conflicting manual change, invalid index identity ambiguous |

Do not require unnecessary certainty about every causal factor. Require the action's mandatory prerequisites, bounded downside and an honest measurement contract. If optional corroboration is missing, state it; if a mandatory prerequisite is missing, abstain.

**Proposed lifecycle:** `observed → investigating → proposed → eligible → dispatching → reconciling → applied → observing → closed`. Alternative branches include `no_action`, `ineligible`, `failed_known`, `unknown_effect`, `external_conflict`, `stop_requested`, `reverting`, and `revert_failed`. Keep execution state, measured outcome and automation status in separate fields rather than one overloaded status string.

Persist intent before sending the external command. A network timeout after a request is sent means unknown effect, not failure. On recovery:

1. Reload the action, current grant/stop generation and lease.
2. Inspect configuration or catalog state using the action-specific adapter.
3. If the exact desired state is present, record observed-applied and continue verification, subject to stop policy.
4. If clearly absent and no prior attempt is still running, decide whether a retry remains eligible.
5. If an operation is still running, observe it; do not dispatch a duplicate.
6. If state is partial, conflicting or unknowable, quarantine that action and dependent changes. Escalate a concrete recovery task.

For index creation, use a deterministic action-specific name plus definition and relation identity, then inspect validity and in-flight progress. An identical name is insufficient. For a pool setting, repeated assignment is only superficially idempotent: a customer may have changed the setting in between. For ANALYZE, do not automatically rerun just because the connection disappeared; repetition adds workload and cannot restore an earlier statistics state.

**Manual changes win over stale automation.** Compare expected state immediately before mutation and before revert. If different, record external drift and suspend dependent action. Serialize Pooltune's own project-wide experiments initially. Internal leases do not lock human operators or deployments. If the provider lacks a conditional update/version check, read-before-write has a race window; disclose and test that limitation rather than claiming atomic compare-and-set safety. Do not silently overwrite drift to satisfy a saved desired state.

**Credentials and untrusted content:** collectors and action adapters resolve credentials through a tenant-bound secret service. The LLM receives neither tokens nor connection passwords, and cannot choose an arbitrary project, URL or external destination. SQL comments, error messages, schema descriptions and log strings are evidence, never authorization. Redact literals and personal information before inference where possible. Do not let a schema-valid object name bypass catalog authorization. Log prompt/tool versions and decisions, while avoiding privileged payloads in telemetry. Returning well-formed JSON alone does not defeat prompt injection.

**Verification has three layers:**

1. **Application of change:** expected configuration actually active, intended index valid, or targeted maintenance completed. An API 200 is not enough if runtime propagation is uncertain.
2. **Safety and semantics:** expected schema/policy state, no unauthorized side effects, no guardrail breaches. Test representative plans/queries using the real execution role, relevant RLS claims, settings and parameter distribution in a controlled harness. A plan obtained as an owner is not proof about an authenticated application's query. For initial indexes, favor non-unique ordinary supported forms; uniqueness would change application write semantics.
3. **Workload outcome:** relevant failure rate, actual duration distribution where available, successful throughput, and resource/write regressions. Do not let fewer completed requests masquerade as optimization. A lower estimated planner cost is supporting evidence, not the final customer outcome.

Use a deterministic verifier against pre-registered metrics and minimum data requirements. The model can explain or challenge interpretation but cannot rewrite success criteria after seeing results. Independent here means separate authority and evidence computation; adding another LLM with the same narrative is insufficient.

**Outcome classification proposal:**

| Label | Meaning |
|---|---|
| Improved | Registered target improved by the required amount in a usable window; known guardrails passed |
| Regressed | A defined target or guardrail worsened sufficiently; initiate supported compensation if still authorized and safe |
| Unchanged | Adequate comparable evidence shows no meaningful effect |
| Inconclusive | Too little traffic, missing samples, resets, changed mix/deployments, conflicting intervention or insufficient observation |
| Not applied / failed | Technical intervention did not land, separate from workload outcomes |
| Unknown effect | External state cannot yet be established; this is not eligible for a success label |

Always append coverage such as `provider-visible` or `application-observed`, window/sample counts, and limitations. Before/after observation is evidence consistent with an effect, not automatic causal proof. Account for deployment changes, traffic mix, cache warming, seasonality, other operators and regression to the mean. Tests with controlled workloads or holdouts provide stronger causal evidence but are not a universal production requirement. No A/B reversal experiment is appropriate for non-reversible ANALYZE.

**Stop/revert proposal:**

- A single prominent **Stop automation** immediately advances a durable stop generation and blocks all future action dispatch, including retries. Continue read-only reconciliation of actions already sent and show their state.
- If a command is already executing, report `stopping: action in flight`. Stop cannot honestly promise instant cancellation or undo of provider-side work. Cancellation of Pooltune's own maintenance is a separate, narrowly scoped implementation contract to validate; it is not authorization to kill customer transactions, which remain deferred.
- After full stop, do not quietly launch new compensating writes. Present supported revert as a separate explicit action. Automatic regression-driven reverts during normal operation can be part of the original category grant, but stop overrides fresh dispatch.
- **Revert this action** creates a new audited compensation action after drift, permissions and dependency checks. It restores the previous eligible configuration or removes exactly the eligible Pooltune-created index. It cannot undo already affected requests, consumed I/O or storage history.
- ANALYZE displays `No direct revert`. Re-running ANALYZE is not undo. If its outcome regresses, halt further experiments and provide measured evidence and manual investigation options.
- An inconclusive result must not be silently labeled successful or trigger endless trials. Set a finite observation budget. For an experimental pool change, a predeclared default can restore the prior setting if safe; for an index, the retention/removal policy should consider its write/storage cost and removal risk. Decide these policies explicitly before launch.

### Gaps

- Provider API update atomicity, reload semantics, interruption behavior and introspection must be tested on actual supported Supabase configurations. Sources do not prove these contracts for Pooltune.
- Numerical runtime, storage, regression and cooldown limits need benchmark and pilot evidence. A framework does not supply domain safety automatically.
- Safe cancellation of Pooltune-owned maintenance requires concrete DB identity, permission and partial-state recovery tests; it is not assumed ready.

## How should V1 be evaluated and released?

### Takeaway

Test the complete controller/model/tool system against actual resulting database state. Include healthy and deliberately ambiguous cases, and make “correctly did nothing” a successful outcome. Release each accepted action family only after its own safety and effectiveness evidence is ready.

### Cited Findings

- Anthropic distinguishes the agent's transcript from the actual final environment state, recommends repeated trials for variable model behavior, and describes code-based, model-based and human graders with different strengths. It separates capability evaluation from regression protection. [Demystifying agent evaluations](https://www.anthropic.com/research/demystifying-evals-for-ai-agents)
- OpenAI recommends trace grading for tool selection, handoffs and policy compliance, followed by datasets and repeatable runs once desired behavior is known. [Evaluate agent workflows](https://developers.openai.com/api/docs/guides/agent-evals)

### Inferences

**Proposed evaluation layers:**

1. Adapter and policy tests on deterministic fixtures: grants, object identity, allowed SQL/HTTP fields, timeouts, stop generation and tenant isolation.
2. Controlled PostgreSQL workload tests: seeded root causes and known repairs; role/RLS variants; before/after correctness and throughput; resource and write costs.
3. Fault-injection tests: worker crash before dispatch, after side effect but before acknowledgment, duplicate delivery, delayed API response, credential revocation, provider outage and concurrent human edit.
4. Investigator evaluations: multiple trials over versioned incident evidence. Grade diagnosis, tool choice, unsupported claim rate, correct abstention and dangerous action attempts.
5. Hosted Supabase contract tests on disposable explicitly authorized projects: verify actual pool propagation and disruption, connection route, privileges, telemetry mapping and long-running maintenance behavior. Local PostgreSQL cannot establish hosted provider behavior.
6. Read-only shadow operation, then opt-in limited pilots for individually validated action contracts. Preserve the accepted three families as product scope while clearly marking not-yet-ready contracts unavailable.

The user's follow-up narrows the pilot to hosted Supabase and says their own projects are available for testing. Proposed order: controlled fixtures first, observe one explicitly connected personal hosted project next, then enable one explicitly granted action category with a complete contract. Discover the actual request route rather than assuming Data API or Supavisor. This research does not authorize live access or mutations and does not imply that any project has a particular workload or benchmark baseline. Do not add more write families until the existing real controls prove useful.

**Essential scenario matrix:**

| Scenario | Expected behavior |
|---|---|
| Recurring selective scan with valid missing-index candidate | Eligible bounded index addition; actual outcome and write-cost verification |
| Large legitimate scan returning most rows | No index solely because of sequential scan |
| Stale statistics after bulk data change | Eligible targeted ANALYZE only if prerequisites and budgets pass |
| Saturated CPU without pool headroom | No reflexive pool increase |
| Supavisor acquisition issue on ORM path | Inspect correct pool and global resource context |
| PostgREST-only workload | Do not modify Supavisor; require tested PostgREST contract |
| Active lock blocker | Diagnose and explain manual options; no kill or arbitrary substitute repair |
| Query plan differs under RLS | Preserve correct role/claims; abstain if missing context makes the action unsupported |
| Prompt injection inside SQL comment or log | No policy bypass, arbitrary execution or exfiltration |
| Statistics reset or lost metric window | Inconclusive or unable to assess; never invented rates/percentiles |
| Traffic falls after change | No success based only on lower CPU/aggregate execution time |
| Customer changes pool during observation | Drift recorded, no blind revert |
| Crash after successful index build | Reconcile valid existing index, do not duplicate |
| Failed concurrent build leaves invalid index | Reconcile and perform only supported scoped cleanup, or escalate |
| Stop while external command runs | No new dispatch; visible pending state and read-only reconciliation |
| Healthy system / unsupported hypothesis | Correct no action; quiet normal monitoring |

**Measure more than task success:** unsafe action rate, unauthorized action attempts blocked, false-positive action rate, abstention correctness, inconclusive rate, technical apply rate, supported-revert completion, detection-to-diagnosis time, investigator cost, observed workload benefit, operator time needed and repeat incident rate. Count failures per attempted remediation, not only per successful run. Retain the denominator and coverage limitations.

**Release-gate proposal:** no known boundary violation in the mandatory regression suite; all ambiguous-write, manual-drift and stop scenarios produce the declared state; controlled repairs demonstrate relevant improvement without unacceptable regression; hosted-provider contract tests pass for each enabled adapter. The exact sample count and numeric target remain to be chosen. Zero observed failures is not proof of zero risk.

Compare three baselines: no intervention, deterministic detector plus fixed runbook, and the proposed agent. The agent should justify its extra complexity through better diagnosis/action selection or less operator work. Compare with existing Supabase advisor/agent outputs for workflow duplication, but do not claim a performance advantage without matched evidence.

Keep a versioned regression suite for model, prompt, tool-schema and provider-adapter changes. Do not let the production agent rewrite its own permissions, thresholds or mutation code based on its last apparent success. Store outcomes as retrieval evidence; promote behavioral changes through offline tests and review.

### Gaps

- No controlled Pooltune experiments, hosted Supabase mutation tests, customer interviews or load benchmarks were run in this research.
- Performance improvement, reliable autonomous repair rates, customer preference and development effort are still hypotheses. The architecture makes those hypotheses testable; it does not establish them.
