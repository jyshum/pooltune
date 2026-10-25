Latest result: [All three managed spike checks completed](managed-mvp-results.md). Prior experiments below remain historical evidence.

Latest follow-up: [short live confirmation](quick-confirmation.md). Azure first-spike 2,459/3,000 with 694.057 ms successful p95, but a later stale-pressure hold remains. Google failed warm-pool readiness before traffic; the local correction is not cloud-verified. Earlier results below are retained unchanged.

# Managed burst gateway results, October 25, 2026

Supabase's app-through-Pooltune path completed the measured workload with zero errors and without a live Supabase configuration change. Azure and Google connected successfully, but their adaptive trials did not establish reliable first-spike improvement. The MVP is not verified across all three providers.

## Measured results

| Provider | Mode | All completed/offered | First spike completed/offered | First spike successful p95, ms |
|---|---|---:|---:|---:|
| Supabase | fixed-base | 288/1,430 | 208/300 | 2865.992 |
| Supabase | burst | 1,430/1,430 | 300/300 | 246.445 |
| Supabase | fixed-ceiling | 1,430/1,430 | 300/300 | 253.738 |
| Azure | fixed-base | 9,516/14,500 | 1,956/3,000 | 894.810 |
| Azure | burst | 10,122/14,500 | 1,219/3,000 | 690.919 |
| Azure | fixed-ceiling | 11,825/14,500 | 2,469/3,000 | 691.199 |
| Google | fixed-base | 10,951/14,500 | 2,284/3,000 | 766.440 |
| Google | burst | 733/14,500 | 0/3,000 | n/a |
| Google | fixed-ceiling | 13,624/14,500 | 2,863/3,000 | 579.955 |

Supabase first-spike comparison: 300/300 completed with Pooltune versus208/300 with fixed5, or92 additional completions. Successful-request p95 was246.445ms versus2865.992ms, a2.620-second difference at that percentile. This does not mean every request saved2.620seconds. Fixed warm8 also completed300/300 at253.738ms. No superiority over an equally warm, appropriately sized fixed pool is established.

The Supabase fixed5 baseline suffered transport/deadline failures and completed no sustained-phase requests after the first spike. Its recovery failure is not fully localized. Do not turn the full-trial completion gap into a general claim about normal Supabase reliability. Use the qualified first-spike comparison and show fixed8 alongside it.

Azure new-route first-spike results were worse with adaptive admission:1219/3000 versus1956/3000 fixed50 and2469/3000 fixed64. A health hold started about5.34seconds into traffic; admission returned at15.28seconds and reserve was observed at15.48seconds. Overall adaptive completions exceeded fixed50, but fixed64 was better. This is not a successful first-spike fix.

Google's temporary instance solved the regional provisioning obstacle for this test and passed database authentication plus verified TLS. Its adaptive run entered unverified_health at7.38seconds, before the first spike at10seconds, and completed no first-spike requests. Fixed pools continued completing work. A test-command PASS means the harness finished collecting results, not that the adaptive evaluation passed.

## What was actually tested

Each mode offered60seconds of read-only SELECT pg_sleep(0.2) traffic:10seconds steady,10seconds first spike,5seconds with no new arrivals,35seconds sustained. Supabase used16clients,8requests/sec steady and30/sec burst,1430offered requests; admission5->8 with8physical connections prewarmed. Azure/Google used96clients,100/sec steady and300/sec burst,14500offered; admission50->64 with64prewarmed. The harness queue was64requests with a3-second end-to-end deadline; Pooltune's queue deadline was2seconds. Physical connection limits include real network overhead, so theoretical connections/0.2 is not observed throughput.

Supabase route: application -> Pooltune PostgreSQL ingress -> owned PgBouncer -> stable Supabase session endpoint -> PostgreSQL. Its hosted pool configuration stayed10. The compared fixed5/fixed8 values belong to the owned pool, not a Supabase configuration change. Azure and Google new-route tests connected owned PgBouncer to direct managed PostgreSQL. They do not certify native managed-pool resizing. Fixed baselines bypass Pooltune admission. Priority scheduling and AI remain deferred.

These are real cloud SQL connections with a synthetic workload, not production validation. There is no representative customer trace, CPU-heavy query mix, cost saving, physical quiet reclamation, fault-tolerant backup routing or universal optimal-sizing claim. The reserve stays physically warm. No failed SQL is replayed. Equal-size warm fixed pooling remains a strong baseline.

## Presentation wording

> In a60-second synthetic workload against real Supabase, Pooltune completed all1430scheduled requests without errors or a live provider resize. During the10-second spike, it completed300/300requests versus208/300with a five-connection fixed pool. An eight-connection warm fixed pool also completed300/300. Production performance and savings are not established.

Do not say "production-proven," "beats a correctly sized pool," "fixes Supabase's native resize," "all three adapters verified," or "91% faster queries." The percentile measures successful end-to-end requests including queueing, not a query-execution speedup.

## Earlier Azure result remains separate

The earlier native Azure background-controller trial grew50->63 and completed14076/14500 versus12366/14500 fixed50 (+13.8%). Sustained-phase successful p95 was300.980ms versus666.963ms (54.9% lower). Its first10-second spike did not improve. This different architecture/run must not be combined with the new-route results as if they were one experiment.

## Follow-up corrections and remaining gates

The new runtime now preserves an unsuccessful-refresh cache entry only until its original source deadline; it does not stamp old data as fresh. Focused regression passed, but Azure was not live-retested after that correction. The generic health-hold trace does not prove which health subcheck failed at every hold.

Google's old120-second cutoff was shorter than its documented60-second sample cadence and up-to165-second publication delay. The adapter-specific allowance is now240seconds, including one15-second poll, while keeping source timestamps, target binding, strict limits and current SQL inventory checks. This deliberately accepts older Google host evidence and is not an instantaneous CPU-overload detector. The correction has focused regression coverage, not a fresh cloud performance result. The prior failed Google result stays in the record. Source: [Google Cloud SQL metrics](https://docs.cloud.google.com/sql/docs/postgres/admin-api/metrics).

Fresh review found and the implementation addressed four issues: transaction read timeout, unbound pressure/database identity, ownership scope across databases, and clean restart. Relevant regressions are retained. Remaining limitations include exact backend PID retention freezing after replacement, startup signal handling, distributed ownership/recovery and absent automatic backup routing. The measured trials predate some fixes. Do not claim the final dirty source is fully live-certified.

## Cleanup and evidence

Azure settings were restored and its temporary firewall rule removed; stopped after662seconds. Google temporary resource deletion was verified at1278seconds from the original window start, within30minutes. An earlier temporary creation was cleaned up after a setup-operation conflict; the corrected attempt stayed within that same total window. Existing Google database was preserved. No cloud resource was started for the five-minute follow-up correction.

Raw successful-comparison exports, phase summaries, failure classes and SHA-256 hashes are in the adjacent burst-gateway-evidence folder in the user copy, or docs/evidence/burst-gateway-2026-10-25 in the repository. Original failed setup/harness attempts remain under private work/burst-path and are not substituted for accepted comparisons. Summary JSON contains no credentials, network rules or configuration files. Report generation recomputes counts and percentiles from the exports. The recorded measurements are immutable evidence, not a claim that later edits were benchmarked.
