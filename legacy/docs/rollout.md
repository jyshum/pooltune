# Fixed-profile canary evaluator

One manager owns one database target, across its PostgreSQL and REST paths. A
private directory lock and a compare-and-swap SQLite journal prevent competing
writers. Experiments use a split stable-policy baseline, then 10% (or eligible 5%),
25%, 50% and 100% candidate exposure. Acceptance is a separate revision after full
observation. Unknown acknowledgements are queried, never blindly resent. Restart
aborts unaccepted experiments through the traffic service's cached fallback.

The profile is fixed before candidate creation, with a calibration evidence hash,
explicit objectives for every affected workload, sample/tail counts, observation
windows, freshness, tolerances, pressure limits, passing-window count and cooldown.
No validated profile means no autonomous promotion. A candidate revision gets one
attempt, including after restart; a changed key does not rerun it until it passes.
A new candidate still observes cooldown. Profiles may explicitly preauthorize a
neutral outcome, otherwise the declared latency benefit is required.

Budgets sum to the fixed effective capacity. Initial cohorts need at least two
slots each. A reserve or concurrency change that disappears through rounding is
ineligible; proportionally equivalent weights are also ineligible. The manager
holds elective provider and capacity changes across the database during an
experiment. Safety reductions abort it. No capacity is borrowed between paths.

Measurements are matched by workload, independent of candidate class changes.
Every eligible arrival has one outcome or pending count; success histograms must
match success counts. Failures, rejections, timeouts, unresolved work and completion
fractions prevent a misleading latency win. Oldest queued/active work and database
pressure bypass sampling thresholds. Missing samples, stale revisions, skewed
actual exposure, demand changes and incomplete windows cannot promote. Independent
passing windows cannot overlap. Stage timeout restores stable as inconclusive.
At 100%, the result explicitly reports that no concurrent stable control exists.

The append-only rollout journal reuses control SQLite. Intent precedes activation;
confirmation follows an observed active revision. A storage failure cannot block
preauthorized safety rollback, and the manager keeps its audit gap visible. Its
runtime driver must preserve transaction bindings, reclassify queues without
extending deadlines, and only confirm budgets once excess occupancy has drained.

## Integration boundary

`control.RolloutDriver` now connects the manager to the durable coordinator and
traffic engine, including separate activation/policy/capacity revisions, baseline
policy reuse, binding drain checks, a two-step full-traffic switch, acceptance
queue migration, restart acceptance reconciliation and scoped rollback. The
PostgreSQL ingress supplies random authenticated session identities and bounded,
payload-free observation records. Real local PostgreSQL canary/rollback evidence
and the runnable local traffic profile are described in `local-runtime.md`.

Arrival-window aggregation and same-host resource signals now flow through the
private measurement API into the driver. Unknown signals do not become healthy
zeroes, and live oldest work is checked even outside the sampled window. Timing
tests ensure a live RPC timestamp is not rejected as being in the future and an
open window does not consume the next evaluation interval. The configured
evaluation interval must not exceed the minimum window.

The local continuous control service now ticks the manager and connects the bounded
agent authority to it. Remaining integration work: calibrated G4 profiles, complete recovery of uncertain owner commands, verified HTTP
cohort identity integration, and complete customer recovery. No missing metrics
are replaced with synthetic success values: a driver without a configured collector reports
metrics unavailable, so the manager cannot promote it. Unit profiles are fixtures,
not production calibration. The complete MVP and managed-provider canary benefit
are not yet established.
