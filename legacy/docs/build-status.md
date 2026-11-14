# Current MVP source publication, October 25, 2026

The narrowed managed gateway MVP, local background tuner, private dashboard and optional Tiger Data history are published on `main`. Azure, Google Cloud and Supabase each have bounded live synthetic spike evidence. See [managed MVP results](managed-mvp-results.md), [gateway setup](burst-gateway.md) and [history setup](traffic-history.md). The public website and Mana demo are delivered. AI and priority scheduling remain deferred; backup routing, high availability and production certification remain unverified.

The checkpoint table below records the earlier, broader architecture. It is historical context, not the current shipping checklist.

# Build status

The full MVP is not complete. Latest implementation details are in
`local-runtime.md`, `rest-runtime.md`, `control-runtime.md`, and the canonical execution ledger.

| Checkpoints | Current status |
| --- | --- |
| B01–B04 | Configuration, durable store, policy compiler and scheduler implemented |
| B05–B09 | Capacity math, telemetry, PG/REST ingress, provider adapters and recovery components implemented; affected runtime/provider gates still open |
| B10–B11 | Budgeted gateway and bounded agent implemented; one real validated candidate recorded, hosted customer access remains gated |
| B12 | Evaluator/manager, real local PG cohort routing, measured driver and rollback implemented; calibrated automatic promotion remains gated |
| B13 | Private authentication/UI plus real local runtime reads, freeze and restore; offline owner password/claim recovery implemented; full onboarding/lock editing remains open |
| B14 | In progress: local PG traffic, continuous control/agent wiring, private admin backend and opt-in local pressure response; REST/PostgREST assembly and measured RLS outcomes; ordered PG/REST capacity telemetry and verified admission growth, independently anchored local PG/REST restart and explicit safety-stop and nonzero-ceiling recovery; remaining physical/provider capacity, distributed recovery and container packaging remain open; native development package build/smoke tests pass |
| B15 | Some real local integration/fault evidence exists; full calibrated performance and provider matrix remain open |
| B16 | Demo/website completion and final release handoff remain open; GSAP and React Bits approved, sponsor integrations deferred |

Unconfigured command modes remain health-only. The explicit local traffic/control
profiles run actual services. Neither local correctness tests nor a control-only
restart establish crash-safe customer release or managed-provider performance.
G1–G5 retain their scoped gates. Do not enable promotion using a fixture profile,
claim physical warm capacity from an admission budget, or reset the AI ledger.
