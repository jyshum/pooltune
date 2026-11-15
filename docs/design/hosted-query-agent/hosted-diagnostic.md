# Hosted investigator: from a queued slowdown to a checked repair

## The problem this step solves

The new investigator could compare explanations and ask Go for evidence, but Railway still ran the older recommendation path. This step connects the new graph to the existing consumer and index executor. It closes the gap between a diagnostic demonstration and a repair performed by a hosted worker.

The flow is: enrolled traffic → Go detects a sustained slowdown → PostgreSQL stores one job → Sonnet compares explanations → Go checks and applies a permitted index → matched reads and writes verify the outcome.

The first hosted qualification covers the existing synthetic single-column index fixture. The other five repair tools still use their earlier qualified model path. They have not silently switched to the new diagnostic adapter. General customer enrollment and broader repair coverage remain on the roadmap.

## What changed

`-mode diagnostic -repair-staging-fixture` selects the new graph in the Go consumer. It sends the measured query plan and existing-index check together. It exposes named Go readers for the exact fixture's plan, indexes and lookup statistics. Other evidence capabilities are explicitly unavailable in this adapter. Sonnet cannot supply SQL, a different table or a connection.

Go records the new graph's model calls, evidence requests, elapsed times and short cause assessments in the existing durable repair report. All existing permission, identity, freshness, capacity, benefit and recovery checks stay in place. A model recommendation alone cannot mark a repair successful.

## Hosted test and measurements

**Passed on November 14, 2026.** Source `4918260`, Railway deployment `58b9a26c-db3b-4c71-8c19-8cee332d24ce`, job **113**. [Full measured artifact](evidence/hosted-diagnostic/run.json) · [Timing summary](evidence/hosted-diagnostic/summary.json).

Sonnet selected the index and rejected stale statistics and cleanup as explanations for this selective scan without a covering index. It used **one real call, 3.582 seconds**, with no additional evidence request. Go built the owned index and verified improvement. The model was not given a requested answer.

| Part | Time | What it includes |
| --- | ---: | --- |
| Traffic begins → job created | **20.817 s** | Two-second collection, four-second test windows, five comparable windows |
| Queue/restart/reporting residual | **23.972 s** | Queue-to-result minus recorded worker time: deliberately held queue, actual restart, test coordination and result-publication overhead; not a standalone reboot benchmark |
| Worker processing, total | **16.124 s** | All checks, evidence, investigation, action and verification |
| ↳ Baseline measurements | 3.475 s | Matched candidate reads, healthy reads and writes |
| ↳ Investigation | 4.097 s | Sonnet 3.582 s plus process/checkpoint overhead; zero requested reads |
| ↳ Index build | 1.502 s | Guarded concurrent build |
| ↳ Verification | 3.230 s | Matched post-repair measurements |
| ↳ Other worker work | 3.819 s | Target checks, connections and remaining bookkeeping, calculated by subtraction |
| Traffic begins → verified result | **60.913 s** | Includes the deliberately inserted restart exercise |

The indented rows divide the worker total; they are not extra time on top of it. Sonnet time is inside investigation time. Fixture creation and source deployment are outside this clock.

| Database measurement | Before | After |
| --- | ---: | ---: |
| Candidate read mean | **9.31655 ms** | **0.02765 ms** |
| Healthy read mean | 0.029625 ms | 0.020450 ms |
| Protected write mean | 0.142 ms | 0.05955 ms |
| Rows examined for one result | 100,000 | 1 |
| Logical blocks per candidate read | 1,334 | 3 |

The candidate was **336.95× faster**, a **99.70% latency reduction**, on the tested inputs. Each side measured 40 candidate reads, 40 healthy reads and 40 writes. All returned the expected results with zero recorded errors. The normal traffic driver separately completed **720 queries**, with no errors or wrong results. The healthy detector window averaged **0.02383 ms** against its 3 ms objective and created no job or model call. These sequential synthetic measurements do not establish peak customer throughput or concurrent-write safety.

The exact queued job survived the actual Consumer-service restart unchanged with zero claims. The replacement claimed it once, saved one model attempt and five graph checkpoints, built one index and completed verification. The final readback found one completed job and no duplicate action. The owned fixture was removed; all 12 public indexes retained the same fingerprint. Temporary test processes were stopped.

**Judgment:** this is enough to move to the remaining diagnostic repair adapters. The worker finished within its existing 25-second deadline, and improvement passed the existing gates. It is one bounded run, not a latency guarantee. The extra evidence path was available but unused here, so this run proves the one-call hosted path. Real requested-reader behavior and mid-graph restart remain covered by the separately labeled local PostgreSQL tests.

## Design choices and trade-offs

| Choice | Why | Cost or limit |
| --- | --- | --- |
| Connect one already-qualified repair first | Separates new investigator integration from changes to the repair itself | Five other diagnostic execution routes still need qualification |
| Reuse useful evidence in the first call | Avoids unnecessary database reads and extra model rounds | The one-call case does not demonstrate a requested live read |
| Bound Go readers to recorded fixture ownership | Gives the model useful evidence without expanding its authority | This reader is not a general customer collector |
| Keep queue state outside the worker container | A service restart cannot erase queued work | Recovery still depends on PostgreSQL availability |
| Keep the existing action executor | Preserves independent checks and uncertain-action recovery | Different repair selection cannot bypass the enrolled action family |

## Technical scope and reproduction

The source is on `feat/pooltune-revamp-dev`. Consumer runs the existing continuous command; the test starts dedicated, temporary detector and consumer processes inside the Railway Consumer container. Its normal service configuration is preserved. Rookery credentials and the model key are delivered privately over SSH stdin, not committed or printed. The bounded test processes and owned fixture are removed afterward. This is staging qualification, not an enabled customer production service.

1. Upload the source-only Docker build to existing Pooltune Staging / Consumer and observe SUCCESS for that exact deployment.
2. Through the existing pinned Rookery TLS route and private Railway state connection, create a journaled consumer fixture with `pooltune-query-lab -target rookery -create-consumer-fixture`.
3. Enroll its healthy and candidate query IDs with a 3 ms mean database execution objective. Run `pooltune-worker -target rookery -enrollment <fixture-enrollment.json> -interval 2s -window 4s`. Send bounded parameterized queries with `$1=43`; literal SQL can produce a different statement ID.
4. Wait for one candidate job with zero claims and a healthy no-change window. Restart the actual Railway Consumer service. Read back the same unchanged job after the replacement container is available.
5. With `POOLTUNE_ANTHROPIC_MODEL=claude-sonnet-5-5`, start `pooltune-consumer -target rookery -project <enrolled-project> -fixture <fixture-id> -mode diagnostic -repair-staging-fixture -poll 100ms` in the replacement container. LangSmith tracing remains disabled.
6. Verify the completed job, matching action report, diagnostic checkpoints and durable model-attempt count. Compare target reads, protected healthy reads and writes before/after. Confirm a single owned index, exact query results and no healthy-query job.
7. Stop the temporary processes. Remove only the journaled fixture with `pooltune-query-lab -target rookery -remove-consumer-fixture <fixture-id>`. Compare the public-index fingerprint and check that no test schema remains.

The restart exercise is a **queued, unclaimed job** test. It does not establish hosted mid-model checkpoint continuation. That mechanism has separate local PostgreSQL/process-restart evidence in diagnostic-loop.md. A short test clock is also not a production detection-latency promise.

## Verification and remaining reporting gap

Go race unit tests and vet, 86 TypeScript tests/typecheck, real PostgreSQL reader tests, existing queued-index action/recovery tests and the diagnostic checkpoint integration suite passed. A fresh reviewer found no Critical or Important defect in the connection. One Minor gap remains: failed worker attempts do not preserve a complete timing breakdown in their result. The durable model-attempt reservation remains, and errors cannot become proposals. Successful investigations retain the complete diagnostic report. Failure reporting belongs in the next operational instrumentation pass.
