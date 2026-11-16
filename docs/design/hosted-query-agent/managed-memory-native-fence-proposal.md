# Function owner-edit fencing: approved practical approach

Status: **Practical native fence approved; implementation and qualification in progress.**

Jared answered the specific fence choice with “whatever is more simple and rpactical, like over engineering isnt good”. This authorizes the recommended native rename fence: preserve exact object identity and every changed body, setting, owner and grant; permit an identical-content save during lock acquisition. Exact installed-generation checks remain mandatory for renewal and recovery. No catalog UPDATE grants are authorized. The original proposal below is retained as decision history.

This supplements the approved [automatic memory fallback design](function-memory-managed-proposal.md). It does not change that approval. Ask for design conflicts and final-only review remain active.

## Problem and evidence

PostgreSQL function replacement writes its catalog tuple without respecting Pooltune's advisory lock or a same-owner `ALTER FUNCTION ... OWNER TO CURRENT_USER` object lock. A concurrent owner replacement can therefore occur between validation and Pooltune's rewrite, including restoration. Repeating a catalog read in a repeatable-read transaction does not detect that new generation.

A real PostgreSQL 17 regression reproduced owner `CREATE OR REPLACE FUNCTION` succeeding inside the permission callback. Locking the exact `pg_proc` row with `SELECT ... FOR UPDATE` prevents this, but requires catalog UPDATE privileges. Rookery's native postgres role has SELECT and lacks UPDATE on that catalog. No catalog grants are requested or added. The fail-closed row-lock prototype is not a deployable Rookery solution.

Same-value `ALTER FUNCTION ... COST` is also insufficient: a concurrent owner cost change can be overwritten by the lock-taking statement before the subsequent content check.

## Proposed native fence

Inside one short owner transaction, rename the exact selected zero-argument function to a unique temporary name and immediately rename it back. PostgreSQL holds its catalog tuple lock until that transaction ends. Compare the exact object identity and complete catalog contents after the lock, including body, owner, settings, grants and cost. If any content changed from the saved baseline, roll back without applying the repair.

For measurement, hold that owner transaction separately while the existing application-role read-only transaction calls the original committed function. Roll back the owner transaction afterward. Application-visible name and definition remain unchanged. For apply and restore, keep the native lock through the operation, and record the actual final committed generation as before.

This does not authorize arbitrary functions, additional catalog privileges, new application errors, or replacing any changed owner content.

## Exact decision needed

The rename itself creates our own catalog generation (`xmin`). Consequently, a semantically identical owner save between the initial read and lock acquisition is indistinguishable after acquisition from no owner save. Every changed catalog value and a replacement object remain distinguishable and must be preserved.

The proposal changes the lock-acquisition rule from **the original generation must remain untouched** to **the exact object and all its content must remain untouched**. After our actual repair commits, its new exact generation still gates allowance renewal and recovery. A subsequent identical owner save still disables renewal and prevents automatic restoration under the original recorded generation.

The alternative is to retain the strict generation rule and leave managed function rewriting unavailable on native roles that cannot lock `pg_proc`. Granting catalog UPDATE is not an alternative proposed here.

## Disposable proof

`TestManagedMemoryRenameFenceProbe` passed with PostgreSQL 17 and Go's race detector in 2.118 seconds. It used a non-superuser function owner with no catalog UPDATE privilege and proved:

- Ordinary read-only calls under the application role still returned the original result while the rename transaction was open.
- Concurrent body replacement, cost changes and execution-grant revocation were blocked by the native tuple lock.
- Owner body, cost and grant changes committed before the fence remained visible in the full catalog row and survived fence rollback.
- Rollback preserved the original name, definition and original catalog generation.
- An identical-content owner replacement can create a new catalog generation without changing the canonical row, demonstrating the stated limitation.

No Rookery function was modified for this probe. Final verified-result publication must also revalidate the exact installed function after the protected traffic window; a sampled result alone cannot establish that an owner has not changed it afterward.
