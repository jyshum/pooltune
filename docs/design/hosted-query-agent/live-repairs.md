# Claude decisions through the hosted repair tools

Pooltune now sends eligible cases for all three implemented repairs to Claude. The model can propose an index, targeted ANALYZE or joint statistics, or decline. Go validates the response and current database conditions before executing and measuring the result. Each request includes the evidence already collected, which keeps the earlier one-call design.

## What changed and why

The statistics branches previously returned a fixed recommendation in `screen_evidence`. They now continue to `investigate` when the evidence qualifies. The prompt describes the three operations, their evidence requirements, the structured response and the unavailable repair types. A model failure propagates to the worker. Every successful live request records its configured model, request duration and returned token counts. The adapter adds these measurements after parsing the answer. [Prompt and model adapter](../../../investigator/model.ts) · [Shared graph](../../../investigator/graph.ts)

The model's response refers to the enrolled run, table and column. An omitted `repair` retains the existing index contract, while `analyze` and `statistics` select the other two operations. Go rejects a mismatched repair, changed identity or invalid response before entering the executor. This slice uses one typed recommendation followed immediately by the qualified Go action. [Go response checks](../../../internal/hostedquery/worker_dispatch.go)

## Evidence gap found by the live model

The first completed ANALYZE investigation, job 45, returned `no_change` in a 2.302-second model request. Its evidence showed an index and a large row-estimation error, but lacked a direct observation of the old distribution. Claude declined and Go ran no refresh. The scripted test had not exposed that missing information.

The collector now reads the stored frequency of the exact synthetic lookup value from PostgreSQL's single-column statistics. That sample says the value appears in every row. The measured scan finds one matching row out of 100,000. This gives the model a factual basis for identifying outdated statistics. The optional field is absent when the singleton sample cannot be matched. This collector is scoped to the enrolled fixture. A general collector needs bounded representative value evidence. [Evidence collector](../../../internal/hostedquery/queued_analyze.go)

## Hosted outcomes

All three successful runs used configured model `claude-haiku-5-5`, one model request per job and four graph checkpoints. Each slow query created one job and the healthy control created zero. The completed outcome was `improved_tested_inputs` in each case.

| Operation and job | Claude request | Graph including Claude | Traffic to queued job | Queue to saved result | Total |
| --- | ---: | ---: | ---: | ---: | ---: |
| Index, 41 | 2.075 s | 2.514 s | 21.157 s | 15.735 s | 36.892 s |
| Targeted ANALYZE, 52 | 2.671 s | 3.136 s | 20.964 s | 11.577 s | 32.541 s |
| Joint statistics, 48 | 2.384 s | 2.817 s | 20.588 s | 12.914 s | 33.502 s |

The Claude timer surrounds the actual structured model request and parsing. Graph time also includes process startup and checkpoint work. The detector used five windows of at least four seconds, a two-second polling interval and a 3 ms fixture objective. Most elapsed time was collecting evidence and verifying the database outcome. These are single-run observations. They do not establish a latency distribution or a controlled comparison against the earlier scripted runs.

| Processing stage | Index | ANALYZE | Joint statistics |
| --- | ---: | ---: | ---: |
| Baseline measurements | 3.549 s | 1.483 s | 2.606 s |
| Graph including model | 2.514 s | 3.136 s | 2.817 s |
| Apply | 1.682 s | 1.331 s | Combined below |
| Verify | 3.236 s | 1.448 s | Combined below |
| Combined apply and verify | 4.918 s | 2.779 s | 3.756 s |
| Other queue wait, setup, checks and state writes | 4.754 s | 4.180 s | 3.735 s |

| Mean database execution | Before | After |
| --- | ---: | ---: |
| Index target read | 11.076 ms | 0.025 ms |
| ANALYZE target read | 10.529 ms | 0.022 ms |
| Joint-statistics input A | 13.782 ms | 0.082 ms |
| Joint-statistics input B | 13.725 ms | 0.195 ms |

ANALYZE corrected an estimate of 106,720 rows to one actual row. Joint statistics changed the estimates from 24,901 to 97 for 100 actual rows and from 25,091 to 293 for 300 actual rows. The existing indexes served both statistics repairs afterward. Joint analysis took 204.774 ms against 197.059 ms for ordinary analysis, and the summary payload used 172 bytes. Repeated maintenance-cost observations remain required before a production conclusion.

The index comparison measured 40 target reads, 40 healthy reads and 40 rolled-back updates per phase. ANALYZE measured ten of each. Joint statistics measured ten of each for each of two inputs. Every protection check passed. Healthy read means stayed below 0.032 ms. Index updates averaged 0.104 to 0.062 ms, ANALYZE updates 0.065 to 0.044 ms, and joint-statistics updates 0.384 to 0.064 ms and 0.078 to 0.058 ms for the two input phases. Fixture cleanup and the fingerprint of the 12 public indexes were verified after each run.

Provider-reported input/output tokens were 1,443/314 for index, 1,601/398 for ANALYZE and 1,506/435 for joint statistics. These counts cover the successful requests. The declined investigation and control calls were additional evaluation work.

Index and joint statistics used deployment `9aaf5181-104b-4d48-8582-8ba02148cf17`. ANALYZE used `b3cedfdf-0c85-44d7-9aa0-5a76e1951923`, which added the missing sample-frequency evidence and its prompt explanation. The final prompt was also checked on saved evidence through live API replays.

## Controls, failures and limits

Five direct live-model control requests returned `no_change` with the final prompt. They covered a healthy indexed lookup, a broad scan returning the whole table, an existing index with no statistics-cause evidence, accurate ordinary statistics and joint estimation errors without a verified column relationship. Request times were 1.576–2.144 seconds. These controls ran locally against the live API with synthetic evidence. The normal graph screened the same cases without calling the provider. Hosted tests also used a healthy control alongside each slow query.

An earlier direct control request hit the existing 8.5-second provider timeout. The first ANALYZE traffic attempt lost its database connection before any investigation was queued. Both attempts are excluded from the successful-run timing table. The interrupted fixture was cleaned up. The SDK has automatic retries disabled, and the queue retains its existing bounded recovery policy.

Claude selected the correct joint-statistics repair, but its explanation called the overly high estimates “underestimated.” The raw values and Go's numeric checks established the outcome. This wording error remains a limitation of the model's explanation quality.

Go selects the fixture adapter and collects evidence for a prequalified cause before Claude runs. These tests measure selection or refusal within that admitted scope. Broader diagnosis across competing causes and model-requested additional evidence remain open. All changes ran on private synthetic Rookery fixtures with the existing staging grants and cleanup checks. Joint-statistics measurements kept the same session-only planner settings as its earlier qualification. Production workload qualification remains open.

## Reproduce and inspect

Use the existing private staging route and create the corresponding index, ANALYZE or statistics fixture. Run its detector and consumer with `-mode anthropic` and the matching `-repair-staging-fixture`, `-analyze-staging-fixture` or `-statistics-staging-fixture` flag. Supply the existing Anthropic configuration privately to the process. Keep the response, checkpoint and catalog checks, then stop the temporary processes and remove the fixture. [Index workflow](queued-index-repair.md) · [ANALYZE workflow](targeted-analyze.md) · [Joint statistics workflow](richer-statistics.md)

Every later repair capability must include a live Claude decision and authorized execution test, along with healthy and unsupported controls, model timing and full outcome timing. The implementation plan records Jared's explicit requirement.

## Verification

Race-enabled Go tests ran against disposable PostgreSQL, and Go vet, all seven graph tests, TypeScript checks and diff checks passed. The added Go tests reject wrong repair, table, column and run identity across all three cases. A queued ANALYZE integration test confirms no additional ANALYZE statement after a wrong repair, changed target, decline or provider timeout. Independent review verified these checks and the stored-frequency collector. Final-prompt live replays selected all three expected repairs from the saved hosted evidence.
