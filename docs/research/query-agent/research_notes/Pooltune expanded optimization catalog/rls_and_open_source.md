# RLS/RPC optimization and open-source Pooltune positioning

Research prepared November 11, 2026. New proposals only; no product changes, database access, repository publication or license selection. Initial target remains managed Supabase. The original three action families and the user's positive response in principle to scoped RPC memory, VACUUM and autovacuum thresholds are preserved; detailed new contracts are not approved implementation scope.

## Which RLS and RPC improvements are concrete enough to build?

### Takeaway

Two worthwhile capabilities are RLS helper-call optimization and membership/query-shape optimization. They can produce actual tested SQL migrations or repository changes. Recommend a reviewed change lane in V1, with the agent doing diagnosis, edit generation and verification before review, rather than giving the existing operational executor permission to rewrite arbitrary security policies.

### Cited Findings

- Supabase documents wrapping row-independent `auth.uid()` and `auth.jwt()` calls in scalar SELECTs so the optimizer can evaluate them once per statement. The technique is valid only when the function result does not depend on row data. It also recommends RLS allow/deny tests under actual application roles. [Row Level Security](https://supabase.com/docs/guides/database/postgres/row-level-security)
- Supabase recommends investigating RLS overhead under the real role and JWT claims, adding explicit client filters where consistent with intended access, and rewriting some correlated membership predicates into a user-scoped set. Comparing RLS on/off is a non-production diagnostic because disabling RLS exposes rows. Large membership sets can need a different approach. [RLS performance](https://supabase.com/docs/guides/database/postgres/row-level-security-performance)
- `auth.role()` is deprecated in favor of policy `TO` roles; `auth.email()` is deprecated in favor of JWT access. This does not imply every arbitrary Boolean expression can be mechanically changed without evaluating semantics. [Deprecated RLS features](https://supabase.com/kb/troubleshooting/deprecated-rls-features-Pm77Zs)
- PostgreSQL volatility labels are promises to the optimizer. `STABLE` means consistent within a statement; `IMMUTABLE` permits constant folding and has stronger invariance requirements. Functions with side effects or values varying within a statement must remain `VOLATILE`. [Function volatility](https://www.postgresql.org/docs/current/xfunc-volatility.html)
- PostgreSQL cannot generally infer parallel safety. A wrong label can conflict with writes, sequences, transaction changes, temporary tables or backend-local state. [Parallel safety](https://www.postgresql.org/docs/current/parallel-safety.html)
- `ALTER POLICY` changes role lists and expressions but requires table ownership. Command type and permissive/restrictive behavior cannot be changed with that command. [ALTER POLICY](https://www.postgresql.org/docs/current/sql-alterpolicy.html)

### Inferences

### Candidate 1: Eliminate repeated row-independent authorization helper evaluation

**Problem:** a high-frequency query evaluates the same user/JWT helper for many candidate rows. The schema and application already have correct authorization, but plan evidence shows repeated helper work.

**Detection:** use Supabase's advisor finding as an input, read `pg_policy`/`pg_policies`, policy-expression parse trees, referenced function identities and definitions, and representative query plans under application roles and claims. Confirm that this path is used and the likely cost matters. An advisor warning alone is not evidence of a measurable customer improvement.

**Concrete change:** for a verified simple SELECT policy, preserve everything except the helper expression:

```sql
-- Before
USING (auth.uid() = owner_id)

-- Proposed expression
USING ((SELECT auth.uid()) = owner_id)

-- Migration shape, generated for the exact existing policy
ALTER POLICY "read_own_documents" ON "app"."documents"
USING ((SELECT auth.uid()) = owner_id);
```

Use AST-aware transformation and schema-qualified function identity, never string replacement across arbitrary SQL. The executor must not treat table/policy/function text as instructions. Initial eligible patterns should be known zero-argument Supabase request helpers with verified row independence, no changes to claim values during the statement, and no side-effectful surrounding behavior. A custom `can_access(row_id)` function is not a safe once-per-statement helper. A zero-argument custom SECURITY DEFINER function is not automatically eligible either; inspect its complete behavior and dependencies or leave it for review.

Save target table identity, complete policy definition, roles, command, permissive flag, USING/WITH CHECK expressions, RLS/FORCE RLS flags, function dependencies, and schema/migration revision. A first rule can restrict itself to SELECT policies to avoid accidentally changing write behavior through implicit WITH CHECK semantics. Extending to INSERT/UPDATE policies requires explicit tests for both old-row visibility and new-row acceptance.

**Why a reviewed lane is justified:** the transformation is concrete and narrowly testable, but operates on authorization. This lane can automatically prepare the migration, inverse migration, tests, plan comparison and evidence report. A person approves the resulting patch/deployment through their normal workflow. This adds substantive agent work rather than a diagnosis-only report, while keeping permission for ordinary database tuning separate from permission to modify access control.

Do not replace `auth.role()` with `(SELECT auth.role())` as the preferred modernization. Propose a `TO authenticated` or `TO anon` migration where the role expression and inheritance semantics have been checked. Changes to the permitted-role set are security changes, even if they also make execution faster.

### Candidate 2: Reduce correlated membership checks and unnecessary query work

**Problem:** for every candidate row in a large table, a policy performs a correlated membership lookup, or the application requests all visible data when its operation already requires a narrower tenant/user subset.

**Possible policy rewrite:**

```sql
-- Before: each target row drives the membership lookup
(SELECT auth.uid()) IN (
  SELECT user_id FROM app.memberships
  WHERE memberships.team_id = documents.team_id
)

-- Candidate: obtain the current user's accessible team set
team_id IN (
  SELECT team_id FROM app.memberships
  WHERE user_id = (SELECT auth.uid())
)
```

This is an example pattern, not a universal SQL equivalence rule. Preserve NULL behavior, types, membership-table RLS, duplicate handling, role inheritance, custom operators/collations, function side effects and policy composition. Compare large as well as small membership sets. For a huge set, a good indexed semi-join may outperform materialization. Candidate evaluation should choose based on actual plans and result equivalence rather than “joins are bad.” An appropriate existing-family index on membership lookup columns may solve the issue without a policy rewrite.

**Application/RPC alternative:** add an explicit authorized filter to an application query, or refactor a known slow SQL/RPC body to prefilter once, preserve result semantics and use an eligible index. Example `.eq('owner_id', userId)` can help when the operation is specifically “my documents.” It would be wrong for an operation intended to include shared/team-visible documents. RLS stays enabled and remains the security boundary; client filtering alone is not authorization.

**Delivery:** for a database function, prepare a versioned SQL migration with the exact original signature, overload identity, body, owner, grants, security mode, search_path and function settings. For application code, prepare a repository patch and normal build/test/deployment proposal. Reading a stored function does not require repository access, but applying a production change without recording its source/migration creates drift. If no source repository is connected, provide a tested migration artifact and explicit deployment instructions, not an imaginary PR. A reviewed PR is a new repository integration, not a capability of the existing Supabase data connection.

Do not introduce a new SECURITY DEFINER helper merely to bypass membership-table RLS during optimization. That is a separate security design. Likewise, never auto-label a function IMMUTABLE or PARALLEL SAFE because a benchmark gets faster. Correct labels require a source/dependency audit and concurrency semantics, not only sample outputs. Request/JWT-dependent functions cannot be treated as globally immutable. Function COST/ROWS annotations and plan settings also need separate evidence contracts; they are not proof of semantic safety.

### Gaps

- The frequency of these patterns in Jared's actual projects is unknown. No production role, policy, function or latency was inspected.
- A finite test suite gives evidence of equivalence, not a mathematical proof for arbitrary SQL. The initial rules should therefore be deliberately narrow.
- Repository integration, staging environments and deployment authorization are new product capabilities to agree on. They are not automatically supplied by the three original action permissions.

## What exact correctness and rollout contract makes this useful?

### Takeaway

Every optimization must preserve the access/result contract under real roles, not merely run faster as an owner. Use independent result/state checks and negative authorization cases before deployment, then measure actual workload outcomes afterward.

### Cited Findings

- Supabase's current guidance includes tests for SELECT/INSERT/UPDATE/DELETE under `anon` and `authenticated`, and demonstrates setting role and JWT claims to reproduce API execution. [RLS guide](https://supabase.com/docs/guides/database/postgres/row-level-security), [RLS performance guide](https://supabase.com/docs/guides/database/postgres/row-level-security-performance)
- Supabase Advisors are existing deterministic health/security/performance checks and are accessible through Studio, MCP, CLI and Management API. Findings must be checked against the intended access model. [Supabase Advisors](https://supabase.com/docs/guides/observability/advisors)

### Inferences

**Proposed artifact produced by the agent:** exact proposed migration or repository diff; current-definition hash; affected tables/functions/routes/roles; mechanism; before/after plan evidence; correctness and authorization tests; forward and inverse migration; expected locking/deployment behavior; and metrics to watch. The permission for “prepare reviewed optimization” allows generating/testing these artifacts, not silently altering production policies or merging a PR.

**Mandatory test matrix:**

| Dimension | Concrete check |
|---|---|
| Anonymous user | No JWT and relevant anonymous-auth cases retain intended access/denial |
| Tenant A owner | Expected own rows remain visible and editable as intended |
| Tenant B user | Cannot read, update, delete or create rows on behalf of Tenant A |
| Shared/team membership | Member, nonmember, revoked member and multi-team cases match the old contract |
| Roles | `anon`, `authenticated`, custom roles, inheritance and intended privileged paths remain correct |
| Read results | Same rows and duplicate multiplicity; same ordering when specified; same NULL values, aggregates, pagination and error behavior |
| Writes | Same affected rows and state changes; failed cross-tenant writes remain denied; UPDATE tests check both USING and WITH CHECK |
| Policy composition | Other permissive/restrictive policies do not widen or narrow effective access after the edit |
| RPC API contract | Signature, argument defaults, return shape, grants, security mode and search_path preserved |
| Runtime context | Claims change between requests without leaking cached authorization across users or pooled sessions |
| Workload range | Small/large tenants, empty membership sets, NULL values and skewed/common/rare parameters |
| Side effects | No new helper execution, external calls, altered sequence/trigger behavior or unintended writes |

Use synthetic non-sensitive fixtures and property-based combinations for permission boundaries. Test the old and new implementation against a known expected contract, not only against each other; otherwise an existing security defect is accepted as the target. If existing access is wrong, report a separate security fix rather than disguise it as a performance optimization.

For pure queries, compare multiset results where ordering is not part of SQL semantics. A count match is insufficient. For data mutations, compare final fixture state and errors in disposable test environments. Do not use production `EXPLAIN ANALYZE` on arbitrary RPCs because execution can have effects. The benchmark role must not bypass RLS; owner-only plans are insufficient. RLS-on/off diagnosis, if needed, belongs only in an isolated test environment.

Before deployment, re-read exact policy/function definitions and migration version and reject drift. Apply a transactional policy migration with a bounded lock wait where feasible; record observed metadata and application test results. Do not disable RLS temporarily in production as a shortcut. The migration requires owner-level authority, so keep credential possession separate from the model's authority.

Post-deployment, observe actual query/API duration distribution where available, failures and throughput under a comparable mix. A plan with an initPlan confirms mechanism, not an end-user improvement. Preserve a rollback artifact, but acknowledge that reverting a policy cannot undo any information already exposed or writes already accepted. This makes predeployment negative authorization tests materially necessary.

**Recommended V1 boundary:** include “prepare and verify an optimization patch” for the two narrowly defined patterns above alongside the autonomous operational lane. Once a rule has enough measured evidence and users explicitly grant a precise policy-edit scope, autonomous application could be a later contract. Do not conflate that possibility with approval today. If a single-lane V1 is essential, keep RLS-aware index optimization automatic and queue tested policy/RPC migrations for review.

### Gaps

- Which review/deployment integration is practical remains a design decision. This research does not select GitHub-only operation or authorize external posting.
- No migration was generated against a real schema, and no benchmarks or authorization tests were run.

## How should the open-source product be positioned against provider features?

### Takeaway

An open-source optimization agent can be worthwhile without an exclusive technical moat. Position it around inspectable action contracts, operator control and verified practical work. Avoid claiming all providers only advise, all automation costs extra, or Pooltune already outperforms native systems.

### Cited Findings

- **Azure SQL Database**, a SQL Server-family service, supports automatic index creation/removal and plan correction, with validation and automatic reversal. This should not be conflated with Azure PostgreSQL. Its documentation does not establish a universal separate surcharge for automatic tuning. [Azure SQL automatic tuning](https://learn.microsoft.com/en-us/azure/azure-sql/database/automatic-tuning-overview?view=azuresql-db)
- **Azure Database for PostgreSQL Flexible Server** calls its recommendation system “autonomous tuning,” but the documented `index_tuning.mode` values are OFF and REPORT. Recommended SQL is applied by the operator. Current documentation requires at least 4 vCores for this feature; the presence of such resource requirements is different from a separately priced tuning add-on. [Azure PostgreSQL autonomous tuning](https://learn.microsoft.com/en-us/azure/postgresql/monitor/concepts-autonomous-tuning)
- Azure PostgreSQL separately has **Intelligent Tuning**, which automatically changes write-related settings, including checkpoint/WAL parameters. Its previous autovacuum tuning was replaced by adaptive autovacuum. Therefore, “Azure PostgreSQL cannot automatically optimize” is false. [Azure PostgreSQL intelligent tuning](https://learn.microsoft.com/en-us/azure/postgresql/monitor/concepts-intelligent-tuning)
- **Google Cloud SQL for PostgreSQL** Index Advisor requires Enterprise Plus and Query Insights. It periodically analyzes queries and returns CREATE INDEX recommendations and estimated effects; this page does not describe autonomous creation plus rollback. This is a verified edition restriction, not evidence that every provider charges a separate tuning fee. [Cloud SQL Index Advisor](https://docs.cloud.google.com/sql/docs/postgres/use-index-advisor)
- **Supabase** already ships deterministic advisors covering security and performance, with multiple access surfaces. Pooltune can use these rather than reproduce every detection rule. [Supabase Advisors](https://supabase.com/docs/guides/observability/advisors)

### Inferences

**Suggested framing:** “An open-source agent that finds and applies verifiable database optimizations, starting with Supabase. Operators choose its permissions, inspect every change and keep control of deployment.” This is a product-direction proposal, not a measured superiority claim.

The motivation can be accessible automation and user control even when a provider offers overlapping capabilities. Cloud SQL's edition restriction supports one specific example of gated access; do not turn that into “all native tuning is paywalled.” The focused research did not establish current standalone tuning prices for Azure services, so quote neither a surcharge nor a guaranteed zero price. Database compute, storage, observability and model execution costs remain possible even for open-source software.

Open-source implications worth designing explicitly:

- A self-hosted controller that can connect to a hosted Supabase project. Self-hosting Pooltune does not mean broadening the initial database target to self-hosted PostgreSQL.
- Secrets remain in the operator-controlled executor, separated from the model and any untrusted query/log text.
- A replaceable model-provider adapter, with an optional deterministic detector/runbook mode for supported repairs. BYO model should be a portability/cost choice, not a promise of equal quality across models.
- Versioned, readable action contracts and repeatable fixtures for every mutation, so contributors can test permissions, correctness, recovery and outcome measurement.
- Exportable evidence/action history, local retention controls and opt-in external telemetry rather than an opaque hosted requirement.
- No automatic spending, uncontrolled production SQL or customer-transaction interruption merely because code is open source.

License choice, trademark, distribution and public repository publication are separate decisions. The user's open-source aspiration is not permission to publish an existing private repository now. No need to prove that providers cannot copy the idea before testing whether the project is useful.

### Gaps

- Current exact provider pricing and optional charges were not established; this brief intentionally makes no universal “free” or “paid add-on” claim.
- Hosting overhead, model costs and contribution appetite for Pooltune are unmeasured. Open source is a distribution/control strategy, not proof of demand or operational safety.
