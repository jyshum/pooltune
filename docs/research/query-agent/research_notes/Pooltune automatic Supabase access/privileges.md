# Pooltune automatic Supabase access: database privileges

Research date: November 12, 2026. Primary-source documentation and source-code review only. No customer project accessed, SQL executed, or permission contract live-tested. Scope: the accepted six repair families; Go backend/executor and TypeScript/LangGraph investigator remain unchanged.

## Can an automatic SQL bootstrap create narrowly scoped access for every repair?

### Takeaway

Automatic role creation and grants are feasible as a provisioning mechanism if the bootstrap identity has the necessary authority. However, a dedicated login plus a few grants does **not** express the exact six-action product policy in PostgreSQL. Several actions require object-owner authority. Others have broader-than-needed grants. Automatic onboarding and narrowly enforced repair permissions are separate claims.

### Cited findings

| Action | Database authority required | Important limit |
|---|---|---|
| Nonunique `CREATE INDEX CONCURRENTLY` | Table-owner authority. PostgreSQL 17's utility-command path invokes `RangeVarCallbackOwnsRelation`, which checks owner/superuser. | `MAINTAIN` is insufficient. Concurrent index creation must be outside a transaction block. [Command dispatch source](https://github.com/postgres/postgres/blob/REL_17_STABLE/src/backend/tcop/utility.c#L1474-L1480), [ownership callback](https://github.com/postgres/postgres/blob/REL_17_STABLE/src/backend/commands/tablecmds.c#L18519-L18560), [CREATE INDEX](https://www.postgresql.org/docs/17/sql-createindex.html). |
| Targeted `ANALYZE` | PostgreSQL 17: table `MAINTAIN`, or qualifying database-owner authority. PostgreSQL 16: table owner/superuser, or qualifying database owner. | Determine server version during onboarding; do not assume the PostgreSQL 17 grant exists on older projects. [PG17](https://www.postgresql.org/docs/17/sql-analyze.html), [PG16](https://www.postgresql.org/docs/16/sql-analyze.html). |
| Extended statistics / higher column statistics target | Table ownership for `CREATE STATISTICS`; table ownership for `ALTER TABLE ... ALTER COLUMN ... SET STATISTICS`. | `MAINTAIN` does not provide these authorizations. [CREATE STATISTICS](https://www.postgresql.org/docs/17/sql-createstatistics.html), [ALTER TABLE](https://www.postgresql.org/docs/17/sql-altertable.html). |
| Plain `VACUUM` / per-table autovacuum settings | PG17: table `MAINTAIN` for vacuum. PG16: owner/superuser or qualifying database owner. Autovacuum storage-parameter alterations require table ownership. | Vacuum cannot run inside a transaction block; tuning table settings is a separate owner-authorized operation. [PG17 VACUUM](https://www.postgresql.org/docs/17/sql-vacuum.html), [PG16 VACUUM](https://www.postgresql.org/docs/16/sql-vacuum.html), [ALTER TABLE](https://www.postgresql.org/docs/17/sql-altertable.html). |
| Function-scoped `work_mem` | Ownership of the function for `ALTER FUNCTION ... SET work_mem`. | Ownership is wider than permission to change one setting. [ALTER FUNCTION](https://www.postgresql.org/docs/17/sql-alterfunction.html). |
| Enrolled cron job schedule changes | Upstream `cron.alter_job` restricts a non-superuser to rows whose `username` is the current effective user. | Granting execute on the function does not by itself authorize another user's job. Changing job username requires superuser upstream. [pg_cron source](https://github.com/citusdata/pg_cron/blob/main/src/job_metadata.c#L1247-L1410). |

`MAINTAIN` permits VACUUM, ANALYZE, CLUSTER, REFRESH MATERIALIZED VIEW, REINDEX and LOCK TABLE. It is table-scoped but is not a grant for only plain vacuum and analyze. Likewise, PostgreSQL documents object-alteration/destruction authority as inherent in ownership, inheritable through the owning role. [Privileges](https://www.postgresql.org/docs/17/ddl-priv.html).

### Inferences

- Granting membership in a customer's broadly privileged owner role would enlarge the attack surface substantially. A product action allowlist in Go could restrict what Pooltune chooses to execute, but it would not reduce that credential's underlying database powers.
- Transferring customer objects to a new owner merely to enable Pooltune is not a neutral setup step. Do not adopt this silently.
- The honest product claim is: "Pooltune enforces an enrolled-object and approved-action policy." Claiming that the database credential itself is incapable of all other operations requires an additional, verified design.

### Gaps

The exact hosted Supabase project's major version, object owners, reserved-role grants, and extension version must be checked. The upstream source establishes normal PostgreSQL/pg_cron behavior, not successful execution on a particular hosted project.

## Can SECURITY DEFINER wrappers narrow privileged actions?

### Takeaway

They are a plausible boundary for some transactional actions, but not a universal fix for all six families.

### Cited findings

- `VACUUM` cannot execute inside a transaction block. `CREATE INDEX CONCURRENTLY` has the same restriction. [VACUUM](https://www.postgresql.org/docs/17/sql-vacuum.html), [CREATE INDEX](https://www.postgresql.org/docs/17/sql-createindex.html).
- A `SECURITY DEFINER` procedure cannot execute transaction-control statements. Changing a helper from a function to a privileged procedure does not provide a general transaction escape. [CREATE PROCEDURE](https://www.postgresql.org/docs/17/sql-createprocedure.html).
- `ALTER FUNCTION` requires function ownership, and a SECURITY DEFINER function runs as its owner. [ALTER FUNCTION](https://www.postgresql.org/docs/17/sql-alterfunction.html).

### Inferences

- A carefully written privileged helper can constrain statistics creation, selected setting changes and cron schedule alteration to enrolled objects and approved values. This is a proposed design, not a validated implementation.
- A normal function wrapper cannot execute concurrent index creation or vacuum. These need an appropriate execution path outside a function transaction, with the caller's required authority, or an independently designed privileged worker. Do not describe a generic "safe SQL RPC" as solving this.
- Helpers must use fixed object identity, safe identifier handling, a protected search path, restricted EXECUTE grants, and no caller-controlled arbitrary SQL. Their owner must actually have authority over each target object.

### Gaps

No helper definitions have been developed or reviewed. Concurrent-index creation remains the clearest blocker to an exact, narrow six-operation role implemented only through ordinary SQL grants and function wrappers.

## What can a monitoring role see?

### Takeaway

A dedicated monitoring identity can avoid blanket access to application rows, but monitoring itself can reveal query text and operational information.

### Cited findings

`pg_monitor` includes `pg_read_all_settings`, `pg_read_all_stats`, and `pg_stat_scan_tables`. These expose normally restricted statistics/settings and monitoring functions; they are not general grants of SELECT on application tables. The documentation warns that these roles expose privileged information. [Predefined roles](https://www.postgresql.org/docs/17/predefined-roles.html).

The earlier project report, `reports/Pooltune representative query validation.md`, documents why Supabase's existing `supabase_read_only_user` should not be treated as a metadata-only identity: its grants include broad data-reading authority and BYPASSRLS. Use that report's pinned Supabase source evidence when designing the bootstrap.

### Inferences

- Separate monitoring from repair execution and from representative-query evaluation. These identities serve different purposes.
- `pg_monitor` is a convenient standard baseline, not the absolute smallest possible observation grant. Pooltune can inspect narrower views/functions if the actual evidence requirements justify the extra setup work.
- Query text, plans, schema names and statistical values can contain sensitive information. "No replication of client database rows" does not mean "no customer information leaves their project."

## What is special about cron ownership?

### Cited findings

Upstream pg_cron executes a scheduled job with the permissions of its scheduling user and only lets users see their own jobs in `cron.job`. `alter_job` applies a current-user username filter for non-superusers; changing the username itself requires superuser. [README](https://github.com/citusdata/pg_cron#setting-up-pg_cron), [implementation](https://github.com/citusdata/pg_cron/blob/main/src/job_metadata.c#L1247-L1410).

### Inferences

- Enrolling a job needs both its ID and execution identity. A newly created Pooltune login cannot necessarily adjust jobs previously scheduled by `postgres` or another application owner.
- A schedule-only helper executing as the job owner could preserve execution identity. Recreating a customer's job under Pooltune's identity is not equivalent and should not be an automatic workaround.
- Test this on the deployed Supabase pg_cron version, including Supabase's limited `postgres` administrator role. Do not assume it behaves as a true upstream superuser.

## Does disconnecting OAuth remove database access?

### Takeaway

Treat OAuth authorization and native database credentials as separate lifecycles.

### Cited findings

PostgreSQL roles have separate LOGIN/NOLOGIN and password attributes, managed through `ALTER ROLE`. These are database objects, not OAuth tokens. [ALTER ROLE](https://www.postgresql.org/docs/17/sql-alterrole.html).

### Inferences

- If bootstrap creates a persistent native login, revoking Supabase OAuth cannot be assumed to delete that role, revoke its grants or rotate its password. Verify any provider-specific cleanup before making that promise.
- A Pooltune disconnect should first stop queued work and dispatch, then remove/revoke the access it installed where credentials still permit. Explicitly account for already-open connections and in-flight work; disabling new logins alone is not an adequate description of session cleanup.
- Customer-side emergency revoke instructions are needed if provider authorization is already lost before cleanup can run.

## Smallest next validation probe

Use one disposable hosted Supabase project with explicit authorization. Record `server_version_num`, effective setup role, table/function owners and `pg_cron` version. Attempt bootstrap and each action under the proposed monitoring/executor identities. Include negative tests: an unenrolled table, a forbidden operation despite MAINTAIN, a job owned by another role, and disconnect with a connection already open. This establishes actual permissions instead of inferring them from API endpoint existence.
