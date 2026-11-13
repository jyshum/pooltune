# VoltAgent database optimizer versus planned Pooltune

## What is the linked file, and what does it actually do?

### Takeaway

Being Markdown does not make it ineffective. It is a Claude Code subagent definition that depends on Claude Code, a model, available tools, and the supplied environment. It is useful to evaluate as a reusable investigation prompt, not as a deployed database service. [Claude Code documentation](https://code.claude.com/docs/en/sub-agents)

### Cited Findings

- Researched November 12, 2026. The exact requested file's most recent change is commit `8ce183f5900c718e5a139982c910039d54017fbd`, dated February 10, 2026. Repository HEAD was `ed74d115b7fb7026a22a4f816a8e3250f425e3b4`, dated November 2, 2026. Raw contents were fetched, not inferred from the filename. [File history API](https://api.github.com/repos/VoltAgent/awesome-claude-code-subagents/commits?path=categories/05-data-ai/database-optimizer.md&per_page=1); [pinned raw definition](https://raw.githubusercontent.com/VoltAgent/awesome-claude-code-subagents/8ce183f5900c718e5a139982c910039d54017fbd/categories/05-data-ai/database-optimizer.md); [repository revision](https://github.com/VoltAgent/awesome-claude-code-subagents/commit/ed74d115b7fb7026a22a4f816a8e3250f425e3b4)
- It selects `sonnet` and declares `Read, Write, Edit, Bash, Glob, Grep`. It asks Claude to obtain context, investigate queries/plans/metrics, implement improvements, establish baselines, change incrementally, test, monitor, document, and prepare rollback. Its scope spans several database engines and broad query, schema, index, memory, maintenance, replication, and infrastructure tuning. [Pinned definition](https://github.com/VoltAgent/awesome-claude-code-subagents/blob/8ce183f5900c718e5a139982c910039d54017fbd/categories/05-data-ai/database-optimizer.md)
- Claude Code treats the Markdown body as a system prompt, with configuration in YAML frontmatter. Subagents have their own context and tool access. Without an explicit permission mode, they inherit the main conversation's mode. Skills are separate reusable content that can be loaded into a subagent. [Official runtime documentation](https://code.claude.com/docs/en/sub-agents)
- The repository supports installation through a Claude Code plugin, copying definitions into `~/.claude/agents/` or `.claude/agents/`, or its installer. It identifies these as built-in Claude Code tools, and says additional external tools require configuration. Installation places instructions in a host runtime; it does not provision a customer database integration. [Pinned README](https://github.com/VoltAgent/awesome-claude-code-subagents/blob/ed74d115b7fb7026a22a4f816a8e3250f425e3b4/README.md)

### Inferences

- A capable model can turn concise guidance into substantial work. With suitable access, shell commands could inspect a database, execute SQL, or create scripts. File length cannot establish diagnosis quality.
- The context-manager request is a suggested communication format, not a supplied database connector. Naming collaborators does not establish that they are installed or connected.
- This definition could be one component of a larger unattended system. Such hosting, credentials, scheduling, and operational controls would have to come from elsewhere. A background subagent task is not, by itself, a continuously operated customer service.

### Gaps

- This review did not install or execute the prompt, connect a database, or test its diagnosis accuracy. No task-specific performance claim follows from reading it.

## How does it overlap with Pooltune's six repairs?

### Takeaway

There is meaningful subject-matter overlap. Pooltune's planned distinction is a narrower set of controlled operations, continuing observation, and evidence of actual outcomes. Those are accepted requirements, not demonstrated advantages. [Pooltune product plan](</Users/jshum/Desktop/code-folders/pooltune-learning/docs/product/query-performance-v1.md>)

### Cited Findings

The comparison below maps the inspected prompt's topics to the accepted product requirements. The overlap column identifies coverage, not proof that the prompt successfully performs the repair. [Prompt](https://github.com/VoltAgent/awesome-claude-code-subagents/blob/8ce183f5900c718e5a139982c910039d54017fbd/categories/05-data-ai/database-optimizer.md); [product plan](</Users/jshum/Desktop/code-folders/pooltune-learning/docs/product/query-performance-v1.md>)

| Pooltune repair | Prompt overlap | Planned Pooltune qualification |
|---|---|---|
| Nonunique indexes | Strong: index selection and multiple index forms | Test relevant reads, write/storage costs, target permission, and recovery. Preserve customer-created indexes. |
| Targeted ANALYZE | Statistics updates and drift | Establish outdated planner information and eligible targets; verify the resulting execution. |
| Extended statistics or bounded statistics target | Statistics targets; extended statistics not specified | Distinguish stale data from correlation/skew problems; cover representative inputs. |
| Plain VACUUM and selected cleanup thresholds | Vacuum settings and bloat | Demonstrate cleanup-related work or late triggering; bound maintenance interference. |
| Function-scoped work_mem | Broad memory tuning | Require enrolled functions, demonstrated spilling, and tested memory/concurrency budgets. |
| Enrolled Cron schedule shifts | No explicit corresponding repair | Demonstrate recurring interference; preserve owner-approved windows and job deadlines. |

- Pooltune plans a hosted Go backend and workers, one TypeScript/LangGraph investigator, and Pooltune-owned PostgreSQL for operational state. Go must enforce permissions and limits; LangGraph coordinates investigation and checkpointed reasoning. The document explicitly says this hosted architecture is not built or validated by the plan. [Product plan](</Users/jshum/Desktop/code-folders/pooltune-learning/docs/product/query-performance-v1.md>)
- Pooltune requires continued work while the customer's laptop/browser is closed; selected telemetry history; explicit grants; durable action records; reconciliation after uncertain failures; stop controls; verification; and honest inconclusive/no-repair outcomes. It also requires reuse of sufficiently valid existing evidence before additional experiments. [Product plan](</Users/jshum/Desktop/code-folders/pooltune-learning/docs/product/query-performance-v1.md>)

### Inferences

- Borrow the investigation checklist and the discipline of examining competing causes. Convert each useful topic into a question with an evidence source and a decision it changes.
- Keep interpretation separate from execution authority. An investigator can recognize an out-of-scope cause without being allowed to rewrite application code or alter infrastructure.
- A fair baseline would run this prompt in a specified host against the same workloads, evidence, permissions, and budgets as Pooltune. Comparing a configured agent to a product-plan document cannot demonstrate a winner.

### Gaps

- No head-to-head evaluation exists in this research. Pooltune has not demonstrated better diagnosis, safer repairs, lower cost, or improved uptime.

## Which limitations matter, and what should Pooltune avoid copying?

### Takeaway

Treat it as a starting checklist. The main concern is the distance between broad instructions and enforceable, measured behavior, not the `.md` extension.

### Cited Findings

- The prompt gives universal numerical targets, including queries below 100 ms and index usage above 95%, and a canned success report containing 87% improvement and 3× traffic. Those appear as instructions/examples, without an accompanying experimental record. [Pinned definition](https://github.com/VoltAgent/awesome-claude-code-subagents/blob/8ce183f5900c718e5a139982c910039d54017fbd/categories/05-data-ai/database-optimizer.md)
- A recursive repository-tree search checked paths containing evaluation, testing, benchmark, validation, and workflow terms. It found agent-topic files and a GitHub workflow that checks plugin-version changes. That workflow does not evaluate database optimization outcomes. [Pinned tree](https://api.github.com/repos/VoltAgent/awesome-claude-code-subagents/git/trees/ed74d115b7fb7026a22a4f816a8e3250f425e3b4?recursive=1); [inspected workflow](https://github.com/VoltAgent/awesome-claude-code-subagents/blob/ed74d115b7fb7026a22a4f816a8e3250f425e3b4/.github/workflows/enforce-plugin-version-bump.yml)
- Claude Code supports permission handling and hooks that validate tool calls. Therefore, the absence of a database-specific boundary in this definition does not mean the host has no controls. [Official documentation](https://code.claude.com/docs/en/sub-agents)

### Inferences

- Targets need workload definitions, measurement intervals, and business relevance. Optimizing an arbitrary index-usage percentage could reward the wrong behavior. Prefer comparable completed work, latency, errors, read/write cost, and resource measurements.
- Example success numbers should never become default report content. Reports should be generated from recorded observations, with uncertainty carried forward.
- Broad shell access is a materially different interface from a narrowly authorized repair tool. Database credentials and host permissions determine what it can actually affect. A prompt requesting caution is not a database privilege boundary.
- A reliable unattended service needs protection against overlapping repairs, repeat actions after crashes, outdated evidence, and instruction-like content in diagnostic inputs. Those concerns require concrete design and evaluation. Neither an agent persona nor choosing LangGraph resolves them automatically.
- Evaluate both justified changes and justified inaction on unfamiliar workloads, misleading symptoms, healthy databases, incomplete evidence, and failed operations. This follows Pooltune's own accepted success criteria. [Product plan](</Users/jshum/Desktop/code-folders/pooltune-learning/docs/product/query-performance-v1.md>)

### Gaps

- The tree search was not an exhaustive content audit or a search of external/private evaluations. The defensible conclusion is that no measured database-optimizer result was established by the inspected materials, not that no evaluation exists anywhere.
