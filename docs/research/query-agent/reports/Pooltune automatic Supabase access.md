# Connect Supabase automatically; keep Pooltune monitoring

**Yes, hosted Pooltune would remain on duty around the clock, independently of the customer's browser. Supabase also exposes an API route that could automate the one-time database setup.** These are separate mechanisms: setup creates access; our hosted workers use that access to monitor and investigate. The documented API supports SQL execution, but we have not tested the exact Pooltune role-and-grant setup on a live project. Several repairs also require broader database authority than their names suggest. This report recommends a validation path, not a newly approved design or a completed integration.

Research date: November 12, 2026. Official documentation and source review only. No customer credentials, live SQL execution or configuration changes were used.

## Automatic setup has a documented route

The Management API provides `POST /v1/projects/{ref}/database/query`, accepting SQL with the OAuth scope `database:write`. This gives us a plausible way to run a fixed setup template after “Connect Supabase.” However, its public contract does not identify the effective PostgreSQL execution role or guarantee that our intended `CREATE ROLE` and `GRANT` statements are permitted. Endpoint availability therefore establishes the route, not successful provisioning. ([SQL endpoint](https://supabase.com/docs/reference/api/v1-run-a-query), [Official OpenAPI](https://api.supabase.com/api/v1-json))

A separate `cli/login-role` endpoint returns a role, temporary password and `ttl_seconds`. It is a temporary CLI credential mechanism, not a configurable permanent Pooltune account. Its request does not let us choose the grants or lifetime. Supabase also explicitly says existing database passwords cannot be retrieved through Management API. ([Login-role endpoint](https://supabase.com/docs/reference/api/v1-create-login-role), [Integration guide](https://supabase.com/docs/guides/integrations/build-a-supabase-oauth-integration#pre-fill-database-connection-details))

The recommended experiment is OAuth authorization, a fixed versioned setup template, then a native database connection and permission checks. Only verified capabilities become active. Successful OAuth alone does not establish network reachability; customer network restrictions still apply. Guided SQL remains a fallback if automated setup lacks authority. ([Connections](https://supabase.com/docs/guides/database/connecting-to-postgres), [Network restrictions](https://supabase.com/docs/guides/platform/network-restrictions))

## Six repairs do not equal six narrow grants

Monitoring, representative-query testing and repair execution need separate permission decisions. A monitoring role can read statistics without blanket application-table access, although query text and settings can still reveal sensitive information. ([PostgreSQL monitoring roles](https://www.postgresql.org/docs/17/predefined-roles.html))

| Accepted repair | Required authority and practical limit |
|---|---|
| Add an index concurrently | Table-owner authority; `MAINTAIN` is insufficient. The command must run outside a transaction block. ([Ownership check](https://github.com/postgres/postgres/blob/REL_17_STABLE/src/backend/tcop/utility.c#L1474-L1480), [CREATE INDEX](https://www.postgresql.org/docs/17/sql-createindex.html)) |
| Refresh statistics | PostgreSQL 17 supports table-level `MAINTAIN`; PostgreSQL 16 uses ownership-based rules. ([PG17](https://www.postgresql.org/docs/17/sql-analyze.html), [PG16](https://www.postgresql.org/docs/16/sql-analyze.html)) |
| Create richer statistics | Creating extended statistics or changing column statistics targets requires table ownership. ([CREATE STATISTICS](https://www.postgresql.org/docs/17/sql-createstatistics.html), [ALTER TABLE](https://www.postgresql.org/docs/17/sql-altertable.html)) |
| Clean old row versions | Plain vacuum supports `MAINTAIN` in PG17; changing table autovacuum settings requires ownership. ([VACUUM](https://www.postgresql.org/docs/17/sql-vacuum.html), [ALTER TABLE](https://www.postgresql.org/docs/17/sql-altertable.html)) |
| Tune function memory | Changing a function's `work_mem` setting requires function ownership. ([ALTER FUNCTION](https://www.postgresql.org/docs/17/sql-alterfunction.html)) |
| Reschedule enrolled jobs | Upstream pg_cron restricts non-superusers to their own jobs. Function-execution permission alone does not authorize another owner's job. ([pg_cron source](https://github.com/citusdata/pg_cron/blob/main/src/job_metadata.c#L1247-L1410)) |

**Our Go action rules can restrict what Pooltune executes, but they do not shrink the underlying credential's powers.** Ownership includes broader alteration authority, and `MAINTAIN` permits more than vacuum and analyze. We must not silently transfer ownership or describe these as exact six-operation grants. Privileged SQL helper functions could narrow some actions, but ordinary functions cannot wrap concurrent index creation or vacuum because of transaction restrictions. ([Privileges](https://www.postgresql.org/docs/17/ddl-priv.html), [CREATE INDEX](https://www.postgresql.org/docs/17/sql-createindex.html), [VACUUM](https://www.postgresql.org/docs/17/sql-vacuum.html))

## Setup authority needs an explicit lifecycle

Keeping an OAuth authorization containing `database:write` preserves its broad API authority, even when daily work uses a narrower native login. Supabase documents reauthorization for changed app scopes; the reviewed contract does not establish silent removal of only the setup permission. Its revocation endpoint revokes the app authorization and corresponding tokens. ([OAuth scopes](https://supabase.com/docs/guides/integrations/build-a-supabase-oauth-integration/oauth-scopes), [Official OpenAPI](https://api.supabase.com/api/v1-json))

A separate bootstrap authorization followed by revocation and narrower authorization is worth investigating, but remains unverified. Alternatively, disclose retained authority or retain guided SQL. **Revoking OAuth must not be assumed to delete a separately created database login.** Disconnect needs to stop dispatch and address native grants, credentials, open connections and in-flight work separately. ([OAuth refresh/revocation](https://supabase.com/docs/guides/integrations/build-a-supabase-oauth-integration#refreshing-an-access-token), [Database roles](https://www.postgresql.org/docs/17/sql-alterrole.html))

## Hosted workers keep watch without continuous model calls

Under the agreed architecture, Go schedules bounded telemetry collection and detects meaningful changes. It starts the TypeScript/LangGraph investigator when evidence warrants investigation. The model does not continuously generate tokens, and customers can close their laptops. SQL setup installs access rather than a resident agent inside their database. Monitoring still depends on our service uptime, valid permissions and database reachability; telemetry reads consume a bounded amount of customer database work.

## Next validation boundary

Use one explicitly selected disposable Supabase project to check setup identity, PostgreSQL version, object ownership, native connectivity and the fixed grants. Test permitted actions and denied actions, including another owner's cron job and an unenrolled table. Finally, revoke OAuth and native access separately while a connection is open. That small probe should precede a promise of automatic setup for all six repairs. It requires no production SQL rollout or customer-data copy.
