# Connect Supabase for read-only monitoring

## First slice

An operator can register a verified native Supabase connection and monitor statements already executed by an application's existing database role. The project owner selects exact statements and mean-latency objectives in a private database allowlist. Pooltune samples only the resulting counters and uses the existing detector and durable investigation queue. This milestone stops at that queue. It grants no query replay, model-provider data access or repair permission.

This implements part of accepted d-onboarding r1 and preserves d-language r2, d-workflow r1, d-hosting r1, and d-reuse r1. d-identity remains proposed for broader diagnostic and repair access. Setup can be operated internally with a SQL client. Monitoring runs as a Pooltune process without customer software or a connection-path change. Account sign-in, automated provider provisioning, ordinary-query investigation readers and repair grants remain product work.

## Connection and consent

- Guided SQL creates a unique NOLOGIN role and matching private schema in one transaction. The owner uses PostgreSQL's password prompt before enabling login. Pooltune receives only the dedicated password via its runtime secret environment and a Supabase CA file. Its state database stores the project identity, sampling history and queue, never the password or admin credential.
- Connect via the canonical direct Supabase hostname derived from the project reference. Use verified TLS and check that PostgreSQL itself sees TLS. The Rookery pooler test verified the front connection but PostgreSQL reported no TLS on the backend connection. The direct route verified TLS 1.3 in a read-only preflight. IPv4-only operators need a supported direct route before using this slice.
- Enrollment is exact by database, application role OID and statement ID. The database owner can find those identities from top-level pg_stat_statements rows. Slot constraints cap the list at 20 and objectives must be finite and positive. The monitor samples one read-only result per poll. No SQL text, parameters, application rows, plans or unrestricted statistics leave the project. An absent counter yields insufficient evidence. Reset, restart and eviction invalidate comparisons as in the existing detector.
- PostgreSQL restricts visibility of other roles' query IDs. A fixed owner-installed SECURITY DEFINER reader projects counters from pg_stat_statements(false) through the owner allowlist. It uses a fixed safe search path, qualified relation names, an authenticated-session role check and no runtime dynamic SQL. The schema and routine deny PUBLIC access and only the dedicated login receives the required usage and execute grants. It refuses disabled, expired or elevated monitor roles, including acquired memberships. Each call uses read-committed isolation so a stale transaction snapshot cannot retain an old allowlist. The login gets no app-table grants and no pg_read_all_stats membership. Existing project privileges inherited from PUBLIC still apply and should be reviewed by the owner.
- Disconnect sets the Pooltune project inactive and retires its queued work in the state transaction that serializes polling. The process opens no target session for a disconnected project when started again. A previously idle monitor closes its connection when it next checks state. The owner can revoke the routine's execute privilege or disable role login for the next database read, including an existing session. A statement already in flight can complete. Stopping the worker and removing the runtime secret finalize the operator disconnect. Rotation uses the database password prompt and a runtime-secret replacement, then verifies the new connection. Removing a query or changing its objective retires the old pending work on the next poll.

## Trade-offs

Guided SQL is the smallest usable route to test the permission contract. Automated Supabase OAuth and Management API setup could reduce setup steps. It needs account isolation, explicit token scopes and a lifecycle for privileged installation credentials, so this work leaves the provisioning decision open.

A direct grant of pg_read_all_stats to the login is easier to install. It exposes cross-role statements and broader statistics even if Pooltune filters internally. The fixed private reader makes PostgreSQL enforce the projection, at the cost of a small installed database contract.

Counter-based detection cannot establish a cause or authorize EXPLAIN under another role. The current investigator requires authentic relation and plan evidence. Ordinary-query evidence adapters are a separate implementation step before investigation and before any model or repair qualification.

[Supabase connection routes](https://supabase.com/docs/guides/database/connecting-to-postgres) · [Supabase TLS](https://supabase.com/docs/guides/platform/ssl-enforcement) · [PostgreSQL statement statistics](https://www.postgresql.org/docs/17/pgstatstatements.html)

## Operator runbook

Build `cmd/pooltune-project` with the repository Go toolchain, or use the binary in the Pooltune image. Run the long-lived monitor in the Pooltune environment with access to the direct endpoint. The development command accepts the existing disposable-local or private Railway state route.

1. Take the project reference and server CA from the Supabase dashboard. Choose and keep one installation key with `openssl rand -hex 8`. Set `PROJECT_REF` and `INSTALLATION_KEY` in the operator shell. Neither is a password.
2. Generate the credential-free install SQL with `pooltune-project setup-sql -ref "$PROJECT_REF" -key "$INSTALLATION_KEY"`. Run it as the project database owner on `postgres`. The setup requires the statement statistics extension with its per-entry generation timestamp. The schema and login will both be named `pt_monitor_<INSTALLATION_KEY>`.
3. In an owner `psql` session use `\password pt_monitor_<INSTALLATION_KEY>` to set a fresh generated password privately. Enable the role with `ALTER ROLE pt_monitor_<INSTALLATION_KEY> LOGIN;`. Supply the password as `POOLTUNE_PROJECT_PASSWORD` in the monitor's secret environment and the CA bundle path as `POOLTUNE_PROJECT_CA_FILE`. Use `POOLTUNE_WORKER_STATE_DSN` for Pooltune state, separate from the monitored database.
4. Run a representative application request through its existing role. As the database owner, look up its exact statement identity. Substitute the installed extension schema for `extensions` if necessary. This owner-side query shows normalized SQL locally for identification. Pooltune's monitor never runs it.

   ```sql
   SELECT userid AS role_oid, userid::regrole AS role_name, queryid, query
   FROM extensions.pg_stat_statements
   WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
     AND toplevel AND userid = 'your_application_role'::regrole
   ORDER BY calls DESC
   LIMIT 20;
   ```

5. Register each selected identity in a distinct slot. Replace the schema name, exact OID and exact signed statement ID below. Choose the latency objective in milliseconds. A statement ID can change after a major database upgrade or object recreation. Look it up again before reenrolling.

   ```sql
   INSERT INTO pt_monitor_<INSTALLATION_KEY>.enrolled_queries
       (slot, role_oid, query_id, mean_execution_ms)
   VALUES (1, 12345, -1234567890123456789, 25);
   ```

6. Run `pooltune-project connect -ref "$PROJECT_REF" -key "$INSTALLATION_KEY"` to verify and register. The response lists only exact enrolled identities and objectives. Run `pooltune-project run -ref "$PROJECT_REF" -key "$INSTALLATION_KEY"` under Pooltune's process supervisor. It polls every minute and groups observations into five-minute windows by default. Inspect its JSON windows and queued investigation IDs. The default detector waits for five complete windows before a decision. Change an objective in the owner registry, or delete a slot to unenroll it.
7. Run `pooltune-project disconnect -ref "$PROJECT_REF" -key "$INSTALLATION_KEY"` against Pooltune state. Wait for the monitor process to exit on its next poll, or stop it with its process supervisor. Disable the database login and remove its password with `ALTER ROLE pt_monitor_<INSTALLATION_KEY> NOLOGIN PASSWORD NULL;`; revoke execute access on the matching `snapshot()` routine. Remove the runtime secret. Historical results remain in Pooltune state. When removing the installation permanently, drop the exact routine and table before dropping their schema, revoke the database CONNECT grant and drop the role.

For manual rotation, disconnect the old generation, disable the role, use `\password` to replace the password, reenable login, and replace the runtime secret. Run `connect` and restart `run` after verification. The old generation cannot resume after reconnect. Automated rotation, end-user setup screens and distinct account ownership remain unimplemented.

## Qualification on November 14, 2026

The Go race-enabled tests passed locally and against real Rookery PostgreSQL 17 over direct TLS, with a disposable local Pooltune state database. Test-owned app-role traffic and its tables were separate from the dedicated monitor. The monitor saw the two enrolled identities and excluded another statement executed by the same app role.

| Rookery measurement | Result |
| --- | --- |
| Slow synthetic statement | 4.147 ms mean in the final detector window. One investigation queued. |
| Healthy synthetic statement | 0.049 ms mean in the final detector window. No investigation queued. |
| Built command verifies project and both enrollments | 0.489 s |
| Built command starts monitoring and queues the slow statement | 5.290 s with one-second test windows |
| Built command disconnects and the worker exits | 0.834 s |

The test proves application table and enrollment write denial independently of the client's read-only setting. It also covers refusal under repeatable-read isolation, immediate refusal on the next call of an already-open session after disabling login, direct password rotation with SQLSTATE 28P01 for the previous password, objective changes, unenrollment, and disconnect/reconnect. The fixture consumer cannot claim these jobs. The disconnect command retired the new pending job and the monitor process stopped. A separate readback found zero test schemas, roles and monitor sessions. The short test settings are not production detection timings.

The initial Rookery test stopped at a too-specific assertion. The session read-only setting returned 25006 before the table-grant check. The revised probe tested the database grant separately. Independent source review found the stale transaction and old-objective ticket issues. Both were reproduced locally, fixed, reverified on Rookery and rereviewed. The final Go race/unit suite, Go vet and focused existing collector, queue, recovery, and consumer PostgreSQL integration tests passed.

Verification boundary: the binary ran against live Supabase with local disposable state. Railway deployment and continuous hosted operation for this new monitor remain to be verified. Ordinary enrolled-query evidence, model calls and repairs were outside this test.
