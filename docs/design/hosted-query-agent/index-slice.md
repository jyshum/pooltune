# Pooltune automatic index trial

November 13, 2026. This record covers the first automatic repair on an isolated synthetic PostgreSQL fixture. The full [hosted design](design.md) and all six V1 repair types remain in scope. The scripted and live Anthropic runs are ready for review.

## Follow a run

1. `cmd/pooltune-query-lab` opens Pooltune's local PostgreSQL and the explicitly selected test target. The Rookery route verifies the pooler hostname and certificate. The command needs an explicit investigator mode.
2. Go claims a session advisory lock, checks visible activity, and records the run in Pooltune PostgreSQL. It creates a private synthetic schema with two 100,000-row tables. The catalog identities and intended index definition reach the journal before the fixture commits.
3. Go samples four fixed input sets, ten times each. The healthy lookup uses its existing primary key. The missing-index lookup breaches the lab threshold when at least three of the latest five batches average over 1 ms.
4. A TypeScript LangGraph run takes the measured plan and catalog evidence from Go. It can close the investigation or return a typed proposal. The configured Anthropic path allows one choice of evidence set and one structured recommendation. A proposal requires both plan and catalog evidence. The scripted mode exercises the same graph and Go gate with a deterministic recommendation.
5. Go requires a current selective scan, exactly the enrolled table and column, a free index name, a matching private fixture and capacity. It issues one `CREATE INDEX CONCURRENTLY` and checks validity, definition and size. There is no SQL field in the proposal.
6. Go compares matched before and after inputs, then records the outcome and drops the owned fixture. Recovery reads pending intent and checks the catalog before touching any fixture. Replaced or ambiguous objects remain in place for investigation.

## Engineering choices

| Choice | Why it fits the first slice | Strongest alternative and tradeoff |
| --- | --- | --- |
| Go eligibility and execution, LangGraph diagnosis | Code can enforce identity, permissions and bounds even if the model guesses. One bounded evidence request and one recommendation cap latency and provider calls. | Go-only diagnosis is cheaper for a known missing-index pattern. A live model needs to show added value on ambiguous and misleading diagnoses before we draw a production conclusion. |
| Concurrent build in a private, committed fixture | Exercises the transaction boundary and catalog states of PostgreSQL's concurrent index build. The fixture shares only the test project's resources. | A regular transactional index could roll back with the fixture and be faster. It blocks writes while building and hides concurrent-build recovery cases. |
| Durable action journal plus graph checkpoints | A graph checkpoint tracks the recommendation. The action journal records what Go intended and inspects PostgreSQL when the build outcome is uncertain. | A graph checkpoint alone has less operational state. It cannot tell whether PostgreSQL applied the DDL before a connection or process failed. |
| Batched, fixed inputs and per-input guards | Lets the team prove the mechanism quickly with actual PostgreSQL work and no query replay outside the private fixture. Paired write transactions roll back and change the indexed value, so they exercise maintenance without changing the lookup inputs. | Comparable normal traffic measures customer impact, including realistic concurrency and write mix. It takes longer and requires more telemetry. Use it for hosted qualification. |

## Rookery evidence

Trial `7f8cfd49289e311477d21cf5` returned `improved_tested_inputs`. The detector, PostgreSQL work, checkpoint storage and measurements were real. The graph ran with a scripted recommendation.

| Metric | Before | After |
| --- | ---: | ---: |
| Target mean database execution time | 12.190 ms | 0.025 ms |
| Logical shared blocks per target lookup | 1,334 | 3 |
| Rows examined for one result | 100,000 | 1 |
| Healthy comparison mean | 0.035 ms | 0.023 ms |
| Value-changing write mean | 0.061 ms | 0.058 ms |
| Target reads / healthy reads / writes | 40 / 40 / 40 | 40 / 40 / 40 |
| Errors in measured operations | 0 | 0 |

PostgreSQL switched from a sequential scan to an index scan. The index used 2,260,992 bytes. The healthy table still had exactly its original primary-key index. All 12 Rookery public index definitions matched the pre-run fingerprint `f429d87200ade1a2faa649fa438e8cbe`; the independent post-run check found zero trial schemas. The local action journal recorded `complete` and graph thread ID matched the recorded run with four checkpoints.

Wall time was 3.89 s including fixture setup and cleanup. Detection sampling took 1.00 s, scripted investigation and checkpointing 0.34 s, index creation 0.23 s and verification sampling 0.40 s. These numbers describe one controlled run and exclude real model inference. The earlier local test changed the plan and blocks but remained inconclusive because it did not meet the preset 5 ms absolute benefit floor.

## Live Anthropic check

Trial `c9a0c0400c550434c5d086e4` used Claude Haiku 5.5 (`claude-haiku-5-5`). This model was chosen for the short, structured investigation. The run does not compare models. The saved graph checkpoint confirms Claude requested the plan and index evidence and returned an index proposal. Go accepted the proposal and built a valid index.

Mean lookup time changed from **16.209 ms to 0.026 ms**. PostgreSQL switched from a sequential scan to an index scan and logical blocks per lookup fell from **1,334 to 3**. Forty target reads, forty healthy reads and forty value-changing writes were measured on each side, with zero errors. The healthy table kept its original index. The journal recorded a completed run with four graph checkpoints. An independent catalog check found zero trial schemas and matched all 12 public indexes to the pre-run fingerprint.

Total wall time was **8.08 s**, including **2.72 s** for the investigation and its checkpoint writes and **0.46 s** for the index build. These timings are for one synthetic run. The live round used Anthropic with cloud tracing disabled. The developer Studio replay is separate.

## One Claude request versus two

Go already had the query plan and the existing-index check. We tested sending both in the first Claude request, saving the round trip where Claude asks for them. Both versions used the same Claude Haiku 5.5 model, saved Rookery evidence, graph screening rules, and recommendation format. We ran six pairs and alternated which version went first. The timer covered the graph investigation and API requests, with PostgreSQL and checkpoint writes outside this replay.

| Approach | Median investigation time | Range | Correct index proposals | Claude requests per investigation |
| --- | ---: | ---: | ---: | ---: |
| Request evidence, then recommend | 2.56 s | 2.28–5.64 s | 6/6 | 2 |
| Send both pieces of evidence up front | 1.61 s | 1.49–2.17 s | 6/6 | 1 |

The single request saved **0.94 s at the median (37% lower latency)** and was faster in all six pairs. A correct proposal matched the original investigation and the exact expected table and column. In both versions, the healthy lookup, an already-covered lookup, and a scan returning many rows all ended with `no_change` and zero Claude requests. The graph handled those decisions before invoking the model.

This was a replay of one known repair, with three control cases. It provides a speed comparison for this workflow. Recommendation quality on unfamiliar cases and overall repair time need separate measurements. The initial comparison ran on `feat/single-call-index-eval`. It informed the new-query check below.

## Default after testing new queries

We repeated the comparison on three new synthetic PostgreSQL queries. They looked up an event, found a customer's queued jobs with an extra filter, and retrieved readings for one device. Their lookup columns were in different positions. The unindexed plans examined **56,000**, **84,000**, and **108,000** rows and returned **1**, **3**, and **24**, respectively. On a disposable local database, we independently added the expected index for each case, verified that PostgreSQL used it and accessed fewer blocks, and confirmed that the results stayed the same. Then we rolled back the fixtures and replayed the evidence to Claude.

| Approach | Median investigation time | Range | Correct table and column | Claude requests per run |
| --- | ---: | ---: | ---: | ---: |
| Request evidence, then recommend | 2.08 s | 2.01–2.28 s | 6/6 | 2 |
| Send both pieces of evidence up front | 1.57 s | 1.33–2.21 s | 6/6 | 1 |

Both versions used Claude Haiku 5.5 and had two runs per query. One call was faster in **5 of 6 paired runs**. The median dropped by **0.51 s (24%)**. Both versions returned `no_change` with zero Claude calls on all five controls: three healthy plans, a case with an existing covering index, and a broad scan that returned the entire table.

The investigator now sends the measured plan and existing-index check in one request by default. LangGraph still screens cases before calling Claude. Go still verifies the proposed identity and eligibility before any database changes. The timing compares investigation only. These cases all fit the current single-column equality-index scope. They provide no quality measurement for compound indexes, unfamiliar repair types or customer workloads.

## Reproduce and inspect

From this repo, install the pinned investigator dependencies with `npm --prefix investigator ci`. Use PostgreSQL 17 and Go 1.27.3 or newer.

- Run the integration and policy tests with `go test -tags=integration ./cmd/pooltune-query-lab ./internal/hostedquery -count=1`. The suite starts disposable PostgreSQL. It covers an actual concurrent build, reconciliation through fresh connections after a simulated process interruption, absent and invalid index state, concurrent deferral, wrong target refusal and preservation of a replaced table. The invalid-index case injects the catalog state on disposable PostgreSQL.
- With the separate local state and fixture connection variables described in [progress](progress.md), run `go run ./cmd/pooltune-query-lab -index-trial scripted`. The Rookery trial used the already-authorized native route with `-target rookery`. On a pending fixture, run `go run ./cmd/pooltune-query-lab -recover-index-trials` with the original target and login. New runs reconcile the same journal identity first.
- For the local developer graph, run `POOLTUNE_STUDIO_MODE=scripted LANGSMITH_TRACING=false npm --prefix investigator run studio -- --host 127.0.0.1 --port 2025`. The dev server registers `pooltune_index`; Studio shows the evidence screening branch, selected evidence and recommendation. A replay uses a saved evidence object and only exercises the graph. Agent Server registration and a scripted replay were verified through its local API.
- For live Anthropic, use `-index-trial anthropic` with `ANTHROPIC_API_KEY` and `POOLTUNE_ANTHROPIC_MODEL` set privately. Only synthetic evidence and the Pooltune state connection go to the investigator process. Use the live mode only in the authorized fixture environment. LangSmith cloud tracing was disabled for the demo.

## Remaining boundaries

- The first fixture qualifies one enrolled plain integer equality column. Compound index qualification and the other five repairs remain future tasks.
- The repair trial is a one-shot development command. Hosted job scheduling, tenant-specific grants, credential rotation, customer disk and pressure admission, and sustained workload verification are still open. The native Rookery credential uses the test project's existing broad admin login.
- Recovery serializes Pooltune trials and checks catalog identities again under locks before dropping. The build rechecks immediately before the concurrent DDL. This fixture assumes its private schema and owner are under the test operator's control. A production contract needs stronger access isolation around the relation.
- Short, sequential synthetic samples establish benefit for these inputs. They do not establish customer tail latency, concurrent write overhead, long-term costs or production calibration.
