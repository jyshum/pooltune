# Evidence for an enrolled Supabase query

We tested the smallest version of customer-side evidence collection. Given one enrolled integer lookup, Pooltune can ask PostgreSQL how it plans to find the row and check whether a matching B-tree index exists. This test used a disposable schema and a temporary restricted database role on Rookery.

## What passed

- Before an index existed, PostgreSQL planned a sequential scan. After the test added a full B-tree index, the plan changed and the index check found it.
- PostgreSQL recorded exactly one execution of the lookup, from the traffic driver. Evidence collection used `EXPLAIN` without `ANALYZE`. It asked for PostgreSQL's estimate without running the lookup. This estimate has its own data format and cannot enter the existing measured-evidence graph. [PostgreSQL EXPLAIN documentation](https://www.postgresql.org/docs/17/sql-explain.html)
- A stale counter, wrong statement ID, wrong role or mismatched table yielded no evidence. The role could read the enrolled table, and PostgreSQL denied a write to it and a read from a second fixture table. Revoking read access stopped evidence collection.
- The fixture and temporary role rolled back together. A subsequent catalog check confirmed both were gone.

The final integration run took **2.060 ms locally** and **168.963 ms from the laptop to Rookery** for the first estimate and index check. Each number is a single observation. The network and several database round trips contribute to the Rookery result. The timing excludes model calls and any repair.

## Trade-offs

| Choice | Benefit | Cost |
| --- | --- | --- |
| Explain the plan without replaying the query | Adds planning and catalog work without repeating the scan. | The estimated plan tells us what PostgreSQL expects. We still need measured evidence to judge impact or qualify a repair. |
| Match an exact enrolled query | Statement, role, database and table identities tell us whether the evidence belongs to the right lookup. A counter reset invalidates earlier observations. | This supports one-column integer lookups on ordinary tables. Joins and tables with row security need separate qualification. |
| Read under a restricted role | The test proves PostgreSQL enforces the role's table grants. Rookery also needed schema access to its statement statistics. | Pooltune used the bootstrap connection to switch into a temporary role. A dedicated login, revocation across connections and parity with application roles still need testing. Roles may inherit permissions from PostgreSQL's public defaults. |
| Check for a full B-tree index | Detects a live, ready, valid B-tree whose first column is the lookup column and uses PostgreSQL's default integer operator class. | A negative result excludes that index shape only. A partial index, another operator class or another index type may still serve some lookups. |

The evidence reader takes a short shared table lock. The caller must end its transaction promptly. Before connecting it to the hosted consumer, use a bounded read-only transaction and account for plan or catalog changes while collection is in progress. Reading statistics for a different application role also needs a separate access decision. PostgreSQL restricts who can see another role's query identifiers. [PostgreSQL statement statistics](https://www.postgresql.org/docs/17/pgstatstatements.html)

A follow-up test verified a separate Rookery login and the difference between revoking a table grant and disabling login. [Dedicated login and revocation](dedicated-login.md)

## Reproduce

Run with PostgreSQL 17 binaries on PATH and the repository's Go toolchain.

```sh
go test -tags=integration -count=1 -v ./internal/hostedquery -run '^TestEnrolledEvidenceLocal$'
```

For the authorized Rookery test, supply `POOLTUNE_EVIDENCE_ROOKERY_DSN` and `POOLTUNE_LAB_ROOKERY_CA_FILE` privately, using the same verified session-pooler route as the [index lab](index-slice.md#reproduce-and-inspect).

```sh
go test -tags=integration -count=1 -v ./internal/hostedquery -run '^TestEnrolledEvidenceRookery$'
```
