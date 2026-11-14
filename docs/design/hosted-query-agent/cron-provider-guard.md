# Scheduling guard using the supported Supabase API

## Problem and observed alternative

The original guard locks `cron.job` rows directly. Rookery's native and connected-tool roles are both `postgres`; neither is superuser, can assume `supabase_admin`, or has the grant option for the catalog's job-ID column. The catalog belongs to `supabase_admin`. A proposed temporary grant is therefore unavailable through our current access and was never attempted. No permission change is needed for the alternative below.

A bounded live probe created one uniquely named **inactive** `SELECT 1` job. Connection A began REPEATABLE READ and read its exact identity/schedule. Connection B committed an independent schedule edit through `cron.alter_job`. A's subsequent `cron.alter_job` failed with **SQLSTATE 40001**, “could not serialize access due to concurrent update.” B's value survived, the job stayed inactive, and it ran zero times. Guarded typed-ID cleanup removed it; no jobs remain. [Probe evidence](evidence/mixed-traffic/cron-api-concurrency-probe.json)

PostgreSQL Repeatable Read refuses an update to a row changed since the transaction's snapshot. The real pg_cron API's SQL update follows this rule. This is a target-row write-conflict guard, not a claim that every cross-object business rule is serializable. [PostgreSQL transaction isolation](https://www.postgresql.org/docs/17/transaction-iso.html) · [Pinned pg_cron AlterJob implementation](https://github.com/citusdata/pg_cron/blob/v1.6.4/src/job_metadata.c)

## Within-scope implementation choice

Use a REPEATABLE READ target transaction for the two existing mutations: activating the enrolled inactive target and changing its future schedule. Read and validate the exact target identity/current state within that transaction before calling the supported job API. The API update obtains its own write lock; a concurrent target metadata edit must abort rather than be overwritten. Remove only the now-unneeded explicit `SELECT FOR UPDATE` catalog reads. Keep the project/trial locks, model/authority checks, real clock and scheduler witnesses, deadlines, complete work, immutable routine checks and all existing recovery behavior.

Replace the direct-catalog-update privilege prerequisite with actual catalog read and exact job-API execution capability checks. There is no direct UPDATE grant, new helper on Rookery, new customer operation, changed schedule or weakened workload requirement. The earlier row-lock implementation and failed attempt remain in history.

This is a routine internal guard implementation choice under the existing delegated fixture work, preserving the required no-overwrite behavior. It is not human approval of new customer permissions. A fresh builder check and regression tests must verify the bounded interpretation before implementation is credited. If the same protection cannot be established, retain the refusal and report the gap.

## Required checks

- A login with catalog SELECT plus the supported job API can activate/change its owned job without direct catalog UPDATE.
- A missing catalog-read or required API privilege refuses before fixture/job creation.
- A second connection changes the target after its identity check but before the API write. The operation must abort with 40001 and preserve that edit, including changed command/identity fields rather than just a different schedule.
- Aborted activation does not claim it activated. An aborted repair after saved intent stays inspectable, never blindly retries or overwrites the independent change. Lost acknowledgement continues to use the existing pending-state rules.
- Keep UTC generation equality tests, typed-ID cleanup, early/idle-error cleanup/reporting tests and three-cycle verification unchanged.
- The local adapter uses real PostgreSQL transactions and a narrowly granted API-shaped updater; label it honestly. The live probe above checks the actual Supabase/pg_cron update behavior, while the full Claude/traffic/six-cycle qualification remains mandatory.

## Trade-off

A direct row lock waits before the change and needs catalog write permission. This approach reads a consistent snapshot and lets PostgreSQL reject a conflicting update. It uses existing provider access, but a conflict can leave a saved intent requiring inspection. Pooltune must treat that as uncertainty and preserve newer state. It does not automatically retry a failed repair transaction merely because general database guidance permits transaction retries. Production coordination across unrelated owner changes and arbitrary routines remains unqualified.
