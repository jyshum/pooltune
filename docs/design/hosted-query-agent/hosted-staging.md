# Hosted detector and consumer staging

[Pooltune Staging](https://railway.com/project/a32a2ecd-2d0f-43d0-b473-3d43cff623e0?environmentId=0541335c-4db7-40cf-a75d-761ad418e816) runs three services in a private Railway environment. One Go process detects a sustained slow query and adds a job. Another claims it and asks the scripted LangGraph investigator for a recommendation. A persistent PostgreSQL volume stores measurements, jobs and graph checkpoints.

The test database contains a journaled fixture with an indexed control and an unindexed lookup, each seeded with 100,000 synthetic rows. The detector and consumer share Pooltune's state database. Test data lives in a separate database on the same PostgreSQL instance.

## Restart exercise

Staging uses a two-second sampling interval and four-second windows to exercise the same decision rule quickly. A bounded traffic driver sends repeated reads with bound parameters. PostgreSQL assigned different query IDs to literal inputs in the first attempt, so the detector correctly found no evidence for the enrolled queries. The hosted detector uses PostgreSQL's statement counters to queue the slow lookup. The test uses a temporary database lock to leave the consumer's first claim unfinished. Railway restarts the consumer service. PostgreSQL and the detector stay running.

The final exercise queued job **4** for the unindexed lookup. The temporary lock caused attempt one to stop with its 30-second claim still open. After a Railway restart, the replacement completed the same job on attempt **2** and saved a scripted index recommendation **0.899 seconds after the original claim expired**. Pooltune's database recorded four checkpoints on `worker-4-2`. The exercise created one job across both queries. The measured plan scanned 100,000 rows to return one, and the catalog check found no covering index.

The first hosted restart preserved and reclaimed its job but exhausted retries when the plan reader rejected the PostgreSQL 18 JSON value `1.00`. The reader now accepts exact whole-number counts in either format and rejects fractional counts for the current single-loop fixture. The repeated exercise verified the fix on PostgreSQL 18.6.

Both application services and PostgreSQL are still running in private staging, with the isolated fixture enrolled for another test. The traffic driver was stopped and the temporary lock released. The graph ran in scripted mode and saved a recommendation. This exercise made no automatic index change.

## Trade-offs

| Choice | Why it fits this stage | Cost |
| --- | --- | --- |
| Separate detector and consumer services | Railway can restart the consumer while the detector continues watching. | Two running processes have separate logs and deployment settings. |
| One PostgreSQL instance, two databases | The test uses durable queue storage and keeps synthetic tables out of Pooltune's state database. | Both databases share resources, a service account and a failure domain. This setup cannot validate customer isolation or quantify customer overhead. |
| Private Railway networking | Workers and PostgreSQL need no public endpoints. Connections use the environment's encrypted WireGuard network and an explicit staging-only route. | The staging route relies on Railway for transport protection. Customer connections need their own verified TLS and least-privilege roles. [Railway private networking](https://docs.railway.com/networking/private-networking/how-it-works) |
| Scripted LangGraph investigator | It exercises the same graph and checkpoint writes with a predictable outcome. | This exercise does not measure Claude latency or retry billing. Previous live model checks remain separate. |
| 30-second claim timeout | A replacement can recover abandoned work while a second consumer waits. | Recovery can wait up to the remaining claim duration plus the next queue check. |
| Upload the local branch to Railway | The staging build uses the local changes without requiring a GitHub push. | Repeat the upload after code changes. Application releases and service settings are managed separately. |

## Runbook

The image builds both Go commands and the fixture tool. Both app services use `/usr/local/bin/pooltune-staging` as their start command. Set one replica, restart policy **Always**, and sleep disabled.

Both services need the following settings:

- `POOLTUNE_STAGING_PRIVATE_STATE=1`
- `POOLTUNE_WORKER_STATE_DSN` references Railway's private Postgres URL for the `railway` database.
- `POOLTUNE_WORKER_TARGET_DSN` references the same private instance and uses the `pooltune_fixture` database. Enable `pg_stat_statements` there.
- `POOLTUNE_STAGING_ROLE=detector` or `consumer`, respectively.

From a container with those connections, run:

```sh
POOLTUNE_LAB_STATE_DSN="$POOLTUNE_WORKER_STATE_DSN" \
POOLTUNE_LAB_TARGET_DSN="$POOLTUNE_WORKER_TARGET_DSN" \
pooltune-query-lab -target staging -create-consumer-fixture
```

Pass its enrollment JSON to the detector as `POOLTUNE_STAGING_ENROLLMENT`. Give the consumer the matching `POOLTUNE_STAGING_PROJECT` and `POOLTUNE_STAGING_FIXTURE`. Upload a source bundle containing the Dockerfile, Go commands and modules, the investigator and the staging launcher. [Railway CLI deployment](https://docs.railway.com/cli/deploying)

For a new restart exercise, run one bounded, sequential pair of lookups about every 100 ms. Use PostgreSQL's extended protocol (for example, psql's `\bind` followed by `\g`) with `$1` so the detector sees the enrolled IDs. Run the healthy lookup early, then keep the slow workload going long enough to form five windows. Take a short-lived exclusive lock on the fixture's healthy table before the first claim. The consumer checks both fixture tables and gives up on that lock quickly, while the driver can keep querying the candidate. Release the lock before the claim expires. Check the job in Pooltune's state database. Verify that the first claim is open, restart **Consumer**, then confirm that the same job ID completes on the next attempt with a saved outcome and graph checkpoints. Stop the synthetic driver when done. To retire a fixture, stop both workers and use `pooltune-query-lab -target staging -remove-consumer-fixture <id>` with the same connections and journal. Preserve the state database until cleanup succeeds.
