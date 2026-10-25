# Capacity and recovery checkpoint review

Fresh read-only reviewer: `capacity_recovery_review`, GPT-6 Astra, on
`5c25e31..115fb76`. No Critical findings, two Important findings, no new Minor.
The reviewer inspected code and did not independently rerun the supplied suite.

1. Growth omitted the retained safety ceiling. A healthy window after a nonzero
   safety reduction could create a forbidden growth intent; traffic rejection
   would leave it uncertain and block future work/recovery.
2. Continuous growth held on pending history without status-query reconciliation.
   Lost activation acknowledgements or failed confirmation writes stayed pending.

Both are accepted as Important. In one fix pass, each failure was reproduced
before changing implementation. The state API now exposes a copied ceiling and
capacity proposals respect it. Service ticks query at most one pending capacity
revision before elective work, including when frozen or growth is disabled.
An active status confirms the existing intent; no activation is replayed. Unknown
status remains uncertain. The lost-ACK and actual SQLite confirmation-trigger
failure regressions pass. A real PG recovery case also preserves the safety cap.

Tests: `TestContinuousGrowthRespectsSafetyCeilingWithoutPendingIntent`,
`TestContinuousGrowthReconcilesLostACKWithoutReplay`, and
`TestContinuousGrowthReconcilesConfirmationWriteFailure` were RED then GREEN.
No second reviewer or re-review was dispatched.

Declined to judge: REST growth/restart, physical provider resizing, production
calibration/performance, distributed fencing/correlated restore, prepared-nonzero
and power-loss faults, containers, and B15/B16. These remain explicit release
work; no local result is substituted for them. The prior Minor imported-fallback
restore limitation remains deferred.

Final verification found that the capacity-1 live case could legitimately hold
on small baseline latency variation due to the approved integer congestion rule.
The test now measures the same unqueued TLS proxy path, uses 100 actual 50 ms
workload samples, and checks growth from 20 to 25 with 28 authenticated sessions.
Controller equations and thresholds were not relaxed. Focused race run measured
0.051827 seconds mean and passed. This is still local correctness evidence.

Delivery check also caught missing controller setup documentation in the native
payload. The actual package test failed before adding controller.md to the
explicit documentation allowlist, then passed. No broad directory copying added.
