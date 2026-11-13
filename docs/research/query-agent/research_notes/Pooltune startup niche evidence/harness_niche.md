# Agent harness, evaluation, and niche fit

## Does adding eight capability families weaken the agent?

### Takeaway
The user's concern is well founded, but the issue is behavior and validation coverage, not distilling or changing the model's weights. Eight families can serve one focused product, but their presence does not prove either good performance or harmful overload.

### Cited Findings
- Anthropic defines an agent harness as the system that supplies inputs, orchestrates tool calls, and returns results. It says agent evaluation measures the model and harness together; final environment state is distinct from an agent's claim of success. It recommends multiple trials because results vary and distinguishes consistency across repeated trials from eventually succeeding once. [Anthropic agent evaluations](https://www.anthropic.com/research/demystifying-evals-for-ai-agents)
- Anthropic's context guidance concerns information supplied during inference, including instructions, tools, retrieved data, and history. It identifies overlapping or overly broad tools as a source of ambiguous choices and advocates sufficient high-signal context with clear tool boundaries. [Anthropic context engineering](https://www.anthropic.com/research/effective-context-engineering-for-ai-agents)
- Anthropic recommends evaluating tools on realistic tasks with verifiable outcomes and held-out cases, not only isolated easy calls. Tool descriptions, meaningful outputs, and functionality boundaries are important alongside selection of which tools to expose. [Anthropic tool engineering](https://www.anthropic.com/research/writing-tools-for-agents)

### Inferences
- Suggested plain explanation: “Adding tools does not retrain or distill the model. It changes the choices and information the agent works with. The risk is choosing the wrong repair, misunderstanding evidence, or claiming success too early.” Do not nitpick the user's wording beyond this one distinction.
- Eight is not a meaningful quality threshold. One broad `run_sql` tool could be harder to constrain than eight narrow operations. Conversely, many individually working operations create more combinations and recovery situations to validate.
- A niche should constrain the promised outcome and evaluation population. It need not artificially reduce all repairs to a single command. A broad diagnosis surface can support a narrower automatic repair scope.
- “The regression tests still pass” is necessary but not sufficient. A broad suite can conceal bad performance for a particular family or customer case, and a suite can omit the actual deployment distribution. Report results by problem and action type, including harmful changes and wrong abstentions.
- Separate three things in conversation: product niche (whose recurring problem); capability catalog (possible repairs); unattended release scope (repairs qualified for this product under available evidence and permissions).

### Gaps
- No Pooltune evaluation has been run. We cannot say eight capabilities harm or preserve actual success rates.
- No public source found establishes a universal safe tool count. Nor do primary agent articles establish that any particular Pooltune niche has market demand.

## What should an evaluation prove before retaining all eight?

### Takeaway
The evaluation should start from a customer symptom and uncertain evidence, then inspect the resulting database and useful workload. A successful command or lower CPU reading alone is inadequate.

### Cited Findings
- PostgreSQL planner costs use arbitrary units and omit some client-facing costs, including transmission. ANALYZE uses sampling, so estimates can vary. Estimated plan cost is therefore not a measured app latency or bill. [PostgreSQL EXPLAIN](https://www.postgresql.org/docs/17/using-explain.html)
- `pg_stat_statements` exposes query calls, planning/execution aggregates, row counts, buffer activity, and temporary blocks. Its timing fields include mean, min, max, and standard deviation rather than app response-time percentiles. [PostgreSQL statement statistics](https://www.postgresql.org/docs/17/pgstatstatements.html)
- Indexes can accelerate selective access but must be maintained on writes. This makes read-only verification insufficient for mixed workloads. [PostgreSQL indexes](https://www.postgresql.org/docs/17/indexes-intro.html)
- Supabase documents testing RLS performance in the appropriate role/auth context and shows optimizations that rely on query and policy semantics. [Supabase RLS performance](https://supabase.com/docs/guides/database/postgres/row-level-security-performance)
- PostgreSQL automatically performs routine cleanup; old transactions can prevent removal of old row versions. Ordinary VACUUM generally makes storage reusable rather than returning it to the operating system. [PostgreSQL routine vacuuming](https://www.postgresql.org/docs/17/routine-vacuuming.html)

### Inferences
Proposed product-level acceptance questions, not an implementation plan:

| Test situation | What Pooltune must demonstrate |
|---|---|
| Genuine repair opportunity | Identify the responsible cause from symptoms and evidence, make an allowed repair, and improve useful work at comparable load. Do not hand the diagnosis to the agent in every test. |
| Healthy database or misleading alert | Leave it alone. High memory use, a sequential scan, or an old-looking index is not automatically a defect. |
| Same symptom, different cause | Distinguish a slow scan from lock waiting, an app making serial calls, provider issues, and actual capacity exhaustion. A candidate tool must not become the default explanation. |
| Several plausible fixes | Prefer a justified repair; do not grade only one exact sequence when several outcomes are valid. |
| Correctness and access | Preserve returned data and intended access under realistic roles, tenants, common/rare parameters, and data distributions. |
| Read/write tradeoff | Faster reads cannot hide excessive write delay, storage growth, failures, or fewer successfully completed requests. |
| Incomplete evidence or authority | State the gap and abstain from dependent changes. Do not invent confidence because an action exists. |
| Repeated autonomous runs | Avoid duplicate indexes, repeated unnecessary maintenance, setting oscillation, and false success after a previous incomplete action. |
| Interruption, external changes, and failed recovery | Report the actual resulting state and avoid unsupported “rolled back” claims. Maintenance may have no ordinary undo. |
| Native maintenance or hardware is the answer | Recognize native recovery and genuine capacity limits. “No local change” can be success. |

Compare a smaller capability set against added families on the same held-out cases, with repeated trials. Assess improvement in completed eligible repairs, harmful-change rate, correct no-action rate, unsupported-case recognition, and diagnosis/verification cost. Add cross-family cases where one repair changes another workload's performance. Do not invent numerical release thresholds before the product's harm tolerance and representative cases are agreed.

### Gaps
- Synthetic fixtures can prove mechanics and expose failures; they cannot establish how often startups need the product.
- Actual p95 app latency requires appropriate application/request measurements. DB aggregates alone cannot prove it.
- Broad benchmark success cannot establish safety on every schema, provider plan, PostgreSQL version, or workload.

## What specific niche best fits the existing catalog?

### Takeaway
Provisional recommendation: **ongoing query-performance care for small Supabase SaaS teams without a dedicated database specialist, as their data and customer usage change**. Strongest alternative for unattended operation: **maintenance for update-heavy Supabase tables**. These are hypotheses to validate, not proven markets.

### Cited Findings
- The existing eight families are: planner statistics; index design/lifecycle; table/index maintenance; scoped RPC execution; existing-pool control; scheduled work; WAL/checkpoint policy; reviewed code/policy optimization. Their detailed operations and permissions are proposals, not all approved autonomous features. [Existing local catalog](/Users/jshum/Desktop/Tech%20Stuff/Resumes/softEng/reports/Pooltune%20expanded%20optimization%20catalog.md)
- The prior problem report compared slow interactions as data changes, release regressions, write/cleanup maintenance, and scheduled-work interference. It explicitly left demand and niche selection unresolved. [Prior local problem research](/Users/jshum/Desktop/Tech%20Stuff/Resumes/softEng/reports/Pooltune%20database%20problems%20and%20niches.md)

### Inferences
Possible product sentence: **“Pooltune helps small teams keep important Supabase queries fast as their app's data changes, by finding and verifying permitted database repairs.”** Add unattended execution as a mode once qualified, not an already established reliability claim. “Without a dedicated database specialist” names the person and unmet labor, while “important queries” and “as data changes” narrow the outcome and trigger.

This niche encompasses schema/data growth, tenant imbalance, and cleanup affecting important queries. It should not promise to manage every form of scale, redesign storage architecture, or always avoid a compute upgrade. A release can trigger the same care workflow without making release protection a separate headline product.

| Existing family | Fit under that niche |
|---|---|
| 1. Planner statistics | Core. Helps the database choose sensible execution methods as data distribution changes. |
| 2. Index design/lifecycle | Core. Helps important requests avoid unnecessary searching. Qualify templates individually; customer-index deletion is a separate scope decision. |
| 3. Table/index maintenance | Supporting repair when measured cleanup/index condition affects enrolled queries. Becomes core in the maintenance alternative. |
| 4. Scoped RPC execution | Conditional core for enrolled database functions. Useful for a proven execution problem, not blanket memory tuning. |
| 5. Existing-pool control | Supporting diagnosis/exceptional repair. It dilutes the headline if presented as general autoscaling; native pooling already addresses much of this. |
| 6. Scheduled work | Supporting when an enrolled job demonstrably interferes with important queries and has a permitted schedule window. Otherwise optional. |
| 7. WAL/checkpoint policy | Most likely to dilute the first release. It targets broader write-path/resource policy; retain research but require concrete target-workload evidence before including. |
| 8. Reviewed code/policy optimization | Relevant escalation lane when DB-only actions cannot solve the query problem. A reviewed patch is a different autonomy promise from unattended maintenance. |

The catalog is coherent at the broad level “PostgreSQL performance operations,” but that remains too broad for a precise interview explanation. No need to discard research. Keep all eight as a roadmap and qualify the ones that complete the chosen customer job. If user strongly wants all eight in V1, condition inclusion on demonstrated value to this same outcome plus evaluation coverage; mere non-regression is insufficient if a family adds no useful coverage.

The maintenance alternative has clearer selected-table authority and recurring work, but native autovacuum is strong competition and some cases are fixed permanently by one setting. It may provide less immediately understandable customer value than “your important queries stay fast.” A product decision should depend on startup episodes and interventions, not the engineer's preference for simpler automation.

### Gaps
- No evidence in this subtask proves demand for the proposed audience or that query care is commercially superior to maintenance. Startup case research from the other workstream should decide how strongly to recommend it.
- User's account of Angel does not establish a database engine, exact problem, or solution. Calls, events, and transactions do not imply MongoDB or NoSQL, and schema design matters in PostgreSQL too. Treat that account as a lead for concrete incident questions, not requirements.
