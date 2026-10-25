# Local warm-pool comparison, profile local-rest-warm-v1

This is a diagnostic comparison, not production calibration or a release gate pass.
Run `bench/run.sh /absolute/new/result.json`. Requires Go, PostgreSQL initdb/pg_ctl,
and PostgREST in PATH. No cloud or model calls. Run without other test suites.

Four modes: warm_pool (minimal TLS forwarding to actual PostgREST),
application_priority (same forwarding plus the existing scheduler called by the
load-generating application), pooltune_equal, and pooltune_priority (actual REST
runtime). The application reference reuses the scheduler to compare the existing paths;
it is not an independent competing scheduler or a shipped customer SDK.

Every mode uses the same PostgreSQL connection limit and PostgREST allocation of
four, the same signed identities/RLS, verified TLS, persistent client connections,
queue bounds and offered request trace. Priority modes reserve one connection for
critical work, with weights 4:1:1. Equal mode has no reserve and weights 1:1:1.
AI, elective capacity changes, canaries and promotion are disabled. The real
runtime and its child are started in all cases; direct modes bypass only admission
and ingress. They retain idle runtime monitoring. This is a controlled local
comparison, not a zero-process baseline.

Fixed traces, declared before measuring:
- steady: 60 requests, 15 ms apart, alternating critical/background, 3 ms SQL sleep.
- burst: 40 background at time zero, 8 critical beginning at 20 ms, 2 ms apart.
- sustained: 160 requests, 4 ms apart, every fourth critical.
Burst/sustained use 40 ms SQL sleep. All functions read real RLS-protected rows.
The sleeps model connection occupancy, not CPU-heavy or representative customer SQL.
Critical deadlines are 200 ms and background deadlines 600 ms, from scheduled
arrival. Queues are bounded at 200, with maximum wait 1 s. Every offered request
is retained, including failures, timeouts, rejection and generator delay.

Each trial has a new database/runtime and four verified warmed connections.
Three repetitions rotate mode order. Percentiles use nearest rank. All-outcome
completion latency includes censored timeouts and fast rejections; it must not be
read as successful-service latency. Report success-only latency alongside all
outcome counts and deadline misses. p99 from these small trials is exploratory.
Whole-host CPU/memory includes the driver and other applications, not isolated DB
resources. Scheduled-to-completion timing includes application queue waiting.
Only application-side queue delay is directly instrumented here. The low-load
end-to-end difference is an estimate of added path cost, not a pure forwarding
microbenchmark. Separate Pooltune queue/downstream spans remain unavailable in
this profile and must not be invented.

The output includes raw payload-free samples, versions, source revision/dirty
state, host context, warm connection count and summaries. No credentials or row
contents are saved. No assertion chooses a winner. Assertions verify warm capacity,
response authorization, trace coverage and accounting. This profile does not test
AI benefit, resizing, crash failover, hosted providers, writes or browser behavior.

The direct/application reference reuses upstream HTTP connections. Current
Pooltune ingress disables upstream keepalive. Thus the low-load difference includes
transport behavior as well as admission, authentication and forwarding. A
transport-normalized microbenchmark remains future work.
