# Design progress

## Dedicated Rookery login, November 14, 2026

Task 12 authenticated to Rookery over the verified session pooler using a temporary dedicated login. It collected the enrolled plan estimate and index check in 221.179 ms in the final test. The statement counter stayed at one execution. Revoking the table grant stopped evidence collection and denied the next lookup on the open session. Disabling the login and clearing the password blocked a fresh connection. The existing session could still run a statement requiring no table access. The test removed the temporary role and schema and verified cleanup. [Results and trade-offs](dedicated-login.md)

## Restricted Supabase evidence, November 14, 2026

Task 11 tested one enrolled equality lookup on a disposable local PostgreSQL instance and on Rookery. A temporary restricted role collected the estimated plan and checked for a full B-tree index without running the lookup a second time. Wrong identities, stale statistics and revoked access yielded no evidence. Direct read and write probes verified the fixture permissions. Both the schema and role rolled back.

Rookery needed explicit grants to switch roles and use the statistics schema. The [evidence record](enrolled-evidence.md) captures the trade-offs and final single-run timings. This test used a bootstrap connection to set the role. Issuing and revoking a dedicated credential, verifying representative customer evidence and wiring the resulting adapter into staging remain open. The estimate type stays separate from the measured evidence accepted by LangGraph.

## Hosted staging restart, November 14, 2026

Jared authorized running the detector and consumer together in staging and verifying a hosted restart. Task 10 deployed both processes to a dedicated private Railway environment using a shared PostgreSQL instance and separate state and synthetic target databases. Railway was selected for this staging test. The [staging record](hosted-staging.md) explains the trade-offs and reproduction steps.

The hosted detector opened job 4 from bound synthetic traffic. The consumer was restarted with an unfinished claim. A replacement completed that same job on attempt two, 0.899 seconds after the original claim expired, with a saved recommendation and four checkpoints. One job was created. The first exercise exposed a PostgreSQL 18 row-count parsing bug. The regression test and hosted rerun verified the fix. The programs and fixture remain available in private staging. The traffic driver and temporary lock were removed.

The race-enabled Go integration suite, investigator tests, TypeScript checks and launcher syntax passed. The staging graph is scripted. This test covers hosted restart and durable state for synthetic queries.

## Continuous investigation consumer, November 13, 2026

Jared authorized the always-on consumer, a process restart test and a Rookery load comparison on the local revamp development branch. Task 9 adds the independent queue consumer and an explicitly enrolled synthetic fixture that survives process loss. The consumer checks Pooltune's database when idle and uses one short-lived target connection per investigation. Queue coordination allows one active job per project.

The restart test killed the actual consumer after its first claim and started a replacement. After lease expiry, attempt two saw the index added while the process was down and saved `no_change` on the same job. The [measurement and trade-off record](always-on-consumer.md) documents two rounds of six Rookery phases with 336 foreground reads each. Each active phase added one 9.857–11.557 ms measured scan. Unindexed client medians shifted by −0.209 to +0.852 ms across the pairs, with variable tail latency. Fresh checks verified fixture cleanup and the public index fingerprint.

Independent review found the claim relied on the state database’s default transaction isolation. A regression test reproduced a serialization failure. The producer and consumer now explicitly use read committed isolation. The race-enabled Go integration suite, Go vet, and investigator checks passed.

This version processes journaled synthetic lookups. Hosting supervision, a durable benchmark journal outside the automated test, and evidence access for real customer enrollments remain production work. The restart test interrupted evidence collection. A crash while a model child process is active can repeat a call after expiry.

## Queued investigation handoff, November 13, 2026

At Jared's request the local development branch was renamed from `feat/hosted-query-worker` to `feat/pooltune-revamp-dev` for broader revamp work. The accepted index milestone remains on `feat/pooltune-revamp`.

Task 8 connects the synthetic traffic job to the existing one-call LangGraph investigator. The [handoff record](worker-investigation.md) explains the queue, retry and process choices in plain language. On the final two live Rookery runs, Claude recommended the expected index and the saved result took 2.037–2.163 seconds from claim. The healthy lookup opened zero jobs. Local PostgreSQL readback confirmed the proposal and four graph checkpoints per run, and fresh Rookery connections confirmed fixture cleanup. The development Studio API replayed the same evidence successfully.

Review reproduced a race between job completion and the producer's cooldown check. The fix uses a shared project lock for both writes, while graph work runs outside the lock. Race-enabled Go integration, Go vet, investigator tests and type checking passed. Production work still needs a supervised consumer and an evidence adapter that can retry against durable enrolled inputs. The current synthetic fixture ends before the model call. Its recommendation has no execution path.

## Background worker intake, November 13, 2026

Jared authorized building the worker and testing with isolated synthetic data on Rookery. Tasks 6–7 were implemented on the local `feat/hosted-query-worker` branch. A dedicated traffic connection issued real reads while the Go worker collected only enrolled statement counters. Pooltune's own PostgreSQL keeps recent evidence and atomically queues one open investigation per query. We chose the existing state database for this first producer so the evidence and job can commit together. The production queue decision remains open.

The [traffic test record](worker-traffic.md) includes the trade-offs, reproduction steps, and three local plus three Rookery runs. All six produced one job for the slow case and zero for the healthy case. Rookery created its first saved job in 5.024–5.045 seconds with one-second test windows. Fresh sessions verified the job, fixture rollback, and matching public index fingerprints. The result measures initial evidence collection for an already-slow lookup. The driver did not invoke Claude or apply a repair.

Independent review caught early window closure when overlapping workers polled before schedule. The added regression test reproduced it, and the fixed worker checks elapsed time as well as the number of samples. Race-enabled integration tests, the investigator tests and TypeScript checks passed. An external supervisor must restart the worker after a poll error. The next step is an investigation consumer that gathers a plan and existing-index evidence for LangGraph.

## Index milestone accepted, November 13, 2026

Jared said, “Yeah, that looks good. I accept the index milestone. Let’s merge this into the Pooltune revamp branch locally, then start the background worker on its own branch. Give me a quick rundown before building.”

The local `feat/pooltune-revamp` branch fast-forwarded to `9f71543`, including the one-call default and the [held-out query evaluation](index-slice.md#default-after-testing-new-queries). Go tests, local PostgreSQL integration tests, and investigator tests and type checking passed. The index milestone covers the isolated repair path. Customer index execution has its own qualification requirements.

## Automatic index slice, November 13, 2026 (historical)

Jared accepted the Rookery milestone in this implementation chat: “Yeah, I said that's recreatus. Let's proceed to building actually implementing, actually detecting an index build, and then actually isolating it and fixing it and improving the outcome and making the repair automatic.” This authorizes automatic test repair in the isolated fixture. Design conflict mode remains **ask** and human review mode remains **milestones**. The index-slice review was pending at this checkpoint. No publication or customer rollout.

The [index slice record](index-slice.md) covers design alternatives, verification, reproduction and remaining work. The Rookery fixture run automatically built a real concurrent index through the Go worker, using the scripted LangGraph recommendation. It measured 12.190 ms before and 0.025 ms after, with a 3.89-second total. The plan switched from sequential to index scan, both measured error counts were zero, and the public index fingerprint and absence of fixture schemas were checked independently. The state journal completed and saved four graph checkpoints. The local Studio API registered the graph and replayed the saved evidence.

Independent review caught weak cleanup identity, unrepresentative no-op writes, recovery coverage gaps and a tool-call sequence that did not return tool evidence. The fixes record fixture identities and intent, validate cleanup under locks, use changing writes, compare each input, and return the chosen evidence to the model in a tool result. Fresh connection recovery, invalid/absent indexes, missing write evidence, overlapping trials and replacement object preservation are covered. The live Anthropic check passed on the separate local branch `feat/hosted-query-live`, based on `cda0d6c`. Claude Haiku 5.5 requested both evidence types and proposed the index. Go built and verified it. The lookup went from 16.209 ms to 0.026 ms in an 8.08 s trial. The journal completed with four graph checkpoints, and a separate catalog check found no leftover fixture or change to public indexes. Human acceptance came at the checkpoint above.

The working checkout is now `/Users/jshum/Desktop/code-folders/pooltune`. The older planning folder and its checkout are preserved in the local-only `legacy/local-archive/pooltuner-plan/`, including uncommitted changes. Its existing linked checkout was reconnected to the moved repository.

Repository layout on `feat/hosted-query-live`: the hosted lab, investigator, and current design documents are at the root. The original traffic-control app, its site/dashboard, old docs and packaging are under `legacy/`. Each Go module has a copy of the disposable PostgreSQL test helper so both test suites run from their own directories. The prior branches preserve their earlier layout. The [plain-English overview](overview.md) introduces the stack and tests.

## M1 implementation and review, November 13, 2026 (historical)

M1 evidence is available. Explicit human acceptance is pending. Design conflict mode remains **ask** (“Ask me first”). Human review mode remains **milestones** (“Pause at agreed milestones”). Both were selected by Jared on November 13. Authorized work includes the bounded synthetic fixture on Rookery and local feature-branch implementation. Automatic index changes require acceptance of M1. Tasks 4–5 remain gated. [Authority and scope](HANDOFF.md).

Branch: `feat/hosted-query-agent` in the existing checkout. Base: `c978614`. Task 1 evidence/detector: `d9f921e`. Task 2 fixture: `5633c8d`. Task 3 CLI/state: `3c6c5ba`. Review fixes: `69eea06`. The design-map source, approval records and HTML are byte-for-byte identical to the handoff commit. Schema validation passed. A fresh export matched after normalizing renderer-generated timestamps and the temporary directory name. The prior Explore/Back caveat remains open.

### Reproduce locally

Use Go 1.27.3 or newer and put the PostgreSQL 17 binaries on PATH. Run `go test -tags=integration ./cmd/pooltune-query-lab ./internal/hostedquery -count=1 -v` from the repo. The integration test starts disposable TLS-enabled PostgreSQL instances and runs real queries without Rookery credentials.

For the CLI, provision two disposable loopback databases. Enable `shared_preload_libraries=pg_stat_statements` on the fixture server and run `CREATE EXTENSION pg_stat_statements` in the fixture database. Supply `POOLTUNE_LAB_TARGET_DSN` and `POOLTUNE_LAB_STATE_DSN` privately as PostgreSQL URLs with the user, password, explicit port and `sslmode=disable` for loopback. Then run `go run ./cmd/pooltune-query-lab -target local`. A successful run prints its report ID and JSON. Read the same JSON from `pooltune_lab_state.runs` in the state database. For authorized Rookery use, choose `-target rookery` and supply the verified session-pooler URL with `sslmode=verify-full` and the downloaded Supabase CA path in `POOLTUNE_LAB_ROOKERY_CA_FILE`.

### Observations

The harness checked actual query results and sampled PostgreSQL execution counters. It sent 20 measured statements per case, in five batches of four. Each case had one warmup and one measured plan. Both tables held 100,000 synthetic rows and had primary keys. The queries returned identical IDs. The healthy case looked up the primary key. The candidate used a selective, unindexed lookup column. All case results were stored in a separate local PostgreSQL database and read back on a fresh connection. The local store is stopped and its data directory is retained at `/Users/jshum/.local/share/pooltune-toolchains/m1-state.TAikPcTP/db`.

| Target, state record | Healthy mean | Candidate mean | Decisions |
| --- | ---: | ---: | --- |
| Local PostgreSQL 17.11, 1 | 0.001827 ms | 1.828963 ms | `no_change` / `investigate` |
| Rookery PostgreSQL 17.6, 2 | 0.027148 ms | 11.920425 ms | `no_change` / `investigate` |

The Rookery run was at 00:23 PST, before the review fixes. A separate local run of the reviewed binary repeated both decisions and was read back as state record 4. Rookery activity visibility was confirmed by a read-only query. No additional Rookery fixture was needed. Healthy batch means were 0.024568, 0.025300, 0.040973, 0.022373 and 0.022528 ms. Candidate batch means were 13.153772, 13.075325, 11.880604, 10.985500 and 10.506924 ms. Healthy used an index-only scan with 1 row examined and 3 logical shared-block accesses per call. Candidate used a sequential scan with 100,000 rows examined and 1,334 logical shared-block accesses per call. Both plans returned 1 row. Rookery's fixture schema was absent after rollback. A separate MCP query matched all 12 public index definitions by count and fingerprint before/after (`f429d87200ade1a2faa649fa438e8cbe`). No candidate repair ran. Rookery is online.

These are database execution times under a synthetic fixture and a preset 1 ms lab objective. The five batches are short, and the threshold is specific to this demonstration. No production latency, write overhead, repaired outcome, customer onboarding, production scheduling or workload guard is qualified. The run used Rookery's existing admin login with hostname and certificate validation through the session pooler. No new project roles or settings were created. OAuth provisioning and a restricted native login remain unverified.

### Verification and choices

- Independent review found mixed-context aggregation, hidden-activity preflight, and an incomplete state write check. The bounded fix pass reproduced and covered each defect. State preflight now appends and rolls back a probe row before target work. The probe can consume an identity value. The reviewer also found gaps in route and rollback tests and a machine-speed assumption. Tests now use a valid test CA, force missing counters after fixture setup, and derive the expected integration decision from the actual batch means. Review used the saved Rookery report and local tests. Independent database checks of the live fixture and public indexes were performed by the implementer.
- `go test -count=1 ./...`, `go test -tags=integration ./cmd/pooltune-query-lab ./internal/hostedquery -count=1`, `go vet ./cmd/pooltune-query-lab ./internal/hostedquery`, and `git diff --check` passed. Local PostgreSQL integration covers fixture rollback, missing telemetry, schema collision, exact counter deltas, both decisions, and report readback after reconnect. Existing gateway and tuner integration checks passed with `go test -tags=integration ./internal/pgwire ./cmd/pooltune-tuner -count=1` after the PostgreSQL test helper change.
- PostgreSQL transaction rollback removes synthetic objects even if a run fails. A committed fixture or the Rookery app load script would support longer observations and app traffic but would require durable cleanup, more exposure to workload changes and a different measurement contract. The lab uses one target session with statement, lock, transaction and idle limits. It leaves shared PostgreSQL statistics intact, including accounting for synthetic statements.
- The M1 detector uses the objective path from the v0 proposal. The fixture fixes the input cohort and checks statement identity and counter epochs both within each batch and across the latest five batches. Remaining v0 triggers are outside this lab.
- Native verification initially failed with the system trust store. The CA served by Supabase's download bucket enabled verification with both psql and the Go harness. No credential was printed or checked in. Local state is disposable development storage on loopback. Production authentication and encrypted storage remain design work.
- Model access remains open for Task 4. There was no `ANTHROPIC_API_KEY` in the environment, and M1 needed no model calls. Before a live investigation, provision Pooltune's key via the operator's secret store and inject it into the investigator process. A customer key is not required.
- M1 needs human acceptance. No push, merge or publication is part of this checkpoint.

## Planning handoff state, November 13, 2026 (historical)

Current task: save/commit the complete planning and research record and start a new implementation chat, per Jared's request “no if that covers eveyrhting, save evryrhting, and start a new chat for implementation.” Start with HANDOFF.md. Product scope is settled; implementation.md still needs concrete tasks/interfaces before code work. This chat performs no product implementation.

Design conflict mode: **ask**, selected by Jared with “Ask me first” in the November 13 interactive reply. Preserve accepted design; ask before material changes.
Human review mode: **milestones**, selected with “Pause at agreed milestones”. The question specified reviewing the working test setup before enabling automatic database changes. M1 is the bounded isolated test setup, healthy comparison and real measurements; pause before automatic index repair. M1 has not been implemented or accepted.

The earlier attempted implementation was interrupted before any code, dependency installation or database modification. A managed worktree request was cancelled with an explicit do-not-retry result; no worktree exists. Honor that cancellation. Existing local checkout is main; product work belongs on an appropriate local feature branch without pushing/merging to main. Native execution is a practical proposal, not an already recorded user execution-method selection.

Rookery restoration and MCP read-only readiness checks are complete. Latest observed status ACTIVE_HEALTHY, PostgreSQL 17.6, execution role postgres. Native credentials, Pooltune OAuth setup and actual performance remain unverified. Project remains online; no timed pause was scheduled. Preflight: Go missing from PATH/common locations; Node available; Docker daemon unavailable; PostgreSQL installed but default local instance not responding; process has no Anthropic API key. Secret values were not printed or copied.

Research reports and notes are archived under docs/research/query-agent. Historical proposals do not override current product decisions. The following entries preserve the earlier sequence and may describe states since superseded by this section.

## Historical record

Completed: imported current product boundaries, Go revision history, explicit approval statements, six repair responsibilities and open design questions.
Validation: design-map schema validation passed and the shared Floorplan export succeeded. Headless Chromium 151.0.7922.34 loaded the file without page errors. Inspected desktop overview and expanded tools screenshots. Selection exposed the intended repair text; a forced Explore click reached the tools view. A 430px viewport had no document horizontal overflow. These checks do not establish complete accessibility or offline-network behavior.

Viewer finding: ordinary automated Explore clicks timed out because the button remained unstable, while a forced click succeeded. Initial screenshots also captured incomplete layout before rendering settled. The viewer is usable for inspection, but reliable unforced Explore/Back navigation remains unverified. No shared skill or renderer code was modified; do not claim this issue fixed. Codex file preview was queued; Chrome control via CUA timed out, so inspection used an isolated headless browser.

No product code or live database writes were performed in the earlier design-map and comparison work. Subsequent Rookery restore work is recorded below.

November 12, 2026 update: saved the five Cast AI / VoltAgent comparison lessons in the canonical product plan and mapped them to existing technical responsibilities in design.md. Added a handoff-readiness table separating customer-facing boundaries from routine builder choices. The six repairs, stack, deployment and evidence-reuse decisions remain unchanged. Graph components/decisions and approval records were not changed, so no new HTML export was required for this prose update.

Supabase setup research exists at /Users/jshum/Desktop/Tech Stuff/Resumes/softEng/reports/Pooltune automatic Supabase access.md. It supports an API provisioning probe, not a verified live setup. Jared offered to test on his project, but no specific project or credentials have been supplied. Comparison research is at /Users/jshum/Desktop/Tech Stuff/Resumes/softEng/reports/Pooltune automation comparisons.md. Relevant comparison source links and accepted lessons are preserved in the product plan so the implementation handoff does not depend on these external report paths.

Next discussion: customer connection, grants and representative query enrollment, followed by operation contracts and trial qualification. The product scope and chosen language stack are not reopened.

Rookery follow-up, November 12, 2026: Jared selected rookery and authorized temporary availability with “yes i give u full access, u can amke rookery available for a bit. we can use this”. Supabase restore_project returned success; the latest readiness check is COMING_UP, and a read-only connection probe reported `Database is possibly not accessible at the moment`. This is restore accepted, not yet database readiness verified. No fake traffic or schema/role changes have been made. Duration of temporary availability is unspecified; no timed pause has been scheduled or promised.

Evidence-boundary discussion: the initial redacted-diagnostics proposal was not accepted as written. Jared replied “lets honestly give it as much as it needs and more if it makes snese, can t we just encrypt it”. Record this as a preference for sufficient diagnostic capability, not approval of unrestricted table copying or an exact retention policy. Encryption in transit/at rest does not conceal evidence from an external model processing it. Revised recommendation: rich diagnostic metadata by default; authorize bounded additional inputs or samples when they resolve a specific evidence gap; run value-dependent measurements in the customer database and return summaries where sufficient. Exact owner controls, retention, redaction and model disclosure remain to be settled. Real values may also appear in plans/statistics, not just query parameters. Broader Rookery access does not settle future customers' data policy.

Subsequent accepted direction: Jared clarified “i want the agent to perform at full capacity” and “that dosnet mean no effort though, just a compromise”. Saved the capability-first compromise in the product plan: upfront disclosed customer grants, autonomous evidence access within them, relevant values when needed, encryption/isolation/retention controls, and evaluation of diagnostic and latency impacts. No repeated approval prompts for evidence already within configured grants. Exact implementation controls remain open, and six repair scopes remain unchanged.

Rookery readiness update: get_project now reports ACTIVE_HEALTHY, and a read-only SQL connection check succeeded: database postgres, execution role postgres, PostgreSQL 17.6. This verifies access through the current Supabase MCP connection; it does not verify Pooltune OAuth setup, a restricted native login or any repair. No load generation, schema changes or new roles have been performed. Project remains online; no timed pause is scheduled.

Scheduling selection, November 12, 2026: Jared delegated the choice with “whatveer allows for best performance and least amount of customer work”. Selected adaptive automatic execution inside upfront grants and per-operation budgets, with tested defaults and optional customer maintenance windows. Record as the assistant's selection under that preference, not human acceptance of exact numeric limits. Product plan records the alternative and trade-offs. Scope is unchanged; d-trials remains proposed because detailed execution limits are unfinished. Next design work is concrete tool contracts and evidence of benefit, rather than another customer scheduling question.

Detection/verification proposal, November 12, 2026: Jared requested concrete initial rules and success/regression cutoffs for all six repairs, with rationale, for later Rookery testing. Saved detection-verification-v0.md. Values are proposed engineering defaults, not provider recommendations or measured results. A fresh builder review identified four semantic gaps: different baselines for detection/benefit/protection; a fast safety path independent of minimum outcome samples; permitting the intended repair in pre/post context matching; and requiring observed guard coverage before declaring no regression. All four were incorporated. No Rookery workload, repair, schema or permission change was run for this proposal. Next work is user review of the default policy and bounded calibration design; existing implementation workflow settings remain unselected.
