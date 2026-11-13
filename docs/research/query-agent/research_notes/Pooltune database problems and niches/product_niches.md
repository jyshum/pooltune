# Pooltune: customer problems and possible niches

Research prepared November 12, 2026. Research only. Hosted Supabase is the initial environment. No live project was accessed and no product scope, code change, automation, or additional action family was approved. The earlier toolbox is a set of candidates, not the definition of the customer problem.

## What problem is left when a provider manages Postgres?

### Takeaway

Managed hosting does not remove workload-specific optimization. However, “Supabase does not optimize databases” would be false. The useful question is which recurring job a developer still has to do, and whether Pooltune can finish that job better than an existing advisor plus their coding assistant.

### Cited Findings

- Supabase tunes Postgres to the compute resources of the plan. Its documentation explicitly says those settings assume a general workflow and particular workloads may benefit from further tuning. Connection limits also reflect compute size. [Supabase performance tuning](https://supabase.com/docs/guides/platform/performance).
- Supabase already provides deterministic advisors through Studio, MCP, CLI and its Management API. Some findings are intentional and require checking the application's intended schema and access model. [Advisors](https://supabase.com/docs/guides/observability/advisors).
- It also publishes an hourly read-only performance-agent workflow. That workflow compares saved measurements, inspects blocking sessions and advisors, suppresses unchanged findings, and can run after a schema change. It does not create indexes, rewrite queries or cancel sessions. [Performance agent](https://supabase.com/docs/guides/observability/automate-with-agents/performance).
- PostgreSQL already performs automatic vacuuming. Its documentation says this is sufficient for many installations, while some need parameter changes or supplementary maintenance. Ordinary vacuum can run alongside normal operations but consumes resources. [PostgreSQL routine vacuuming](https://www.postgresql.org/docs/17/routine-vacuuming.html).

### Inferences

The possible gap is completing a repair with the application's context: identify the affected user task, establish a cause, select an allowed change, test correctness, apply through the proper owner, and verify the result. A dashboard that repeats native warnings does not close that gap. An open-source tool could still be worthwhile without exclusivity or a defensible commercial moat.

Three boundaries matter:

| Responsibility | Example | Appropriate Pooltune outcome |
|---|---|---|
| Provider/runtime | Regional outage, gateway defect, host replacement, managed service rollout | Preserve evidence, identify likely layer, prepare escalation; do not invent a database repair |
| Database workload | Missing useful index, stale planning statistics, table-specific maintenance falling behind | A tested, bounded repair may be possible with a project grant |
| Application meaning | Too many sequential requests, tenant permission rule, billing job deadline | Repository or migration work with correctness tests and the owner's deployment process |

An “AI-built app” describes a possible audience and acquisition channel. It does not identify a database failure mechanism. Neither AI-generated nor human-written SQL should be presumed correct, secure, or slow without evidence.

### Gaps

Documentation proves mechanisms and tool access, not customer demand, prevalence, willingness to delegate writes, or how frequently useful repairs arise.

## What do actual reported episodes show?

### Takeaway

The same complaint, “my Supabase app is slow,” can lead to different products. The examples below are existence evidence, not representative statistics or independently reproduced root causes.

### Cited Findings

1. **Growing data and slow pages.** A developer describes a Next.js app becoming slow as data grew. Exporting slow-query information to a coding assistant helped find an index, but the largest improvement came from replacing sequential requests with one RPC. The author reports repeating the exercise during one week. [First-person account](https://www.reddit.com/r/SaaS/comments/1qqacnk/using_supabase_slow_query_reports_llms_to_fix/).
2. **Background work and unclear incident cause.** A Supabase user reported repeated outages while a billing-recovery job ran every minute. Later they changed it to every five minutes and observed stability, but explicitly noted an earlier restart and did not claim the schedule change proved causation. Missing metrics made the episode harder to diagnose. [User incident discussion](https://github.com/orgs/supabase/discussions/51210).
3. **Slow requests with fast database work.** An issue reports browser delays while upstream service time was short; other users supplied similar timing comparisons. This is a user-reported gateway hypothesis, not a verified database defect. [Reported gateway stalls](https://github.com/supabase/supabase/issues/52011).
4. **A provider-owned defect can need a provider-owned fix.** Supabase's resolved JWT-rejection incident describes fleet deployment changes and a project upgrade as part of its resolution. This is not evidence that an index or vacuum could have repaired it. [Official incident history](https://status.supabase.com/incidents/6q5902p2xd9f).

### Inferences

The first example supports useful performance assistance but also identifies a strong substitute: an on-demand coding assistant using existing reports. The second supports investigating workload scheduling, while warning against treating temporal correlation as a safe action trigger. The third makes request-to-database attribution a prerequisite for claiming user-visible improvement.

### Gaps

These sources do not establish the share of incidents in each category. We did not find a representative Supabase customer incident dataset. Public reports skew toward unusual failures, and many threads never confirm a final cause.

## Which niches deserve comparison before selecting one?

### Takeaway

Keep four primary hypotheses and one adjacent outcome in consideration. Narrow by **person + trigger + desired outcome + permitted autonomy**, rather than selecting an arbitrary number of SQL operations.

### Cited Findings

Supabase's query guide explains both useful indexing and its write cost, and notes that inaccurate statistics can produce poor planning decisions. Supabase Cron stores schedules and execution histories and supports SQL, functions and HTTP calls. These establish technical mechanisms, not the popularity of either niche. [Query optimization](https://supabase.com/docs/guides/database/query-optimization), [Cron](https://supabase.com/docs/guides/cron).

### Inferences: proposed product hypotheses

| Hypothesis | Person and trigger | Job and desired outcome | Why an agent might help | When a simpler workflow wins |
|---|---|---|---|---|
| **Slow application interactions as data evolves** | Developer responsible for a live app; a search, list or dashboard becomes slow | Find which part causes the delay and ship a verified repair | Connect request timing, actual role, query evidence and code; finish an index or reviewed query change | If rare and obvious, a slow-query export plus coding assistant may suffice |
| **Performance regression after a release** | Team that deploys regularly; latency changes after code, schema or policy changes | Detect the regression early, associate evidence with the release, prepare or apply a qualified repair | Preserve comparable baselines and follow a change through verification | Migration checks and a release-triggered CI job may be enough; always-on reasoning is unnecessary |
| **Maintenance falling behind on changing data** | Owner of ingestion, sync, event or queue tables; frequent updates/deletes or bulk imports | Keep recurring cleanup and planning healthy without waking a developer | Repeated conditions may fit narrow table grants, maintenance budgets and independent verification | Default autovacuum or one durable table setting may solve it permanently |
| **Scheduled work interfering with customer requests** | Developer owning background jobs and foreground API; repeated slow windows or missed job deadlines | Complete background work within its deadline while protecting foreground response times | Observe recurring contention and adjust only jobs with registered timing flexibility | Static staggering may be all that is needed; irregular jobs or unknown business deadlines prevent safe autonomy |
| **Capacity or cost pressure** | Founder or technical owner facing a resource limit or proposed upgrade | Find avoidable work before spending more, or establish that capacity is truly needed | Rank causes and track whether an optimization reduced pressure | Often a periodic audit. Bills alone do not identify the cause, and spending changes remain outside automatic scope |

**1. Slow interactions/data evolution.** Example: a tenant's order page grows from hundreds to many thousands of rows and the existing query plan becomes expensive. Candidate work could include an eligible index, refreshed statistics, or a tested RPC/query migration. Success is the relevant interaction meeting its latency target with unchanged results and permissions. This niche offers broad usefulness but often crosses into app code. Do not call it autonomous database tuning if most valuable repairs require repository access and review.

**2. Release regressions.** Example: a new filter or RLS policy makes a previously fast request scan much more data. The job is to connect the new release to evidence, not assume every coincident slowdown is caused by deployment. Outcomes might be a tested migration, a narrow approved database repair, or a patch ready for review. Release timing gives a useful trigger and a more concrete test opportunity than continuously asking an LLM whether everything looks healthy. Data changes can also cause regressions without deployments.

**3. Maintenance.** Example: an import repeatedly changes much of a table, but cleanup or statistics lag during a predictable busy period. The customer job is keeping that workload healthy, not issuing VACUUM whenever dead rows exist. A controlled pilot can compare native autovacuum with a targeted intervention. If old snapshots prevent cleanup, more vacuuming does not solve the blocker. If native maintenance catches up without harm, Pooltune should do nothing. This could support recurring autonomy, but may instead resolve to one durable setting and monitoring.

**4. Background interference.** Example: catalog synchronization, reports and embeddings run at the same time as peak requests. The owner must identify which jobs can move and their latest completion times. A slow job alone is not waste: it may be business-critical. Candidate repairs include staggering schedules or preparing batching changes in the repository. Automatically disabling jobs is not the default; some pg_cron states can interrupt active work. This niche could be useful even without huge traffic because a few expensive jobs share a small machine. Its main uncertainty is the amount of owner-supplied job intent required.

**5. Capacity/cost.** Treat as an outcome across the other problems unless interviews establish a separate recurring workflow. “Saved compute” requires a measured resource reduction and an actual spend decision. Lower estimated query cost is not a verified bill saving. No automatic upgrades, paid branches or provider configuration that raises spend are implied.

### Gaps and kill criteria

- Slow interactions: reconsider if most delay is network/rendering/provider behavior, or every useful fix is a one-off app redesign already handled easily by the user's coding agent.
- Release protection: reconsider if users rarely deploy, cannot supply a usable baseline, or already catch the relevant failures reliably in CI.
- Maintenance: reconsider if default maintenance handles observed workloads, if a single durable change removes recurring work, or if practical repairs mostly require blocking rebuilds or interrupting customer transactions.
- Background work: reconsider if jobs cannot be deferred, failures occur independently of jobs, or static scheduling removes the recurring problem.
- Cost: reconsider as a standalone niche if savings are too small or infrequent to outweigh installation, monitoring and verification effort.
- Across all: reconsider unattended execution if users want explanations or patches but consistently refuse category grants. That can support a reviewed product, but it is a different workflow.

These are proposed falsification criteria, not evidence that a niche has failed. Infrequent incidents may still be valuable; frequency has to be measured over an appropriate period.

## After choosing a problem, what is actually reachable?

### Takeaway

There is enough documented access to investigate all four primary hypotheses. Read access does not prove a proposed repair is deployable, effective, or acceptable on every hosted project.

### Cited Findings

- Hosted metrics expose CPU, I/O, WAL, connections and query statistics. The feed is beta, so names and labels can change. [Metrics API](https://supabase.com/docs/guides/observability/metrics).
- Earlier research verified a scoped Management API metrics route: `GET /v1/projects/{ref}/analytics/endpoints/metrics`, avoiding a data-access secret solely for metrics where the endpoint is available. Project and plan availability still require onboarding checks. [Management metrics reference](https://supabase.com/docs/reference/api/v1-scrape-project-metrics).
- SDK trace propagation can connect application spans with gateway and Edge Function logs, but needs explicit instrumentation and supported SDK versions. It does not create application traces automatically. [Client-side tracing](https://supabase.com/docs/guides/observability/client-side-tracing).

### Inferences: capabilities mapped to the customer job

| Job | Useful evidence | Reachable repair boundary |
|---|---|---|
| Slow interaction or release regression | Request traces/logs, saved query statistics, actual role and query plan, release identity, schema/policy changes | Bounded SQL maintenance or index actions where qualified; source/RPC/RLS changes need migration/repository and deployment ownership |
| Recurring maintenance | Table activity and maintenance history, long snapshots, progress, disk/WAL pressure | Plain maintenance and table overrides use SQL and version-dependent privileges; no automatic blocking rebuild or customer cancellation |
| Scheduled interference | `cron.job`, `cron.job_run_details`, simultaneous foreground timings and resource pressure | Owner-approved schedule-only changes require hosted behavior tests; app batching belongs in code |
| Capacity pressure | Sustained resource demand, burst/headroom limits, relevant query and job contribution, actual bill | Optimize qualified workload first; return a capacity recommendation when repair evidence is absent |

Use typed, fixed diagnostic collectors with redaction, not a model's unrestricted SQL console. Aggregate query statistics are not a browser latency distribution. A comparison across different tenants, parameters or load may be misleading. LLM reasoning can be event-triggered after deterministic collection; no need to call a model for every database query.

## What discovery can one week accomplish?

### Takeaway

A week can test whether the problems and workflow fit are real enough to choose a direction. It cannot demonstrate market prevalence or the frequency of rare incidents. This is a discovery proposal, not an authorized monitoring or mutation task.

### Inferences: proposed sequence

1. **Reconstruct recent episodes.** Use Jared's projects and a few willing users with different workloads. Ask for the last concrete slowdown, failed job, emergency upgrade or time-consuming investigation. Record trigger, impact, duration, fix, evidence and who did the work. Avoid “would you use an AI DBA?” questions.
2. **Establish the existing-tool baseline.** For each episode, ask what the native advisor, slow-query report, read-only agent, and current coding assistant could already resolve. Record what remained manual. Do not confuse unfamiliarity with a tool with a permanent product gap.
3. **Observe representative activity if separately authorized.** Collect minimal read-only evidence and user-supplied release/job events. Track actionable opportunities, non-actions, missing evidence and whether a cause lies outside the database. Preserve privacy and set an observation budget.
4. **Replay specific cases in controlled fixtures.** Test whether one candidate workflow reaches a correct repair and verifies it. Jared's own projects are useful for integration and realism; synthetic fixtures prove mechanics, not demand. Avoid inventing production incident rates from a load generator.
5. **Compare three delivery modes.** Would on-demand assistance, a release-triggered check, or unattended operation have saved the most human work? Ask which exact action category the owner would allow without per-action confirmation, and which outputs require review.
6. **Choose or extend observation.** Select a niche only when its repeated human job and feasible completion path are clearer. If no relevant event appears in a week, inspect historical cases or extend observation. Record “insufficient observation,” not “no demand.”

Measure: human investigation time, time between finding and repair, correctness and verification burden, native-tool coverage, fraction needing app code, fraction eligible for an accepted automatic action, recurrence, and permission willingness. Count installations and alerts separately from useful repairs. Agree project-specific success thresholds before the pilot rather than presenting invented industry benchmarks.

### Questions that distinguish the hypotheses

- Which real problem would you most want Pooltune to own: a slow user action, a release regression, recurring maintenance, or background-job interference?
- When did that last happen, and what did you have to do manually?
- Was the database actually slow, or was that only the first suspicion?
- Do you own the repository and deployment workflow as well as the Supabase project?
- Is the pain finding the cause, implementing the fix, safely shipping it, or checking whether it worked?
- Which tables or jobs could receive a standing repair grant, and what cannot be delayed or interrupted?
- Would the same job occur again next week or next release, or would one good migration solve it?

### Gaps

No customer interviews, live observation or deployment experiments were performed. The evidence supports keeping several problem hypotheses alive. It does not yet justify choosing one, adding action families, or committing to an always-on autonomous product.
