# Fixed versus automatic pool sizing: local-pg-sizing-v1

Predeclared diagnostic profile. No production or savings threshold is asserted.
Run `bench/run-sizing.sh /absolute/new/result.json`. Requires Go, PostgreSQL and
PgBouncer. No model/cloud calls, no priority classes, no concurrent benchmark jobs.

Four cases, one exploratory round before deciding whether replication is useful:
1. fixed_warm: direct PgBouncer, maximum20 and minimum20.
2. fixed_native_idle: direct PgBouncer, maximum20 and minimum2, native idle cleanup.
3. fixed_same_path: existing Pooltune forwarding runtime, admission20, pool20,
   elective tuning frozen. Separates runtime-path behavior from tuning behavior.
4. automatic: existing runtime/control, starting admission/pool20, minimum
   admission2, maximum admission20, warm ratio0. Existing one-second tick,
   five-second measurement/cooldown and30-second quiet windows stay unchanged.

All use PostgreSQL role CONNECTION LIMIT20, transaction pooling, no reserve pool,
server_idle_timeout1s, verified TLS/SCRAM, one role/class,48 persistent client
sessions, and the same scheduled read trace. The hard PostgreSQL role limit is
separate from the52-client runtime connection limit. Fixed native cleanup is a
real comparator, not a weaker deliberately undersized pool. PgBouncer documents
min_pool_size, default_pool_size and server_idle_timeout at
https://www.pgbouncer.org/config .

Before each trial, measure100 serial real50ms sleep/read queries through PgBouncer
for the existing controller baseline; record mean/variance. These are measured
local inputs, not production calibration. Explicit local diagnostic host thresholds
are grow.95/backoff.98/stop.999; whole-host CPU/memory includes all components.

Trace: steady0-3s at40 requests/s; burst3-9s at320/s; quiet9-79s with no offered
queries; rebound79-85s at500/s; saturation85-91s at600/s. All measured queries use
SELECT current_user with a50ms SQL sleep. This exercises connection occupancy,
not CPU saturation or representative customer SQL. Every request has a one-second
deadline from its scheduled arrival. Record client-session waiting and all failures.
No automatic request retry. Successful responses must preserve the original role.

Warm20 actual backends before measuring. Close idle driver sessions after the first
burst and reopen before rebound because the current forwarding runtime has a
60-second idle session timeout. The same reconnection procedure applies to all
cases; connection setup is outside the offered query timing and explicitly timed
separately. One direct keeper client remains connected throughout every case.

Sample actual PgBouncer server inventory and settings once per second, plus
scheduler admission/queue/uncertainty and whole-host pressure where available.
Capture connection counts through the70-second quiet interval rather than equating
a configured target with physical reclamation. Missing observations stay missing.

This tests the CURRENT controller and physical adapter, including existing
forwarding/admission. It is not the proposed background-only product. That design,
independent tuner crash behavior, pool recovery, hosted profiles and resource/cost
savings need separate evidence. Tests validate identity, hard caps, trace coverage
and artifact integrity; they do not force the controller to resize or pick a winner.

Execution order: automatic, fixed_warm, fixed_native_idle, fixed_same_path.
This single exploratory round is not randomized statistical evidence.

Inventory correction before the accepted run: SHOW SERVERS also includes
active_cancel transport sockets. They are recorded separately and excluded from
established SQL backend counts; new connections without a backend PID are also
separate. Unknown states or duplicate established PIDs invalidate the observation.
The raw-row count must not be equated with database capacity. A separate real
cancellation test cross-checks this distinction against pg_stat_activity.

The result is checkpointed after every trial, including validation failures,
with Complete=false until all cases validate. Connection/config observations are
approximately one-second samples, timestamped before their probes; do not infer
precise transition times or integrated connection savings. Generic Failed outcomes
are retained but not attributed to a specific cause without a separate diagnosis.
