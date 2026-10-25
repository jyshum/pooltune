# Local REST growth and nonzero-ceiling recovery review

Fresh independent review: GPT-6 Astra, `rest_growth_review`, October 24, 2026.
Scope: `f99a005..a0300e1`, plus relevant shared controller, scheduler, activation,
persistence and recovery interactions. No Critical findings. Two Important
findings reproduced in read-only overlays. No new Minor findings.

1. Nonzero recovery checked idle state before persistence, allowing new admission
   and an unknown outcome before clearing the ceiling. The fix holds the scheduler
   accounting lock through its final idle/uncertainty check, local durable write
   and installation. Persistence failure retains the ceiling and admission.
2. Growth ignored uncertainty in the later capacity snapshot, and had no final
   activation uncertainty guard. The fix checks both the snapshot and the actual
   scheduler at activation, with an immutable rejection record for late failure.
   The control intent can be resolved without replay or a permanently pending write.

Both original behaviors were observed RED before fixes, then GREEN. Regression
coverage includes a blocked durable write with concurrent admission or unknown
completion, storage failure, newly uncertain metrics for PG and REST, uncertainty
after proposal collection with ledger rebuild, work arriving during proof
collection, and preserved queued work during resume. The corrected late-growth
fixture starts below its ceiling; the old source overlay fails and the fix passes.

Ruling: local durable persistence runs under the scheduler accounting lock for
these safety-sensitive activations. This gives a single ordering point and avoids
clearing a safety latch based on an earlier unlocked observation. Cost: a local
storage stall delays accounting; G4 production latency calibration must measure it.
The callback performs only local durable writes and must not call the scheduler
or wait for request completion. Active transactions are not cancelled.

The reviewer declined production calibration/benefit and promotion, physical or
provider resize/reclamation, distributed ownership and correlated restore/lost
anchor, uncertain accepted old-epoch lineage, nonempty prepared work/power loss,
hosted compatibility/key refresh/public TLS, full onboarding/locks, containers and
configured packaged restart, hosted managed AI/customer distribution, and B15/B16.
These remain release gates. The prior imported-fallback restore-selection Minor
remains deferred. No paid calls, secrets, push or accounting changes.

One fix pass, no re-review. Final ordinary-source race/integration suite passed
after all fixes, including actual PostgreSQL/PostgREST runtime tests (94.226s for
the runtime package). Vet, module integrity and diff checks passed. Chromium 2/2
passed before these backend-only review fixes. Native delivery results are recorded
in the execution ledger and delivery checkpoint; full MVP gates remain open.
