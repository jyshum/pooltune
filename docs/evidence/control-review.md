# Continuous-control checkpoint review

A fresh-context reviewer inspected the branch from the main merge base through
this B14 working tree. Coverage included service/admin/runtime/safety, activation,
recovery/coordinator/driver, rollout manager, agent execution, scheduler transitions,
capacity equations, provider lifecycle, gateway HTTP/service, administrator auth,
selected ingress code and UI changes. This was not an exhaustive line-by-line audit
of the approximately 17,000 added lines or a customer-release approval.

Three Important findings entered one fix pass:

1. A local safety stop failed after promotion while old candidate transactions
   drained. The stable-only runtime no longer held the candidate policy needed by
   the scheduler. Preserve the accepted policy for the stopped draining cohort,
   and expose any installation failure as an audit gap. Regression:
   `TestSafetyStopWhileAcceptedCandidateTransactionsDrain`, failed before fix.
2. A replaced observation window caused a definite pre-dispatch refusal to be
   recorded as uncertain canary activation, blocking future agent runs forever.
   An explicit `ErrCanaryRejected` contract now distinguishes proven refusal from
   transport/activation uncertainty. Rejection is retained as such; genuinely
   uncertain calls still block replay. Regressions:
   `TestServiceStaleWindowIsDefiniteCanaryRejection` and
   `TestDefiniteCanaryRejectionDoesNotBlockFutureRuns`, failed before fix.
3. Equal guardrail version labels did not prove equal control/traffic registries
   or numeric bounds. The private state now carries an authority fingerprint;
   startup and each authority snapshot require an exact match. Deep copies use a
   fresh destination so configured slices cannot alias a caller's registry.
   Regression: `TestServiceRejectsDifferentAuthorityWithSameVersionAndWorkloadIDs`
   covers selectors, default classes and bounds, and failed before fix.

An additional readiness regression verifies that the real dashboard cannot report
ready when a safety stop leaves zero admission capacity.

Deferred Minor: confirmed imported fallback revisions use observed-envelope files,
while the owner restore loader currently accepts retained activation-intent files.
Such fallback revisions remain visible in history but cannot yet be selected for
restore. Ordinary confirmed activation-intent revisions are supported. No history
is removed or fabricated to work around this.

Review scope rulings: calibration/statistical adequacy and performance benefit
remain G4; live managed-provider/Supabase RLS evidence remains G3; independent
traffic/orphan/anchor and power-loss evidence remains G1; hosted gateway/customer
access, containers and B15/B16 acceptance are not established by this review.
REST runtime assembly, full onboarding/locks/admin recovery and elective capacity
growth are explicitly unfinished. The cost of treating any of these as passed
would be an unsupported release claim, so their corresponding capabilities remain
withheld. The requested complete MVP is still not achieved.
