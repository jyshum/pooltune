# Release gates

Status at the local REST-growth/ceiling-recovery checkpoint: **MVP incomplete; no customer
release or autonomous promotion claim.**

| Gate | Evidence available | Still required |
| --- | --- | --- |
| G1 Recovery | Durable intent/confirmation, uncertain outcome accounting, snapshot quarantine, actual control restart preserving session/freeze/history, SIGKILL/fresh-process local PG recovery, separately retained local epoch/snapshot anchor, real old-transaction refusal; actual REST parent SIGKILL, surviving-child exclusion and independent recovery; active transactions survive canary/rollback/local safety tests | External/distributed epoch fencing, correlated restore/lost-anchor recovery, uncertain control lineage, prepared-transaction and power-loss fault matrix, complete backup tooling |
| G2 Administration | Real private authentication/API/UI tests; actual local backend measurements, freeze/restore and session retention; offline password/claim recovery with atomic audit and exclusive ownership | Complete onboarding/locks, remote/customer TLS/public REST separation review, container exposure verification |
| G3 Compatibility | Real isolated PostgreSQL 17.11 TLS/SCRAM and supported pgx exchanges; actual local PostgREST TLS frontend, SQL/RLS/JWT, cohort outcomes and child lifecycle; labeled browser fixtures; conservative provider adapter components | Real Azure/Cloud SQL/Supabase runtime profiles and operations, hosted Supabase RLS/JWT/key rotation and public REST deployment |
| G4 Performance | Actual local arrivals/outcomes, ordered scheduler occupancy and host pressure; verified authenticated PG capacity; measured local baseline and bounded PG and REST 20-to-25 growth; explicit owner stop and nonzero-ceiling recovery after real five-second host/backend health window; deterministic evaluator/controller tests | Reproducible production calibration, physical/provider resizing, quiet reclamation, performance comparisons and promotion thresholds |
| G5 Managed AI | Persistent maximum-cost accounting, owner access tests, real validated candidate after an earlier failed run | Hosted deployment security and full customer distribution/data lifecycle evidence |

The calibration loader verifies a configured artifact hash. It does not replace
reviewing that artifact. Fixture profiles and controlled workload results cannot
close a managed-provider or performance gate. Sponsor integrations are deferred;
GSAP and React Bits remain the approved website choices.

Docker, Colima and Podman commands are absent on the current host. No container
build, image-layer secret scan, Compose startup or container restart evidence is
claimed. Native package allowlist/integrity/startup tests pass, but B14 container packaging and B15/B16 remain open.

## SWE feedback checkpoint: warm-pool comparison

See `benchmark-results.md` and `bench/README.md`: 36 actual local REST/PostgreSQL
trials compare provider-only warm pooling, application-side priority, equal-class
Pooltune and priority Pooltune at four fixed database connections. Priority helps
the important workload at a background-work cost; application-side priority is
similar. No unique Pooltune advantage, AI benefit or production latency bound is
established. G4 remains open for separate queue/downstream spans, calibrated
customer workloads, pool tuning, statistically supported margins and providers.

G1 explicitly includes **safe backup routing and measured failover** as an open
gap. Independent restart recovery is not uninterrupted availability. No direct
fallback or active/standby readiness is claimed. See `recovery.md` for the missing
ownership, old-work, write-outcome and overload checks. Customer limits remain
local authority over every AI candidate; onboarding/locks and actual AI benefit
are separate unfinished items. This checkpoint does not approve the unfinished
physical-pool runtime changes or close B14/B15/B16.

## Narrowed MVP: fixed versus automatic sizing diagnostic

AI and business-priority scheduling are deferred from the MVP by customer
instruction; see `mvp-scope.md`. Their earlier gates do not mandate bringing those
features back into this release. Safety, recovery, administration, provider
compatibility and measurable value remain required for the selected tuning path.

`pool-sizing-results.md` records four actual local pool-sizing cases and34,560
scheduled reads. The current automatic case did not establish an advantage:
native PgBouncer cleanup retained2 idle-period connections while automatic
retained20; automatic also completed fewer overload-period requests than the
fixed cases. Admission cuts are not physical reclamation evidence. Current
forwarding/cancellation behavior and quiet physical-operation ordering need
investigation before making a performance or useful-capacity claim.

This is one occupancy/sleep workload, not CPU calibration, statistical superiority,
managed-provider evidence, a released physical adapter or the proposed background
controller. G4 and relevant recovery/compatibility gates stay open. Proxy backup
routing remains unresolved for deployments still using the proxy; a background
controller must separately prove that its failure preserves the normal pooler path.
