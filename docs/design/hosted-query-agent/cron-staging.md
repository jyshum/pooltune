# Enrolled scheduling: staging result and remaining setup

## What happened

The first Railway/Rookery attempt stopped before activating its target job. It made **zero scheduling repairs and zero Claude calls**. Scheduling is implemented, but its actual repair and future-cycle qualification remain open. Source `73e07a0`, deployment `e82d97cc-31aa-4e1a-a977-7aa356f8d146`, fixture `b443166365c328e71e6333f7`. [Full failed attempt and recovery](evidence/mixed-traffic/initial-cron-prerequisite-failed.json)

Normal monitoring created no investigation. The short manual spike averaged **6.310 ms**, above the fixed **3 ms** objective, but also created no investigation. All **2,223 offered requests** completed without errors or missed slots. The attempt lasted **558.270 seconds**, including clock alignment, healthy comparisons, the spike and failed automatic cleanup attempts. This is test setup/failure time, not agent decision latency. There are no before/after scheduled-repair timings to report.

## Problems found

A follow-up check also reproduced an activation failure under `TZ=UTC`, matching Railway, while the local default timezone passed. The scheduler-generation comparison used `time.Time` values whose location representation changed after JSON persistence. Commit `7b88d99` compares actual instants while preserving exact process/start identity. UTC regression and race tests passed; final narrow review is recorded in progress. The original driver saved a generic command failure, so its report alone does not identify which activation guard refused first.

Independently, the native Rookery test role can read the Cron catalog and use job-management functions, but it cannot lock catalog rows with `SELECT ... FOR UPDATE`. Our activation guard needs that lock so a job cannot change between its checks and schedule update. The prerequisite check originally omitted this permission. The native empty row-lock probe independently returned “permission denied for table job”; both table-column authority checks were false. Commit `7ed06ac` adds the prerequisite refusal before any fixture/job creation. PostgreSQL requires UPDATE permission on at least one column for this kind of row lock. [PostgreSQL locking privileges](https://www.postgresql.org/docs/17/sql-select.html)

Automatic cleanup also hit a real API difference that the local catalog adapter missed. pg_cron accepts either a job name or a numeric job ID. An untyped argument selected the name overload: a read-only `PREPARE` probe reported `{text}`. Explicit `bigint` selected the intended ID operation. Commit `7ed06ac` binds that type explicitly and tests both overloads, including preservation of an unrelated numeric-looking job name. [Pinned pg_cron API definitions](https://github.com/citusdata/pg_cron/blob/v1.6.4/src/job_metadata.c)

## Recovery

The target remained inactive and had **zero execution-history entries**. Recovery compared both exact job identities and the original routine owners, definitions, signatures and private permissions. It removed only the two owned jobs using explicit numeric IDs, then used the existing fixture cleanup. Readback confirmed **zero Cron jobs, zero Pooltune fixture schemas**, and the same 12 public index definitions/fingerprint. The original `passed=false` and `cleanup_failed=true` fields remain in the evidence; controller recovery is recorded separately. The Cron extension stays enabled as requested. No permission was expanded during recovery.

## Historical grant option and provider-compatible path

First finish and review the UTC scheduler comparison, typed-ID cleanup and pre-creation permission guard. The guard should refuse this setup before creating a fixture or job when row-lock authority is absent.

A minimal temporary setup option is `GRANT UPDATE (jobid) ON cron.job TO postgres`, followed by `REVOKE UPDATE (jobid) ON cron.job FROM postgres` after the bounded test and cleanup. It enables the required lock, but it also grants column-write authority; it is not a lock-only permission. The test code would still act only on its exact owned jobs. Record the original column privilege before any grant, and remove only a grant added by this trial. No other job columns or server settings are proposed.

**No grant was requested or performed.** Further read-only inspection showed both available connections lack grant authority and cannot assume the catalog owner. The grant option is therefore unavailable through current access. A real inactive-job probe instead demonstrated write-conflict refusal through the supported API inside a consistent-snapshot transaction. See the [provider-compatible guard design](cron-provider-guard.md) for its evidence, required tests and trade-offs. It must preserve the same no-overwrite behavior; removing protection or broadening permissions is not an acceptable shortcut.

Once the provider-compatible guard is implemented, reviewed and verified, use the same required three original and three future cycles, real Claude decision, unchanged traffic/deadline gates, consumer restart and owned cleanup. One passing scheduling alteration alone is insufficient.
