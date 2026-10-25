# Capacity math and local evidence

The controller is implemented as a pure Go function. It proposes changes and
returns effective safety bounds; it does not activate revisions or call a
provider. The opt-in local traffic profile now wires its pressure response into versioned
admission reductions; see control-runtime.md. Verified local PG/REST admission growth and ordered demand are wired below. Physical
warm-pool management and provider operations remain integration work.
Constants below are explicit proposal settings, not calibrated production
defaults. No local unit test establishes cloud readiness or latency performance.

## Contract

`NextCapacity(now, state, metrics, guardrails)` takes an injected clock time,
revision/epoch identity, commanded and pending targets, last conservative bounds,
timestamped evidence and operator-approved configuration. It returns the identity,
time, optional numeric evidence, planned/effective limits, physical desired/warm
budget for PostgreSQL only, stop/recovery state, canary-abort and pending-cancel
flags, a reason and data-quality indicators. Missing numeric evidence is nil.

Admission uses concurrent transactions for PostgreSQL and outstanding requests
for REST. Physical ceilings, observed active-plus-idle connections and verified
usable capacity are separate facts. Observed idle is optional readback; a desired
warm budget is not an observed idle count. REST never emits a physical mutation.
The database owner supplies disjoint fixed PG and REST physical allocations.
Other-consumer buffers exclude both allocations and the admin reserve. No idle
allocation is transferred automatically. Unverified REST topology requires an
explicit conservative envelope and is reported as unverified by the coordinator.

## Eight calculations

1. Arrival rate per workload is `max(arrivals_1s, arrivals_5s / 5)`.
2. Demand is `ceil((sum(rate * baseline) + sum(queue * baseline) / 2) / 0.8)`.
3. Current mean occupancy and healthy baseline use the same successful-completion
   mix, at least 20 successes, and five complete seconds under unchanged settings.
4. Congestion is `ceil(C * max(0, 1 - baseline/current))`, used only with at least
   80% average occupancy. Alpha is `max(1, 0.1*C)` and beta `max(2, 0.2*C)`.
5. Explicit overload failures (at least 20 terminal outcomes), overdue unfinished
   work and fresh CPU/memory signals provide independent overload evidence. Fresh
   stop thresholds or a physical reserve breach latch zero new admission.
6. Physical safe capacity is the minimum of fixed PG allocation, user/provider/
   memory ceilings and database maximum less admin, other and REST reserves.
   Admission also respects its own ceiling. Stale bounds retain the last safe cap.
7. In order: stop/recovery; freeze; storage/provider-uncertainty hold; safety
   backoff; experiment/pending/evaluation/cooldown hold; bounded demand growth;
   quiet reduction; hold. Growth is at most `max(1, ceil(0.25*C))`, backoff uses
   `floor(0.8*C)` with a minimum of one subject to safety bounds, and quiet
   reduction uses `ceil(0.9*C)` with a 30-second quiet period and cut interval.
8. Desired PG capacity is planned admission plus `ceil(w*C_plan)`, bounded by
   physical capacity. Elective physical decreases require a quiet drain period.
   Existing provider targets may remain higher while draining; no eviction is
   authorized. Pressure never authorizes new warm expansion.

The full equation specification is in the parent planning archive. This is a
Netflix concurrency-limits-inspired combination, not a port or claim that Netflix
uses these constants. See https://github.com/Netflix/concurrency-limits.

## State transitions and integration obligations

The control owner calls the function about once per second from cached snapshots.
The pure function has no timer, network requests, background goroutines or hidden
mutable state. B09 owns ticking, durable intent and activation. B08 owns provider
operations and independent usable-capacity verification. API acknowledgement is
never readiness. A repeated pending target has `Changed=false`; it is reconciled
without creating another intent. Safety limits can tighten during freezes/outages.
No pending increase activates during a storage outage or uncertain provider state.

Persist safety latch, healthy-since and last-tick times with the controller state;
clear the healthy interval after restart. Recovery requires explicit resume,
continuous fresh healthy evidence for five seconds and no pending/uncertain
operation. A gap over two seconds resets the recovery interval. Resume starts at
the minimum of approved startup, safe and verified usable capacity. A lower cap
drains existing work; it neither kills transactions nor retries uncertain writes.

## Telemetry

Each recorder has one immutable target/path/policy/capacity/guardrail identity and
nonzero epoch. Recreate on settings change or restart; never combine old epochs.
Register at most 128 workload IDs plus one shared unknown bucket. There are six
fixed one-second buckets; snapshots expose the last five fully elapsed seconds,
one-second arrivals, timestamps and current runtime summaries. A partial startup
window is explicitly incomplete. Unknown demand lacks an invented baseline.

Ingress records arrivals after authentication and fixed quota checks, including
requests later rejected by concurrency admission. Record each terminal outcome
once: success, ordinary failure, overload, timeout or rejection. Uncertain work
is not terminal. Syntax/auth/business failures and local rejects are not counted
as downstream overload. Terminal timing records queue and service separately.

The trusted runtime calls SetInFlight at every occupancy transition and provides
bounded per-workload active/uncertain/queue counts and oldest ages. A serialized
event order is required; periodic samples do not establish time-weighted occupancy.
The opt-in local PG path below now wires these hooks through the scheduler.
Bounded in-memory recording and summaries share its accounting lock, preserving
transition order without I/O or external observer callbacks. Mismatched counts
or stale summaries disable growth. Other ingress profiles remain gated.

Histograms have upper bounds of 1/10/100 microseconds, 1/10/100 milliseconds,
1/10 seconds and an overflow bucket. Return a range or unavailable tail estimate
when the selected bucket cannot establish a finite percentile. Success-only
service histograms are accompanied by all-terminal total/queue histograms,
reject/timeout/uncertain counters and unfinished ages. Never average percentiles.
Recorded/unknown/omitted totals are epoch-level coverage information. Overflow,
invalid timing or backwards clocks invalidate evidence; an omitted event blocks
growth for that recorder's epoch. Counter saturation is bounded and disclosed.

All structures are payload-free. No SQL, rows, request bodies or credentials are
collected. A mutex gives coherent snapshots and counters rather than a custom
lock-free design. Remaining runtime profiles, real provider evidence, tuning and p99
measurements remain B06-B09/B15 work and G1/G3/G4 release gates.

## Opt-in local PostgreSQL admission growth

The local PG runtime now records arrival, queue exit, terminal outcome and every
occupancy transition under the scheduler accounting lock. Telemetry is bounded,
payload-free memory, with no observer callback or I/O in admission. Uncertain
work retains occupancy. An activation starts a fresh evidence window after old
bindings drain. Queue overflow invalidates growth coverage rather than silently
sampling accepted traffic. SQL failures do not become successful service samples.

A one-second background monitor compares authenticated proxy backend PIDs with
`pg_stat_activity`, reads cluster connection limits/reserves, accounts for other
clients, and reserves one monitoring connection. Configured `MaxSessions` is an
owner allocation ceiling, never readiness. Growth is bounded by the number of
verified authenticated sessions. This direct-connection profile does not create
or resize a reusable backend pool or mutate provider settings.

To opt in, set traffic `StartCapacity` below `Capacity` and provide approved
`CapacitySafety` guardrails and workload baselines. In the control profile set
`Service.Capacity.Guardrails` to the identical complete guardrails, plus
`Service.Capacity.EvidenceSHA256` and an absolute `CapacityCalibrationFile`.
The loader reads the private artifact and checks its digest. The runtime also
binds telemetry to the full guardrail digest, not just its version string.
Missing, stale, mismatched or incomplete evidence holds growth.

Continuous control persists a new capacity intent before activation, keeping the
policy unchanged. Freeze, canary/evaluation hold, old bindings, uncertain work,
pending intents and cooldown block growth. Host pressure reductions remain local
to traffic if control is unavailable. Existing safety ceilings are never silently
lifted by a growth proposal. Physical/provider resizing and quiet reclamation remain release work.
The explicit safety-stop resume workflow is described in operations.md.

The integration case measures 100 unqueued TLS proxy requests, then drives real
PostgreSQL traffic with 28 authenticated sessions. The case at higher concurrency demonstrates a bounded 20-to-25 admission increase with actual host readings.
An earlier 1-to-2 case passed, but at capacity 1 small measured latency changes
can legitimately hold growth because congestion is rounded to an integer. Its local `pg_sleep` workload
and test thresholds are not a production calibration or performance benefit claim.

The state API includes the durable safety ceiling so control cannot create a
forbidden growth intent after a reduction. A pending capacity intent is queried
by revision before further elective work, even if growth is disabled or frozen.
A verified active status repairs a lost ACK or confirmation-write failure without
resending activation. Unknown status retains its uncertainty and recovery gate.

## Verified local safety recovery

For both local profiles, `safety-resume` is a separate authenticated owner action.
Ordinary automation resume never lifts a safety ceiling. Traffic requires an
acknowledged safety ceiling, five continuous seconds of fresh local-host
pressure below both grow thresholds, no active or uncertain work, and fresh
backend/capacity verification. A gap over two seconds, revision/epoch change,
unhealthy observation or restart resets the interval. Evidence is runtime-owned
and rechecked when the activation arrives; the request cannot supply a proof.

PostgreSQL requires verified authenticated idle sessions, no other cluster
clients or prepared transactions, and available unreserved slots. REST checks its
owned child, private HTTP readiness, exact child application identity, idle pool,
role permissions, absence of other clients/prepared work and fixed allocation.
HTTP concurrency remains separate from physical connections. Resume uses the
minimum of approved start and verified safe capacity, retains the exact policy,
and clears the latch only through a durable new revision. Freeze, experiments,
pending intents and audit gaps block it. For a nonzero ceiling, verified capacity
must cover the current admission limit. Recovery removes the ceiling at exactly
that limit, preserving policy; it never jumps back to the original capacity.
Subsequent growth requires a new measured window and the normal cooldown.
The current conservative profile requires idle work for recovery, so a busy
installation may need a quiet interval. Existing transactions are not interrupted.

Expired evidence produces a durable rejection, separate from activation history.
Lost replies are queried without replay. Rejected intents never advance the
active revision, and replay/restart preserves their rejection audit. Ambiguous
writes or missing payloads still freeze recovery instead of assuming rejection.
Actual PG and REST integrations inject a stop, observe real host/backend health
for five seconds, then verify resumed SQL or anonymous RLS reads. The stop is
fault injection, not a claim of measured host overload or production calibration.

## Opt-in local REST admission growth

REST uses the same ordered scheduler recorder, controller and durable intent path.
Set traffic `CapacitySafety.Start` below `Capacity`, with its `Ceiling` and
`HTTPEnvelope` equal to `Capacity`. Control uses `Service.Path=rest`, matching
complete `Service.Capacity.Guardrails`, and the private hash-pinned
`CapacityCalibrationFile`/`EvidenceSHA256` setup above. Unknown workload demand,
missing baseline or telemetry coverage, uncertainty, freeze, canary, cooldown and
latched ceilings block growth as on PG.

A background probe checks actual cluster slots, visibility, the restricted login,
prepared-work absence and the fixed physical allocation. Only the exact owned
PostgREST application's active/idle backends are allowed for growth. Recovery
continues to require idle work. The owned child and private HTTP endpoint must
be ready. Missing evidence immediately replaces the cache, and stale evidence
cannot grow admission. The verified ready value is an HTTP request envelope;
physical observations separately report `RESTEnvelope` and `ObservedREST` in
connections. No REST decision resizes PostgREST, Supavisor or a provider.

Completed HTTP errors are terminal failures, not successful occupancy samples.
HTTP 429/503/504 are recorded as overload evidence; interrupted responses retain
uncertain occupancy. Pre-classification request-slot overflow invalidates capacity
coverage for the revision. This avoids learning from only successful or admitted
requests while overload is omitted.

Actual local testing measures 100 unqueued TLS HTTP requests and runs 28
concurrent RLS-protected requests, producing HTTP admission growth 20 to 25 while
the physical allocation stays 32. Client transport is included in that test
baseline, so it is explicitly a correctness exercise, not the exact permit-boundary
production calibration required by G4. No performance improvement is claimed.

Safety recovery and capacity growth serialize their final scheduler check, local
durable write and installation with admission/outcome accounting. Recovery also
requires zero active work at that boundary; growth rejects any uncertainty,
including uncertainty in the newer capacity snapshot. A late failed safety check
creates a durable rejected intent, so reconciliation does not replay or strand it.
Existing queued bindings and active transactions are retained. Local persistence
latency can briefly delay scheduler accounting during these activations; production
latency calibration must include this cost. A storage failure retains the previous
admission and safety ceiling and marks the audit gap.
