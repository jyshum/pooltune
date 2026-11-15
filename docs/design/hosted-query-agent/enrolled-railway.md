# Enrolled query monitoring and diagnosis on Railway

## Build track

| Task | What passed | Where it ran |
| --- | --- | --- |
| 36. Connect and enroll | Dedicated TLS connection, app-role monitoring, slow/healthy separation, disconnect and manual rotation | Live Rookery with a local operator process and disposable local state |
| 37. Read evidence and diagnose | A real enrolled lookup reached Sonnet with owner-approved metadata and measured counters | Live Rookery with a local operator process and disposable local state |
| 38. Deploy the full flow | Railway detected a slowdown, queued it, ran Sonnet and saved the diagnosis to Railway Postgres | Private Railway staging, live Rookery, existing Railway state database |

This closes the deployed-monitor verification item under [Connect Supabase](roadmap.md). The operator path now works from connection through a saved diagnosis. Customer sign-in, setup screens and hosted secret lifecycle remain on that track. Additional query shapes, controlled experiments on those shapes, and repair targeting remain on their respective tracks.

## Why the five Database Tools cards stay yellow

| Card | Verified piece | Still missing |
| --- | --- | --- |
| Inspect existing evidence | An owner-approved lookup combines measured latency and page accesses with current table and index metadata. Fixture readers cover the six tested repair cases. | Authorized live plans and more readers for ordinary enrolled queries, including estimates versus actual rows, cleanup visibility, attributed spill and job history. |
| Run a justified experiment | The owned repair cases ran controlled before-and-after tests. | Choose and budget experiments for ordinary enrolled traffic, with representative inputs and resource headroom. This monitor reads existing evidence for diagnosis. |
| Add a nonunique index | Single-column and ordered two-column fixture builds passed. | Choose authorized real columns and their order for a supported customer predicate. The current compound executor still constructs the fixed lookup and padding keys. |
| Improve statistics detail | Joint most-common-value statistics passed for one two-column fixture. | Select real columns and implement bounded individual-column statistics targets with prior values and restoration. |
| Perform targeted cleanup | Bounded plain VACUUM passed. | Detect recurring late cleanup and adjust a table's cleanup thresholds within approved limits. |

The Build Status overview contained stale hard-coded text that still described connecting queries as future work. The updated overview reads milestones from the same data as the cards. The Database Tools view now shows a green card for the exact enrolled evidence that passed, alongside the amber inspection family.

## Deployment and Rookery verification

The existing [Railway staging project](https://railway.com/project/a32a2ecd-2d0f-43d0-b473-3d43cff623e0?environmentId=0541335c-4db7-40cf-a75d-761ad418e816) has a dedicated private **Enrolled Queries** service. Source commit `e5b4dcf` was uploaded from a clean archive. The active verification used deployment `6e2376c3-929b-46eb-b3c2-0d916a046c7d`. It used the existing private Postgres reference and verified direct Supabase TLS. One process runs the monitor and diagnoses queued jobs with independent state sessions. The diagnosis path opens a second Supabase session only for an eligible job. Full model prose and query measurements stay in Pooltune state.

The first active deployment failed at the Rookery connection. Railway resolved the endpoint to IPv6 and the staged egress change had yet to take effect. The scoped live service update enabled outbound IPv6, and the next deployment registered successfully. The existing Detector, Consumer and Postgres deployment IDs stayed the same. [Railway outbound networking](https://docs.railway.com/networking/outbound-networking)

Run on November 14, 2026, using one owned unindexed lookup and one owned indexed control, each on 100,000 synthetic rows. The test used two-second polls and four-second detector windows. The owner approved diagnosis for the exact slow statement.

| Measurement | Result |
| --- | --- |
| App traffic | 80 slow/healthy pairs, zero errors. Both PostgreSQL statement counters went from 1 to 81, matching the traffic driver. |
| Slow lookup | Job `153` queued 18.466 s after the driver started. |
| Healthy lookup | `no_change` in the same detector window, no queued job. |
| Model | One `claude-sonnet-5-5` call, 4.003 s. Supported cause was a missing leading index. |
| Result | Attempt one saved the read-only diagnosis. Worker elapsed 5.873 s. Driver start to saved result was 24.791 s. |
| Scope | The flow reused actual traffic counters and catalog facts. An execution plan and a repair were outside this run. |

A local regression proved that a disconnect requested before first registration persists a stop marker and blocks later automatic activation. The Railway disconnect run also wrote the disconnected state. The app fixture and dedicated login were limited to the test, and the expected application query count showed no replay. Go race tests, affected PostgreSQL integrations, vet, shell syntax and independent source review passed. This did not exercise an interrupted model call or customer rollout.

## Operate the staging service

Set `POOLTUNE_STAGING_ROLE=project-monitor`, `POOLTUNE_STAGING_PRIVATE_STATE=1` and `POOLTUNE_WORKER_STATE_DSN=${{Postgres.DATABASE_URL}}`. The service requires outbound IPv6 for the direct Rookery endpoint and has no public domain.

- `POOLTUNE_PROJECT_MODE=active` uses the project reference, installation key, scoped password, Supabase CA and server-side model settings. Apply the [owner monitoring grant](supabase-monitoring.md) and [lookup evidence grant](enrolled-diagnosis.md) first. An already-disconnected installation needs explicit operator reconnection.
- `POOLTUNE_PROJECT_MODE=disconnect` records the stop in state without opening the Supabase connection. `standby` keeps the image available without polling or calling the model.

The test project was disconnected in deployment `0d8788a5-e53d-4f61-8ce5-71b76b055901`. Afterwards, the pilot variables were removed from the service and Rookery reported zero pilot roles, schemas or sessions. The public-index fingerprint matched the before value. Standby deployment `e56de2a6-bc7c-4c84-ac58-afc925a47d08` was healthy and emitted its standby event. The redundant staged IPv6 entry was also cleared. [Recorded event readback](evidence/enrolled-railway.json)
