# Enrolled Supabase evidence in hosted staging

The hosted consumer can claim a queued investigation and read plan estimates through a dedicated Rookery login. The measured repair lab still has its own path. The new consumer mode saves `estimated_evidence` in Pooltune PostgreSQL and records `needs_measured_evidence`. It has no graph call or repair action.

## What passed

The test used a short-lived dedicated login, verified TLS to Rookery's session pooler, and a temporary synthetic table. Three jobs were inserted directly into Pooltune's staging queue to exercise the hosted consumer.

| Job | Result |
| --- | --- |
| 8, mismatched query ID | Saved `insufficient_evidence`. The job did not match the enrolled query. |
| 9, matching query | Saved `needs_measured_evidence` with a sequential scan estimate and no matching full B-tree index. It took **2.644 s** from the consumer starting the attempt to preparing the result for storage, including the database connection. |
| 10, table grant revoked | Saved `insufficient_evidence` without a plan. |

PostgreSQL recorded a single execution of the enrolled lookup. The traffic driver made that call. The consumer asked the planner for estimates. These runs verified the consumer and queue through staging. The detector's dedicated-login enrollment remains a separate integration. The initial run rejected an enrollment file with string-encoded OIDs. The final run used numeric OIDs and passed. Afterward, the synthetic Rookery table and login were removed, the public index fingerprint matched, and Railway returned to the measured fixture configuration. The unused job from the initial run was closed.

## Design trade-offs

- **Explicit enrollment.** The consumer matches database, role, query, and relation identities to the agreed lookup. The first hosted test covers the identity that ran the query. Visibility into other application roles still needs qualification.
- **Fresh read-only connection.** The consumer checks current grants inside a short, read-only transaction. Rechecking on each job adds connection and network time. Pooltune can evaluate pooling after measuring revocation behavior, latency, and per-customer connection budgets.
- **Separate estimates and measured results.** An estimated plan can guide further investigation. The existing index graph expects measured evidence. This mode records the estimate and leaves the follow-up decision visible. The next stage should reuse existing measurements when they are sufficient.
- **Railway staging.** The experiment reused the hosted queue and worker image. The credential was provided as a temporary service variable. Production needs a credential store, rotation and a disconnect workflow. [V1 build sequence and hosting alternatives](roadmap.md)

## Runbook

The staging launcher accepts `POOLTUNE_STAGING_ROLE=enrolled-consumer`. Provide the dedicated TLS connection through `POOLTUNE_WORKER_TARGET_DSN`, the Rookery CA through `POOLTUNE_STAGING_ROOKERY_CA`, and the matching project and enrollment through `POOLTUNE_STAGING_PROJECT` and `POOLTUNE_STAGING_LOOKUP`. Enrollment uses numeric database, role, and table OIDs and a string for the signed 64-bit query ID. The representative integer comes from enrollment. The job must carry the matching PostgreSQL counter generation.

After the test, return the consumer to its measured fixture configuration and remove temporary credentials and test objects. The [dedicated login test](dedicated-login.md) covers fresh authentication and the distinction between removing table access and disabling login.
