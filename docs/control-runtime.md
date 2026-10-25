# Continuous local control

`pooltune control -local-control-runtime /absolute/path/control.json -listen 127.0.0.1:8081`
starts a continuous control process for the opt-in local PostgreSQL or REST runtime.
Unconfigured modes remain health-only. `runtime.ControlConfig` is the exact JSON
schema; unknown fields and trailing documents reject. Times use RFC3339 and
durations use integer nanoseconds. Traffic profiles remain separate; see `local-runtime.md` and `rest-runtime.md`.

The control profile names a private existing `Directory`, the traffic `Socket`,
and `Service` settings: target, path, approved registry/guardrails, tick, window,
maximum evidence age and resource source (`local-host`). Registry/guardrails must
match the traffic profile exactly; the private state carries their fingerprint,
which startup and each authority read verify independently of version labels. A nil `Evaluation` disables inference and policy promotion. Capacity growth is a
separate opt-in profile described in controller.md. Control adopts only the ready, stable-only initial
activation into an empty ledger. A successor with missing history is rejected.
It never replays an activation to discover whether it happened.

Control owns `control.db`, its intent files, service state, rollout journal and
agent history under its private directory. Traffic owns its own snapshot and
socket directory. An exclusive directory lock prevents a second control owner.
Keep both directories across updates. The initial registry is configured and
approved by the operator before this development profile starts traffic.

## Continuous measurement and agent wiring

The service ticks without calling a model. Stable arrival windows supply measured
workload counts and p95 histogram upper bounds to the agent authority. All known
workloads need complete coverage, fresh local-host resources and bounded latency
histograms. Unknown work, pending/uncertain outcomes, stale/mixed revisions and
missing pressure cannot become successful evidence. New activation immediately
invalidates the old observation.

The optional bounded agent worker runs separately from control ticks. The existing
trigger gate requires sustained complete windows; the runner independently checks
freshness, cooldown, frozen state, versions and limits before inference and before
canary dispatch. Its authority calls the real manager, driver, SQLite coordinator
and traffic socket. Model waits do not block freeze or deterministic evaluation.
No provider key enters this process.

To configure automation, supply an owner-reviewed `Service.Evaluation`, `Lease`,
`CalibrationFile`, agent bounds, trigger settings, and `ManagedAI` endpoint,
installation-token file, secret root and timeout. The loader checks the actual
calibration file against the profile's SHA-256, not only a `Validated` boolean.
That digest identifies evidence; it does not certify its statistical adequacy.
There is no calibrated production profile supplied by this checkpoint. G4 remains
open. HTTPS is required for the managed gateway. No paid calls were made here;
the existing persistent $5 test ledger remains unchanged at $0.005854.

## Real private administrator backend

Optional `Admin` configuration supplies a separate literal-loopback `Listen`,
matching `http://IP:port` origin, built UI asset directory, private secret root,
claim-file reference, claim expiry and session lifetime. The health listener and
private administration listener are separate from PostgreSQL and public REST.
The existing claim/password/session/CSRF protections wrap the real backend.

The backend reads actual runtime state, measured aggregates, activation history
and sanitized agent runs. Missing metrics and unverified physical capacity appear
as unavailable. The displayed latency is the largest workload p95 bucket upper
bound, not a pooled percentile. Freeze and resume create durable activation
revisions, and restore recompiles a previously confirmed policy as a new revision
under the current bounds. Restore freezes elective automation and does not undo
SQL. Expected revisions and durable command keys prevent stale writes or duplicate
commands. Imported fallback revisions currently cannot be selected for restore
because their observed-envelope storage differs from activation intents; this
known minor limitation is retained in the review record. Uncertain owner commands block further elective changes and are queried
by revision, never resent automatically.

This is the backend for a preapproved local profile, not complete interactive
onboarding. Online boundary/lock editing, connection-profile editing, reviewed
AI discovery activation remain release work. Offline owner password/claim recovery
is available through `pooltune-owner`; see `owner-recovery.md`. The
API refuses those unsupported mutations. A control restart retains sessions,
freeze state and command results; this is not proof of independent traffic crash
recovery. Do not delete history to clear an uncertain command.

## Local capacity pressure

The optional traffic `CapacitySafety` field supplies explicit approved controller
guardrails with the same guardrail version and a ceiling no greater than the
traffic profile's capacity. The one-second host sampler calls the existing
capacity equations locally. Pressure backoff or hard stop tightens a versioned
admission ceiling, aborts an unaccepted canary and keeps active transactions bound.
It still takes effect if snapshot persistence fails, with an exposed audit gap.
Control later imports and acknowledges that fallback through its ledger.

The opt-in PG and REST capacity paths supply ordered demand windows and actual
backend verification for elective admission growth. REST uses an independently
verified HTTP envelope with a fixed physical pool. Physical/provider pool mutation
remains unavailable. A safety ceiling is latched and cannot be raised by ordinary
activation, ordinary automation resume or restore. An explicit owner safety-stop
resume or nonzero ceiling release requires a fresh five-second healthy runtime proof; see operations.md.
Opt-in independently anchored PG/REST restart
is documented in recovery.md; other restart profiles remain quarantined. The configured numeric thresholds
are operator inputs, not calibration supplied by the software.

## Evidence

`go test -race -tags=integration ./internal/runtime -run TestContinuous -v`
uses isolated real PostgreSQL with TLS/SCRAM, real Unix control, SQLite history
and real authenticated HTTP administration. It verifies measured SQL reaching
the dashboard API, freeze without interrupting SQL, control restart preserving
sessions/freeze, restore-as-new-revision and shutdown. The separate browser suites
still use labeled controlled providers; they do not prove actual Supabase RLS.

Component tests cover bounded sustained triggers, blocked-model isolation,
manager partition/rollback, stale evidence/commands, restore validation,
idempotency across restart, local capacity stop during storage failure and
preserved active transaction bindings. Their artificial profiles are fixtures,
not performance or managed-provider evidence.

Set control `RecoverLocalPostgres` for PG or `RecoverLocalREST` for REST, only
with an independently anchored matching traffic
profile. Control compares its confirmed payload before requesting recovery and
imports the resulting new recovery revision. See recovery.md for refusal handling.

Safety recovery and capacity growth serialize their final scheduler check, local
durable write and installation with admission/outcome accounting. Recovery also
requires zero active work at that boundary; growth rejects any uncertainty,
including uncertainty in the newer capacity snapshot. A late failed safety check
creates a durable rejected intent, so reconciliation does not replay or strand it.
Existing queued bindings and active transactions are retained. Local persistence
latency can briefly delay scheduler accounting during these activations; production
latency calibration must include this cost. A storage failure retains the previous
admission and safety ceiling and marks the audit gap.
