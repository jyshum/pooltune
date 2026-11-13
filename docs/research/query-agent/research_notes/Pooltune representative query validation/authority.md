# Monitoring authority and representative request context

Researched November 12, 2026. No customer SQL, role grants, identity replay, configuration change or live capability probe was executed. Templates are research proposals. Supabase source is pinned to `16f89701ccd464d80204b7718a0914d04a67c666`; hosted deployment state remains to be verified.

## Does Supabase's read-only SQL role have sufficient monitoring visibility?

### Takeaway
The public source now establishes more than the previous report: modern Supabase migrations grant `pg_monitor` to `supabase_read_only_user`, giving inherited `pg_read_all_stats`. This is strong source evidence, but not proof every customer's hosted project has the migration or identical effective rights. Also, this built-in role is much broader than a metadata-only credential.

### Cited Findings
- Initial schema creates `supabase_read_only_user` with LOGIN and BYPASSRLS, and grants `pg_read_all_data`. — [Pinned initialization](https://github.com/supabase/postgres/blob/16f89701ccd464d80204b7718a0914d04a67c666/migrations/db/init-scripts/00000000000000-initial-schema.sql)
- Migration `20250710151649` sets that role's `default_transaction_read_only=on`. — [Pinned migration](https://github.com/supabase/postgres/blob/16f89701ccd464d80204b7718a0914d04a67c666/migrations/db/migrations/20250710151649_supabase_read_only_user_default_transaction_read_only.sql)
- Migration `20251001204436` grants `pg_monitor` to `supabase_read_only_user`; expected PostgreSQL 17 role tests confirm membership in both `pg_monitor` and `pg_read_all_data`. — [Grant migration](https://github.com/supabase/postgres/blob/16f89701ccd464d80204b7718a0914d04a67c666/migrations/db/migrations/20251001204436_predefined_role_grants.sql), [PG17 expected roles](https://github.com/supabase/postgres/blob/16f89701ccd464d80204b7718a0914d04a67c666/nix/tests/expected/z_17_roles.out)
- PostgreSQL `pg_monitor` includes `pg_read_all_stats`, `pg_read_all_settings`, and `pg_stat_scan_tables`. `pg_read_all_data` grants SELECT-like access and schema usage, independently of monitoring. `pg_read_all_stats` permits inspecting otherwise restricted statistical information. — [Predefined roles](https://www.postgresql.org/docs/17/predefined-roles.html)
- `pg_stat_statements` requires superuser or effective `pg_read_all_stats` privileges to see other users' query text and `queryid`. Less privileged users may still see numeric statistics. Query text can retain constants in some circumstances; role names alone are not a privacy guarantee. — [Query statistics](https://www.postgresql.org/docs/17/pgstatstatements.html)
- Management read-only API executes as `supabase_read_only_user`, is beta, and requires schema-qualified references. — [Endpoint reference](https://supabase.com/docs/reference/api/v1-read-only-query)

### Inferences
- The source-backed expectation is that this role can identify application query patterns, not merely see counters. Capability probes remain necessary for actual hosted versions, extension installation, role inheritance and visibility. Correct the old statement from "no source establishes membership" to "migration/test source establishes membership; live deployment unverified."
- Crucial onboarding disclosure: the read-only endpoint role can potentially read customer application records and bypass RLS. Pooltune may restrict itself to metrics/catalog queries, but that is an enforced application policy, not a least-privileged metadata-only token. Do not claim that its credential technically cannot read rows.
- A narrower native PostgreSQL inspection role could use `pg_read_all_stats` plus required catalog/config access, without blanket `pg_read_all_data` or BYPASSRLS. It still has access to query text, including potentially sensitive literals. This is a permission-design option, not a grant performed here or proof one role suffices for plans/replay.
- Keep inspection, request-context testing, and repair authority distinct. Table MAINTAIN privileges authorize certain maintenance, not arbitrary ownership-level index/function/settings DDL. Do not grant cancellation authority merely for monitoring; transaction termination is outside V1.

### Gaps
- Public source does not prove every hosted customer's current grants, inherited flags or Management API enforcement. The private control plane is not fully available for audit.
- Read-only transaction/default settings are not a comprehensive sandbox for arbitrary functions, external side effects, expensive reads or session state. Evidence tools must remain bounded and allowlisted.

## What read-only probes can distinguish missing visibility from missing workload?

### Takeaway
Probe identity, effective monitoring authority and extension schema first. Then inspect aggregate visibility for an enrolled application role and, where available, an owner-approved known query. Return booleans/counts, not query text or customer rows.

### Cited Findings
- PostgreSQL provides `pg_has_role`: MEMBER is membership, USAGE means privilege immediately available, SET checks permission to change role. `row_security_active` checks whether RLS applies in the current context. — [Privilege inspection functions](https://www.postgresql.org/docs/17/functions-info.html)
- Extensions and statement statistics have database-local installation and bounded tracked entries. Query identities may change with object identities and other execution context; query statistics may be reset or evicted. — [Query statistics](https://www.postgresql.org/docs/17/pgstatstatements.html)

### Inferences

Proposed probe A, plain SELECT statements suitable for read-only access; this is not executed:

```sql
SELECT current_user AS execution_role,
       session_user AS connection_role,
       pg_catalog.current_setting('server_version_num') AS server_version_num,
       pg_catalog.current_setting('transaction_read_only') AS transaction_read_only,
       pg_catalog.current_setting('default_transaction_read_only') AS default_read_only,
       pg_catalog.pg_has_role(current_user, 'pg_read_all_stats', 'USAGE') AS reads_all_stats,
       pg_catalog.pg_has_role(current_user, 'pg_monitor', 'USAGE') AS monitor_effective,
       pg_catalog.pg_has_role(current_user, 'pg_read_all_data', 'USAGE') AS reads_all_data,
       r.rolsuper AS is_superuser,
       r.rolbypassrls AS bypasses_rls
FROM pg_catalog.pg_roles AS r
WHERE r.rolname = current_user;

SELECT e.extname, e.extversion, n.nspname AS extension_schema
FROM pg_catalog.pg_extension AS e
JOIN pg_catalog.pg_namespace AS n ON n.oid = e.extnamespace
WHERE e.extname = 'pg_stat_statements';
```

Probe B is constructed only after discovering and safely quoting the extension schema. `extensions` below is an example, not a hard-coded assumption. Bind `$1` to an enrolled application role OID, and optionally `$2` to a known query ID from the same database/version context. Do not guess either.

```sql
SELECT count(*) AS entries,
       count(s.queryid) AS identified_entries,
       count(*) FILTER (
         WHERE s.query IS NOT NULL
           AND s.query <> '<insufficient privilege>'
       ) AS text_visible_entries,
       coalesce(sum(s.calls), 0) AS observed_calls
FROM extensions.pg_stat_statements AS s
WHERE s.dbid = (
  SELECT d.oid FROM pg_catalog.pg_database AS d
  WHERE d.datname = pg_catalog.current_database()
)
AND s.userid = $1::oid;

SELECT count(*) AS matching_entries,
       coalesce(sum(s.calls), 0) AS matching_calls
FROM extensions.pg_stat_statements AS s
WHERE s.dbid = (
  SELECT d.oid FROM pg_catalog.pg_database AS d
  WHERE d.datname = pg_catalog.current_database()
)
AND s.userid = $1::oid
AND s.queryid = $2::bigint;
```

Output only counts/role identifiers. The text-visibility predicate evaluates on the customer database without returning SQL. Treat `<insufficient privilege>` as an implementation marker to confirm alongside effective privileges and null query IDs, not the sole visibility test. Preserve PostgreSQL bigint identifiers as lossless values across Rust/TypeScript boundaries.

Validation matrix:

| Case | Expected interpretation |
| --- | --- |
| Extension missing | Query-statistics capability absent; no automatic installation in this probe. |
| SELECT denied | Permission failure, distinct from zero workload. |
| Numeric entries present, other-role IDs/text masked | Attribution insufficient despite counters. |
| Zero entries | Unknown: idle project, wrong role/database, reset, eviction, disabled tracking or no relevant workload. Not proof of denied access or healthy performance. |
| Own probe query visible only | Does not establish visibility of application-role queries. |
| Approved known app query executes through its actual path and matching calls increase | Positive end-to-end attribution evidence; generated test execution needs separate authorization. |
| Role/config changes or restart | Reprobe capabilities; do not keep stale authorization assumptions. |

If no known query ID exists, use owner-enrolled benign test interaction in an authorized test environment and correlate its normalized shape/calls through the tool. Do not fetch all user SQL just to prove visibility. Do not reset shared statement statistics.

### Gaps
- Templates were not executed or syntax-tested against a live PostgreSQL instance. The collector must probe extension version/columns and adapt safely.
- A known fingerprint can disappear through resets/eviction and must not be treated as globally stable. A count probe cannot demonstrate result equivalence, tenant correctness or query-level latency percentiles.

## How can tests reproduce application permissions without storing real user JWTs?

### Takeaway
Native PostgreSQL can run a transaction as an enrolled application role with transaction-local request claims. This reproduces parts of database authorization, not JWT authentication or the complete HTTP request path. It requires delegated role-switch authority that the monitoring credential does not automatically have.

### Cited Findings
- PostgREST authenticates JWTs and then uses `SET LOCAL ROLE` for request authorization; claims are signed at the HTTP boundary. — [PostgREST authentication](https://docs.postgrest.org/en/v14/references/auth.html)
- PostgREST applies request claims, headers, cookies, method/path, search path, impersonated-role settings, selected function settings and optional pre-request hooks within transactions. It also uses prepared statements when configured. — [Transaction context](https://docs.postgrest.org/en/v14/references/transactions.html)
- Native `SET ROLE` requires eligible SET membership and does not automatically apply the target role's ALTER ROLE settings. `SET LOCAL` lasts only through its transaction. — [SET ROLE](https://www.postgresql.org/docs/17/sql-set-role.html), [SET](https://www.postgresql.org/docs/17/sql-set.html)
- Table owners normally bypass RLS, and BYPASSRLS/superuser roles bypass it. Security-definer functions can execute with authority different from their caller. — [RLS](https://www.postgresql.org/docs/17/ddl-rowsecurity.html), [Functions](https://www.postgresql.org/docs/17/sql-createfunction.html)
- Prepared statements persist within a session and can choose custom or generic plans. — [PREPARE](https://www.postgresql.org/docs/17/sql-prepare.html)
- Server statement_timeout bounds statement runtime; lock_timeout separately bounds lock waiting. DISCARD ALL resets extensive session state but cannot run inside a transaction. — [Timeouts](https://www.postgresql.org/docs/17/runtime-config-client.html), [DISCARD](https://www.postgresql.org/docs/17/sql-discard.html)

### Inferences

Recommended replay authority: an explicitly enrolled test identity/tenant and approved role, with the minimum claims the relevant policy actually reads. Do not harvest or persist live user JWTs. Claims still may identify a test customer and need protection. Setting claims locally does not verify a signature and must never be treated as customer authentication.

Illustrative native-connection sequence for an already-authorized, side-effect-reviewed read experiment, issued through one pinned transaction handle:

```sql
BEGIN READ ONLY;
SET LOCAL statement_timeout = '2s';
SET LOCAL lock_timeout = '200ms';
SET LOCAL ROLE authenticated;
SELECT pg_catalog.set_config('request.jwt.claims', $1::text, true)
       IS NOT NULL AS claims_set;
-- Apply the enrolled request's relevant search_path/settings safely.
-- Confirm current_user and row_security_active for relevant tables.
-- Run only the approved representative read/plan with bound inputs.
ROLLBACK;
```

Timeout values are examples, not product decisions. The parameter is a generated, authorized test-claims JSON document, not an actual JWT. The boolean avoids echoing claims in the setup response. Role identifiers cannot be bound as ordinary SQL values: derive them from an allowlist and quote safely. Before replay, PG16+ can inspect `pg_has_role(session_user, enrolled_role, 'SET')`; version-gate the privilege vocabulary on older PG.

Scope/accuracy checks:

- Use the real application's effective role, RLS behavior, relevant claims and parameter types. Running as `supabase_read_only_user`, postgres, service_role or table owner can bypass RLS and produce false confidence.
- Match actual relation/RPC SQL and its surrounding PostgREST query where relevant. A raw function call does not necessarily reproduce outer filtering, sorting, limits, serialization, pre-request hooks or HTTP latency.
- Security-definer functions remain different even after switching caller role. Inspect the function and its dependencies; do not pretend SET ROLE forces every nested operation to use caller authority.
- Match plan-cache/prepared behavior and important settings when comparing plans. A literal query's custom plan can differ from the app's prepared generic plan.
- Read-only and rollback do not justify executing arbitrary unknown functions. Review side effects and cost; some experiments remain staging-only or ineligible.
- Never run request-context setup in one pooled connection and the query in another. Keep role/claims/query/rollback in one transaction. Direct or session-pool access simplifies controlled testing; transaction pooling is acceptable only with verified same-transaction behavior and compatible prepared usage.
- On success/error, ensure rollback completes and clear/verify state before reuse. If network loss makes cleanup uncertain, discard the client connection; do not hand it to another investigation. Partition pools by project/credential rather than a shared cross-customer credential.
- HTTP request cancellation is not proof the server query stopped. Server-side timeout and durable uncertain-state handling remain necessary. This governs Pooltune's own bounded test statements, not permission to terminate customer sessions.
- The Management read-only endpoint provides no documented multi-request session affinity or role-switch context contract. Do not build replay around consecutive HTTP calls assuming shared transaction state. Native PG is the clearer candidate pending tests.

Minimal test matrix: authenticated tenant A, authenticated tenant B, explicitly allowed anonymous behavior, missing-claim denial, differing customer sizes, security-definer functions, prepared custom/generic behavior, failure/timeout/rollback, connection reuse after failure, insufficient SET authority, and no test identity provided. Last case must produce "cannot validate this repair" rather than administrator-mode success.

### Gaps
- Supabase API role-switch rights, exact hosted request settings and all custom RLS claims are customer-specific. No authorization for replay or role provisioning has been granted by this research.
- Full authentication equivalence needs actual requests through the application/API using authorized test identities; SQL replay only evaluates the database layer. End-to-end verification remains a separate test lane.
- If a customer cannot supply safe representative identities/inputs or authorize sufficient context, automatic repair eligibility is narrower. This is an explicit evidence limit, not a reason to collect live credentials silently.
