# Activation and recovery development profile

Traffic owns a private snapshot directory and scheduler. Control owns its SQLite ledger and a different private intent directory. Neither process opens the other's database. The Unix socket is 0600 inside a 0700 directory; an exclusive filesystem lock prevents another owner opening that state directory. Both local profiles also hold a shared host/OS-owner lock keyed by verified
PostgreSQL cluster identity, independent of state-directory or target labels.
The local profile therefore excludes a second cluster budget, including another
database name in that cluster. This is not distributed or multi-account fencing;
see local-runtime.md. Do not run replicas that each own the same budget.

One activation binds one target and ingress path. PostgreSQL and REST have fixed separate allocations, so a change never borrows another path's budget. Control persists the complete intent payload and append-only intent event before sending an activation. Traffic validates checksum, schema, guardrails, epoch, predecessor, policy bounds and capacity, fsyncs its snapshot with atomic rename, then installs policy and limits together in the scheduler. All disk/JSON/control work stays outside individual request admission.

Retries of an identical revision are idempotent. A lost acknowledgement is queried by revision; it is not treated as failure and does not cause a provider write replay. Coordinator identity is bound to target/path before writing intent. SQLite provider lifecycle records are append-only and contain operation IDs and settings, not access tokens. Provider restart reads the last durable state and blocks incomplete operations.

A candidate lease expires locally. The same rollback routine fences candidate admissions, reclassifies bounded queued batches with the stable policy and preserves active transaction bindings. The global limit continues counting old candidate work. A stopped candidate is removed only after its active work drains. Fallback and restart create new activation revisions instead of reusing old history IDs. Control imports observed fallback state while leaving an unacknowledged older revision explicitly uncertain. The observed runtime predecessor and last confirmed ledger predecessor are both retained when they differ.

Storage uncertainty freezes elective updates. Safety fallback still fences candidate traffic and can retain the safe runtime policy if persistence fails; it exposes an audit gap until control durably records the observed state and acknowledges it. The traffic fallback backlog is bounded. Acknowledgement does not clear uncertain database work.

## Recovery gate

Every new traffic process starts with zero effective capacity. It loads the stable snapshot, drops the old candidate and requires a newer epoch. Before enabling traffic, a trusted reconciler must compare the independent control ledger and provide fresh evidence that capacity is valid and old backend work is resolved. No such proof is inferred from a closed socket, successful process startup, elapsed local timeout or restored backup.

Component tests inject this reconciler; the opt-in local PG/REST profiles below performs actual backend reconciliation. **A managed-provider or distributed backend/orphan reconciler is not implemented yet.** Ingress unknown reservations remain conservative. Restoring an old traffic snapshot against a newer independent ledger is tested and blocked. Restoring every state source together cannot be detected from those sources alone; an independently maintained epoch/revision anchor and deployment ownership must be verified before G1 passes. Neither this callback interface nor a configuration boolean is evidence that a real provider meets that requirement.

The unconfigured CLI remains health-only; explicit local traffic/control profiles
now run real services. Control-only restart and the opt-in independently anchored local PG/REST restart below are verified. Other traffic restart profiles remain quarantined. B14/B15 must wire control, ingress telemetry, provider observations, ownership and real recovery profiles before enabling customer traffic. No G1-G5 gate is declared passed by these component tests.

## Verification

`go test -race ./internal/control ./internal/store ./internal/durable` covers private Unix round trips, lost acknowledgements, immutable intent payloads, target/path binding, exclusive state ownership, symlink/path confinement, persistent provider operations, stale snapshots, restart quarantine, new recovery revisions, lease expiry preserving active work and storage uncertainty freezing changes.

Storage-failure tests inject failure, including failure after snapshot persistence, rather than filling the user's disk. They prove admission behavior around persistence boundaries, not physical disk durability under power loss. The local process-kill and old-transaction tests below add real integration evidence. Filesystem/power-loss faults and managed-provider orphan tests remain to collect.

## Opt-in independent local restart

`local-pg-1` and `local-rest-1` can set an absolute `RecoveryDirectory` created with mode 0700. This
must be a separately retained directory, outside the traffic directory and its
ancestors, and outside control backups. Configure it at initial installation.
An existing unanchored traffic snapshot cannot silently create a new anchor.
Set control `RecoverLocalPostgres` or `RecoverLocalREST`, matching its path, to
true to enable the recovery handshake.

The anchor contains the target/path, approved policy authority, highest attempted
epoch and digest of the traffic snapshot. Traffic writes the anchor before each
snapshot and reserves a strictly newer epoch before recovery. An interrupted
attempt consumes its epoch. A snapshot/anchor mismatch blocks startup, including
a stale traffic backup paired with an equally stale control backup.

A restarted process exposes private control and health while admission remains
zero. PostgreSQL startup connections are rejected before opening an upstream
session while quarantined. Control reopens its own SQLite ledger, rejects pending
or uncertain intents, except an exact checksum/epoch-bound persisted resume
rejection that can be reconciled without activation. It compares the complete confirmed intent (or imported
recovery payload) against traffic. Traffic separately verifies that confirmation,
its retained anchor, monitor visibility, absence of other cluster client backends
and prepared transactions, and sufficient unreserved connection slots. A live
old transaction blocks recovery. The process never kills it or infers completion
from a disconnected frontend. Successful recovery creates a new revision and
bounds startup admission by the configured start limit and prior stable limit.

Verified here: real orphan refusal/removal, repeated recovery through imported
history, and SIGKILL followed by a fresh OS traffic process, separately reopened
control ledger and Unix-socket recovery for both local paths. REST additionally
proves a surviving child retains the cluster ownership lock after parent SIGKILL,
even if the socket is removed. The operator must stop that known child before
replacement; recovery does not infer death from an idle pool or absent socket. Epoch reuse, missing confirmation and
stale traffic snapshots are covered by component tests. Prepared-transaction
refusal is implemented, but a nonempty prepared-transaction fault test and actual
power-loss/storage-order fault testing remain open.

Recovery procedure:
1. Stop competing owners. Preserve traffic, control and anchor state separately.
2. Inspect PostgreSQL for remaining work. Let it finish or resolve it through an
   explicit database-operator action. Pooltune does not cancel unknown work.
3. Increase the configured traffic epoch above the anchor's highest attempted
   epoch. Start traffic, then control with recovery enabled.
4. Verify readiness and a new `recovery-<epoch>` history entry. A refusal requires
   investigation; do not delete state, invent confirmation or reset the anchor.

Limitations: this is one-host, one-OS-owner recovery. The anchor is durable local
state, not external consensus. Restoring or losing all three stores together is
not detectable. Keep the anchor outside coordinated backup restore jobs. A lost
anchor, ambiguous persistence, uncertain control lineage, remote owner, multiple
OS users, or unavailable backend inventory remains quarantined. External fencing,
full backup tooling and those fault matrices are still G1 release gates.

REST starts its owned replacement child only after cluster ownership, no-other-
client and prepared-work checks pass. The new frontend remains quarantined until
control supplies confirmation. Recovery rechecks the new child's exact random
application name, idle pool and private readiness. An old child or transaction
blocks startup. Graceful shutdown quarantines admission without inventing an
unacknowledged safety revision. Actual child termination is required before the
parent releases the shared lease during normal close. Never unlink ownership
lock files to bypass a surviving process.

A definitively rejected safety resume is persisted on traffic and in a separate
append-only control rejection table. Its intent remains audit history but cannot
be confirmed or become active. A lost rejection response can be recovered after
traffic restart using its original checksum and epoch. An accepted but unconfirmed
old-epoch activation is still uncertain lineage and remains a release gate.

## Backup routing gap

A spare container alone does not provide uninterrupted service. Automatic traffic
failover and a safe direct-to-database bypass are **not implemented or verified**.
The route/health controller must survive a traffic-process crash independently.
A standby must establish exclusive ownership and reconcile old database work
before sharing or replacing the connection budget. The current local ownership
lock deliberately excludes a second active scheduler; it is not distributed HA.

A direct fallback loses queue/priority protection and must have a separately
approved total connection budget covering surviving old work. Never trigger it
merely because Pooltune is intentionally rejecting overload or has stopped for
safety. An in-flight write may have committed before the connection was lost;
route switching cannot make replay safe or preserve its live transaction.

Required future evidence: actual process kill during reads and writes; measured
outage/loss and recovery time; no duplicate writes; no budget overshoot with a
surviving instance; network partition and stale health; safe return to Pooltune.
Until then, describe the verified behavior as recovery, not seamless failover.
