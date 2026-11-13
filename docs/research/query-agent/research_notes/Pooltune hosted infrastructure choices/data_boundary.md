# Hosted Pooltune data, onboarding, and privacy boundary

Research date: November 12, 2026. Research only. No customer project was accessed and no integration was tested. Fully hosted deployment and six repair types are accepted; technical mechanisms below are proposals or documented possibilities, not implemented capabilities.

## Can customer database information remain client-side, and what must Pooltune ingest?

### Takeaway
The application records can stay in the customer's existing Supabase database. In a fully hosted, unattended design, selected diagnostic results must reach Pooltune's servers; browser-only collection contradicts the requirement to operate while the browser/laptop is off. Distinguish customer database execution from browser execution.

### Cited Findings
- Supabase supports backend PostgreSQL connections and a frontend Data API. The Data API is designed for permitted data access under RLS; persistent backends can use direct PostgreSQL or session pooling as appropriate. A client SDK is not an administrative agent credential. — [Connection methods](https://supabase.com/docs/guides/database/connecting-to-postgres)
- `pg_stat_statements` records a representative query, query identity, call count, cumulative execution time, buffer activity, temporary blocks and other counters. Rows are keyed by database, role, query identity, and top-level status. Literal replacement is not guaranteed sanitization: PostgreSQL explicitly warns some normalized queries can still display constants during entry churn. Identity stability also has limits. — [PG17 query statistics](https://www.postgresql.org/docs/17/pgstatstatements.html)
- Execution plans support JSON output and show query expressions, estimates, and measured execution details when analysis is used. Documentation examples contain literal filter values. Thus plans are not inherently free of customer data. — [EXPLAIN](https://www.postgresql.org/docs/17/using-explain.html)
- PostgreSQL supports structured and indexable `jsonb` alongside regular relational records. — [JSON types](https://www.postgresql.org/docs/17/datatype-json.html)

### Inferences
- Recommended boundary: queries/maintenance execute on customer Supabase; selected outputs go to Pooltune workers; explicitly selected/redacted diagnostic evidence goes to Anthropic. Customer browser only configures the service and displays summaries. This is hosted remote management, not data replication.
- Proposed minimum ingest:
  1. Pooltune accounts, team membership, customer/project references, allowed targets/actions, retention policy and budgets.
  2. Project capability snapshot: PostgreSQL version, accessible extensions/views, available metrics, connection state, permissions.
  3. Bounded interval query counters, timestamps/reset markers, resource samples, enrolled-table statistics, selected job timing/outcomes.
  4. On-demand sanitized query structure, relevant table/index/function definitions, plans, and experiment evidence.
  5. Durable jobs, leases, investigation findings, action intent/status, before/after observations, recovery decisions, model tokens/cost.
  6. Protected credentials or secret references, accessible only to authorized backend paths.
- Do not ingest entire application tables, full request bodies, all application logs, all query executions, or a database replica by default. Treat sample parameters, error strings, SQL text, plans, cron command text and names as potentially sensitive. A table name or query comment can contain secrets or misleading instructions. Tool evidence is untrusted data, not instructions.
- Cost/control recommendation: store numeric counters and fingerprints routinely, deduplicate SQL text, fetch full evidence only for bounded investigations, downsample older measurements, expire detailed payloads sooner, cap per-project retained series/payload sizes. A shortlist of important/high-impact queries plus periodic discovery is better than unbounded copying. Rankings alone can miss newly emerging problems, so maintain bounded broad discovery and pinned important queries.
- Example sizing model, NOT measured capacity: projects × retained query patterns × snapshots/day × retention days determines snapshot count. At 100 projects × 50 patterns × 288 five-minute intervals = 1.44 million snapshots/day before rollups. This is why retention/aggregation matters more than customer application row count alone. Do not present five-minute cadence or these counts as final design.
- Our data shape is largely consistent across customers even when their apps differ. Accounts, jobs and metrics are relational; variable execution plans can be JSONB. Managed PostgreSQL, potentially Supabase for Pooltune's own DB/Auth, is a reasonable starting point. MongoDB is not required by varied customer schemas.

### Gaps
- Exact fields required by each repair, safe statement parsing/redaction, per-project sampling budgets, retention and storage cost remain to be measured.
- Aggregate query statistics do not supply the representative parameter/role sets needed to validate all repairs. Explicit sample provision or authorized controlled experiments are required for some cases. Missing context should make a repair ineligible, not invite made-up samples or a guarantee of privacy.
- Source evidence establishes data mechanisms, not that all Supabase projects expose every metric at every plan/version.

## How should connection authorization and tenant isolation work?

### Takeaway
Three identities must remain separate: a user signing into Pooltune, Pooltune's Supabase Management API authorization, and the SQL role allowed to inspect/modify the customer's database. A successful OAuth connection does not prove eligibility for all six repairs.

### Cited Findings
- Supabase documents a Connect Supabase OAuth flow with state validation, recommended PKCE, access/refresh tokens and revocation handling. Its guide says an existing project's database password cannot be retrieved from the Management API. — [OAuth integration](https://supabase.com/docs/guides/integrations/build-a-supabase-oauth-integration)
- OAuth scopes restrict Management API endpoints, are configured on the OAuth app, and changes require existing users to authorize again. — [OAuth scopes](https://supabase.com/docs/guides/integrations/build-a-supabase-oauth-integration/oauth-scopes)
- Current scoped PATs can restrict organizations, projects and permissions. Token rights never exceed the user's underlying rights. Direct SQL using a database password is not constrained by the Management API token's scopes. This is a documented alternative for a private pilot, not an equivalent OAuth UX. — [Personal access tokens](https://supabase.com/docs/guides/platform/personal-access-tokens)
- `POST /v1/projects/{ref}/database/query/read-only` is documented as beta/experimental, executes as `supabase_read_only_user`, requires schema-qualified references, and uses `database:read` OAuth or `database_read` token permission. — [Read-only SQL endpoint](https://supabase.com/docs/reference/api/v1-read-only-query)
- In PostgreSQL 17, plain VACUUM generally requires table MAINTAIN privilege and cannot run inside a transaction block. — [VACUUM](https://www.postgresql.org/docs/17/sql-vacuum.html)
- Concurrent index creation cannot run in a transaction block; failed attempts can leave invalid indexes that consume write overhead. — [CREATE INDEX](https://www.postgresql.org/docs/17/sql-createindex.html)
- Supabase secret credentials use the privileged `service_role` and can bypass RLS; they must not be exposed to browsers/customers. — [RLS and bypass roles](https://supabase.com/docs/guides/database/postgres/row-level-security)

### Inferences
- Suggested onboarding: sign into Pooltune; connect authorized Supabase organization; choose projects to enroll; capability probe; observe; separately enroll repair targets and provide appropriate SQL authority. Exact setup steps are unresolved. Do not demand unrestricted postgres credentials as the assumed final solution.
- Project selection inside Pooltune is our policy boundary. Do not claim that selecting one project narrows an OAuth token at the provider unless provider semantics have been verified. OAuth guide exposes organization selection and app-level scopes; reviewed docs do not establish a project-scoped OAuth flow. Scoped PATs explicitly do support project restrictions.
- Store OAuth refresh tokens/SQL credentials encrypted or in a managed secret store, keep decryption keys separate, never include them in prompts/logs, and revoke access on disconnect. Serialize token refresh to avoid races, stop jobs on revoked grants, and check project/action permission again at execution time.
- Dedicated inspection and execution paths reduce accidental privilege use. The repair path still needs operation-specific SQL authority; MAINTAIN does not imply blanket DDL rights. Determine a narrow role/grant model and test it rather than equating read API, app anon key, service role and PostgreSQL ownership.
- Prefer a real PostgreSQL connection for operations requiring top-level nontransaction behavior unless Management API execution semantics are explicitly verified. An HTTP SQL endpoint existing does not establish safe retry/cancellation/transaction behavior for long maintenance.
- Tenant isolation: every record/job is tied to organization and project; backend derives authorized ownership from verified identity and stored grants, not user-provided IDs alone. Use compound foreign keys/queries to prevent mismatched organization-project references; RLS can defend dashboard access, but privileged workers must enforce authorization themselves. A service-role connection does not inherit automatic tenant isolation.
- Bounded per-project connections matter for a many-customer service: opening default pools for every connected project can itself create customer load. Design lazy connection acquisition/eviction and fixed project/global ceilings; do not leave one permanent large pool per customer.

### Gaps
- OAuth docs are internally uneven: overview says full Management API access while scopes page supports restrictions. Use endpoint-specific scope docs and verify denial paths in a test integration. Do not infer organization/project narrowing beyond the docs.
- No real OAuth app, token refresh, RLS policy, role grant, network restriction, or six-repair privilege matrix was tested.
- Whether repair authority can be provisioned smoothly without asking for broad existing DB passwords needs a dedicated onboarding experiment. Management API login-role APIs exist in current catalog, but their privilege model was not researched enough here to recommend them as solved onboarding.

## What privacy claim is defensible when using Anthropic?

### Takeaway
Promise minimized, disclosed diagnostic collection, not zero access or zero retention. Hosting the agent means Pooltune processes selected customer evidence; remote model reasoning means selected evidence reaches the provider.

### Cited Findings
- Anthropic's commercial retention article states ordinary API inputs/outputs are deleted within 30 days, with exceptions for longer-lived features, other agreements, policy enforcement and legal obligations. — [Commercial retention](https://privacy.claude.com/en/articles/7996866-how-long-do-you-store-my-organization-s-data)
- Current platform docs describe feature-specific arrangements, ZDR eligibility, covered models requiring 30-day retention, and no model training on retained data without express permission. Some wording about default retention differs from the general commercial article; do not flatten this into a universal zero-retention promise. — [API data handling](https://platform.claude.com/docs/en/manage-claude/api-and-data-retention)

### Inferences
- Send compact task-specific evidence, not wholesale database exports or arbitrary raw logs. Separate acquisition, sanitization, prompt construction and audit metadata so minimization can be tested.
- Retain diagnostic evidence only as necessary to investigate, prove actions and support recovery. Deleting ordinary metrics is different from deleting a customer credential or breaking an action audit trail; define each lifecycle.
- Honest customer wording: application records remain in the existing database; Pooltune processes selected database structure/performance evidence; selected evidence is sent to the named model provider; authorized experiments may need additional inputs. Document exactly which fields are collected and their retention.
- A language choice cannot solve privacy or isolation. Go/Rust/TypeScript workers all require the same explicit authorization, bounded access and payload handling.

### Gaps
- Exact Anthropic model, workspace arrangement and optional features are not selected, so exact contractual retention and feature eligibility must be checked at implementation. No customer's data was sent during this research.
- Need practical tests that confidential literals/credentials do not escape into prompts, application logs, tracing spans, error reporting or support bundles. Regex-only SQL redaction is not established as sufficient.
