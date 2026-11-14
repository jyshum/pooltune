# Managed gateway results

All three target providers now have a successful live adaptive spike test. These use real managed PostgreSQL with a synthetic, read-only workload. Azure and Google completed the latest run without a health pause, SQL error or transport/deadline failure. Supabase's accepted result is reused without another test.

| Provider | Mode | Completed / offered | First-spike completions | First-spike successful p95 |
|---|---|---:|---:|---:|
| Azure | fixed-base | 9,516 / 14,500 | 1,956 / 3,000 | 894.810 ms |
| Azure | burst | 11,874 / 14,500 | 2,476 / 3,000 | 694.561 ms |
| Azure | fixed-ceiling | 11,825 / 14,500 | 2,469 / 3,000 | 691.199 ms |
| Google | fixed-base | 10,951 / 14,500 | 2,284 / 3,000 | 766.440 ms |
| Google | burst | 13,621 / 14,500 | 2,853 / 3,000 | 582.992 ms |
| Google | fixed-ceiling | 13,624 / 14,500 | 2,863 / 3,000 | 579.955 ms |
| Supabase | fixed-base | 288 / 1,430 | 208 / 300 | 2865.992 ms |
| Supabase | burst | 1,430 / 1,430 | 300 / 300 | 246.445 ms |
| Supabase | fixed-ceiling | 1,430 / 1,430 | 300 / 300 | 253.738 ms |

Azure completed 2,358 more requests overall than the earlier fixed-50 baseline. Google completed 2,670 more. In the first spike, Azure completed 520 more and Google 569 more. These are cross-run comparisons, not randomized estimates. Both adaptive runs were comparable to the earlier warm fixed-64 runs. No superiority over an equally sized warm fixed pool is established.

Azure reserve was observed about160ms after the spike began; Google about180ms; Supabase's earlier measurement was about220ms. These are sampled observations, not latency guarantees. Remaining Azure/Google failures were generator queue rejections:2,626 and879 respectively. They are not database errors or completed requests. Supabase adaptive completed all1,430 with zero failures. The smaller Supabase fixed-5 baseline had an unlocalized recovery collapse after its initial spike; do not market that aggregate gap as normal Supabase behavior.

## Fixes verified

Azure's provider-specific source-age allowance is135seconds:60seconds emission, up to60seconds publication and15seconds polling allowance. Azure is polled every5seconds. Source timestamps are preserved; expired evidence still stops admission. This accepts15seconds older evidence than the previous120-second setting. It is not instantaneous CPU protection. [Azure metric emission](https://learn.microsoft.com/en-us/azure/postgresql/monitor/concepts-monitoring), [platform metric publication](https://learn.microsoft.com/en-us/azure/azure-monitor/logs/data-ingestion-time).

Google startup now recognizes already established PgBouncer backends in ordinary used/tested states. PgBouncer still checks them before reuse; exact PostgreSQL backend identity, connection budget and authentication checks remain required. Google completed the live workload with this correction. [PgBouncer states](https://www.pgbouncer.org/usage.html#show-pools).

## Test and support boundaries

Each mode offers60seconds of traffic:10seconds steady,10seconds first spike,5seconds no new arrivals,35seconds sustained. Queries are SELECT pg_sleep(0.2). Azure/Google use96clients,100RPS steady and300RPS peak,14,500offered. Supabase uses16clients,8/30RPS,1,430offered. Successful p95 excludes failed/rejected requests. Sorted percentile index is floor(0.95*n), capped at n-1.

The application connects through Pooltune and an owned PgBouncer pool. Azure/Google use direct managed PostgreSQL; Supabase uses its stable session endpoint. This does not certify native Azure/Google managed-pool resizing or disruptive Supavisor resizing. Hosted settings stay unchanged. Warm reserve is physically retained, so physical reclamation and cloud-bill savings are not established.

Production query mixes, long-duration operation, distributed ownership, automatic backup routing and transparent crash recovery remain outside this proof. No failed SQL is replayed. Unknown transaction outcomes retain capacity until reconciliation. Exact backend replacement can freeze the gateway. AI and priority scheduling remain deferred.

## Service UI and delivery

The private managed dashboard now shows actual admission, established capacity, queue, active/unknown work and source-metric age. It requires a private claim token and administrator login. It is read-only; CLI startup controls admission and Ctrl+C drains the service. The browser marks lost or expired responses unavailable. Browser/server clock skew can cause a false stale display when accessed remotely.

The cloud-tested runner was compiled before the dashboard work. Its hash is in the evidence summary. The dashboard has separate API/auth, browser and package checks; it was not the binary running the cloud comparison. Detailed setup is in burst-gateway.md. Claude's public website is unchanged at the user's request.

Evidence includes sanitized raw trials and recomputed summaries with hashes. Original private exports are retained; source resource identifiers were removed from the delivered copies. Earlier failed trials remain documented separately and are not relabeled as passed.
