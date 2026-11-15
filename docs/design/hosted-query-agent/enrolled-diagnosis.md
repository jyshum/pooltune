# Diagnose an enrolled lookup

This slice follows the existing Go permissions boundary and the single LangGraph investigator. A project owner can opt an already-monitored statement into Sonnet diagnosis. The first supported shape is one schema-qualified SELECT of an integer column with an equality predicate on that same column, on an ordinary non-RLS heap table. The statement runs in the application as before.

The owner confirms the statement against PostgreSQL's observed normalized query text when granting access. A private fixed PostgreSQL routine reads the current enrolled identity, objective, counter generation and a small set of catalog facts. The routine returns no query text, schema or table names, application rows, input values or statistics samples. Pooltune adds the five measured detector windows and checks the same grant immediately before Sonnet and after the result. PostgreSQL supplies table size and maintenance estimates and whether a valid unfiltered default integer B-tree starts with the lookup column. Sonnet compares likely causes and names what the evidence cannot establish. The graph result stays diagnostic and grants no execution authority.

This scope uses passive catalog evidence and actual query latency/page counts. A planner replay could add stronger plan evidence, but requires reproducing application role and input context, plus a separate grant and work budget. Metadata and counters alone cannot prove an actual scan node, a cause, or repair benefit. Broader shape support, representative planning, hosted deployment, and customer repairs remain separate work.

## Operate the reader

1. Start from a connected project with the [monitoring setup](supabase-monitoring.md). Generate the optional owner SQL with `pooltune-project setup-evidence-sql -ref "$PROJECT_REF" -key "$INSTALLATION_KEY"`. Run it once as the same project database owner. The existing monitor credential and grants stay usable without this optional script.
2. Enroll a statement in the monitoring registry and run it under the application role. The owner then grants diagnosis for that exact role, signed query ID, relation and column. Replace the example values and the private schema name with the actual installation values. The call succeeds only when PostgreSQL has observed the supported spelling and the statement is in the monitoring registry.

   ```sql
   SELECT pt_monitor_<INSTALLATION_KEY>.allow_lookup(
       12345::oid, -1234567890123456789::bigint,
       'application.items'::regclass, 'lookup'::name
   );
   ```

3. Run the normal monitor until it queues an investigation. From the repository root, run `pooltune-project diagnose -ref "$PROJECT_REF" -key "$INSTALLATION_KEY"` with the Pooltune model provider configuration in the worker environment. One command checks at most one queued job. It uses the existing state database and publishes a saved diagnosis, an evidence-unavailable outcome, or JSON null when no eligible job is ready. State retains the scoped measurements, LangGraph checkpoint and result. The project owner grants the metadata disclosure to Sonnet through the lookup opt-in.
4. Revoke a lookup grant by deleting that row from the private `lookup_grants` table. Removing the monitored statement also removes its grant. The consumer checks the live grant, objective, counter generation, connection epoch and job lease before a model call and verifies again before saving the diagnosis. Revocation stops subsequent calls. A request the provider already received can finish. The command withholds the diagnosis and its graph link if it observes revocation during the call. Pooltune’s internal checkpoint can retain evidence and model output from an already-admitted request.

The lookup branch accepts the fixed schema-qualified SQL spelling, with one integer equality predicate and no extra clauses. PostgreSQL’s normalized text checks the statement spelling. It does not prove the resolved operator or parameter type. Query plans, actual scan counts and experiment authorization remain outside this evidence contract. A missing grant, changed table shape or fresh counter generation requires new eligible evidence.

[PostgreSQL query identity and normalized text](https://www.postgresql.org/docs/17/pgstatstatements.html)

## Rookery qualification

On November 14, 2026, the application test role executed an actual enrolled integer lookup against an owned, synthetic 100,000-row table on live Rookery. Go detected five windows around 8.7–8.9 ms against a 0.1 ms objective. Each measured call touched 443 shared pages, matching the table’s 443 estimated pages. The catalog reader found no matching leading B-tree. The operator command opened the real queue job, read evidence over direct TLS, called `claude-sonnet-5-5` once through the existing durable graph, and saved the diagnosis. Sonnet named a costly scan as the leading hypothesis and called out the missing measured plan and uncertainty about stale statistics.

Final run timing was 3.723 s for Sonnet and 4.152 s for the claimed investigation through final checks, excluding detector collection and fixture setup. The exact statement counter stayed at seven throughout diagnosis. The existing repair consumer could not claim this job and the saved diagnosis had no repair proposal. A separate readback returned zero owned test schemas, roles and monitor sessions.

Race-enabled PostgreSQL tests covered an unauthorized grant attempt by the monitor, refusal for a different query shape, missing consent, revocation during the model call, and inherited sequence privileges. The two review findings were reproduced and fixed. The final source rereview, Go race tests and vet, affected PostgreSQL/graph tests, and all 95 TypeScript tests and type checking passed.

This test used the operator command with live Supabase and a disposable local state database. Hosted deployment, additional query shapes, live plans, broader diagnosis quality and repair qualification remain open.
