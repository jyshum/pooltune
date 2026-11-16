# Repair authority for a connected Supabase project

**Status** The owner-side Go worker can link to the hosted project, read the latest repair choices and report native database permissions. Automatic action delivery still requires measured evidence from an ordinary connected query and the repair queue. The six bounded Railway repair proofs remain the hosted action baseline.

## What the current flow does

The owner picks repair families in the existing setup form. Pooltune stores those choices with the project and installs a dedicated monitor login. Only a configured Rookery fixture currently supplies the worker with the database authority to execute repairs. The [MVP plan](mvp-build-plan.md#1-connect-and-enroll-a-project) requires the ordinary connection to do this with no query IDs or table list. The [V1 product scope](../../product/query-performance-v1.md#six-repair-types-in-v1) also requires explicit enrollment for functions and scheduled jobs, including approved windows for a schedule change.

The setup credential authorizes installation. The repair runtime needs a separate, revocable way to execute the six typed operations. Selecting a repair does not authorize arbitrary SQL or let a model add an operation or target to the owner's grant.

## PostgreSQL verification

A disposable PostgreSQL 17.11 instance reproduced these results. The test admin owned two tables and one function. The connector received `MAINTAIN` on one table and `EXECUTE` on a single fixed test routine. It had no `SELECT` grant on either table.

| Check | Result |
| --- | --- |
| Targeted `ANALYZE` and plain `VACUUM` on the granted table | `00000` for both. `MAINTAIN` was absent on the other table. |
| Connector attempts a concurrent compound index, a column statistics-target change, or `ALTER FUNCTION` | `42501` for each. PostgreSQL required ownership. |
| Connector calls an owner-side SQL function containing only the fixed concurrent index operation | `25001`. PostgreSQL refused `CREATE INDEX CONCURRENTLY` from a function. |
| Owner runs the identical index statement in a native top-level session | `00000`. PostgreSQL reported a valid, ready, nonunique two-column index. |

`MAINTAIN` also allows other table maintenance operations, so it does not encode the six individual Pooltune selections. A SQL function cannot cover the native-session actions. [PostgreSQL privileges](https://www.postgresql.org/docs/17/ddl-priv.html), [CREATE INDEX](https://www.postgresql.org/docs/17/sql-createindex.html), [VACUUM](https://www.postgresql.org/docs/17/sql-vacuum.html)

## Supabase-hosted option

Provision a small repair executor inside the connected Supabase project through an Edge Function. The single short-lived setup token would add **Edge Functions Read-write** for that project alongside **Database Read-write**. The hosted Go backend would continue to discover workloads, check the owner's policy, admit evidence, record intent and verify effects. The proposed TypeScript executor is a change to the approved Go-only database tool design. It would perform a second, independent check before a native database action. The project-side executor would use a deployment-pinned version and a project-specific, revocable request identity. The setup token would no longer be needed after provisioning. [Function deployment permission](https://supabase.com/docs/reference/api/v1-deploy-a-function), [Edge Function database access](https://supabase.com/docs/guides/functions/connect-to-postgres)

The executor accepts a finite list of repair operations. It binds each request to the installation, current permission version, exact database object and owner, expiry, and previously recorded action. It derives the statement from catalog identities and the operation's limits. It does not accept caller-provided SQL. It rejects requests for disabled, replaced, or out-of-policy objects. Pause, disconnect, and revocation invalidate new work. A restarted worker reconciles an already-started action before attempting a new one.

Table discovery stays automatic within the owner's selected repair policy. For function memory and scheduling, Pooltune shows the discovered exact function or job and its proposed bounds for one-time confirmation. A Cron change also needs an owner-approved window. This follows the existing V1 enrollment rule and requires no query IDs.

Before implementing the action routes, prove that the Supabase-hosted executor can open TLS-verified, native owner-capable sessions while preserving the application's traffic path. Supabase documents a transaction-pooled connection example for Edge Functions, which is insufficient for the current session-level locks and native-session checks. The Rookery test must prove a usable direct connection and an owner-controlled way to revoke the executor. The local PostgreSQL probe does not establish either provider property.

## Owner-hosted Go path

Keep privileged execution in Go and run the same narrow interface as an owner-hosted service. The owner gives the service native database access in their own environment, and Pooltune continues to send bounded, revocable repair requests. This reuses more of the present Go executors and preserves the approved language decision. It adds a worker deployment and credential configuration to onboarding. Keeping the existing setup token in Railway for arbitrary database queries is outside the proposed designs.

## Go permission checks

The signed-in project setup lists the database requirements for each checkbox. The owner can keep the repair login and password in their own environment and run the same native check through `pooltune-project repair-access`. The command verifies the managed installation against its normal setup markers, connects to the canonical Supabase endpoint using its trusted CA, and reports each operation's current PostgreSQL permission.

Set `POOLTUNE_PROJECT_CA_FILE`, `POOLTUNE_REPAIR_DB_ROLE`, and `POOLTUNE_REPAIR_DB_PASSWORD` in the owner's environment. Run the command with the active project reference and installation key. The worker needs a route to the direct database endpoint. Rookery's direct endpoint requires IPv6. The native session identity check depends on connecting directly to PostgreSQL.

```sh
pooltune-project repair-access -ref PROJECT_REF -key INSTALLATION_KEY -scope authorized-targets.json
```

The operator scope contains the exact installation ID in `project`, its six checkbox keys in `capabilities`, and the applicable `table`, `function`, `cron`, and `cron_window` objects. Callers can supply a specific table as `{"DatabaseOID": 5, "TableOID": 16400, "Schema": "app", "Relation": "orders"}`. These object IDs identify what the native check may inspect. They are not query IDs. The regular project setup continues to discover workload without asking the owner for them. A `granted` result is a live database privilege check. The repair still requires an authenticated action request, a current owner grant, admitted workload evidence, a durable intent and independent verification.

## Link a worker to the dashboard

On a connected project, choose **Edit repair choices** to update the selected operations. Choose **Create worker key** and save the displayed value as `POOLTUNE_REPAIR_WORKER_TOKEN` in the owner-side Go worker's environment. The server retains a digest of that key. Replacing it, revoking it, or disconnecting the project invalidates the old key.

Run a single permission check using the same scope and database environment as `repair-access`.

```sh
pooltune-project repair-worker -ref PROJECT_REF -key INSTALLATION_KEY \
  -scope authorized-targets.json -origin https://pooltune-web-preview-staging.up.railway.app -once
```

Omit `-once` to check for permission changes every 15 seconds. The worker checks the overlap between its local scope and the current dashboard selection. It sends only the operation names and access statuses. A permission report shows which operations the database login could perform on the checked objects. Workload evidence, authorization at action time and result verification still gate a repair.

## Verification sequence after the design decision

1. Prove the executor's native connection, installation binding, secret isolation and revocation on disposable Rookery. Confirm with two different installations that a credential cannot authorize another installation or an unselected repair.
2. Connect the existing setup form to the verified executor. Persist the owner grant and display the actual availability of each selected operation. Test retry, pause, disconnect, and a repaired object being dropped and recreated.
3. Wire compound index and the table-maintenance operations through the existing action journal and verification. Preserve matching protected traffic and test restart after an action at least once.
4. Wire explicitly enrolled memory and Cron targets, retain the existing budgets and scheduling limits, and re-run all six checkboxes from the usual Railway form against eligible Rookery workloads.
5. Mark the connection and repair coverage cards green only after the normal route produces saved evidence for the actual grants and refusals.
