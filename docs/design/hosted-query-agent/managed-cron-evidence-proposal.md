# Managed scheduling evidence: approved database-observed contract

Status: the database-observed protection option was approved on November 15, 2026. Implementation and live qualification are in progress; generic scheduling is not yet enabled. The earlier alternatives below are retained as decision history.

## Approval history

Jared selected **“Use database checks and disclose the visibility limit”** after the question recorded in [progress, Task 64 approval](progress.md#task-64-approval-database-observed-scheduling-protection). The managed route now has a distinct evidence type for actual completed database work. Application offers, errors and missed requests remain unobserved. Existing native job identity, frequency, approved windows, deadlines, scheduler acknowledgements and recovery requirements stay in force. The prepared fixture contract remains unchanged.

Implemented components include the explicit managed period policy, native handoff branch that rechecks actual job history, and exact temporary witness creation/cleanup primitives. Their local tests do not establish that the full managed observer, durable lifecycle and Railway flow are complete.

## What is already supported

The owner can enroll an existing named job without entering query IDs. The encrypted selection binds database, owner, job ID, name, command digest, full catalog fingerprint and generation. It records the existing frequency and approved alternative minute, deadline and maximum runtime. A fresh catalog read rejects changed selections.

The new native Go reader checks that identity and reads actual job history from the last three cycles. It reports missing scheduler prerequisites and workload evidence. Completed job runs are observations, not proof that a repair improved application performance. No customer command is invoked and no job is created or modified.

On 2026-11-15, a read-only native Rookery preflight observed postgres, no direct UPDATE privilege on cron.job, EXECUTE on cron.alter_job, pg_cron 1.6.4, GMT, run logging enabled, background-worker mode off, scheduling database postgres, one visible launcher and zero owned jobs. The existing API guard therefore needs neither a new role nor expanded catalog privileges on this project.

The existing [provider guard](cron-provider-guard.md) uses an exact owned SELECT 1 job scheduled every two seconds. Two distinct later successful witness starts prove scheduler progress after commit. Temporary witness provisioning and guarded removal were part of the bounded earlier runs. A finite-lived witness under the same identity and cleanup requirements is consistent with that mechanism; a new permanent scheduler helper is not implied. No witness was provisioned by this task.

## Actual evidence gap

`ProjectCronPeriod` and `CronMeasure` require offered request counts, errors and missed slots, matched target/read/write demand in aligned busy/control/quiet windows, and live blocking attributed to the exact scheduler PID/run. The old observer obtained these from its controlled workload generator. Ordinary `pg_stat_statements` reports completed statements; it does not reveal requests that failed before reaching PostgreSQL, were never offered, or were missed by a client's scheduler. Setting offered equal to completed calls and errors/misses to zero would fabricate evidence.

The current investigator `cronCause` also fixes the original two-minute schedule, 120-second deadline, 40 target/write and 80 protected-read offers. The generic native action supports a wider approved periodic window. Copying fixture counts into generic evidence would bypass the intended protection instead of generalizing it.

## Available telemetry and alternatives

1. **Reuse provider telemetry when already available.** The application has optional, project-scoped Analytics Logs Read support through `LogsToken`. The ordinary automatic setup does not populate it and discards the installation token. The native database credential is not a Management API token. The existing log reader extracts measured plans, not offered-demand or complete request/error counts. Supabase logs can supplement database errors and service requests, but retention, source coverage and recording settings matter. API gateway requests do not cover an application using a direct PostgreSQL connection, and no matching log rows cannot establish zero errors. [Supabase log querying](https://supabase.com/docs/guides/observability/advanced-log-filtering), [Postgres logging](https://supabase.com/docs/guides/database/postgres/postgres-log-config). This option is useful evidence enrichment, not an automatic substitute for all current guards.

2. **Approve a database-observed protection contract.** Preserve actual job completion, deadlines, frequency, exact ownership, attributable blocking, counter generations, matched completed-work rates, live protected read/write observations, real scheduler acknowledgements and durable recovery. Explicitly report that application offers and application errors are unobserved. Design comparable windows and minimum completed demand without pretending fixture counts are real. This changes an existing protection guarantee and requires approval before execution code is enabled.

3. **Integrate application request telemetry.** Collect actual attempted/completed/error counts and query attribution from an application SDK or an existing trusted observability integration. This most closely preserves the old offered-demand contract, but needs additional customer integration and a defined authenticated ingestion protocol. It conflicts with claiming that a database credential alone supplies the full evidence.

Recommendation: propose option 2 explicitly for the managed database-only product, with strict refusals when completed-work comparisons or live attribution are insufficient. Use option 1 as optional supplementary evidence. If the original end-to-end request guarantees must remain unchanged, use option 3 and disclose the added integration. Until that decision, keep generic scheduling enrolled and observable, with mutations disabled. Do not retain the setup PAT, broaden scopes, create a persistent helper, or silently weaken acknowledgement as a shortcut.

## Required qualification after the decision

Implement operation-specific evidence separate from the fixture representation. Prove current-owner catalogue gates, job replacement and revocation refusals, exact temporary-witness cleanup, three original and three future cycles, protected demand, real Sonnet, scheduler acknowledgement and restart recovery through the normal managed setup. A successful schedule API call alone does not satisfy this milestone.
