# Dedicated Rookery login and revocation

Pooltune connected to Rookery as its own temporary PostgreSQL login using the Supabase session pooler and verified TLS. PostgreSQL reported the temporary login as both the session identity and the effective role. A separate bootstrap connection created the disposable schema and granted access. The evidence collector operated through the dedicated connection.

## What passed

| Check | Rookery result |
| --- | --- |
| Collect evidence for the enrolled query | Collected the estimated sequential scan plan and the existing-index check. The final check took **221.179 ms** on an open connection. |
| Avoid repeating the query | PostgreSQL still counted exactly one execution after evidence collection. The test driver made that call. |
| Bound privileges | PostgreSQL denied a read from a second fixture table and a write to the enrolled table. |
| Revoke the enrolled table grant | A new evidence check through the same open connection returned no evidence. A direct lookup was denied with SQLSTATE `42501`. |
| Disable the login and clear its password | The open connection still answered `SELECT 1`. A fresh connection using the former credential was rejected by Supavisor with `EAUTHQUERY` (SQLSTATE `XX000`). Local PostgreSQL rejected it with `28P01`. |
| Clean up | Subsequent catalog checks confirmed that the fixture schema and test login were gone. |

The 221 ms measurement includes a read-only transaction, the plan estimate and the catalog check. It is one observation. Connection startup and model requests were outside that measurement.

## Trade-offs

- **Separate login.** Pooltune can use a narrower identity for routine reads than the account used for setup. The test granted schema access and read access to one fixture table. Policies for permissions inherited from PostgreSQL's public defaults and application-role access across customers still need qualification.
- **Two separate revocation steps.** Removing table access stops further reads at PostgreSQL's next permission check, even on sessions the pooler keeps open. Disabling login and clearing its password addresses new connections. A complete customer disconnect will also need to stop queued work and close Pooltune's sessions. The test applies to subsequent statements after revocation, with no query already in flight. Pooler authentication and password rotation behavior can differ by route. [PostgreSQL role settings](https://www.postgresql.org/docs/17/sql-alterrole.html) · [Supabase custom-role and pooler behavior](https://supabase.com/kb/troubleshooting/fatal-password-authentication-failed)
- **Temporary fixture.** The test uses a random password with a two-minute lifetime and sends only its SCRAM verifier in role DDL. Successful authentication through a separate connection requires committing the fixture first, so an interrupted test can leave test objects for cleanup. Cleanup is registered before setup and reconnects independently, so it can recover if setup committed but the original connection lost the reply. Cleanup checks the generated schema's ownership marker before removing the fixture. Treat the SCRAM verifier as sensitive even though it contains no cleartext password. Rookery's application tables were outside the test.

This qualifies one integer lookup under the identity that ran it. It still takes an explicit design for reading statistics across application identities and for enrolling independently managed credentials in staging. The evidence remains estimate-only.

## Reproduce

With the repository Go toolchain and PostgreSQL binaries on PATH:

```sh
go test -tags=integration -count=1 -v ./internal/hostedquery -run '^TestDedicatedEvidenceLoginLocal$'
```

For Rookery, supply the verified session pooler DSN and CA through `POOLTUNE_EVIDENCE_ROOKERY_DSN` and `POOLTUNE_LAB_ROOKERY_CA_FILE`, then run:

```sh
go test -tags=integration -count=1 -v ./internal/hostedquery -run '^TestDedicatedEvidenceLoginRookery$'
```
