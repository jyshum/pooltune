# Bounded plain VACUUM qualification

Task 24 uses the existing private fixture, shared queue and one investigator. This is a synthetic staging contract, not production authority.

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
