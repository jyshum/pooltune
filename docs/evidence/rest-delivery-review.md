# REST and local delivery review

A fresh independent reviewer inspected branch `9964835..55d6863`, focusing on the
REST/runtime/owner/package changes and their earlier scheduler, control, storage
and authentication interactions. The reviewer also ran the full race/integration
suite successfully. The review sampled the broader branch; it was not an exhaustive
security audit or release approval. No Critical findings or new Minor findings
were reported. Three Important findings were reproduced and fixed in one pass.

1. Distinct state directories admitted competing local schedulers for one database.
   Real PG and REST constructor tests failed before the fix. Both profiles now hold
   the same host/OS-owner lock keyed by verified PostgreSQL cluster identity.
   Concurrent acquisition admits one owner. Database aliases and different database
   names cannot create another physical budget; max_connections is cluster-wide.
   Identity-read failure rejects startup. This lock does not supply distributed
   fencing, independent epoch recovery or permission to delete old snapshots.
2. Full REST observation closed stable admissions while unverifiable callers still
   selected stable. A real anonymous RLS request failed during full-stage drain.
   Fallback now clears claimed principal/tenant identity and uses bounded normal
   classification in the admission cohort that remains open. Actual PostgREST
   tests cover anonymous requests and valid upstream JWTs after scheduling-key
   expiry through full drain, observation, acceptance and candidate drain.
3. Vite copied untracked ui/public files into the native package. A nested fake
   private-key sentinel reproduced the leak in the extracted package. No approved
   public-directory assets existed, so Vite public copying is disabled. Root and
   nested sentinels are now checked against both package directory and archive.

Each finding has RED-to-GREEN regression evidence in the execution ledger. The
fix pass runs the whole Go race/integration suite, vet, browser fixtures and native
package checks. There is no second reviewer pass. Final test outputs and native
package metadata identify the verified checkpoint.

## Deferred and declined scope

The earlier Minor limitation remains: imported fallback revisions appear in
history but cannot yet be selected for restore, because observed-envelope storage
and activation-intent lookup differ.

The executor accepts the review's declined scope as explicit release work, not
completed behavior:

- G1: independent restart, backend orphan/epoch reconciliation, backup recovery
  and power-loss durability need their own evidence.
- G2/G3/G5: hosted provider behavior, public browser deployment and hosted gateway
  operation cannot be established by these isolated local tests.
- G4: calibration, performance benefit and production promotion settings remain
  unapproved and unverified. Correctness profiles do not grant promotion authority.
- Full ordered capacity growth, interactive onboarding/locks, online key refresh,
  containers, B15 and B16 remain incomplete. Native artifacts do not replace the
  absent container runtime or close those tests.

These limits keep affected capabilities unavailable or unclaimed. They do not
prevent continued local MVP implementation. No additional paid model calls were
made and the persistent $5 accounting ledger remains unchanged.
