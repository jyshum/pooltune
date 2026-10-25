# Short live confirmation, October 25, 2026

Azure's first spike improved against the earlier fixed-50 measurement. Google did not reach traffic readiness, so its benefit remains unconfirmed. This is not an all-provider release pass.

| Azure mode | First spike completed / 3,000 | Successful p95 | All completed / 14,500 |
|---|---:|---:|---:|
| Prior fixed 50 | 1,956 | 894.810 ms | 9,516 |
| Current Pooltune 50 to 64 | 2,459 | 694.057 ms | 10,614 |
| Prior fixed 64 | 2,469 | 691.199 ms | 11,825 |

Pooltune completed 503 more first-spike requests than the earlier fixed-50 run. Successful p95 was 200.753 ms lower. Reserve was first observed at 10.140 seconds, about 140 ms after the scheduled spike. This is sampled reaction time, not a guarantee. The equally warm fixed-64 baseline performed comparably during the first spike and better overall. The baselines came from earlier runs; conditions and timing differ. No cost-saving or superiority over a correctly sized fixed pool is established.

Azure's full trial was not clean: 2,261 generator queue rejections, 96 SQLSTATE 57014 errors and 1,529 mixed transport/deadline failures. At 40.18 seconds, the 09:37:00 source pressure sample crossed the 120-second age limit. Admission resumed at 45.28 seconds when the 09:38:00 sample arrived. SQL inventory remained current in the sampled hold trace. The existing freshness stop remains enabled. This leaves sustained availability under delayed Azure metrics unresolved.

Google authenticated and established 64 distinct database connections, but startup failed before traffic with `pool connections settling: login=0 tested=0 used=64`. Our readiness code rejected PgBouncer's normal idle state. Its `sv_used` connections await a liveness check before reuse, rather than a new login. A local real PostgreSQL/PgBouncer regression reproduced the false stop; the follow-up counts established used/tested connections while keeping exact backend identity, connection budget and PgBouncer's liveness checks. The follow-up is not live-tested on Google. [PgBouncer state definitions](https://www.pgbouncer.org/usage.html#show-pools).

Supabase's accepted prior result is unchanged: all 1,430 requests completed, first-spike 300/300 versus 208/300 fixed-5, and comparable performance to fixed-8. No new Supabase test or provider resize was performed.

The cloud window ran parallel setup and at most one adaptive-only 60-second workload per provider. Google failed setup, so only Azure executed traffic. No second cloud run was started. Each workload uses read-only `SELECT pg_sleep(0.2)`, not a representative production query trace. Priority scheduling, AI, automatic backup routing and production recovery remain unverified or deferred.

Raw exports, summary and the immutable tested-runner hash are saved in `quick-confirmation-evidence/` for the user copy and `docs/evidence/quick-confirmation-2026-10-25/` in the repository. Source changed afterward for the local readiness fix; it is not the same as the cloud-tested binary.
