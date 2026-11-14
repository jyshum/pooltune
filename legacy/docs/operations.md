# Local operations and release limits

This checkpoint supports the opt-in isolated local PostgreSQL and REST/PostgREST development profiles.
It is not a customer distribution. Container builds, managed-provider runtime
profiles and distributed recovery have not been verified. Independently anchored
local PG and REST restart are tested within the limits in recovery.md.

Build the Go binaries and static administrator UI from the pinned lockfiles:

```sh
go build -o bin/pooltune ./cmd/pooltune
go build -o bin/pooltune-owner ./cmd/pooltune-owner
npm --prefix ui ci
npm --prefix ui run build
```

`scripts/package-local.sh` builds a native development package with an explicit
payload allowlist and integrity manifest. See `docs/install.md`. This does not
replace container image, exposure or restart verification. Offline owner recovery
is documented in `docs/owner-recovery.md`; stop control first and preserve history.

Use `docs/local-runtime.md` and `docs/rest-runtime.md` for the traffic schemas and `docs/control-runtime.md`
for the continuous control/admin schema. State and secret directories must already
exist with mode 0700; secret files must be regular private files with mode 0600.
Supply explicit approved bounds. Do not copy fixture evaluation profiles into a
live configuration or store credentials in a profile, Git, command output or UI.

Start traffic first, verify loopback `/readyz`, then start control with its own
loopback health listener. The private administrator listener is configured
separately and requires its one-time claim token and password. Keep it loopback;
remote/customer TLS administration and recovery review remain G2 work. A failed
control or gateway process does not stop established traffic. A stopped traffic
process leaves independent recovery requirements and may leave uncertain work.

For a control-only update, stop control, preserve its entire directory, update the
binary/static assets, and restart with the same configuration. The tested control
restart retains authentication sessions, freeze, history and command results.
An unaccepted experiment is aborted or a durable pending acceptance reconciled
by the manager. Do not retry an uncertain mutation with a new key.

Use the private dashboard to freeze elective changes. Restore creates a new policy
and activation revision from a previously confirmed activation, recompiles under
current bounds, and keeps automation frozen. It does not replay or undo SQL.
A pressure safety ceiling cannot be raised through ordinary resume or restore.
For a safety restriction, unfreeze automation when appropriate, then wait
for **Resume traffic safely** (zero admission) or **Release safety ceiling**
(nonzero admission). It becomes available only after
five continuous seconds of fresh healthy pressure, verified backend capacity and
no active/uncertain work. Clicking it creates a new revision at the approved
starting bound for a stop, or at the unchanged admission limit for a nonzero
ceiling. Recovery requires idle backend/scheduler work and may need a quiet
interval. Subsequent growth still needs a fresh measurement window. Traffic rechecks the proof and may reject it if health changed.
A rejection leaves the stop in place and is recorded in history. Retry only after
the health condition is restored. A pending/unknown response is reconciled by
status without replay; do not issue a new key to bypass uncertainty.

Do not delete traffic snapshots, SQLite ledgers, owner command state or uncertain
reservations to make readiness green. Do not infer backend completion from a closed
socket, elapsed timeout, restart, or a restored backup. The component backup API
exists, but the full independent traffic/epoch/backend recovery procedure is still
G1 work and no disaster-recovery runbook is claimed here.

The owned AI gateway uses a separate persistent accounting database. Preserve its
$5 development cap and all spending/reservations. No provider key belongs in
customer images, browser assets or installation tokens. The recorded development
spending remains $0.005854; this checkpoint made no paid calls.

Safety recovery and capacity growth serialize their final scheduler check, local
durable write and installation with admission/outcome accounting. Recovery also
requires zero active work at that boundary; growth rejects any uncertainty,
including uncertainty in the newer capacity snapshot. A late failed safety check
creates a durable rejected intent, so reconciliation does not replay or strand it.
Existing queued bindings and active transactions are retained. Local persistence
latency can briefly delay scheduler accounting during these activations; production
latency calibration must include this cost. A storage failure retains the previous
admission and safety ceiling and marks the audit gap.
