# Make query repairs testable through enrollment

**Resolve the missing-context problem with explicit query enrollment: connect Supabase for broad monitoring, then register a small set of representative requests and an approved test identity for important queries.** This lets Pooltune test the relevant operation without collecting all application traffic or copying customer tables. The owner supplies the meaning and permission once; the hosted agent can reuse qualified cases while their schema, context and grants remain current. Two execution lanes support this: real Supabase HTTP requests, or exact enrolled SQL in a controlled connection. A source review also confirms that Supabase's built-in read-only role receives monitoring privileges, while revealing that it can read application data and bypass row-level security. These findings close the architectural gaps; a real-project probe is still required before claiming working integration.

Research dated November 12, 2026. No live project, credential, grant, query execution, configuration change or index build was used. This proposal preserves Rust collection/execution, one TypeScript/LangGraph investigator, Pooltune-owned PostgreSQL, and exactly the six accepted repairs.

## Monitoring authority is documented, but differs from test authority

The previous report left the built-in role's monitoring membership unverified. Pinned Supabase migrations now establish that **`supabase_read_only_user` receives `pg_monitor`**, and role tests confirm that grant. PostgreSQL's `pg_monitor` includes `pg_read_all_stats`, which permits visibility into other roles' statement identifiers and text. This is source-backed expected behavior, not proof every hosted project has the same migration state. ([Grant migration](https://github.com/supabase/postgres/blob/16f89701ccd464d80204b7718a0914d04a67c666/migrations/db/migrations/20251001204436_predefined_role_grants.sql), [Expected role tests](https://github.com/supabase/postgres/blob/16f89701ccd464d80204b7718a0914d04a67c666/nix/tests/expected/z_17_roles.out), [PostgreSQL predefined roles](https://www.postgresql.org/docs/17/predefined-roles.html))

However, initialization also gives this role `pg_read_all_data` and `BYPASSRLS`; another migration sets default transactions read-only. **“Read-only” does not mean “metadata-only.”** Pooltune can enforce a policy of collecting only permitted diagnostic evidence, but cannot describe that credential as technically incapable of reading customer rows. ([Initialization source](https://github.com/supabase/postgres/blob/16f89701ccd464d80204b7718a0914d04a67c666/migrations/db/init-scripts/00000000000000-initial-schema.sql), [Read-only default](https://github.com/supabase/postgres/blob/16f89701ccd464d80204b7718a0914d04a67c666/migrations/db/migrations/20250710151649_supabase_read_only_user_default_transaction_read_only.sql))

For production onboarding, prefer a deliberately scoped native inspection role where practical, with required statistical/catalog access rather than blanket data access. The Management API remains a possible inspection transport with its broader authority clearly disclosed. Neither identity should impersonate an end user implicitly. Monitoring, representative testing and repair execution are separate grants; evidence access does not confer index creation or role-switch permission.

The first read-only probe checks current/session user, effective `pg_read_all_stats` privileges, RLS-bypass status, extension namespace/version and actual application-role visibility. Count visible identifiers without exporting SQL. A zero count is inconclusive: wrong role, no activity, resets and disabled tracking can all explain it. Seeing only Pooltune's own probe does not establish application visibility. A later authorized known request whose matched call count increases supplies stronger attribution evidence. The unexecuted [authority probe templates](</Users/jshum/Desktop/Tech Stuff/Resumes/softEng/research_notes/Pooltune representative query validation/authority.md>) document these distinctions.

## Enrollment supplies the examples telemetry cannot reconstruct

Statement statistics normalize many values and aggregate executions. Gateway logs sometimes contain filter values in URLs, but do not provide a universal archive of RPC bodies, original authorization tokens and complete claims. Therefore neither source is a reliable request-replay corpus. ([Statement statistics](https://www.postgresql.org/docs/17/pgstatstatements.html), [Supabase log fields](https://supabase.com/docs/guides/observability/log-field-reference))

The proposed experience is practical: Pooltune discovers an expensive family, and, when representative validation is missing, asks the owner to enroll the relevant operation. For example, “orders list” needs its selected fields, filters, ordering, pagination and an approved identity. Existing developer examples can provide most of that. Enrollment can also happen proactively for important features. No new app SDK or deployment is mandatory for this baseline.

A small case set should cover meaningful differences, not every combination. An initial four to six cases is a proposal, not a tested default. A multi-tenant list might include a typical customer, a large customer, broad and narrow filters, a later page, and an empty result. Single-tenant applications need different cases. Record why each case was selected; the largest customer's data distribution is not necessarily the application's request distribution. Prepared plans and pagination can change execution behavior, so a single convenient literal does not establish coverage. ([Prepared plans](https://www.postgresql.org/docs/17/sql-prepare.html), [Pagination behavior](https://www.postgresql.org/docs/17/queries-limit.html))

An illustrative enrollment record contains references, not secrets:

```json
{
  "enrollmentId": "orders-list-v1",
  "projectRef": "<authorized-project>",
  "requestTemplateRef": "request-template-17",
  "testIdentityRef": "approved-test-account-3",
  "caseSetRef": "orders-representative-cases-2",
  "caseLabels": ["typical", "large_tenant", "empty_result"],
  "sqlFixtureRef": null,
  "queryMapping": "unverified",
  "allowedTests": ["estimated_plan"],
  "measuredTestPolicyRef": null,
  "repairPolicyRef": null,
  "contextVersion": "1"
}
```

Request and case references must resolve to protected, approved data. Retain only needed typed parameters, sensitivity/provenance information and expiry; use encrypted short-retention storage or authorized in-database selection where appropriate. The model receives coarse case labels and opaque references, not JWTs, cookies or arbitrary customer values. A request or case ID is a correlation reference, not authentication. The backing test identity still needs a controlled credential lifecycle. Plans and URLs also require redaction because their expressions can reveal values.

## Two test lanes preserve the real operation

**The HTTP lane is the direct fit for Supabase-js applications.** Issue a separate enrolled Data API request under an approved test identity, preserving selection, filters, pagination, representation preferences and RPC arguments. Request JSON verbose EXPLAIN instead of ordinary row output. PostgREST documents extracting the top-level `Query Identifier` and looking it up in `pg_stat_statements`; keep database/user/top-level identity and version context with the mapping. This connects an approved request to a monitored family without guessing a SQL hash. ([Official mapping workflow](https://docs.postgrest.org/en/stable/how-tos/debugging-performance-with-pg-stat-statements.html))

Supabase plan access is disabled by default and needs protected configuration; an existing pre-request hook must be preserved. `.explain()` does not export generated SQL with bound values. It creates a new diagnostic request, not a recording of the historical slow request. A matched identifier establishes linkage, not identical inputs, claims or prepared-plan history. Production configuration changes require their own authorized setup. ([Supabase plan guide](https://supabase.com/docs/guides/api/query-plans), [Pinned client implementation](https://github.com/supabase/supabase-js/blob/6964595de0298d99e25b658fbe20d8582f98796e/packages/core/postgrest-js/src/PostgrestTransformBuilder.ts))

**The SQL lane is available when the owner supplies an exact reviewed statement, binding types/order and approved context.** One pinned transaction applies the enrolled role, relevant claims, search path and settings, then runs a bounded test and rolls back. Native role switching requires appropriate authority and does not automatically apply all target-role settings. Claims supplied inside PostgreSQL reproduce part of database authorization; they do not verify a JWT signature or reproduce the whole HTTP path. ([SET ROLE](https://www.postgresql.org/docs/17/sql-set-role.html), [PostgREST context](https://docs.postgrest.org/en/v14/references/transactions.html))

Role setup, parameters and query execution must share the controlled transaction. On uncertain cleanup, discard the connection instead of lending its state to another investigation. Read-only transactions and rollback are additional controls, not permission to execute unknown functions with possible external effects. Test reviewed SELECT dependencies first; explicitly enrolled RPCs require their own qualification. An administrator or BYPASSRLS execution is not representative simply because it returns a plan.

HypoPG provides an important distinction: hypothetical indexes exist only in their connection and affect estimated EXPLAIN, not measured execution. The SQL lane can use a single session for candidate preflight. An HTTP request through PostgREST cannot see hypothetical indexes created in Pooltune's separate SQL session. We should not invent session affinity or approximate generated SQL and label it an exact test. ([HypoPG usage](https://hypopg.readthedocs.io/en/rel1_stable/usage.html))

## A physical index trial precedes measured benefit

**No hypothetical plan proves actual candidate runtime.** The no-copy workflow therefore needs a clearly named controlled index trial. First measure qualified baseline cases and ordinary workload intervals. Inspect existing indexes and propose a bounded candidate set. Use hypothetical estimates where the SQL lane supports them; HTTP-only enrollment still provides actual baseline plans and structural evidence but not hypothetical candidate execution.

Next, if an owner-approved repair policy permits a controlled physical trial, create one eligible nonunique index within its table, size, timing and resource limits. At that point its measured benefit remains unproven. Concurrent creation has real scans, waits, write overhead and failure artifacts; uncertain completion requires reconciliation before retry. This report proposes that policy distinction and does not authorize any production trial itself. ([Concurrent index creation](https://www.postgresql.org/docs/17/sql-createindex.html))

Repeat the same authorized cases after the index is valid, then observe ordinary workload, neighboring queries, writes and resource use. Separate estimated cost, controlled milliseconds and normal-workload measurements in the evidence. Record first/subsequent runs and buffer reads/hits because repeated testing and index building warm caches. Do not flush production caches to make a benchmark. EXPLAIN ANALYZE executes the query, excludes client delivery time and adds measurement overhead. ([EXPLAIN](https://www.postgresql.org/docs/17/sql-explain.html), [Measurement caveats](https://www.postgresql.org/docs/17/using-explain.html))

Before the trial, define meaningful improvement and regression limits from the baseline's variability and owner goals. Insufficient comparable traffic produces “inconclusive,” not success. Recovery can remove an eligible Pooltune-created index under its grant, but cannot undo build resources already spent. Ordinary workload monitoring continues because a small representative case set cannot cover all future inputs.

Two alternatives remain legitimate:

| Alternative | Benefit | Tradeoff |
|---|---|---|
| Optional server-side request sampling | Supplies real examples automatically for enrolled paths and allowed fields. Supabase-js supports custom `global.fetch`. | Requires an application integration; misses bypassing clients. Never collect raw authorization headers or response bodies by default. ([Supported hook](https://supabase.com/docs/reference/javascript/initializing)) |
| Representative isolated test environment | Measures a real candidate before production mutation. | Requires separately approved data, configuration, cost and lifecycle work; does not reproduce production cache/concurrency automatically. No clone is created by this proposal. ([Supabase branching](https://supabase.com/docs/guides/deployment/branching)) |

## Qualification makes recurring automation usable

Expose separate coverage: monitored queries, enrolled/testable queries and repair-qualified queries. Capabilities need individual flags: HTTP measurement can work without SQL/HypoPG, while SQL estimation can exist before measured execution is authorized. Schema changes, revoked grants, expired identity/fixtures or unexplained query mappings suspend the affected repair qualification, not all monitoring.

The first future demonstration should qualify one SELECT family, establish application-role visibility, link its request to a fingerprint, collect representative baseline cases, and attempt one permitted index trial. Acceptance requires observed improvement under declared limits and accurate failure recovery. Negative cases must refuse execution for wrong roles, missing parameters, unreviewed functions, changed schemas, saturation and stale authority. An HTTP-only case must explicitly report hypothetical comparison unavailable. These tests have not yet been performed.

## Conclusion

The recommendation is **connect for discovery, enroll for representative testing, and configure bounded repair authority once for eligible recurring work**. This answers the missing-context issue without making an SDK, customer-data replica or per-fix human approval mandatory. Setup remains necessary, and some discoveries will stay monitor-only until suitable examples and authority exist.

The next concrete step is an authorized test-project capability probe followed by one enrolled query demonstration. That will validate source-backed monitoring expectations, the deployed HTTP plan mapping, identity behavior and measured trial policy. It should precede any claim that Pooltune can autonomously validate every discovered query.
