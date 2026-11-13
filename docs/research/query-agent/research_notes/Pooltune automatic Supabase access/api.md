# Automatic Supabase database access provisioning

Research date: 2026-11-12. Read-only review of the current public Management API OpenAPI document, official integration docs, and database connection docs. No customer project, credentials, endpoint execution, SQL setup, or privilege test was used. This is a contract-level finding, not live validation.

## Can Connect Supabase remove manual SQL setup?

### Takeaway

**Automatic setup is supported in principle, but dedicated least-privilege Pooltune role creation is not yet fully verified.** The API offers authenticated SQL execution and a separate endpoint that issues temporary native database credentials. The public SQL endpoint contract does not specify its PostgreSQL execution identity or guarantee that the intended custom `CREATE ROLE` and `GRANT` statements will succeed. A small disposable-project integration test must close that gap before promising zero-copy/paste onboarding.

### Cited findings

The current [official OpenAPI document](https://api.supabase.com/api/v1-json) exposes:

| Endpoint | OAuth scope | Request | Response / limitation |
| --- | --- | --- | --- |
| `POST /v1/projects/{ref}/database/query` | `database:write` | JSON `query` required nonempty string; optional `parameters` array and `read_only` boolean | HTTP 201; no result content schema in current spec. Experimental/Beta. |
| `POST /v1/projects/{ref}/cli/login-role` | `database:write` | JSON `read_only` required boolean | HTTP 201 with required `role`, `password`, `ttl_seconds`. No configurable role name, grants, password or TTL input. Experimental/Beta. |
| `DELETE /v1/projects/{ref}/cli/login-role` | `database:write` | No per-role selector documented | Deletes existing CLI login roles. Not a documented Pooltune-specific cleanup mechanism. |

The [SQL endpoint reference](https://supabase.com/docs/reference/api/v1-run-a-query) confirms SQL execution. The [temporary login endpoint reference](https://supabase.com/docs/reference/api/v1-create-login-role) explicitly describes a CLI login role with a temporary password. Its expiry must be read from `ttl_seconds`; the contract does not specify a fixed lifetime or custom privilege profile.

The [integration guide](https://supabase.com/docs/guides/integrations/build-a-supabase-oauth-integration#pre-fill-database-connection-details) says existing database passwords cannot be retrieved through Management API. Issuing new temporary credentials or creating a separate login is a different mechanism from retrieving that password.

[Supabase role documentation](https://supabase.com/docs/guides/database/postgres/roles) documents SQL login roles and recommends a separate database user for each service. Its description of Dashboard SQL operating as `postgres` is not evidence that Management API SQL always runs under the same identity.

### Proposed implementation, not yet a proven contract

1. Complete OAuth authorization and check granted scopes/project references.
2. Execute a fixed, versioned bootstrap SQL template using the SQL endpoint. Generate a dedicated credential server-side; do not have the model invent setup SQL or permissions.
3. Attempt a native PostgreSQL connection using that credential and run explicit privilege/capability probes.
4. Save the encrypted credential and verified capabilities; activate only permitted monitors/actions.
5. Provide the same guided SQL setup as a fallback when API execution authority or connection reachability prevents automation.

The temporary CLI route is an alternative bootstrap path worth testing, not the permanent Pooltune connector. Public issue reports indicate CLI use of `cli_login_postgres` with session-level role switching, but the OpenAPI contract does not guarantee that privilege behavior or isolation between consumers. Do not rely on those reports as a stable least-privilege contract. Reissuing or deleting shared CLI roles may affect other CLI users; isolation needs verification.

### Gaps

- SQL endpoint's actual database `session_user` and `current_user`.
- Whether the intended role creation and grant template is permitted for all supported project/organization roles and Postgres versions.
- SQL execution timeout, transactional wrapping, session affinity across requests, cancellation, and output limits.
- Custom native role access through the selected network path after setup.
- Exact permissions needed for each repair family. A metadata observer role does not automatically have index/function/table maintenance authority.

## Can bootstrap privilege be removed afterward without disturbing the connection?

### Takeaway

**Do not promise silent OAuth downscoping.** If Pooltune retains the same authorization containing `database:write`, it retains that Management API capability even if routine work uses a narrower PostgreSQL login. Runtime convention is not a credential-level permission reduction.

### Cited findings

[OAuth scopes documentation](https://supabase.com/docs/guides/integrations/build-a-supabase-oauth-integration/oauth-scopes) says scopes are configured on the OAuth app and existing users must reauthorize to apply changed scopes. Database Write covers SQL execution and other database configuration operations, so it is wider than a single role-creation permission.

The [official OpenAPI document](https://api.supabase.com/api/v1-json) provides these lifecycle contracts:

- `GET /v1/oauth/authorize`: includes optional `scope`, `project_ref`, state and PKCE inputs. A requested project is not by itself proof of final authorization restriction.
- `POST /v1/oauth/token`: exchanges authorization codes or refresh tokens. Response includes `access_token`, `expires_in`, `token_type`; normally a `refresh_token`; optional granted `scope`; and `project_refs`, where null represents all projects. Use returned values rather than inventing a fixed token lifetime.
- The token request schema includes `scope`, but that alone does not document scope reduction behavior, token-family effects, or invalidation of previously issued wider tokens.
- `POST /v1/oauth/revoke`: JSON `client_id`, `client_secret`, `refresh_token`, all required; response 204. Its purpose is revocation of the app authorization and corresponding tokens, not selective removal of Database Write while preserving other permissions.

The [integration guide's refresh section](https://supabase.com/docs/guides/integrations/build-a-supabase-oauth-integration#refreshing-an-access-token) confirms refresh support and says revocation prevents refresh and stops access tokens working.

### Product choices supported by the evidence

- Keep the authorization with Database Write and clearly disclose that scope; restrict its use inside the application and isolate stored credentials. Do not call this provider-enforced least privilege.
- Explore separate bootstrap authorization followed by revocation and a genuinely narrower ongoing authorization, but treat the exact reauthorization flow as unverified. Do not assume app-wide scope edits are appropriate for per-customer setup.
- Keep guided SQL setup if avoiding broad Management API authorization is a product requirement.

A separately created PostgreSQL credential is a separate access path. The reviewed OAuth revocation contract does not promise that it disables or deletes that custom database role. Pooltune needs explicit native credential cleanup/rotation and a clear disconnect policy. Conversely, stopping Pooltune's own scheduling immediately is under Pooltune's control.

Do not confuse Management API OAuth with the Supabase Auth OAuth-server product used to authenticate application end users. Its token lifetimes/revocation behavior are not evidence for this integration.

## Does automatic setup guarantee native connection availability?

### Takeaway

No. Provisioning credentials and reaching the database are separate checks.

### Cited findings

[Supabase connection documentation](https://supabase.com/docs/guides/database/connecting-to-postgres) documents direct connections on IPv6 unless using the IPv4 add-on, and shared session pooling as the IPv4 alternative. A custom role through the shared pooler uses `[ROLE].[PROJECT-REF]`. The pooler host cannot reliably be inferred just from the region. Transaction pooling does not preserve session-level state across transactions; direct/session connections fit tasks that require it.

[Network restriction documentation](https://supabase.com/docs/guides/platform/network-restrictions) provides the provider's network access control mechanism. Onboarding must verify reachability and handle customer restrictions rather than treating OAuth success as proof of native connectivity. Do not silently broaden network restrictions as part of setup.

## Does the agent still monitor around the clock?

Yes, as an architecture decision. Pooltune's hosted scheduler and Go collectors remain active independently of the browser. They periodically read authorized telemetry. Rules decide whether a change merits a LangGraph investigation; the model need not generate continuously. SQL bootstrap creates access, not a resident autonomous agent inside the customer's PostgreSQL server. Monitoring availability still depends on Pooltune uptime, credential validity and project reachability. Reading telemetry incurs some database work, so bounded frequency and query budgets remain necessary.

## Next verification boundary

The next useful step is a small integration spike in an explicitly selected disposable Supabase project: OAuth with minimal necessary scopes; check execution identity; apply the fixed role/grant template; connect natively; verify allowed and denied operations; exercise cleanup; confirm what happens to native and API access after disconnect/revocation. That experiment has not been run by this research task.
