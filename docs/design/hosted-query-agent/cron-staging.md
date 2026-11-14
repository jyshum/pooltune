# Enrolled scheduling: staging result and remaining setup

## What happened

The first Railway/Rookery attempt stopped before activating its target job. It made **zero scheduling repairs and zero Claude calls**. Scheduling is implemented, but its actual repair and future-cycle qualification remain open. Source `73e07a0`, deployment `e82d97cc-31aa-4e1a-a977-7aa356f8d146`, fixture `b443166365c328e71e6333f7`. [Full failed attempt and recovery](evidence/mixed-traffic/initial-cron-prerequisite-failed.json)

Normal monitoring created no investigation. The short manual spike averaged **6.310 ms**, above the fixed **3 ms** objective, but also created no investigation. All **2,223 offered requests** completed without errors or missed slots. The attempt lasted **558.270 seconds**, including clock alignment, healthy comparisons, the spike and failed automatic cleanup attempts. This is test setup/failure time, not agent decision latency. There are no before/after scheduled-repair timings to report.

## Provider-guard follow-up: activation passed, measurement timing refused

Source `fc3d781`, deployment `4d001521-03b6-409d-8d80-652b1cfdc141`, fixture `d8effa98576ae11f4abf0117`. Supported-API activation succeeded without a privilege grant. The first real scheduled target run succeeded in **16.458 seconds**. The short spike averaged **3.137 ms** against a fixed **3 ms** objective and created no investigation.

The sustained workload created job **101** after **20.932 seconds**. The consumer saved `awaiting_evidence` and a delayed child **102**. It made **zero Claude calls and zero schedule repairs**, because three actual periods were not yet available. The report's 4.196-second `repair_seconds` field is the time to that intermediate waiting result, not a verified repair.

Recording the first period failed with the generic `fixture command exited 1`. The saved phase timestamps independently show an invalid measurement: the control window began **1.026 seconds** after its boundary and the quiet window **1.015 seconds** after its boundary, exceeding the prespecified one-second allowance. The driver discarded command stderr, so the report does not establish which internal check failed first. The bounded timing fix must remove repeated network/setup delays from window alignment without relaxing the gate. Failure diagnostics and complete period evidence also need preservation.

The delayed child remains pending in Pooltune state after the private fixture consumer stops. Existing fixture cleanup removes database jobs/objects and marks its journal recovered; it does not terminalize investigation rows. This is a lifecycle limitation to address before general customer operation, not a running target job or a completed investigation.

All **3,051 requests** completed with zero errors or missed slots. Automatic cleanup verified zero owned jobs/fixtures and unchanged unrelated jobs/public indexes; an independent read confirmed empty jobs and fixtures. Total attempt time was **523.725 seconds**, including setup, clock alignment, one cycle and cleanup. This is failed qualification, not an agent-speed result. [Preserved follow-up evidence](evidence/mixed-traffic/cron-window-alignment-failed.json).

## Aligned-driver run: brief fault was too small

Source `a350470`, deployment `960be266-a46c-470b-afd0-a89b1a672fd0`, fixture `1227b325242d8621f125799d`. The manual spike window started **0.293 seconds** after its boundary and ended at **20.443 seconds**, within the unchanged timing limits. The manual 30-hold workload completed in **2.508 seconds**, but its 40 target requests averaged only **2.196 ms**, below the fixed **3 ms** objective. The harness therefore stopped before target activation, Claude or any schedule repair. This trial does not qualify spike rejection or the missing full scheduler flow.

All **2,393 offered requests** completed with zero errors/misses. Automatic cleanup verified the owned jobs and fixture absent and unrelated jobs/public indexes preserved. Total attempt time was **400.352 seconds**. The earlier 30-hold spike was only marginally above the objective at 3.137 ms. The next bounded calibration must strengthen the manual fault without lowering the objective, changing scheduled 200-hold work, or removing the brief-spike rejection test. [Preserved failed calibration](evidence/mixed-traffic/cron-spike-calibration-failed.json).

## Complete original periods: consumer checked before the final save

Source `3e199eb`, deployment `6286b26f-cc9d-4cb5-a717-d9be4d63d4f6`, fixture `a6bb5fa743a67d4334127334`. The 60-hold brief spike averaged **9.825 ms**, crossed the fixed **3 ms** objective and created no investigation. All three scheduled original executions succeeded. Their measured busy/control query means were **38.097/0.076 ms**, **32.586/0.075 ms**, and **33.650/0.073 ms**, with real attributed blocking and all measurement windows within bounds.

Job **103** detected the repeated workload after **19.830 seconds** and deferred evidence collection. Its child **105** checked at the earliest scheduled time, loaded only two saved periods and finalized `inconclusive` at **21:59:24.290 UTC**. The third period finished saving at **21:59:24.300717 UTC**, just afterward. The report and an independent read of persisted recording times establish this readiness race. There were **zero Claude calls and zero schedule repairs**. The report's 304.460-second `repair_seconds` is a failed evidence-wait outcome, not successful repair time.

The fix must treat the timer as the earliest check, then defer incomplete evidence using the existing durable queue within the unchanged model-admission deadline. It must still require all three valid periods and refuse expired or invalid evidence. A larger blind delay is not evidence of readiness.

Traffic reported zero errors/missed slots, and owned cleanup plus unrelated-job/public-index preservation passed. Total run time was **834.454 seconds**. [Preserved result and recording times](evidence/mixed-traffic/cron-evidence-readiness-failed.json).

## Full evidence reached the investigator; structured output was rejected

Source `4c74d84`, deployment `7be552db-f3f2-4d92-b369-af39ac7e052c`, fixture `01bbe3c5a621ada9ecb0a112`. The brief spike averaged **10.865 ms** and was ignored. Three original scheduled periods and their measurements completed. Busy/control means were **37.129/0.151 ms**, **38.697/0.082 ms**, and **31.884/0.086 ms**, with attributed blocking. The ready-evidence consumer reached the real investigator on thread `worker-108-1`.

The investigator failed before saving a valid recommendation. Three graph checkpoints and its saved error show a Zod schema error: `Invalid input: expected object, received null`. Installed LangChain returns `parsed:null` when structured-output parsing fails; the wrapper then parsed that null and lost the useful cause. No scheduling mutation occurred. The driver's bounded acknowledgement wait correctly expired. Automatic owned cleanup and preservation checks passed; traffic had no errors or missed slots. Total attempt time was **848.236 seconds**. Its 4.530-second result field describes the first awaiting-evidence result, not a repair. [Full failed run](evidence/mixed-traffic/cron-model-output-failed.json).

One separate real Claude replay used the exact saved evidence, prompt, 600-token limit and 8.5-second request timeout, with no database action. It selected `propose` / `cron` in **3.823 seconds**, but its **1,205-character explanation** exceeded the existing **1,000-character schema limit**. Input/output token counts were **13,045/600**. This demonstrates a reproducible response-format defect on actual evidence; the original raw provider response was not retained, so its precise parser failure cannot be reconstructed. [Allowlisted replay metadata](evidence/mixed-traffic/cron-model-output-replay.json).

The correction should request a short explanation and preserve safe schema-error categories. It must not accept invalid output, enlarge the reason limit, alter permissions or increase model/time budgets. A successful replay still does not qualify actual schedule application and three future cycles.

## Short explanation fixed formatting; decision still needs qualification

Commit `8e731d6` asks for a short explanation while retaining the 1,000-character schema limit and the same model/time budgets. It also records safe parser-error categories without accepting invalid raw output. Independent source review passed; 63 TypeScript tests and typecheck passed.

A second separate saved-evidence replay returned valid `no_change` in **1.863 seconds**, using **13,078 input / 250 output tokens**. Its reason incorrectly claimed the owner-approved alternate differed from the offered alternate, although both are `1-59/2 * * * *`. This is a decision failure for the intended positive case, not a successful repair. No database action occurred. [Preserved second replay](evidence/mixed-traffic/cron-short-reason-replay-declined.json).

The next step is to inspect how the existing enrollment permission is represented to the model. Keep refusal for weak or unauthorized evidence and all independent Go checks. Repeated unchanged calls until a proposal appears would not establish reliability. Scheduling remains unqualified.

## Problems found

A follow-up check also reproduced an activation failure under `TZ=UTC`, matching Railway, while the local default timezone passed. The scheduler-generation comparison used `time.Time` values whose location representation changed after JSON persistence. Commit `7b88d99` compares actual instants while preserving exact process/start identity. UTC regression and race tests passed; final narrow review is recorded in progress. The original driver saved a generic command failure, so its report alone does not identify which activation guard refused first.

Independently, the native Rookery test role can read the Cron catalog and use job-management functions, but it cannot lock catalog rows with `SELECT ... FOR UPDATE`. Our activation guard needs that lock so a job cannot change between its checks and schedule update. The prerequisite check originally omitted this permission. The native empty row-lock probe independently returned “permission denied for table job”; both table-column authority checks were false. Commit `7ed06ac` adds the prerequisite refusal before any fixture/job creation. PostgreSQL requires UPDATE permission on at least one column for this kind of row lock. [PostgreSQL locking privileges](https://www.postgresql.org/docs/17/sql-select.html)

Automatic cleanup also hit a real API difference that the local catalog adapter missed. pg_cron accepts either a job name or a numeric job ID. An untyped argument selected the name overload: a read-only `PREPARE` probe reported `{text}`. Explicit `bigint` selected the intended ID operation. Commit `7ed06ac` binds that type explicitly and tests both overloads, including preservation of an unrelated numeric-looking job name. [Pinned pg_cron API definitions](https://github.com/citusdata/pg_cron/blob/v1.6.4/src/job_metadata.c)

## Recovery

The target remained inactive and had **zero execution-history entries**. Recovery compared both exact job identities and the original routine owners, definitions, signatures and private permissions. It removed only the two owned jobs using explicit numeric IDs, then used the existing fixture cleanup. Readback confirmed **zero Cron jobs, zero Pooltune fixture schemas**, and the same 12 public index definitions/fingerprint. The original `passed=false` and `cleanup_failed=true` fields remain in the evidence; controller recovery is recorded separately. The Cron extension stays enabled as requested. No permission was expanded during recovery.

## Historical grant option and provider-compatible path

The UTC comparison, typed-ID cleanup and pre-creation permission checks were implemented and reviewed. The temporary direct-row-lock guard refused the original setup before creating jobs. It was then replaced by the supported API guard below.

A minimal temporary setup option is `GRANT UPDATE (jobid) ON cron.job TO postgres`, followed by `REVOKE UPDATE (jobid) ON cron.job FROM postgres` after the bounded test and cleanup. It enables the required lock, but it also grants column-write authority; it is not a lock-only permission. The test code would still act only on its exact owned jobs. Record the original column privilege before any grant, and remove only a grant added by this trial. No other job columns or server settings are proposed.

**No grant was requested or performed.** Further read-only inspection showed both available connections lack grant authority and cannot assume the catalog owner. The grant option is therefore unavailable through current access. A real inactive-job probe instead demonstrated write-conflict refusal through the supported API inside a consistent-snapshot transaction. See the [provider-compatible guard design](cron-provider-guard.md) for its evidence, required tests and trade-offs. It must preserve the same no-overwrite behavior; removing protection or broadening permissions is not an acceptable shortcut.

The provider-compatible guard is implemented in `fc3d781` and passed independent scoped review. Both the target and active diagnostic witness receive supported API writes inside one Repeatable Read transaction, after saved intent. Concurrent changes abort instead of overwriting the other change. Restricted-role tests passed without direct catalog UPDATE; the complete Go integration suite passed under UTC. No extra Rookery permission was granted.

Deployment `4d001521-03b6-409d-8d80-652b1cfdc141` reached SUCCESS; its failed timing follow-up is recorded above. The scheduling repair must still pass three original and three future cycles, real Claude, unchanged traffic/deadline gates, consumer restart and owned cleanup. One passing scheduling alteration alone is insufficient.
