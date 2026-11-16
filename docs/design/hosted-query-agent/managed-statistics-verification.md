# Managed statistics verification decision

Status: Option 1 approved by Jared on November 15, 2026, after the benefits and recovery tradeoff were explained. His reply was “i approve, i thoguht this was all already set up- though”. Task 62 in the canonical implementation and progress records carries this approval. Implementation and live qualification are in progress. Option 2 remains an alternative, not the selected design.

Task 62 now implements the committed managed route locally. Review and live Rookery qualification remain required before claiming it deployed or verified there. Prepared Rookery fixture repairs keep their existing transaction and exact-sample rollback behavior.

## Approved Task 62 scope

Persist the prior configuration, exact trial object identity and deadline before committing. After commit, measure the same candidate inputs and matched actual application traffic. Retain only a qualified benefit. On regression, missing evidence, an expired deadline or restart, reconcile the exact recorded action and restore only configuration that still matches Pooltune's trial. Do not overwrite owner edits or replay an uncertain mutation. Restoration regenerates planner statistics and does not promise the exact prior sampled state. No arbitrary SQL or synthetic customer writes are authorized.

## What is implemented

The managed Go worker can identify an eligible ordinary integer lookup from discovered traffic, resolve its actual table and columns, and sample existing values under the application's role. The statistics checkbox admits two bounded cases:

- Increase one column's statistics target when its recent sample is full, misses the sampled value, and underestimates the measured result by at least tenfold. The increase stays within the existing limit of twice the prior target, at most 100 additional entries and at most 200 overall.
- Create one most-common-values summary for two actual columns when two distinct sampled pairs demonstrate correlation and at least tenfold estimate errors despite recent statistics.

Both paths require the exact native owner credential, selected capability, current project epoch, table size limit, model proposal, and durable action record. They recheck the original evidence inside the action transaction, persist the exact commit receipt on an independent state connection, then commit and measure the same inputs alongside matched actual normal read/write traffic. Query values do not leave the executor. The worker does not synthesize customer writes.

The managed verification deadline is 12 seconds, shortened when the caller has less time available. Retention requires every existing sampled-input benefit gate, matched normal traffic, no protected regression, and the exact recorded configuration still present before the deadline. Missing evidence or a failed check restores the original target or removes the exact joint object, then regenerates statistics. A separate durable restoration receipt makes recovery after a lost commit acknowledgement idempotent. Owner edits, including same-value column changes or renamed/replaced objects, are preserved.

Real local PostgreSQL tests cover ordinary tables, non-default column positions, application-role measurement, both managed checkbox variants, committed visibility, missing traffic, regression restoration, backend termination after commit, lost restoration acknowledgement, independent durable receipts, stale epoch refusal, and owner edits. The older uncommitted and fixture tests continue to cover exact original-sample rollback. The dashboard distinguishes retained benefit from regenerated-sample restoration and makes no benefit claim after interrupted recovery. The focused statistics/activity/legacy-statistics race suite passed in 36.421 seconds. These are local tests on prepared data, not live Rookery qualification.

## Earlier finding and rollback-only baseline

The existing native trial keeps its statistics change uncommitted while measuring it. This allows PostgreSQL to restore the exact original planner sample if verification fails. The candidate measurements in that transaction see the trial statistics. Other application sessions still see the original statistics until commit.

Consequently, ordinary read/write counters collected during the trial can show concurrent load or locking harm. They cannot prove that other queries choose acceptable plans with the new statistics. Calling those counters protected-plan verification would overstate the evidence.

Before Task 62, the managed path therefore always rolled back its temporary statistics trial. Historical reports can show an improvement in a sampled input, but their overall result remains `inconclusive` or `regressed`, `retained=false`, with restoration recorded. Those reports do not mark the richer-statistics node complete or a customer repair successful.

## Option 1: Commit a bounded trial and observe normal traffic

Commit the proposed change after candidate checks, then collect matched normal-traffic evidence from the affected queries. Preserve a durable verification deadline and stop further changes while qualification is pending.

If the change is harmful or cannot qualify, restore the original column target or remove only the exact statistics object Pooltune created, then regenerate statistics. That restores configuration, but it cannot promise the exact original sampled planner state. Record this difference explicitly. An interrupted worker must inspect the exact intent and current catalog before doing anything further.

This option tests the application's actual plans and adds no synthetic customer writes. It exposes the application to a temporarily committed change and revises the exact-sample restoration guarantee. Jared approved this option after that tradeoff was explained. The approval does not establish that implementation or live verification has passed.

## Option 2: Qualify representative protected probes inside the transaction

Keep the trial uncommitted. Discover or explicitly enroll representative protected reads that are affected by the same statistics. Bind their roles, supported query shapes, input buckets and identities. Measure them in the action transaction under their application roles so they see the trial statistics. Roll back unless candidate and protected probes qualify.

This option preserves exact restoration. Its coverage is limited to the supported and representative probes. It must not execute arbitrary observed SQL, replay customer writes, or treat unavailable write protection as passing. Where read-only probes and normal load counters cannot establish a required guard, retention stays unavailable.

This option needs an approved coverage contract and additional evidence readers. It does not silently broaden enrollment or substitute invented inputs for unavailable application context.

## References

- [Approved MVP build sequence](mvp-build-plan.md)
- [Statistics repair scope and fixture evidence](richer-statistics.md)
- [Shared benefit, protection and uncertainty rules](detection-verification-v0.md#3-shared-success-regression-and-uncertainty-rules)
- [Managed credential authority](repair-authority.md)
