# How Pooltune improves a database

Pooltune is designed to spot expensive database work, figure out what can improve it, make a permitted change, and check that the change helped. A small team should be able to connect its Supabase project and see what happened without keeping the dashboard open.

## The problem

As an app grows, a database query that used to be cheap can become expensive. It may look through more rows, guess wrong about how many results it will find, or compete with work running at the same time. A slow query is a reason to look closer. The cause determines what to do.

An **index** gives a database a quicker way to find rows. Think of a book index that points you to the right pages. PostgreSQL can use an index to find a few matching records without reading the whole table. It pays for that shortcut with storage and extra work when indexed values change. If a query needs most of the table, reading it in order can be the better choice.

## How a repair works

1. **Measure.** Go collects evidence for enrolled queries and looks for repeated problems. A short spike should not automatically cause a repair.
2. **Investigate.** One TypeScript/LangGraph workflow gives Claude the measured plan, existing-index information and evidence for the repair being considered. Claude can recommend a supported operation or leave things alone.
3. **Act.** Go checks the exact target, current conditions, permission and operating limits. It saves its intent, then performs the specific operation. Claude does not send unrestricted SQL to the database.
4. **Verify and recover.** Go checks the same work afterward and measures protected traffic. A setting change can be restored if the benefit is uncertain. After an interruption, the worker checks what actually happened before deciding what comes next.

Application queries continue using PostgreSQL while the background investigation runs. We measure query response time separately from the time it takes Pooltune to detect and verify a repair. A model request adds to time-to-repair; it is not inserted into each application query. Measurement and repair work still consume database resources, so the tests also check monitoring cost, healthy requests and writes.

The prompt helps interpret evidence. The tools, measurements, saved state and refusal rules turn that interpretation into controlled work. The current fixtures give the investigator evidence for one qualified cause at a time. Letting it investigate competing causes and request additional evidence remains a separate build step.

| Repair family | Problem it addresses | Main trade-off |
| --- | --- | --- |
| Add an index | Finding a few matching rows takes too much scanning. | Faster reads cost storage and extra write work. |
| Refresh statistics | PostgreSQL's description of the data has become stale. | Refreshing helps only if stale information is the cause. |
| Improve statistics detail | Ordinary summaries miss skew or relationships between columns. | Better summaries take extra analysis work and storage. |
| Bounded cleanup | Old row versions or visibility checks create avoidable work. | Cleanup consumes resources and cannot be undone. |
| Function memory | A particular function's sort writes temporary files. | More room per call multiplies across concurrent calls. |
| Job scheduling | Background work repeatedly competes with important queries. | Moving work must preserve required executions and deadlines. |

## Why the worker remembers unfinished work

A queue is a to-do list saved in a database. Restarting the worker does not erase that list. Pooltune records which investigation owns a change, what it intended to do, and what result it actually observed. After an interruption, it checks the database before repeating an operation.

Some answers arrive later. After moving a scheduled job, Pooltune must watch future executions before calling the repair successful. It saves a follow-up task and releases the current work slot while waiting. The scheduled wake-up time means “check now,” not “the evidence must already be ready.” The worker checks the saved evidence and stays within a deadline. This connects the queue, restart recovery and measured verification into one workflow.

## Why these pieces fit

| Piece | Job | Tradeoff |
| --- | --- | --- |
| **Go** | Runs background work and controls what can change. We can test the rules by giving them the same input and checking the outcome. | Rules are predictable and cheap to run. Working out all the possible causes of a slow query this way takes lots of cases. |
| **TypeScript and LangGraph** | TypeScript gives the tools and recommendations a defined shape. LangGraph organizes the investigation into steps, such as screening evidence, investigating, and closing the case. | The workflow is easier to inspect and can use Claude where evidence needs interpretation. Two runtimes add deployment and debugging work; model calls add time and variability. |
| **PostgreSQL** | The customer's database does the actual work. Pooltune uses a separate database to remember measurements, jobs, and actions. | Recording intent takes extra writes. It gives the worker a way to check for changes that completed just before a crash. |
| **Studio and the dashboard** | Studio lets developers watch the investigator's steps. The customer dashboard will show connected projects, measured outcomes, and a pause control. | Studio helps build and debug. The dashboard needs its own simpler presentation and account permissions. |

## The main tests

- **Healthy traffic and brief spikes.** Leave healthy work alone and avoid reacting to a short fluctuation.
- **A sustained, diagnosed problem.** Real evidence reaches Claude; Go applies only the matching permitted operation and measures the result.
- **Misleading or incomplete evidence.** Wrong targets, missing authority, changed objects or unknown limits must not become a repair.
- **Side effects.** Measure healthy reads, writes, errors and missed requests alongside the target. A benefit on one query can carry a cost elsewhere.
- **Interrupted work.** Recover from saved intent without repeating an uncertain action or overwriting a newer setting.
- **Required scheduled work.** A moved job must actually finish its future runs by their deadlines before the repair is called successful.

## Where we are

The detector, durable queue and continuous consumer run in Railway staging. Queued work has survived a hosted restart. The shared investigator uses real Claude, and Go performs the permitted repair and verifies the outcome. These are implemented components, with controlled staging coverage rather than customer rollout approval.

The index, statistics refresh, richer statistics and bounded cleanup have passed mixed-traffic qualification on disposable Rookery data. The same harness checks healthy traffic, brief spikes, sustained slowdowns, side effects, monitoring load and cleanup. [First three repairs](mixed-traffic.md) · [Cleanup results](bounded-vacuum.md)

Function memory passed after reducing redundant diagnostic work: ongoing query time fell from 214.9 to 148.9 ms and temporary writes fell to zero. Scheduling now also passed the full hosted flow: Claude chose the allowed move, Go applied it, the consumer restarted, and three future job runs completed on time. Target database mean fell from 33.64 to 0.090 ms; client p95 fell from roughly 219–225 to 151 ms. All six families now have a bounded live-Claude staging case. Earlier failed/inconclusive trials remain documented, and broader customer coverage is still open. [Memory evidence](function-memory.md) · [Scheduling results and timing](cron-staging.md)

## Why we send both pieces of evidence in one Claude call

Go already has the measured query plan and knows whether an existing index can serve the lookup. Sending both up front saves the first round trip, when Claude used to ask for them. LangGraph screens obvious no-change cases before any model call.

On saved Rookery evidence, the median investigation fell from **2.56 s to 1.61 s (37%)**. On three unfamiliar synthetic queries it fell from **2.08 s to 1.57 s (24%)**. Both approaches picked the expected index in all six runs of each comparison. One call is now the default. These benchmarks timed the graph investigation and model requests during replay. [Inputs, test cases and exact timings](index-slice.md#one-claude-request-versus-two)

## What to build next

The six bounded repair cases are qualified. Next, broaden diagnosis and workload coverage. One passing synthetic case does not cover every index, function, input or maintenance pattern. Compound indexes, column statistics targets, recurring cleanup settings and writes to repaired tables still need their own qualification.

The product also needs customer connection and grants, account sign-in, a dashboard, account isolation, operating budgets and rollout checks. The existing staged worker and tools are the foundation for those pieces; they are not the finished hosted service.

[Full V1 roadmap](roadmap.md) · [Implementation plan](implementation.md) · [Current progress](progress.md)
