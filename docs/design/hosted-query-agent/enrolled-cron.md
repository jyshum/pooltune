# Enrolled schedule qualification

This is repair family six. A background job can make an important query wait even when the query itself is well designed. Pooltune may move future executions into an explicitly allowed quiet period. It must preserve the job's work, frequency and deadline. Finishing the schedule change is not the same as proving it helped.

## Prespecified staging scope

Jared approved enabling Supabase Cron on Rookery on November 14. The `enable_pooltune_staging_cron` migration installed pg_cron 1.6.4. The readback found zero existing jobs. Do not remove the extension during cleanup or change any unrelated job. Create one uniquely named Pooltune test job, pin its ID/owner/database/command and record its original schedule durably.

Use two-minute test cycles in UTC. The original schedule is `*/2 * * * *`; the offered alternative is `1-59/2 * * * *`. Each cycle requires exactly one successful execution before the next even minute. The important query's busy period is the first 20 seconds of the even minute. The odd minute is the explicitly declared quiet window for this synthetic application. These are test-owner constraints, not guessed customer deadlines.

The private job models lock contention on the owned candidate table. Its pinned procedure may use short, bounded locks separated by transaction commits, with a total duration below 20 seconds. Start with 200 holds of 80 ms, each in its own transaction. The target query continues at two calls/second in measured affected/control periods; healthy reads and value-changing healthy-table writes continue at four and two/second. The short hold must stay below the existing request timeout. Cap the procedure's total lifetime and set lock timeouts; never lock application tables. Record the actual selected constants before running. If this fixture does not produce the required evidence, report that failure and revise the fixture explicitly instead of lowering the evidence threshold.

This is an intentionally induced lock-contention workload, not a claim about representative customer CPU jobs. Attribute blocking to the exact enrolled job backend and backend start, alongside pg_cron execution history. Coincident timing alone is insufficient.

## Evidence and acceptance

Observe three original scheduled executions and three matched job-free control periods, with at least 20 target calls in each period and unchanged offered demand. Each affected period must show at least 30% and 20 ms extra mean latency, with measured contention and successful required job execution. Preserve counter generations and role/query identity. Store selected measurements durably so the consumer can inspect them; no fabricated model evidence or manual recommendation injection.

After those periods, the shared investigator must make a real Claude call. Its typed proposal can choose the enrolled alternate window or no change. Go independently checks identity, current schedule, allowed window, required frequency/deadline, absence of an active/queued duplicate and current conditions. It changes only the schedule. Do not rewrite the command or cancel a running job as the repair.

Apply after the current cycle's odd-minute boundary has passed, with a declared guard margin, so the already-completed even-minute run is not duplicated by a new odd-minute run in that same cycle. Refuse a late or ambiguous handoff. Record the first eligible future execution and cycle boundaries before changing the schedule. Across three future executions, verify exactly one completed run per cycle by its deadline, matched foreground benefit of at least 20% and 5 ms above the noise gate, and no measured harm to the quiet-window healthy traffic. Missed/duplicate work or missing history cannot count as success.

## Durable delayed verification

The existing consumer bounds each claim to 25 seconds. Keep that bound. Waiting for several real scheduled executions belongs in durable state, not a long-running model call or a sleeping worker claim.

Add an explicit not-before time to the existing queue, defaulting to immediately eligible for existing repairs. After applying the schedule, atomically finish the initial claim with `awaiting_verification` and create one uniquely associated follow-up claim due after the three required execution windows. Preserve the original investigation/action link. The existing unique unfinished-query rule must still prevent duplicate follow-ups; queue transitions must occur under the existing project lock and verify claim ownership. A future claim must not prevent other due queries from running. Tests must cover competing consumers and restart before the due time.

The follow-up inspects the journal, pinned job and measured periods, verifies the result without another model decision or another schedule mutation, and records the final outcome. Pending-action recovery first inspects the current schedule. It must not blindly repeat `alter_job`, overwrite an independent setting, or label a pending outcome verified. Persisted intent and conditional queue transitions must survive a crash between target mutation and state acknowledgement. A confirmed regression can restore only the unchanged Pooltune-applied schedule, subject to the same no-duplicate/deadline checks; otherwise report the conflict for review.

## Measurements and limits

Report detection/evidence accumulation, queue delay, Claude time, schedule-action time and future-cycle verification separately. A several-minute outcome check is expected because the job must actually execute three times. Do not include fixture construction in incident-to-action latency or imply the entire cycle wait was model latency.

Extend the existing traffic driver and reporting helpers, including short-spike rejection, monitoring comparisons, errors, missed request slots, process recovery and owned-object cleanup. The cron scenario needs a longer wall-clock budget than the query-repair scenarios. Remove only the recorded test job and private objects after its execution is confirmed finished. Preserve unrelated jobs and extension setup.

The strongest simpler alternative is changing a schedule and immediately reporting success. That is faster to demonstrate but cannot establish that required work still happens or that contention moved safely. This design pays the real observation time once and keeps the service's individual work steps short.

References: [pg_cron](https://github.com/citusdata/pg_cron), [Supabase Cron](https://supabase.com/docs/guides/cron/quickstart).
