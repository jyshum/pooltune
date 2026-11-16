# Pooltune MVP build plan

**Approved by Jared on November 15, 2026, with the simplified onboarding below.** The existing [V1 product scope](../../product/query-performance-v1.md) defines the six repair families. Use this page for build order, ownership and pass conditions. Record completed work and evidence in [implementation](implementation.md), [progress](progress.md) and the [design map](design.html).

## Starting point when approved

- The existing landing page leads to invite signup and sign-in. An owner can connect a Supabase project, choose queries to watch, read saved latency and Sonnet diagnoses, and disconnect. The Railway dashboard completed that flow on an owned Rookery fixture. [Hosted proof](evidence/hosted-preview-railway.json)
- The investigator can use an owner-captured execution plan and measured plans from two kinds of ordinary app queries. It can request named evidence and return a reasoned no-change result. [Plan evidence](traffic-plans.md) · [Investigator proof](hosted-diagnostic.md)
- All six repair families have a working bounded case in staging. Single-column and two-column nonunique indexes both ran. The compound case uses fixed columns and positions. The repair worker runs on separate staging fixtures. The dashboard's connected-project worker currently ends after diagnosis. [Repair evidence](repair-expansion.md) · [Dashboard worker](../../../internal/hostedapp/supervisor.go#L152)

The MVP is the joined flow. An owner connects an existing Supabase project and grants specific repair powers. Pooltune watches the chosen workload in the background, investigates a problem, carries out an eligible repair, checks its effect, and shows what happened. A healthy query, insufficient evidence or denied permission produces an explained no-change outcome.

Railway has verified five repair families from the normal setup with zero manual query IDs, plus index restart recovery and both settings. The scheduling fixture reached a native schedule change and still needs follow-up verification. Operation-specific repair authority for a normally connected project remains open. [Latest build progress](progress.md#tasks-5658-simple-repair-setup-scheduling-and-the-remaining-settings)

## Build in this order

### 1. Connect and enroll a project

[Connect and enroll a project](design.html#v=n%3Abackend&s=n%3Aconnect-project) · [Credentials and disconnect](design.html#v=n%3Abackend&s=n%3Acredential-lifecycle) · [Sign-in and account access](design.html#v=n%3Adashboard&s=n%3Aauth)

**Build.** Keep the original landing and account entry. Connect a Supabase project using a scoped setup credential. Show the access requested and let the owner select one or more of the six repair types or select all. Pooltune discovers the workload and checks each discovered target against PostgreSQL privileges and the selected operations. No query IDs, table list or free-text instructions are required. Optional natural-language preferences belong to V2. Manage setup and runtime credentials separately and bind them to the account and project.

**Pass.** A new owner signs in, connects a project, sees the permissions and starts the agent without defining any query or table. The background worker discovers traffic. A repair requires a selected family and an independently eligible, authorized target. Another account, an unsupported target and a revoked grant are refused. The dashboard shows what the owner selected and what the worker can actually execute.

### 2. Inspect existing evidence

[Inspect existing evidence](design.html#v=n%3Atools&s=n%3Ainspection) · [Investigator](design.html#s=n%3Ainvestigator)

**Build.** Bind normal app traffic to the exact query, application role and database objects. Add the plans and database observations the six repair families need, including usable indexes, stale or insufficient statistics, cleanup backlog, disk spill and job interference. Support representative app connection paths, and identify paths where the required evidence is unavailable. Give Sonnet fresh, relevant evidence and make it explain the cause, missing information or reason for doing nothing.

**Pass.** An ordinary slow query produces the correct diagnosis with measured evidence. A healthy query and a misleading slowdown are left alone. A query with unavailable evidence shows the gap instead of producing a repair.

### 3. Run a justified experiment

[Run a justified experiment](design.html#v=n%3Atools&s=n%3Aexperiments)

**Build.** Use ordinary traffic and existing observations when they already answer the question. When a missing measurement could change the decision, measure the exact approved query with representative inputs inside the project's time, load and permission limits. Defer when those limits or the requested evidence cannot be checked.

**Pass.** On the same connected project, an investigation with enough evidence skips the experiment. One that needs a measurement can request it and finish. An unsafe or unapproved experiment is refused while normal app traffic continues.

### 4. Qualify broader repairs

[Qualify broader repairs](design.html#v=n%3Atools&s=n%3Arepair-coverage) · [Add a nonunique index](design.html#v=n%3Atools&s=n%3Aindex) · [Choose indexes for real queries](design.html#v=n%3Aindex&s=n%3Aindex-compound-general)

**Build.** Connect the dashboard's background worker to the repair and verification path. Use one investigator for all six approved operations. Go checks the current target, owner grant, existing objects and resource limits before it executes a repair. Persist the intended action before changing PostgreSQL.

| Repair family | Required behavior on an enrolled project |
| --- | --- |
| [Add a nonunique index](design.html#v=n%3Atools&s=n%3Aindex) | Choose authorized table and column names from the query's filters and ordering. Build single-column and compound indexes with the correct key order. Use real column positions and avoid equivalent indexes. Verify supported shapes, including two equality filters and representative equality with range or ordering. Measure storage and effect on writes. |
| [Refresh statistics](design.html#v=n%3Atools&s=n%3Arefresh) | Use targeted ANALYZE on the eligible tables or columns for the affected query. Show that fresh statistics improve the outcome under representative traffic. |
| [Improve statistics detail](design.html#v=n%3Atools&s=n%3Aricher) | Choose extended statistics for relevant real columns and add the bounded individual-column statistics-target adjustment. Measure improvement and analysis cost. Restore the previous setting when appropriate. |
| [Perform targeted cleanup](design.html#v=n%3Atools&s=n%3Acleanup) | Bind plain VACUUM to an approved busy table. Add bounded cleanup-trigger adjustments when late cleanup is the demonstrated cause and measure whether backlog improves. |
| [Function memory](design.html#v=n%3Atools&s=n%3Amemory) | Use a function-specific memory change for an enrolled function when evidence shows spill and the project has enough memory under concurrent calls. |
| [Job scheduling](design.html#v=n%3Atools&s=n%3Acron) | Move enrolled future Cron runs inside owner-approved windows, keeping deadlines and frequency while reducing measured interference. |

**Pass.** From the normal hosted project flow, each family completes at least one eligible case with real evidence and shows a refusal when its conditions fail. For indexes, prove both single-column and compound repair on enrolled tables whose names and column positions differ from the original fixture.

### 5. Show results and recover safely

[Project activity and controls](design.html#v=n%3Adashboard&s=n%3Aactivity) · [Verify and recover](design.html#v=n%3Atools&s=n%3Arecovery) · [Developer viewer](design.html#s=n%3Astudio)

**Build.** Show the query, evidence, what Sonnet decided, any change made, and observed before-and-after results. Include the effect on protected reads and writes, no-change reasons, failures and a pause control. Make the worker inspect uncertain actions after a restart before retrying, and restore what is safe and supported. In the developer viewer, show one actual hosted investigation and its linked evidence, action and verification using authorized access.

**Pass.** After a qualifying repair, the owner's dashboard shows actual benefit and protected traffic. If the worker restarts mid-action, it reconciles the recorded change without applying it twice. If a change is inconclusive or causes a regression, the dashboard shows that result and any recovery. A paused project starts no new repair.

### 6. Prove the whole thing from the normal product flow

[Production operations](design.html#s=n%3Aoperations) · [Hosted API](design.html#s=n%3Abackend)

**Build.** Finish operational checks for worker and database reconnect, account isolation and credential expiry, and recovery of the Pooltune state database. Assemble one repeatable Railway run through the existing landing, account, hosted project and dashboard paths. Use an owned Rookery project with representative reads and writes across the six repair cases and the healthy control. Keep the worker running after closing the browser.

**Pass.** A person can start at the existing landing page, connect an approved project, run normal app traffic, and open the dashboard to see **detect → diagnose → apply → verify**. The same service proves all six families using eligible cases, keeps healthy traffic working and has a recorded restart or recovery check. Capture the result and exact limits for each map node. A compound-index run gives an interview demonstration as soon as that route works. The complete MVP check covers the other five through the same product entry.

After the Railway run passes, Jared connects the chosen real project manually and selects which repairs to authorize. Use a short, supervised first session with actual workload and compare the results in the dashboard. A week of monitoring can follow as a rollout check when useful.

## Parallel work after confirmation

Use the existing project and query identity. Share the six operation names and the job and result fields the workers and dashboard use. Run these workstreams together.

| Workstream | Steps | Parallel deliverable |
| --- | --- | --- |
| Project and permissions | 1 | Owner setup, grant checks, credential lifecycle and project control. |
| Evidence and investigation | 2 and 3 | Shape-aware evidence, justified observations and Sonnet decisions. |
| Repair and verification | 4 and worker parts of 5 | Hosted repair entry, all six contracts, action journal and interrupted-action recovery. General compound indexing goes first within this workstream. |
| Dashboard and live run | Dashboard and viewer parts of 5, preparation for 6 | Before-and-after and failure states, pause control, and the repeatable Railway workload. |

Join the workstreams through the real hosted flow before the full check in Step 6. Report a short update whenever a product path passes or a real blocker changes what the interview can show. For each named node, record what shipped, what the worker did on Railway, and what still blocks green. A unit test proves a component. The connected-project run proves the product flow.
