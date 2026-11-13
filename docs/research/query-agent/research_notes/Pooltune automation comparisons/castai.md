# Cast AI compared with Pooltune

Research date: November 12, 2026. Read-only public research. “Documented” below means official product documentation, not independently tested behavior. “Observed source” means code inspected. No deployment, customer credentials, or product validation occurred.

## 1. What is Cast AI, and is it really read-only?

### Takeaway

Cast AI is an infrastructure automation company with several products. A read-only assessment agent is one component, not the whole product. For the database product specifically, distinguish the read-only Performance Advisor from the active caching/pooling proxy.

### Cited Findings

- **Documented product scope:** Current navigation lists Kubernetes optimization, cost/latency monitoring, storage autoscaling, Database Optimizer, and AI infrastructure products. Kubernetes optimization concerns cloud nodes and workload CPU/memory requests and replicas, not PostgreSQL query repair. [Company site](https://cast.ai/); [scaling policies](https://docs.cast.ai/docs/woop-scaling-policies)
- **Documented privilege separation:** The Kubernetes assessment agent reads resources and sends snapshots. Automation Phase 2 adds a Cluster Controller with node patch/update/delete and pod eviction/delete permissions. The installer/operator has lifecycle write permissions even in Phase 1; “read-only agent” does not mean installation creates nothing. Phase 2 also grants namespace administration in `castai-agent`. [Permission tables](https://docs.cast.ai/docs/kubernetes-permissions); [controller](https://docs.cast.ai/docs/cluster-controller)
- **Observed source:** The public Go controller polls Cast AI for typed actions, limits concurrent actions, tracks action IDs, executes handlers, and acknowledges success/errors. Handlers include drain/delete/patch node, evict pod, and chart rollback. This is concrete automation code. It is not evidence that a language model determines each action. The repository was archived April 21, 2026, and says development moved to internal GitLab, so it is historical implementation evidence. [Controller source](https://github.com/castai/cluster-controller/blob/main/internal/controller/controller.go); [handlers](https://github.com/castai/cluster-controller/blob/main/internal/actions/actions.go); [repository notice](https://github.com/castai/cluster-controller)
- **Company claim, corroborated reporting:** Cast AI announced a valuation exceeding $1 billion on January 12, 2026 after investment from Pacific Alliance Ventures. LRT/BNS reported the announcement January 13. This supports saying “reported unicorn,” not claiming an independently audited current valuation or attributing it specifically to Database Optimizer. [Official release](https://cast.ai/press-release/cast-ai-valued-at-over-1-billion-with-the-launch-of-its-gpu-marketplace/); [LRT](https://www.lrt.lt/en/news-in-english/19/2805099/lithuania-s-fifth-unicorn-vilnius-based-cast-ai-crosses-1bn-valuation)

### Inferences

- The useful business pattern is visible value through assessment, followed by explicitly enabled optimization. Unicorn status is not evidence for any particular safety architecture or feature.

### Gaps

- Proprietary control-plane decision algorithms and commercial results were not independently audited. Public historical source cannot establish the exact current production implementation.

## 2. What does Database Optimizer actually do?

### Takeaway

DBO overlaps with Pooltune in diagnosis and performance goals. Its documented automatic intervention is primarily changing how database traffic is served. Its advisor recommends schema/query improvements for human review; Pooltune proposes executing six constrained database repairs.

### Cited Findings

- **Documented released product:** August 24, 2026 release notes describe DBO 2.0 as in production, combining caching, connection pooling, and Performance Advisor around database instances. This is a vendor release claim, stronger than treating a landing-page promise as a shipped capability. [Release notes](https://docs.cast.ai/changelog/august-2026)
- **Documented advisor:** A read-only agent examines `pg_stat_statements` or MySQL `performance_schema`, table/index metadata, and column statistics. It ranks index recommendations by database time reclaimable. For problems indexes cannot fix, an LLM plus heuristics proposes query rewrites. The advisor never modifies schema and recommendations require human review. It works without routing traffic through the proxy. [Introduction](https://docs.cast.ai/docs/dbo-introduction); [Performance Advisor](https://docs.cast.ai/docs/dbo-performance-advisor)
- **Evidence limitation:** Recommendation pages contain SQL, reasoning, and before/after explain-plan comparisons with predicted effects. These are recommendation evidence, not proof that customer production execution improved after applying a repair. The docs call recommendations validated but do not establish an independent controlled benchmark or the full qualification methodology. [Performance Advisor](https://docs.cast.ai/docs/dbo-performance-advisor)
- **Documented runtime architecture:** A Rust proxy runs inside customer Kubernetes or Amazon ECS, speaks PostgreSQL/MySQL wire protocols, and caches query responses in customer infrastructure. ML caching decisions occur within the proxy. Customers change the application connection string. This differs from a fully hosted service requiring no customer deployment and preserving the application's existing database path. [Architecture](https://docs.cast.ai/docs/dbo-how-does-it-work); [client connection](https://docs.cast.ai/docs/dbo-connecting-client-applications)
- **Documented caching mechanics:** Deterministic reads are cache candidates; writes pass through. Observed write traffic drives row-level invalidation where parsing permits, otherwise table-level invalidation; unexplained data changes can disable caching for affected queries. This uses wire traffic rather than WAL privileges. Cache separation is documented by database and user. [Architecture](https://docs.cast.ai/docs/dbo-how-does-it-work); [FAQ](https://docs.cast.ai/docs/dbo-faq)
- **Documented permissions and pooling:** The proxy needs ordinary application connectivity, not special database administration powers. A dedicated read-only database identity is for the optional advisor; cloud Performance Insights permissions are another separate grant. Pooling is documented as shipped, using PgDog for PostgreSQL and ProxySQL for MySQL. Caching read queries does not mean the proxy handles no writes or that all components share read-only credentials. [Access requirements](https://docs.cast.ai/docs/dbo-access-requirements-and-permissions); [FAQ](https://docs.cast.ai/docs/dbo-faq)
- **Documented controls:** New proxied databases start in passthrough mode to gather a baseline and project caching benefit. Auto enables caching. Off forwards queries uncached, and per-query override rules exist. The instance audit log records configuration/component changes, initiator, and available before/after values. [Connection flow](https://docs.cast.ai/docs/dbo-connecting-client-applications); [pause troubleshooting](https://docs.cast.ai/docs/dbo-pause-dbo-for-troubleshooting); [audit log](https://docs.cast.ai/docs/dbo-audit-log)

### Inferences

- “AI” is not synonymous with an LLM-controlled repair loop. DBO explicitly uses ML for caching and an LLM for some rewrite recommendations. Kubernetes autoscaling has documented metrics, policies, and controllers. CastPilot is a separate natural-language assistant, largely read-only except guided scaling-policy generation. [CastPilot](https://docs.cast.ai/docs/castpilot-assistant)
- DBO's admission path and cache correctness create different risks from Pooltune's maintenance actions. A cache can change observed results even without editing stored rows; a read-only advisor can still impose collection overhead.

### Gaps

- No verified Supabase-specific qualification, RLS/session-state compatibility proof, full transaction-isolation matrix, or formal invalidation guarantee was found. Do not infer these from generic PostgreSQL support.
- “Never stale” marketing is stronger than the inspected mechanics establish, particularly for writes bypassing the proxy. Troubleshooting explicitly anticipates apparently stale results. [Caching claims](https://docs.cast.ai/docs/dbo-caching); [troubleshooting](https://docs.cast.ai/docs/dbo-pause-dbo-for-troubleshooting)
- Data-boundary documentation is inconsistent: introduction describes identifier placeholders for rewrite analysis, while the indexed security page describes analytics retaining database/table/column names. The security URL returned 404 when opened. Treat these as different or insufficiently reconciled data paths, not blanket proof that all metadata is anonymous. [Introduction](https://docs.cast.ai/docs/dbo-introduction); [indexed security URL](https://docs.cast.ai/docs/dbo-security)

## 3. What should Pooltune borrow, and what should it not copy?

### Takeaway

Borrow staged authority, measurable outcomes, explicit policies, and recoverable actions. Preserve Pooltune's accepted scope. Its hosted six-repair service remains a proposal, not a demonstrated competitive capability.

### Cited Findings

- Pooltune's accepted plan specifies nonunique index creation, targeted ANALYZE, extended statistics/statistics targets, bounded VACUUM/cleanup settings, enrolled function work_mem, and approved-window Cron shifts. It excludes pooling changes, proxies, application rewrites, and purchasing compute. Go owns scheduling/tools/enforcement, one TypeScript/LangGraph investigator reasons, and owned PostgreSQL records evidence/actions. [Accepted plan](/Users/jshum/Desktop/code-folders/pooltune-learning/docs/product/query-performance-v1.md)
- Cast's workload autoscaler documents confidence from historical data, conservative early adjustments, thresholds, and automatic disabling after persistent OOM events. Its rollout configuration can change pods sequentially and wait for readiness. These are concrete bounded-control examples, not a universal database recipe. [Autoscaler](https://docs.cast.ai/docs/workload-autoscaling-overview); [rollout configuration](https://docs.cast.ai/docs/workload-autoscaling-configuration)
- Historical node deletion code rechecks identity and uses a UID precondition. Controller action deduplication is in memory and acknowledgement-dependent; it does not establish exactly-once effects across crashes. [Deletion handler](https://github.com/castai/cluster-controller/blob/main/internal/actions/delete_node_handler.go); [controller](https://github.com/castai/cluster-controller/blob/main/internal/controller/controller.go)

### Inferences

- **Product positioning:** “Maintains important Supabase queries through permitted repairs” is more specific than “AI database optimizer.” Index diagnosis overlaps directly with DBO; automatic execution of the six permitted scopes is the proposed distinction. Do not claim Cast cannot perform other maintenance merely because these docs do not document it.
- **Tool contracts:** Separate inspect, experiment, and execute grants. Encode target identity, fresh preconditions, resource budgets, and permitted effects in Go. A favorable model explanation must not grant authority.
- **Evidence reuse:** Start from observed workload and existing valid evidence. Present estimated plan benefit separately from measured execution benefit. Request extra execution only for a decision-changing gap. Cast's projection-versus-actual UX is useful; its recommendations do not justify redundant tests or guaranteed savings claims.
- **Recovery:** Record durable action IDs and reconcile uncertain outcomes before retries. Expose stop/history controls. Define rollback separately for each repair; stopping optimization does not undo ANALYZE or VACUUM.
- **Deployment:** Do not copy the customer-side proxy or multi-component Kubernetes installation into the approved hosted design. Those components solve a different placement and traffic-serving problem.

### Gaps

- Pooltune has not demonstrated production repair safety, efficacy, scalability, or savings. Competitive differentiation here is a testable product hypothesis. The technical design must qualify each operation before unattended release.
