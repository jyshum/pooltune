# Fixed versus automatic pool sizing

**Exploratory local diagnostic. No production performance or release approval.**

Four real PostgreSQL/PgBouncer cases, 8,640 offered reads each, 34,560 total. Each case includes a real 70-second quiet interval and existing 30-second controller quiet windows. AI and business-priority scheduling are disabled. Current Pooltune forwarding/admission is still present in its two cases; this is not the proposed background-only architecture.

Source base `c4152d39efaddad6d168b306bd9f57012689bff3`, dirty checkout. arch: arm64, cpus: 15, dirty: true, go: go1.27.3, os: darwin, pg_config: PostgreSQL 17.11 (Homebrew), pgbouncer: PgBouncer 1.26.1
libevent 2.1.13-stable
adns: evdns2
tls: OpenSSL 4.0.3 29 Sep 2026, sysctl: Apple M5 Pro. The archived source patch/hashes retain the exact measured context, including unfinished physical work.

## Main finding

**The current automatic tuner did not demonstrate a benefit in this workload.**
All four cases completed the initial 120 steady and 1,920 burst requests. During
late quiet, native PgBouncer cleanup retained 2 established connections; the other
cases retained 20. Automatic tuning reduced admission 20 -> 18 -> 17 but did not
reduce the physical pool's maximum/minimum or established connection count.

After traffic returned, automatic completed 901/6,600 requests, compared with
1,615 for fixed warm, 1,546 for native idle cleanup and 1,614 for the frozen
Pooltune path. Every case had severe overload failures. Both Pooltune cases
retained uncertain reservations (automatic 17, frozen 20), so current forwarding
and cancellation/recovery behavior also needs investigation. The measurement
cannot attribute every failure to sizing or establish its exact cause.

This run starts the automatic pool fully warm at the same 20-connection ceiling
as the fixed baseline. It tests the idle/return tradeoff; it does not establish
benefit from starting smaller and growing, adapting to CPU-heavy queries, or
choosing an optimal ceiling. PgBouncer already manages actual connections within
a fixed ceiling. A stronger product claim requires evidence that changing that
ceiling under changing database conditions adds value beyond native cleanup.

The practical next work is to diagnose physical reclamation and post-timeout
behavior, then test the proposed background-only controller independently. This
result does not approve extra features, release the current runtime, or prove
that automatic tuning can never help.

## Outcomes

Success-only p95 is in milliseconds and must be read with completion/failure counts. Failed means a generic connection/query error, not necessarily a timeout or an identified rejection. The one-second deadline starts at scheduled arrival; driver-session waiting is included.

| Case | Phase | Offered | Success | Timeout | Failed | Successful p95 |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| fixed_warm | steady | 120 | 120 | 0 | 0 | 54.8 |
| fixed_warm | burst | 1920 | 1920 | 0 | 0 | 53.1 |
| fixed_warm | rebound | 3000 | 1615 | 1385 | 0 | 950.8 |
| fixed_warm | saturation | 3600 | 0 | 3460 | 140 | n/a |
| fixed_native_idle | steady | 120 | 120 | 0 | 0 | 54.8 |
| fixed_native_idle | burst | 1920 | 1920 | 0 | 0 | 53.2 |
| fixed_native_idle | rebound | 3000 | 1546 | 1454 | 0 | 954.8 |
| fixed_native_idle | saturation | 3600 | 0 | 3600 | 0 | n/a |
| fixed_same_path | steady | 120 | 120 | 0 | 0 | 55.3 |
| fixed_same_path | burst | 1920 | 1920 | 0 | 0 | 53.4 |
| fixed_same_path | rebound | 3000 | 1614 | 129 | 1257 | 951.2 |
| fixed_same_path | saturation | 3600 | 0 | 192 | 3408 | n/a |
| automatic | steady | 120 | 120 | 0 | 0 | 53.5 |
| automatic | burst | 1920 | 1920 | 0 | 0 | 53.2 |
| automatic | rebound | 3000 | 901 | 176 | 1923 | 951.3 |
| automatic | saturation | 3600 | 0 | 210 | 3390 | n/a |

## Observed connections

Late quiet counts are the valid samples between 65 and 77 seconds, before driver sessions reopen. These are approximate 1 Hz observations; no precise transition-time or integrated connection-saving claim. PgBouncer cancellation transport and new connections without a backend PID are separate from established SQL backend identities.

| Case | Late quiet established connections | Late quiet admission | Maximum observed uncertain reservations | Valid inventory samples |
| --- | ---: | ---: | ---: | ---: |
| fixed_warm | 20 to 20 | pool ceiling20; no Pooltune admission | 0 | 92/92 |
| fixed_native_idle | 2 to 2 | pool ceiling20; no Pooltune admission | 0 | 92/92 |
| fixed_same_path | 20 to 20 | 20 to 20 | 20 | 92/92 |
| automatic | 20 to 20 | 17 to 18 | 17 | 92/92 |

## Interpretation boundaries

- Compare automatic with fixed_same_path for the current tuning behavior, and compare both with direct baselines for the total current product path. A poor forwarding result is not proof that background-only pool tuning cannot help.
- fixed_native_idle uses a fixed maximum 20, minimum 2 and PgBouncer native idle cleanup. It is already capable of opening/reclaiming connections within that fixed ceiling. This makes a stronger comparator than a permanently full warm pool. See [PgBouncer configuration](https://www.pgbouncer.org/config).
- This is one exploratory round with a predeclared fixed order. The workload uses 50 ms SQL sleeps and authenticated reads. It tests occupancy contention, not CPU saturation, varied query types, writes, hosted providers or production benefit. Baselines were measured from 100 real serial reads for each case; those local inputs are not production calibration.
- Generic failed requests are prominently retained. Cancellation/admission observations can suggest follow-up investigations, but safe SQLSTATE/stage details and a dedicated diagnosis are required to establish causes.
- Whole-host CPU/memory samples include the driver and unrelated host activity. Idle-connection reductions do not by themselves establish lower memory use, money saved or extra database compute.
- Driver sessions are closed after the first burst and reopened before rebound in every case to avoid the current proxy 60-second idle timeout. Reconnection duration is recorded separately and excluded from the offered-query timing. The underlying pool is not reset during the quiet period.

## Verification and evidence

Every accepted trial retained all 8,640 scheduled outcomes, verified original database identity on successful responses, checked its configured maximum and established backend-count samples, and passed the inventory-coverage check. The report Complete flag is true and every ValidationErrors list is empty.

- [Profile and reproduction](../bench/pool-sizing.md)
- [Raw results, gzip JSON](evidence/pool-sizing-v1.json.gz)
- [Source hashes](evidence/pool-sizing-v1-source.json)
- [Exact source delta, gzip patch](evidence/pool-sizing-v1-workspace.patch.gz)
- [Accepted run log](evidence/pool-sizing-v1.log)
- [Independent review and scoped rulings](evidence/pool-sizing-review.md)
- Raw SHA-256: `3d163c146194603e1448ac48d7968ea04161d0576f6b35f94a0cbb86691b293a`

The original attempt stopped after197.58s on a raw server-row assertion and lost its raw observations. Its log/source are preserved, and it is not used as comparison evidence. A real cancellation regression separately observed 21 raw PgBouncer rows but 20 established SQL backend identities and 20 actual PostgreSQL application backends. The accepted run excludes cancellation transport from SQL capacity, records it separately, and retains the cap validation. New evidence/checkpoint and inventory tests were observed failing before the fixes.

Deferred review minors: approximate pre-probe sample timing; generic failure categories. These limit causal and precise savings claims. No re-review after the one fix pass.

The current physical runtime, independent pooled recovery, proposed background-only tuner, crash behavior, managed-provider compatibility and customer release remain unapproved. No production behavior changed for this benchmark. No paid calls; existing persistent $5 cap and $0.005854 accounting preserved.

Post-fix verification: full ordinary source-root Go race suite and vet (including integration/performance source analysis) pass. The accepted real comparison passed in 395.339s; actual cancellation regression passed in 4.040s. The earlier full production race/integration checkpoint remains separate; no production code changed during this diagnostic. No browser/container/package or paid-model run was needed.
