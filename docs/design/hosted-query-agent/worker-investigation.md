# From a slow query to a saved investigation

The Go worker now hands one queued job to LangGraph in the synthetic traffic lab. It claims the job, measures the exact lookup, checks for an existing index, and sends both observations to the existing one-call investigator. When the graph returns, Go checks that the answer refers to the same table and column and saves it. This run ends with a recommendation.

## Design choices and trade-offs

| Choice | Why we chose it | Trade-off |
| --- | --- | --- |
| Keep jobs in Pooltune's PostgreSQL | The detector and consumer can coordinate on one record. A temporary claim lets a consumer recover abandoned work. | A dedicated queue could scale delivery independently. It would still need to coordinate with the database. After a lost model result, a retry may pay for a second call. |
| Keep the fixture adapter narrow | Go measures a known synthetic lookup and checks its identity before calling the model. It rolls the fixture back before waiting on Claude. | Existing customer queries need an approved way to obtain a fresh plan and index evidence. The synthetic runner cannot recover the same fixture after rollback. |
| Reuse the current LangGraph runner | The graph and its checkpoints already work, and both pieces of evidence fit in one request. | Each investigation launches a Node process and checks the checkpoint schema. A long-running graph service could reuse its connections and avoid startup. Measure that against this baseline before switching. |
| Reuse recent outcomes | A completed lookup with the same latency target and counter generation waits 60 minutes before opening another job. Unresolved outcomes wait five minutes. | Production needs to also invalidate findings when the plan, schema, or severity changes. The present limits are development values. |

Claims last 30 seconds. Each attempt has a 25-second deadline, with at most two claims per job. The producer and consumer coordinate briefly in PostgreSQL. They release the lock before the slow model call. Each attempt gets its own graph thread so a retry cannot pick up a previous attempt's recommendation.

## Measured results

The two live runs used Claude Haiku 5.5. Both recommended a B-tree index on the enrolled lookup after seeing a scan of 100,000 rows returning one row, with no covering index. Each run created one job for the slow lookup and none for the healthy lookup. Fresh connections verified the saved proposal, four graph checkpoints per run, and fixture cleanup.

| Run | Investigator | Traffic start to queued job | Claim to saved result | Graph runner |
| --- | --- | ---: | ---: | ---: |
| Local 1 | scripted | 5.008 s | 0.488 s | 0.475 s |
| Local 2 | scripted | 5.003 s | 0.296 s | 0.290 s |
| Rookery 1 | anthropic | 5.025 s | 2.037 s | 1.828 s |
| Rookery 2 | anthropic | 5.026 s | 2.163 s | 2.063 s |

Claim-to-result includes collecting the plan and index evidence, rolling back the fixture, launching the graph, and saving the result. Graph time includes Node startup, PostgreSQL checkpoints, and the model call in live mode. The gap between the first queued job and the claim in this demo includes finishing the seven-second traffic run. The fixture controls when collection can start. The lab timings are a baseline for this workflow, with very few runs.

## Watch the graph in Studio

Start the scripted local LangGraph server from the repo root.

```sh
POOLTUNE_STUDIO_MODE=scripted LANGSMITH_TRACING=false npm --prefix investigator run studio -- --host 127.0.0.1 --port 2025
```

Open [LangSmith Studio](https://smith.langchain.com/studio?baseUrl=http%3A%2F%2F127.0.0.1%3A2025). Select `pooltune_index` and supply the report's `investigation.evidence` object as the graph's `evidence` input. Studio shows evidence screening and the resulting recommendation. The original run's checkpoints are saved in Pooltune's PostgreSQL. Studio's local development server uses its own threads, so this opens a replay of the saved evidence.

For a report, use the same disposable database setup as the [worker traffic test](worker-traffic.md), then run:

```sh
go run ./cmd/pooltune-query-lab -target local -worker-traffic -worker-investigation scripted
```

For the authorized Rookery fixture, use `-target rookery -worker-investigation anthropic` with the same verified connection, CA, and private Anthropic settings as the [index lab](index-slice.md). The standalone worker still ends at enqueue. A deployed consumer needs its own supervised process, retryable evidence collection, and production access controls.
