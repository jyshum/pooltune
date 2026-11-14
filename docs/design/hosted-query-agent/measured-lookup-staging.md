# A measured query plan in the hosted index investigation

Pooltune can now take a slow enrolled lookup from Rookery traffic through to an index recommendation in the staging investigator. The detector notices sustained slowness. The consumer verifies the exact lookup and checks whether an index already covers it. If the current plan predicts a table scan and the traffic evidence is fresh, the staging consumer can measure a single read. LangGraph receives the actual scan work and the existing-index check together. The original enrollment path can still save estimates without executing another read.

## Design choices

- **Spend the extra read only when it can change the decision.** Staging had timing and page counts for the whole query, plus a planner estimate. The measured plan adds how many rows the scan inspected to return the answer. The consumer skips the read for an existing matching index, stale or missing timings, a different plan shape, or a table outside the marked fixture.
- **Keep the cost bounded.** The read uses the enrolled integer input inside the same read-only transaction as the estimate and catalog check. It has a 750 ms statement limit, immediate failure on the initial table lock if busy, and no parallel query workers during the measured read. The fixture query names only the enrolled table, so an inherited child cannot add to the read even if attached concurrently. Plan instrumentation records rows and page counts while leaving per-node clock timing off. PostgreSQL still reports total execution time. [PostgreSQL EXPLAIN](https://www.postgresql.org/docs/17/sql-explain.html)
- **Allow at most one measurement per job.** The queue records each attempt before work starts. A replacement consumer can recheck the estimate after interruption. It skips the measured read on attempt two, even if the first attempt stopped before executing it. This trades completeness for a predictable limit. Keep the staging enrollment temporary, since a later qualifying job has its own attempt budget.
- **Reuse the existing investigator.** This run uses the real TypeScript LangGraph workflow with its scripted recommendation. It checks whether the workflow can consume the measured plan and persist the recommendation without introducing model network latency. The regular enrolled estimate mode remains available.

## Runbook

Use a short-lived, dedicated `pt_login_` role and a private schema of the same name. Mark only the synthetic candidate table with the comment `pooltune measured staging fixture <project>`, where the project is `staging/rookery/<schema>`. Use a bound integer and `FROM ONLY` in the fixture driver. PostgreSQL assigns a different query ID to the `ONLY` form. Enroll that ID in both the detector and the consumer.

Switch the Railway consumer to `measured-enrolled-consumer` for the experiment. Verify the healthy lookup remains under its staging objective, the candidate opens one job, and the recommendation is saved with graph checkpoints. Check the candidate still has zero indexes and that PostgreSQL recorded one measured statement. Restore both workers to the normal fixtures and remove the temporary role and tables.

## Final Railway staging result

The driver ran 240 paired reads against two synthetic 100,000-row tables with a 3 ms fixture target. The unindexed lookup averaged 7.983 ms and created job 18. The indexed lookup averaged 0.371 ms, returned `no_change`, and created zero jobs.

| Step | Result |
| --- | --- |
| Traffic start to queue | 18.130 s with shortened staging windows |
| One measured lookup | 7.789 ms in PostgreSQL. It examined 100,000 rows and recorded 443 shared-page accesses to return one row. |
| Scripted LangGraph workflow | 0.441 s, including checkpoint work |
| Queue to saved recommendation | 3.829 s on attempt one |
| Database and graph checks | One measured utility statement, four saved checkpoints, and zero candidate indexes |

PostgreSQL also counted 248 ordinary calls to each lookup, including warmups. It recorded the measured read as a separate utility statement. The graph and queue timings cover different portions of the handoff. The queue duration includes waiting for the consumer and its database calls. Single-run timings do not establish the production cost of the measurement or the speed benefit of applying the index.

The temporary Rookery schema and login were removed, the public index fingerprint matched, and Railway returned to the normal fixture configuration.

Local integration coverage includes estimate-only mode, missing and stale evidence, an existing index, an unmarked table, the concurrent table change, and no replay on a retry. The race-enabled Go suite, Go vet, investigator tests, TypeScript checks, and launcher syntax passed.

## Qualification notes

A Railway build exceeded the first harness wait limit before any traffic ran. Cleanup restored the prior fixture, and the next attempt used a longer deployment wait. The first completed run reached job 16 and successfully saved a recommendation. Independent review then found an edge case in which concurrent PostgreSQL inheritance changes could extend the lookup to another table. The final version pins the statement to the single enrolled table. A local PostgreSQL regression test attaches a child from another session between the estimate and the measured read and verifies the measured row count stays at the enrolled table's 30,000 rows.

## What this unlocks

The earlier traffic and page counts told us which lookup deserved attention. The measured plan tells us whether it was doing avoidable scan work. That gets the hosted path to a typed recommendation for the first index repair. Customer execution and post-repair measurement are separate steps. They require the right grants, a load check, and verification of both faster reads and acceptable write cost. [V1 roadmap](roadmap.md)
