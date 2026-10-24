# Bounded local scheduler

`internal/scheduler` owns admission for one target and one ingress path. Use separate instances and allocations for PostgreSQL transactions and REST requests. One process owns the target/path scheduler, including its stable and candidate cohorts. The package performs no network, storage, SQL parsing, authentication, or AI calls. The executable remains health-only.

## API and trusted inputs

Construct with `New(Config, Snapshot)`. Every config field requires an explicit positive ceiling; the test fixtures are not production defaults. `MaxActive` bounds accepted effective capacity. Queue count, queued buffered bytes, distinct fairness keys, distinct workload keys, per-tenant queued count, and per-tenant occupancy have independent ceilings. Structural maxima are 100,000 queued entries, 4,096 fairness keys, 4,096 workload keys, two cohorts, and 1,000,000 burst tokens. These bound implementation work and memory; they are not calibrated safe database values.

Call `Acquire(context.Context, domain.Classification)`. The trusted ingress authenticates identity, calls B03, assigns a cohort, and supplies truthful `BufferedBytes` for all retained payload buffers. The scheduler stores the byte count, not the payload. Queue count bounds zero-byte requests. The scheduler rejects invalid, blocked, stopped-cohort, expired, or stale-new-arrival classifications before admission. Classification is a copied value. Identifiers have a 128-byte ceiling, while the length-prefixed B03 fairness key allows 544 bytes. Client hints must never bypass B03 or choose privileged classifications.

The effective queue deadline is the earlier of the classification deadline and context deadline. The classification deadline already includes the policy maximum queue wait. Local `time.Now` values retain Go's monotonic clock component. Do not replace local deadlines with wall-clock-only timestamps from remote clients. Cancellation, expiry, and grant compete under one mutex. Exactly one result wins: a permit with nil error, or nil permit with an error. If a grant wins a cancellation race, the caller owns the returned permit and must handle its backend outcome.

Every arrival enters the same scheduling queues, including arrivals while capacity is free. `QueueLimit` caps total waiting entries for that workload across classes and cohorts. `ConcurrencyCap` caps active/uncertain workload occupancy and the arriving request's tenant occupancy, subject to the stricter global `MaxTenantActive`. Tenant counts span all classes, workloads, and cohorts. Policy cap reductions do not kill existing work; they restrict subsequent grants. A full queue can reject a new arrival even when a bounded dispatch continuation is pending.

Per-tenant token buckets bound accepted arrival rate and burst. `ErrRateLimited` and the `RateRejected` counter distinguish this case from `ErrQueueFull`. Rejected queue/cardinality attempts do not consume tokens. Idle rate state stays indexed until the bucket has fully refilled, so rotating identities cannot evict depleted state to reset their bucket. Rate state uses the same fairness-key ceiling; exhausted cardinality returns `ErrCardinality`. A bounded expiry heap reclaims eligible idle state without scanning all tenants. Rate expiry for exceptionally small rates is checked at most one year ahead and extended until refill is actually complete.

## Permit lifecycle and observations

A permit binds its owning scheduler, cohort, class, workload, fairness identity, original policy revision, admission-time limit revision, and grant timestamp. Read the copied binding through `Classification()`, `LimitRevision()`, and `StartedAt()`. Policy and limit revisions can differ after capacity-only changes. These bindings remain unchanged by later installs and rollbacks.

Call `Release(permit, Finished)` only when backend completion is known. It frees occupancy once. `Release(permit, Unknown)` retains all occupancy as an uncertain reservation. Unrecognized outcomes conservatively behave as unknown. Repeated release, release through another scheduler, and release of a copied permit cannot change accounting. Frontend cancellation or disconnection is not proof of backend completion.

`Reconcile(permit)` frees an uncertain reservation once, after the protocol/recovery owner positively confirms backend completion. It returns whether reconciliation won. This API does not supply that evidence or reconstruct orphaned permits after process restart; G1 recovery work remains required.

`Stats()` returns a consistent copied snapshot of aggregate effective capacity and critical reserve, occupancy, uncertain occupancy, queued count/bytes, bounded key counts, rate rejections, and each cohort's budget/reserve/stop state. `ByWorkload` contains active, uncertain, queued, oldest-active age, and oldest-queued age. Active and queued heads are maintained per workload, so a snapshot walks at most the configured workload keys, not every request. Unknown work retains its original active age. This is an input for B05, not a telemetry service or completion-window evaluator.

## Capacity and fairness

For cohort budget `B > 0`, compute reserve `r = min(floor(rho*B), B-1)` from that cohort's compiled policy, with shared capacity `S = B-r`. At zero budget nothing is admitted. Before every grant, check aggregate effective capacity, the cohort budget, workload capacity, and tenant capacity, including uncertain reservations.

An eligible critical queue fills unoccupied reserve first. All classes compete for shared capacity when `max(0, activeCritical-r) + activeNormal + activeBackground < S`. There is always at least one shared slot at positive budget. Normal/background work never borrows the protected reserve. An idle reserve therefore costs utilization.

Each compiled policy's positive weights become a smooth interleaved round-robin cycle of at most 256 entries outside the mutex. Empty/capped classes are skipped without banked credit. Within each class, rotate tenant FIFO heads. A tenant's FIFO uses an enqueue-order heap so rollback can merge entries into another cohort without scanning requests or resetting order. FIFO means a capped head can block later work from that same tenant/class; another tenant remains eligible. Background progress requires completions and eligibility; weights do not guarantee deadlines or backend CPU shares.

Dispatch runs on arrival, release, reconciliation, expiry, and installs. It removes at most 32 expired entries and 32 idle rate entries and processes at most 32 grants/cancelled heads per pass. If more work is immediately eligible, one queued continuation starts another bounded pass. A capped set does not schedule a spinning continuation. Selection inspects only bounded tenant heads, never all waiting requests; worst-case selection still depends on configured tenant cardinality. Deadline wakeups use a timer rather than a controller polling tick. No performance or p99 guarantee follows from these bounds.

## Installing limits and policies

`Install(Snapshot)` atomically installs policies and cohort budgets. It copies maps and compiles class-weight cycles outside the accounting mutex. Effective capacity can be zero or lower than current occupancy. Existing permits drain normally; no occupancy is forgotten or work killed. Budget sums and queued bytes are checked with subtraction before addition to prevent integer wraparound.

Target, ingress path, guardrail version, and nonzero ownership epoch must match the initial owner. Epoch is an ownership identity, not an update sequence. The limit revision is an opaque binding for telemetry. B09 must enforce durable predecessor ordering, activation authority, and restart reconciliation. `LimitSnapshot.CriticalReserve` must equal the sum of actual cohort reserves derived from each compiled policy and budget, including the zero/one-slot cases. Inconsistent snapshots are rejected. Capacity callers must recompute this aggregate field when changing cohort budgets.

`InstallLimits(LimitSnapshot)` preserves cohort policies and stop fences. It prepares outside the mutex and returns `ErrStale` if another install wins before it can apply; refresh the control snapshot and retry. It cannot accidentally restore an old policy. Caller-owned maps may be changed after return, but must not be mutated concurrently during a call.

Cohorts have independent budgets under the aggregate ceiling. They do not lend slots. Installing a split while stable occupancy exceeds its new share drains into the partition; candidate grants still check aggregate occupancy. A cohort cannot be removed until its active, uncertain, and queued work drains. Ordinary policy changes affect new classifications. Existing queued entries retain class, policy revision, enqueue order, and deadline while using current capacity checks and class scheduling settings.

The scheduler supports independent canary budgets, but does not establish a valid experiment. B09/B12 must enforce the 5-10% cohort assignment, minimum two slots per cohort, integer representation of the changed policy effect, baseline split, sample balance, safety abort, and promotion criteria. Tiny budgets remain supported for normal operation and safety backoff. Shadow results do not replace a real canary.

## Bounded rollback

`StopCohort(name)` is a synchronous fence on candidate grants and new arrivals. The stop survives installs. To reuse its name for a later experiment, drain it, remove it in an install, then add a new cohort in a subsequent install.

`RollbackBatch(candidate, stable, callback)` first fences candidate admission, detaches at most 32 candidate waiters, and invokes the supplied trusted mapping outside the lock. Call again while the returned candidate queued count is positive. Concurrent batches for the same candidate return `ErrRollbackBusy`. The mapping must perform bounded local work using the stable policy and an immutable workload-keyed registry. It must retain workload ID, fairness identity, and buffered-byte count. If a future classifier needs more trusted request context than that mapping provides, the ingress/rollout owner must retain it safely; this package does not infer identity or classification from client hints.

Successful results bind the current stable revision and cohort, retain original enqueue order/time, and take the earlier original/new deadline. Active candidate permits keep their old binding and occupancy. Callback errors, including direct or wrapped `ErrBlocked`, panics, stale results, or changed identities preserve stopped candidate work for retry. Only a successful mapping with a validated stable revision and unchanged workload/fairness identity can explicitly reject work by setting `Blocked=true`; the waiter then receives `ErrBlocked`. Cancellation and expiry can win while the callback runs. Queue and byte accounting remains reserved throughout the detached interval.

## Verification scope

Tests exercise reserve/share behavior, zero/one-slot budgets, class weights, tenant rotation, cross-cohort caps, cancellation races, uncertain reconciliation, count/byte/key/rate ceilings, expiry, draining, copied inputs, invalid snapshots, arithmetic overflow, old bindings, bounded rollback, and workload age heads. The two admission benchmarks measure local Go accounting fixtures without a database, network, TLS, or model work. They do not validate provider behavior, production load, or tail latency.

G1-G5 remain open for their affected capabilities: crash recovery/orphan resolution, public administration and ingress security, actual provider/protocol compatibility, calibrated performance and promotion profiles, and funded AI/data handling. No live traffic path or cloud/model integration is enabled by B04.
