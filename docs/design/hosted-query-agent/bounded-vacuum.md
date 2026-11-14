# Bounded plain VACUUM qualification

Task 24 uses the existing private fixture, shared queue and one investigator. This is a synthetic staging contract, not production authority.

## Hosted result, November 14

The repair passed the shared traffic test. Ordinary target query time fell from **37.344 to 8.737 ms**, a **76.6% reduction**. In plain English, the index was already present, but PostgreSQL still visited table rows to check whether they were visible. Cleanup restored the information that lets it avoid those extra visits. This adds a useful repair without building another index.

Railway deployment `86bbdd83-1aea-4fc3-b873-a457d862df0c`, source `17a8d7d`, Rookery job **85**, attempt **1**. The frozen test objective was **14.43975 ms**. One real Claude request chose `vacuum`; Go independently checked and applied it. The three-second spike averaged **55.225 ms** and created **zero jobs**. All **1,974 offered requests completed**, with **zero errors and missed slots**. Owned fixtures were removed and the 12 application-index definitions remained unchanged. [Complete final evidence](evidence/mixed-traffic/final-vacuum.json)

| Stage | Seconds |
| --- | ---: |
| Sustained fault to queued investigation | 9.044 |
| Queue creation to saved verified result | 14.325 |
| Total fault to verified result | 23.369 |
| Matched pre-repair measurements | 2.038 |
| Investigator, including Claude and graph overhead | 2.313 |
| Claude request alone, included above | 1.866 |
| Final guards, cause recheck and VACUUM | 3.883 |
| Matched post-repair verification | 1.882 |

The last four component measurements sit inside the queue-to-result duration. Connection, catalog, state and queue work account for the remainder. The action measurement includes guards and network round trips; it is not a claim that the VACUUM SQL exceeded its two-second server limit. Fixture construction, healthy-history collection and report download are outside fault-to-result timing.

Ten controlled measurements fell from **41.399 to 9.817 ms**, with heap fetches **50,000 → 0** and shared-page accesses **50,277 → 277**. Sampled healthy reads were **0.0317 → 0.0297 ms**, and healthy-table writes **0.4316 → 0.1916 ms**. These satisfy the unchanged benefit and protection gates. Ongoing-traffic means above are a separate measurement, not interchangeable with these controlled samples.

Steady counter collection used **39.760 ms of database execution over about 24 seconds**, or **1.657 ms per elapsed second**. This is database execution accounting, not CPU utilization. Target client p95 was **149.90/149.18 ms** in monitoring-off phases and **149.34/148.94 ms** in matched on phases. That shows no material measured increase here; it does not establish a universal overhead guarantee.

The first hosted attempt failed after Claude chose the correct action. Recovery inspected without repeating an uncertain VACUUM. A pooler probe exposed the wrong process-identity assumption: the protocol connection ID differed from PostgreSQL's backend PID. The guard now reads the actual PID within its transaction. The final run above passed after that correction; the original failed run remains saved. A separate deployment packaging attempt lost the launch script's executable bit and never reached traffic testing; preserving Git's file mode corrected it. [Initial failed evidence](evidence/mixed-traffic/initial-vacuum-interrupted.json)

Two direct live-provider controls declined healthy visibility and blocked cleanup in **1.873/1.976 seconds**. The normal graph screens those cases without calling Claude; these extra requests tested the four-repair prompt separately. [Control evidence](evidence/mixed-traffic/vacuum-live-controls.json)

Engineering judgment: sufficient to continue the remaining bounded repairs. Production expansion still needs the coverage listed below, especially writes to the repaired table, customer credentials, resource headroom and longer operating periods. A single successful run is not a latency guarantee.

## Prespecified workload and gates

The final workload is fixed before repair qualification: 100,000 candidate rows, `lookup=id`, 64-byte padding, fixed physical insertion permutation `(n * 7919) % 100003`, existing nonunique B-tree `(lookup)`. The target is `SELECT sum(lookup) FROM ONLY <owned candidate> WHERE lookup >= $1 AND lookup <= $2`, inputs `1,50000`, result `1250025000`. No forced planner method is used. All target sessions use `random_page_cost=1` and `max_parallel_workers_per_gather=0` for a matched warm-cache context. Initial fixture validation at default random_page_cost=4 chose a sequential scan and correctly failed before measuring benefit; the session context was then fixed before qualification. The full range still became a sequential scan after table growth from updates, so eligibility was narrowed to the fixed lower half before measuring any repair benefit. The measured fully consumed index-only scan must return 50,000 scan rows. Padding-only updates clear visibility without changing the result. Plain VACUUM restores the baseline between the short and sustained faults. Autovacuum is disabled only for this disposable table during setup.

Ten measured target reads, healthy primary-key reads and value-changing healthy-table writes on each side. Warm-up is excluded. Mechanism requires at least 1,000 heap fetches and 0.5 heap fetches per scan row before action, with at least 50% reduction afterward. Success also requires latency gain >=5 ms AND >=20% AND greater than three times the larger median absolute deviation. Shared sampled read/write regression gates apply. Failure to meet the latency gate remains inconclusive even if heap fetches fall to zero.

The traffic driver retains eight target reads, four healthy reads and two healthy-table writes per second. Its objective is declared from ten healthy pre-fault samples as max(3 ms, 1.5 times healthy database mean); it is then frozen. This objective only detects a fault. It does not replace the fixed benefit gates. Monitoring off/on order is reversed for a second pair; short fault, restored baseline, sustained fault and postrepair phases remain separate. Driver reports errors and missed offered requests even if qualification fails.

## Action contract

Only plain VACUUM of the enrolled owned candidate. No FULL, ANALYZE, threshold change, parallel workers, truncation, customer cancellation or arbitrary SQL. Existing 24 MiB table and 8 MiB index bounds remain. Statement timeout 2 seconds, lock timeout 250 ms, maintenance_work_mem 16 MiB, PARALLEL 0, TRUNCATE FALSE. Ordinary healthy writes remain concurrent.

Initial private fixture setup also uses ordinary ANALYZE and plain VACUUM to establish the clean baseline. Neither is an additional agent repair.

Failed setup uses an independent five-second cleanup context and fresh connections to remove only its durably owned fixture. Startup reconciles only interrupted `vacuum_preparing` records under the target lock; ready and pending repair fixtures remain intact. Explicit journal recovery also recognizes interrupted preparation. Ownership drift prevents cleanup.

Persist pending intent before action. Recheck identity, index identity, current visibility evidence, table budget, active maintenance, old snapshots, prepared transactions and replication horizons. If completion is uncertain, inspect current visibility and identity and report repair_interrupted; never issue VACUUM automatically again. Verified outcomes replay from the journal.

A separate short-lived guard transaction holds ACCESS SHARE on both tables across the nontransaction VACUUM, preventing relation removal or rename while permitting VACUUM and ordinary writes. The private schema belongs exclusively to the fixture owner; hostile same-owner schema/catalog mutation is outside this staging contract. This does not establish a production credential or DDL coordination policy.


## Local verification observations

The first two fixture eligibility checks refused a sequential scan, before a repair ran. The final range test on disposable PostgreSQL 17 measured 1.554 ms clean, 7.374 ms after padding updates and 1.474 ms after the bounded action. Mean heap fetches fell from 56,831 to zero for 50,000 scan rows. The unchanged latency/mechanism gates passed for this measured run. These local timings do not substitute for the required hosted live-model traffic run.

Tests also cover a healthy fixture without a model call, wrong repair and identity, model decline/provider failure, changed index definition or OID, concurrent external cleanup, active maintenance locks, old snapshots, durable verified replay and pending-action visibility inspection with no repeated VACUUM. A separate guard test verifies concurrent healthy writes while table definition changes are blocked during measurement.

Limits: sampled tested-input latency and healthy-table writes do not qualify candidate-table write concurrency, whole-app protection, cold caches, every input, resource headroom or production credentials. Cleanup thresholds, recurring cycles and exact rollback remain unimplemented. Transaction-horizon admission checks both `backend_xid` and `backend_xmin` with a conservative five-second cutoff; ordinary shorter transactions remain allowed. Successful cleanup does not imply file shrinkage.


Fresh review reproduced two gaps: interrupted setup could leave a committed fixture, and an idle writer with backend_xid but no backend_xmin could pin cleanup. Regression tests now cover cancellation at the post-seed VACUUM boundary, startup recovery, ready/changed-owner preservation, and xid-only blockers before and after model investigation. Both fixes passed focused tests. The latest local visibility check measured 7.386 ms before and 1.438 ms after, with 56,831 heap fetches reduced to zero. Hosted qualification is still separate.
