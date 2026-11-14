# Observing how much work the lookup did

A slow query can spend time touching many pages of a table or index. PostgreSQL keeps a running count for each tracked statement. The detector already reads this count with the call count and execution time. Pooltune now keeps the difference from each of the five decision windows and saves it with the investigation. This lets the consumer compare work per call without re-running the lookup or adding another database round trip for the new evidence.

## Design choices and trade-offs

- **Reuse whole-query counts.** A shared-page access can be a hit in PostgreSQL's cache or a read into that cache. Repeated touches count again. This tells us how much page work the statement did across normal calls. It does not measure physical disk reads, count distinct pages, or identify which step of the query did the work. [PostgreSQL statement statistics](https://www.postgresql.org/docs/17/pgstatstatements.html)
- **Keep the numbers with the original timing windows.** Both measurements use the same completed calls, statement identity, and counter generation. Older or incomplete jobs keep their valid timings and show the page count as unavailable. A measured zero remains a valid count.
- **Keep repair decisions behind measured plan evidence.** A measured plan can show rows examined and page accesses at the specific scan. A whole-query count and a planner estimate can help decide whether that would be worth gathering. They cannot establish the impact of creating an index. [PostgreSQL EXPLAIN](https://www.postgresql.org/docs/17/using-explain.html)

## First staging run

With the same synthetic fixtures as Task 15 and a 1 ms target, the unindexed lookup used 443 shared-page accesses per call. The indexed lookup used 3. Both queries breached the latency target during the first five shortened windows. The indexed query ranged from 1.129 to 1.557 ms in those windows, then returned to `no_change`. The detector was responding to real latency. Only the unindexed query had a matching consumer enrollment. Its result, job 13, kept the page counts. The indexed job ended as `insufficient_evidence`.

The initial test expected the indexed query to stay below 1 ms throughout. We reran with a 3 ms fixture target to check the page-count transfer with a control that stayed below its objective. The production detection policy did not change.

## Staging rerun

The run sent 240 paired reads against two 100,000-row fixtures. Both queries used a 3 ms objective and the detector sampled every 2 seconds in staging windows of at least 4 seconds.

| Measure | Unindexed lookup | Indexed lookup |
| --- | ---: | ---: |
| Mean server execution time | 7.571 ms | 0.258 ms |
| Shared-page accesses per call | 443 | 3 |
| PostgreSQL call count, including eight warmups | 248 | 248 |
| Investigation jobs | 1 | 0 |

The slow lookup opened job 15 after 19.099 seconds. The consumer saved its first result 2.914 seconds after queue creation. The saved counts matched the original five windows exactly, totaling 42,971 page accesses over 97 reads. PostgreSQL counted only the driver's executions. The consumer recorded `missing_measured_plan`. The test stopped before any model call or repair. After both runs, Railway returned to its normal fixture configuration. The temporary Rookery logins and tables were removed, and the public index fingerprint matched its pre-test value.

Local PostgreSQL integration tests cover preserving the existing behavior for an old queue record, omitting invalid page counts, and retaining a measured zero. The race-enabled Go suite, Go vet, investigator tests and TypeScript checks passed. Independent review found no blocking issues.

## Path toward production

The next useful qualification is a measured plan for the exact enrolled query. First look for an existing authorized measurement that matches the query, input, role, and current plan. The ordinary statement counters cannot provide this on their own. Supabase exposes slow-plan logging through PostgreSQL’s `auto_explain`. Reading a plan that the project already captured could avoid another execution. Enabling new logging introduces collection overhead and can capture query text or input data. The test used the existing logging settings. [Supabase log configuration](https://supabase.com/docs/guides/database/postgres/postgres-log-config) · [PostgreSQL auto_explain](https://www.postgresql.org/docs/17/auto-explain.html)

If no match is available, a single explicitly permitted, time-limited measured read on the synthetic fixture is the strongest next test. Record its cost and compare the slow and healthy decisions. This preserves the default path of watching traffic and limits additional work to cases where the result can change the decision.

Production rollout also needs evidence under normal application login roles with explicit grants, representative query traffic, and verified index execution and recovery. The short staging window measures the handoff under sustained synthetic traffic. Compare production detection time, queue wait, evidence collection, and model time individually before changing thresholds or polling frequency. [V1 rollout and remaining repairs](roadmap.md)
