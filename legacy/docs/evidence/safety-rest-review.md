# Safety resume and REST recovery review

Fresh independent reviewer: safety_rest_review, GPT-6 Astra. Read-only range
`a0119d1..b65186b`, with relevant broader branch interactions. This is not an
exhaustive security/protocol audit. No Critical findings, two Important, no new
Minor. Both Important grades accepted by their effect on users.

1. A successful safety resume was undone by the next pressure sample because
   the controller retained its old in-memory SafetyStopped latch. The reviewer
   independently reproduced this using a read-only overlay and an actual PG
   integration test that waited 2.2 seconds after resume. Parent regressions
   subsequently reproduced it for both PG and REST by waiting 2.5 seconds.
   Fix: derive the latch from the engine's durable safety ceiling whenever
   there is no audit gap; retain conservative local state during uncertainty.
2. A failed host sample cleared resource caches but left the prior healthy
   resume proof usable for up to two seconds and could preserve HealthySince.
   Shared production sample handlers now invalidate the proof immediately on
   error. Both paths reproduced RED, then are verified for immediate invalidation
   and a new healthy interval. The initial unit harness omitted PG's allocated
   cache pointers; that harness error was fixed before observing the real RED.

The reviewer recognized independent history/anchor/backend checks, child-held
cluster ownership, rejection durability and explicit release limits. One fix
pass uses failing regressions plus the complete suite; no re-review is requested.

## Declined scopes and executor rulings

Each remains withheld, not silently counted as complete:

- Production calibration/performance/promotion: G4 evidence absent.
- REST growth, nonzero ceiling release, physical/provider resize and quiet
  reclamation: future capacity work; reduced throughput is the current limit.
- Distributed/multiple-owner fencing, correlated restore and lost-anchor repair:
  local anchor cannot prove these; recovery stays quarantined.
- Accepted but unconfirmed old-epoch lineage: no fabricated confirmation.
- Nonempty prepared-transaction and physical power-loss faults: implemented
  queries and injected storage tests do not establish the missing fault matrix.
- Hosted providers, hosted Supabase authorization/key rotation and public TLS:
  local correctness does not establish these deployment profiles.
- Complete onboarding/locks and online key refresh: remaining product work.
- Container/image-layer/Compose execution: native tests cannot substitute.
- Hosted gateway security and funded-model behavior: unchanged; no paid calls,
  accounting reset or new hosted claim.
- B15/B16 demo, website and complete MVP acceptance: unfinished.
- Imported fallback selection for owner restore: prior deferred Minor remains.
- Exhaustive whole-branch security/protocol audit: not claimed by sampled review.

Cost of incorrectly closing these scopes would be unsupported release claims or
unsafe activation. Gates remain explicit in release-gates.md. A surviving REST
child requires explicit operator termination; Pooltune never guesses a PID to
kill. Durable rejection records are bounded and refuse further unrecordable
rejections conservatively. These are recorded scope decisions, not proof of full
MVP readiness.

## Fix-pass verification

Both Important findings are fixed with observed RED then GREEN regressions.
The final `go test -race -tags=integration ./cmd/... ./internal/... -count=1`
passed across all ordinary source packages. The actual PG and REST tests retain
admission for later sampling ticks and then execute SQL/RLS requests. Sampler
failure tests exercise both production handlers and verify immediate invalidation
and restarted health timing. Vet and diff checks pass. Chromium 2/2 passed for
the separate safety-resume control using labeled synthetic health and real auth.
Module integrity also passed. No paid calls, cloud mutations, or re-review.
