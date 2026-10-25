Latest result: [All three managed spike checks completed](managed-mvp-results.md). Prior experiments below remain historical evidence.

Latest checkpoint, October25: see [Managed burst gateway results](burst-gateway-results.md) and [machine-readable evidence](evidence/burst-gateway-2026-10-25/summary.json). Supabase new-route spike evidence is positive against fixed5 and comparable to fixed8. Azure/Google new-route adaptive verification remains incomplete. Older results below are retained as history.

Latest evidence, October25: a subsequent Azure default50 comparison now shows positive sustained-load benefit,12366 versus14076completed out of14500; actual growth50->63, sustainedp95 666.963ms versus300.980ms. The initial10-secondspike did not improve. Supabase/Google remain blocked. The following is the earlier undersized5->8 experiment, retained as history. The subsequent gateway results and their public raw evidence are linked above.

# Managed test result, October 25, 2026

**The tuner helped the existing undersized Azure pool in this bounded test. It did not outperform a well-sized fixed warm pool.** These are separate findings. The earlier comparison omitted the unchanged fixed5 baseline; that missing baseline has now been measured.

| Mode | Completed / scheduled | Deadline failures | Client-queue rejections | Burst p95 among completed requests |
|---|---:|---:|---:|---:|
| Existing fixed5 | 793 / 1,561 | 678 | 90 | 2.868 seconds |
| Actual tuner5 ->7 ->8 | 1,561 / 1,561 | 0 | 0 | 2.322 seconds |
| Fixed warm8 | 1,561 / 1,561 | 0 | 0 | 0.284 seconds |

Compared with leaving the pool at5, the tuner completed768 additional scheduled requests. Completion rose from50.8% to100%. Successful burst requests had a p95 about0.55seconds lower. Because timed-out/rejected requests are excluded from that latency percentile, it is not a claim that every request saved0.55seconds. Relative to the well-sized fixed8 pool, the tuner saved no failures and its burst p95 was about2.04seconds worse.

Why: the workload asked for30requests/second during the burst. Each query deliberately occupied a backend for about0.2seconds. Five connections provide a theoretical ceiling of25queries/second before overhead; eight provide40. The fixed5 pool accumulated a queue and reached the3-second request deadline. Increasing its ceiling helped. Fixed8 already had enough capacity, so the tuner could not improve on it simply by reacting later. This establishes a useful correction of an undersized limit, not unique superiority over competent manual sizing.

The actual Azure controller observed waiting clients, used provider CPU/memory plus a direct SQL health check, persisted changes and grew5 ->7 ->8. First waiting appeared at about9.28seconds; configured and established7 appeared at21.93seconds. The8 ceiling appeared at38.8seconds while only7 backends were established. Full growth reconciliation finished later. No intentional quiet-period connection reclamation or hosting-cost saving was established.

The same93-second workload scheduled8seconds at8requests/second,35seconds at30/second,35seconds quiet and15seconds at30/second. All modes used16persistent clients, the same bounded64-request client queue and3-second deadline. Queries were read-only pg_sleep(0.2), not customer SQL. A failed connection is replaced for the next distinct request; failed requests are never retried. The previous Azure trials had zero failed requests, so that harness correction did not affect their executed paths.

These are single sequential trials, not randomized production estimates. Fixed5 ran after a separate Azure restart. Its initial delayed CPU reading included startup (69.4% in the preceding minute); subsequently retrieved metrics were27.7%,4.8% and11.0% across the workload minutes. This environment difference and the synthetic workload limit causal/general performance claims. We have not tested whether tuning improves CPU-bound database overload or varied optimal concurrency.

Supabase: an early single read-only transaction survived a10 ->11 change, but the stronger persistent-client test invalidated that reassuring smoke. All12persistent clients received SQLSTATE57P01, and the monitor disconnected. Size10 was restored. Automatic Supavisor resizing remains blocked. HTTP metrics/config reads work; the observed PgBouncer metrics are not shared Supavisor waiting-client counts. The managed CLI's Supabase pressure remained unverified during its70-second read-only smoke. No Supabase tuning-benefit claim is supported.

Google remains a mandatory target, blocked by the previously repeated regional ERROR_STOCKOUT. It was not restarted for this test.

Implementation: experimental Azure managed pool adapter, source-bound cloud pressure reader, managed controller profile and --managed-config CLI route were added. The runtime engine was exercised against Azure. Its separate console CLI smoke stayed frozen/unready with generic observation failures, so successful standalone CLI automation is not claimed. Supabase config observation is exposed, while --managed-resume is refused. Managed MVP completion remains blocked.

The first Azure adaptive run revealed a real journal defect: its3421-byte provider operation URL exceeded the old2048-byte bound. A regression failed, the bound was increased to8192, and the repaired adaptive trial above was measured. Failed and diagnostic runs remain retained; they are not substituted for the repaired result.

Review and checks: fresh review found two Important harness issues, both reproduced and fixed in one pass: opaque URL query redaction and concurrent evidence snapshots. The redaction regression and race regression passed after fixes. The ordinary source-root race suite, full source-root race/integration suite, vet and diff checks passed. A final test-only connection-replacement regression passed against real PostgreSQL. No extra broad suite or cloud matrix was run for the missing fixed5 baseline.

Review rulings: full managed MVP readiness, Supabase control and Google verification remain mandatory gates; distributed fencing and arbitrary consumer inventories are not covered by this exclusive single-role experiment; production performance/cost and sustained-overload claims are unproven; managed process-kill recovery remains unverified; the CLI failure is unlocalized; Azure shutdown is now verified; unrelated dirty legacy work and concurrent website commits are preserved. The deferred Minor is sanitized observation-stage diagnostics. These exclusions cost release readiness and generality; none authorizes a broader support claim.

Cleanup: first Azure window restored default5/min0, removed its exact temporary firewall rule, and stopped at25minutes33seconds. The additional fixed5 window made no pool-size change, removed its rule and stopped after7minutes30seconds, within its12-minute cap. Supabase is restored to10. No application schema/table writes, AI calls, commits, pushes or website edits were made by this work.

Next priority: preserve this qualified value claim, fix standalone managed CLI readiness, and investigate a safe Supabase control mechanism without live tenant resizing. Do not resume polish or promise production seconds/request savings from this experiment.

Raw summaries and measurements are in the current Codex task outputs/managed-comparison-evidence/summary.json. No credentials or opaque Azure operation query strings are included.
