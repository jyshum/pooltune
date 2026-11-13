# Representative query and context acquisition for Supabase

Research date: November 12, 2026. Research only. No customer connections, configuration changes, application changes, or test execution. This preserves the accepted six-repair product scope.

## Can connecting Supabase alone provide representative queries?

### Takeaway

Connecting Supabase can provide broad workload discovery. It does not automatically supply every input and user context needed to replay an expensive request. Explicit query enrollment can fill that gap without requiring a customer SDK or an application deployment.

### Cited Findings

- `pg_stat_statements` aggregates normalized statement text. Constants become parameters; it does not retain a representative parameter set or the original JWT. Its identity includes database, user, query identifier, and top-level status. Access to other users’ text and identifiers requires appropriate privileges. Entries can disappear through eviction or reset. This is workload evidence, not a request archive. [PostgreSQL 17 reference](https://www.postgresql.org/docs/17/pgstatstatements.html)
- Supabase gateway logs document request method, URL, path, search, selected headers, and response metadata. Not every field is populated. The documented request-header list omits Authorization. It does not document a general RPC request-body archive or complete JWT claims. URLs can nevertheless contain real filter values, so they are customer data. [Supabase log field reference](https://supabase.com/docs/guides/observability/log-field-reference)
- Some log interfaces expose JWT subjects or Auth actor IDs. Identity metadata does not provide the original token or all application claims. Retention depends on the plan. [Supabase Logs](https://supabase.com/docs/guides/observability/logs)
- Supabase’s slow-query guide explicitly distinguishes direct connections as `postgres` from Data API queries using other roles: its example role-level logging configuration does not cover those API requests. Query Performance omits failed/timed-out queries. Statement logging may expose personal information or secrets and has overhead. [Supabase slow-query logging](https://supabase.com/docs/guides/observability/log-slow-queries)
- `supabase-js` supports a custom `global.fetch` implementation. This is a supported integration point, not a built-in Pooltune capture service. [Supabase JavaScript initialization](https://supabase.com/docs/reference/javascript/initializing)

### Inferences

Proposed acquisition routes:

| Route | What Pooltune gains | What it costs or cannot establish |
|---|---|---|
| Connect-only observation | Query families, workload contribution, table/index evidence; sometimes useful URL samples | No universal replay inputs, complete request context, or exact route mapping |
| Explicit enrollment with owner-provided fixtures | An approved request template, representative typed inputs, and authorized context references | Small owner setup; the fixture’s coverage must be declared |
| Optional server-side custom-fetch sampling | Real examples for approved paths and parameter names, request timing and contextual labels | An application deployment; captures only requests passing through that client |

The baseline product should remain connect-only. An investigation can say “found an expensive query; representative validation is missing” rather than pretending it can execute the normalized text. Enrollment should be offered when that gap blocks a specific repair, or proactively for important queries. A dashboard form can accept a request template and test identity without requiring a new SDK; this is a proposed Pooltune experience.

Optional sampling should collect only allowlisted request structure and bounded parameter examples. Exclude raw Authorization headers, cookies, API keys, and response bodies. Capture an opaque context reference supplied by a trusted server, not a token sent to the model. A server-only wrapper will not cover browser clients that bypass it. Browser-reported context is evidence, not authority to impersonate a user.

### Gaps

- No verified passive Supabase feed universally provides request body, typed SQL binding order, full claims, and the generated executable SQL together.
- Installed project permissions, retention, extensions, and PostgREST version need probing. Hosted documentation does not prove every existing project exposes every option.
- Useful replay coverage depends on the owner’s fixture quality. One small tenant is not representative of a large tenant.

## Can an HTTP request be linked to its plan and query statistics?

### Takeaway

Yes. PostgREST documents a concrete verbose-plan query-identifier workflow. This is much stronger than matching URLs or guessing a SQL hash, but it requires a runnable representative request and enabled, protected plan access.

### Cited Findings

- PostgREST 16’s official workflow requests `Accept: application/vnd.pgrst.plan+json; options=verbose`, reads the top-level `Query Identifier`, and looks that value up in `pg_stat_statements`. Requirements include PostgreSQL 14+ and `db-plan-enabled`. The returned statistics contain normalized SQL. [Official PostgREST workflow](https://docs.postgrest.org/en/stable/how-tos/debugging-performance-with-pg-stat-statements.html)
- Supabase `.explain()` returns a plan instead of data, including for RPCs and writes. It is disabled by default. Supabase instructs production users to install pre-request protection before enabling it, and warns that there is one existing pre-request hook to preserve. `analyze` executes the query and can modify data. [Supabase plan guide](https://supabase.com/docs/guides/api/query-plans)
- At supabase-js revision `6964595de0298d99e25b658fbe20d8582f98796e`, `.explain()` sets the plan Accept header on the existing builder, preserves its original Accept value in the `for` parameter, and composes requested options. Defaults include `analyze: false` and `format: text`; JSON and verbose are available. It does not provide a generated-SQL export. [Pinned transform builder](https://github.com/supabase/supabase-js/blob/6964595de0298d99e25b658fbe20d8582f98796e/packages/core/postgrest-js/src/PostgrestTransformBuilder.ts)
- At PostgREST revision `19747ac6d7c415a5eadf8e6f37ae707e10466c93`, read/write/function statement builders pass generated SQL through `mtSnippet`; the plan media type applies `explainF` to that statement. The generated statement includes response aggregation and request-specific details. [Pinned statement builder](https://github.com/PostgREST/postgrest/blob/19747ac6d7c415a5eadf8e6f37ae707e10466c93/src/library/PostgREST/Query/Statements.hs)
- PostgREST also documents query logging, but availability and behavior depend on server configuration and log level. A normal access log is basic request metadata, not the full SQL and binding history. [PostgREST observability](https://docs.postgrest.org/en/stable/references/observability.html)

### Inferences

Proposed verified mapping procedure:

1. Obtain an enrolled request with representative parameters, original representation/preferences, and an approved identity/context.
2. Check plan availability and protection. If disabled, mark this capability unavailable pending authorized setup. Do not silently enable global access or overwrite the existing pre-request function.
3. Issue a separate diagnostic plan request with JSON and verbose options. Do not replace a normal application response with a plan.
4. Preserve the returned query identifier losslessly. Join statistics using known database/user/top-level context where available, and retain mapping provenance and version.
5. Mark mapping as verified only when the identifier and context match. An absent entry can mean the normal request has never successfully run, or its entry was evicted/reset.

This obtains the plan for a newly issued equivalent request, not the historical plan used by an earlier slow request. It also does not produce raw executable SQL or recover missing parameters. An observed identifier is authoritative evidence; a guessed hash of a URL or normalized SQL is not.

### Gaps

- The documented route must be checked against the deployed PostgREST/PostgreSQL versions and enabled settings.
- An arbitrary expensive normalized query cannot always be inverted into a unique HTTP request. The workflow is request-to-query, not a guaranteed query-to-request decoder.
- A query identifier groups a query structure; it does not identify one historical request or establish its parameter values.

## What context is required, and can HTTP-only enrollment use HypoPG?

### Takeaway

Replaying the same route under an administrator role can test a different workload. HTTP plan requests also cannot automatically see hypothetical indexes created in Pooltune’s separate SQL connection.

### Cited Findings

- PostgREST sets request-scoped headers, cookies, JWT claims, method/path, role, and schema context in a transaction. Pre-request behavior can affect the request. The execution mode varies by HTTP method and RPC volatility. [PostgREST transaction reference](https://docs.postgrest.org/en/stable/references/transactions.html)
- PostgreSQL prepared statements can use parameter-specific custom plans or generic plans. In automatic mode, PostgreSQL initially evaluates custom executions before comparing their average estimated cost with a generic plan. Statistics and schema changes can trigger replanning. [PostgreSQL 17 PREPARE](https://www.postgresql.org/docs/17/sql-prepare.html)
- HypoPG stores hypothetical indexes in the current connection’s private memory. Other connections do not see them, and they affect plain EXPLAIN only, not EXPLAIN ANALYZE execution. [HypoPG usage](https://hypopg.readthedocs.io/en/rel1_stable/usage.html)

### Inferences

Preserve relevant role, tenant/claims, request settings and search path. Prefer an owner-designated test identity and controlled credential lifecycle. Catalog access or a service credential is not permission to fabricate an arbitrary customer’s identity. Direct SQL role/claim emulation can be useful, but should be labeled a database-context test rather than end-to-end authentication verification.

A new HTTP plan may differ from a production connection’s cached prepared-plan history. Test typical and skewed input classes where authorized. Do not promise the exact plan of every historical request merely because its identifier matches.

There are two honest index-validation paths:

- **Executable SQL enrollment:** obtain a reviewed parameterized SQL template, its binding types/order, and authorized context. On one controlled SQL connection, apply that context, create hypothetical candidates, and compare estimated plans. Matching the original HTTP query identifier/context is additional evidence of fidelity. Normalized pg_stat_statements text alone is insufficient.
- **HTTP-only enrollment:** inspect the actual generated request plan and relation/index evidence. HypoPG in another connection provides no direct before-build HTTP experiment. If the repair policy allows it, a bounded physical-index build can be followed by the enrolled HTTP plan and matched-window runtime measurements. This has greater resource/change exposure and must be described as a staged repair experiment, not prevalidated hypothetical success.

Do not insert session-affinity assumptions or a new customer RPC merely to bridge this gap. A future SQL reconstruction facility would need its own verified binding/context contract. Roughly translating a REST filter into a SELECT can screen an idea but does not validate the full PostgREST statement.

### Gaps

- No verified standard `.explain()` feature returns executable generated SQL with bindings or lets Pooltune reserve the same PostgREST backend session for its own HypoPG setup.
- No verified automatic binding-order reconstruction from normalized text plus an HTTP fixture was established in this research.

## What minimum enrollment record should V1 accept?

### Takeaway

Enrollment should be a small evidence package for a particular important query. It should make missing evidence visible and separate inspection permission from permission to execute a measured test.

### Cited Findings

- The distinctions above follow from the documented separation between normalized statement statistics, HTTP plan representation, request transaction context, and connection-private hypothetical indexes. [Statement statistics](https://www.postgresql.org/docs/17/pgstatstatements.html), [plan mapping](https://docs.postgrest.org/en/stable/how-tos/debugging-performance-with-pg-stat-statements.html), [request context](https://docs.postgrest.org/en/stable/references/transactions.html), [HypoPG](https://hypopg.readthedocs.io/en/rel1_stable/usage.html).

### Inferences

Proposed fields, not an implementation commitment:

| Field group | Contents |
|---|---|
| Identity | Project, enrollment ID/version, owner-facing label, creation/provenance timestamps |
| Request shape | Table/view/RPC, method, schema/resource, selection, filter/order/range and representation/preferences; restrict destination to the enrolled project |
| Fixture classes | Bounded typed examples; common, empty-result, and large/skewed cases when relevant; provenance, sensitivity and expiry |
| Context | Opaque approved identity reference; relevant role/claims/settings references; credentials stored separately from model-visible evidence |
| Optional SQL fixture | Reviewed template, binding types/order and context; explicit indication whether equivalence to the API request is verified |
| Evidence linkage | Observed query identifier with database/user/top-level context, project versions, mapping timestamp and verified/heuristic/unmapped status |
| Test authorization | Plan-only versus measured read; allowed functions and reviewed side effects; execution and resource budgets; result expectations as summaries |
| Completeness | Missing context/parameters, unsupported plan access, freshness/schema fingerprint, retention and sampling provenance |

Recommendation: keep connect-only discovery, add owner enrollment as the direct V1 mechanism for representative validation, and make SDK sampling optional. Display monitoring coverage and repair-validation coverage separately. Never silently treat all discovered queries as executable tests. This adds evidence quality to the six existing repairs without adding a seventh repair family or requiring database replication.

### Gaps

- Exact retention, fixture count, resource limits, and credential onboarding remain product decisions. No settings are selected here.
- A fixture can support a test without proving every user, input distribution, or future workload is covered. Post-repair workload monitoring remains necessary.
