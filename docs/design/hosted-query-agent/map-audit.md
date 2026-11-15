# Build-map audit: what yellow actually means

Audited November 14, 2026 against implementation through `99ff9e8` and the retained hosted repair reports. This is a source/status audit, not a new runtime qualification or visual browser test.

The six bounded repair paths are implemented and passed real Sonnet in staging. Yellow parent cards represent larger responsibilities. The earlier map obscured this distinction and had stale overview text saying hosted integration was unfinished. That text is corrected. Cards now separate **Build next** from **Verify next**; existing tested children remain green.

| Area | Implemented evidence | Concrete missing implementation | Additional verification |
|---|---|---|---|
| Nonunique indexes | Scalar case plus fixed two-column equality case; hosted compound 11.370→0.036 ms | Choose authorized real tables, columns and key order from enrolled queries. Current columns validator accepts only scalar or `[2,3]`; executor constructs `lookup` or `lookup,padding`. | Qualify newly supported predicate/order shapes, index overlap, storage and concurrent committed writes. |
| Statistics detail | `CREATE STATISTICS ... (mcv) ON lookup,padding` followed by ANALYZE; hosted 13.520→0.276 ms | Implement the V1 individual-column statistics-target adjustment, prior-value capture and restoration; generalize the joint-statistics target. | Uneven inputs, analysis resource cost and restoration. |
| Cleanup | Plain VACUUM; hosted 38.844→8.627 ms | Implement V1 cleanup-trigger threshold adjustments with bounds and prior values. Fixture setup disabling autovacuum is not that repair. | Demonstrated recurring late-trigger cause and maintenance interference. |
| Investigator | Screen → compare causes → request evidence → finish; six hosted action routes and a separate requested ANALYZE observation | General enrolled-query evidence adapters and complete failed-attempt timing reports. Current diagnostic consumer is restricted to one owned Rookery fixture. | Other requested hosted readers, representative competing causes, and hosted restart during a model call. Local resume tests already exist. |
| Developer viewer | Both graphs registered; recorded earlier API registration and scripted replay | Attach Go evidence tools to Studio sessions; connect worker/queue/action/verification events into a visible run history. Studio's exported diagnostic reader currently throws an explicit unattached-reader error. | Demonstrate actual hosted end-to-end viewing. Existing developer registration/replay is not this evidence. |

An index such as `(tenant_id, created_at)` for an equality filter plus a date range is an example of a shape to design and qualify, not a claim that every such index must be added or that all PostgreSQL index variants are V1 requirements.

## Source checks

- `internal/hostedquery/index_policy.go`: `ValidIndexColumns` limits the ordered identity.
- `internal/hostedquery/compound_index.go`: `compoundQuery` fixes two equality predicates and owned fixture identities.
- `internal/hostedquery/index_trial.go`: `buildBoundedIndex` constructs the fixed index keys.
- `internal/hostedquery/statistics_samples.go`: `refreshExtendedStatistics` creates multicolumn MCV statistics. Source search across internal/cmd/investigator found no implemented SET STATISTICS repair.
- `docs/product/query-performance-v1.md`: includes column statistics-target and cleanup-threshold adjustments separately from their tested counterparts.
- `cmd/pooltune-consumer/main.go`: diagnostic mode requires exactly one owned repair fixture route.
- `investigator/diagnostic.ts`: diagnostic graph and request loop; `diagnostic-model.ts`: Studio export explicitly lacks its Go reader.
- `investigator/langgraph.json` and `docs/design/hosted-query-agent/index-slice.md`: registration and recorded replay scope.
- `repair-expansion.md` and its raw reports: current hosted success, request and recovery limits.

Map colors are scoped evidence labels, not completion percentages or production approval. Collector/queue/state green cards also explicitly say staging and retain their production limits. Accepted architecture decisions and approvals remain unchanged. No customer grants or deployment configuration changed in this audit.
