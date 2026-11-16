# Managed cleanup-trigger verification

Task 65 connects the existing bounded cleanup-threshold operation to eligible ordinary discovered tables. Plain VACUUM remains a separate path under the same cleanup permission.

## Preserved limits

The native owner must match the exact ordinary table. Two fresh observations must show increasing update/delete backlog between the existing threshold and the proposed threshold. The scale factor decreases by the existing bounded step, never below 0.05. The table must already have insert-triggered cleanup disabled, have automatic cleanup enabled, and expose a native cleanup interval of at most 120 seconds. Pooltune does not change those prerequisites. Unsupported tables may still qualify separately for plain VACUUM.

The investigator sees the measured earlier/later trigger observations and exact proposed scale factor. Go independently rechecks the visibility cause and trigger inside the locked native transaction. This avoids mistaking the action's own lock for competing maintenance while retaining the checks against other work.

The durable action stores the original option, exact target/query/counter context, deadline, normal read/write protection baseline and confirmed native receipt. Verification waits for actual PostgreSQL automatic-cleanup history, a substantial backlog decrease, the same sampled query input and comparable normal traffic. The sampled value is not journaled; a credential-keyed digest verifies a newly sampled value after restart. An unmatched sample cannot demonstrate benefit.

Insufficient benefit restores only the recorded option, after matching its current value and owner. Restoration intent is saved before the write. An interrupted forward write or restoration is inspected without blindly repeating it. Owner changes are preserved. Permission removal or pause stops further mutation.

## Local verification

A real PostgreSQL 17 integration test used an ordinary `billing.orders` table and a separate application role. It discovered the query through the normal monitor reader, supplied actual trigger evidence to the diagnostic callback, changed the native option, restarted the state connection, observed a genuine automatic cleanup and verified the same sampled input against ongoing application traffic. No manual VACUUM was issued during the repair or follow-up.

In the recorded focused run, dead-tuple estimates fell from 5013 to 14. Protected traffic regressed, so the result remained regressed and the previous option was restored. This is a recovery/protection result, not a retained-improvement claim. The test made one diagnostic callback across the restart. It uses a scripted local proposal; Railway/Rookery qualification with the live investigator is still required.

Checkpoint tests cover stale epochs, changed deadlines, forged automatic-cleanup claims, acknowledgement/continuation persistence and recorded restoration without repeat writes. A separate real-PostgreSQL regression excludes Pooltune catalog reads and constant scheduler witnesses from protected application-read traffic, including when the application uses the same database role.

## Outstanding qualification

Run this ordinary-project path on Rookery through Railway with the real investigator and update the map only to the scope actually demonstrated. Existing prepared cleanup proofs remain separately scoped. A worker-state reconnect in the local test does not establish a Railway process restart during this new repair.
