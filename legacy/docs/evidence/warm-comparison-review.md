# Warm-pool comparison review

Fresh independent GPT-6 Astra reviewer `warm_comparison_review` inspected the
completed diagnostic/AI-boundary/documentation changes and relevant existing
runtime, scheduler, ingress and policy behavior. No implementation agents, paid
model calls or re-review. This is not an exhaustive security or whole-product
release audit. The separate physical-pool changes remain unfinished.

No Critical findings. One Important finding: application queue expiration could
race its request context timer and be mislabeled as rejection. Original evidence
had 216 such outcomes, all after their deadline. Success counts, deadline misses
and the published latency table were unaffected. The regression
`TestQueueDeadlineIsTimeoutBeforeContextTimerFires` failed for direct/wrapped queue
expiry and deadline errors with a nil context error, then passed after explicit
error classification. Original evidence is retained as
`warm-pool-v1-original.json.gz` and `warm-pool-v1-original-report.md`; corrected
measurements are in `warm-pool-v1.json.gz`.

One deferred Minor: a transport-normalized comparison remains absent. The current
baseline/application paths reuse upstream HTTP connections; Pooltune ingress does
not. Documentation now discloses that complete path costs include this difference.
No production transport change was made for the benchmark.

The reviewer independently checked all36 trials/3216 samples, raw hash, table
arithmetic, max generator lag, warming/accounting checks, authority enforcement
and explicit release boundaries. The parent executes test suites; reviewer did
not run competing load.

## Rulings on review exclusions

- Physical PgBouncer and quiet reclamation are retained but excluded from this
  completed checkpoint. Passing tests do not replace their pending review.
- Parent verification supplies regression-suite evidence; no reviewer test run
  is implied.
- Local diagnostic data cannot settle production margins or isolated database
  resource use. G4 remains open.
- AI usefulness, paid inference, canary improvement and model costs are not
  measured by this fixed-policy comparison. No extra spending was authorized.
- Failover, fallback, partitions and uncertain-write recovery remain G1 work.
- Hosted providers, containers, browsers and full onboarding remain their
  existing compatibility/delivery gates.
- The measured tree is explicitly dirty. A scoped comparison commit must not be
  described as the exact complete source tree or a verified packaged release.
