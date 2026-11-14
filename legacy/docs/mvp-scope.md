# Narrowed MVP scope

Customer direction, updated October 25, 2026. This supersedes the older requirement to
include business-priority scheduling and managed AI in the first release.
It records product scope, not a claim that the runtime has already been changed.

## Product purpose

Automatically adjust a supported pooler's connection limits within customer-set
bounds as demand changes. Keep enough usable connections for the observed work,
reduce unused connections during quiet periods, and avoid increasing concurrency
when the database is already under pressure. Opening connections does not add
database processing power or promise lower hosting bills.

## In the MVP

- Verified pool/database observations and deterministic capacity calculations.
- Customer-approved minimum, maximum and safety thresholds.
- Bounded growth, quiet reclamation, conservative handling of incomplete changes,
  freeze/manual override, recovery and a clear status/history view.
- Verified managed tuning for ALL three mandatory targets: Azure PostgreSQL
  managed PgBouncer, Google Cloud SQL managed pooling, and Supabase Supavisor.
  Local PgBouncer is a development fixture only. A local-only tuner does not
  meet the MVP; discovery or successful database login does not prove tuning.
- Real comparison against a well-configured fixed warm pool, using identical
  work and hard database connection budgets. Include steady demand, bursts,
  quiet periods and sustained overload. Measure timeouts, completed work,
  response times, actual connection occupancy and host/database pressure.

## Deferred from the MVP

Business-priority classification and custom priority scheduling, AI discovery and
policy agents, managed inference distribution and sponsor integrations. Preserve
source/history as deferred work rather than deleting reusable control/recovery
components indiscriminately. Retain the existing persistent $5 AI accounting
ledger and its recorded $0.005854 spending. Make no further paid calls for this
narrowed MVP without a concrete newly authorized need.

Removing business priority does not remove connection ceilings, pressure checks,
request deadlines or the underlying pooler's ordinary waiting behavior.

## Evidence and unanswered product questions

The prior warm-pool comparison held physical capacity fixed. It demonstrated
priority tradeoffs and similar results from application-side priority; it did not
prove priority universally useless, and did not test automatic resizing benefit.
Local physical growth and reclamation now pass correctness tests, but those
changes remain uncommitted and need independent review. Feasibility is not
performance benefit. Compare the deterministic tuner with a strong fixed-pool
baseline before claiming faster requests, fewer timeouts or resource savings.
Report actual costs only if billing evidence exists.

Preferred architecture to investigate: a background tuner adjusting a separately
running existing pooler, preserving the normal application-to-pooler-to-database
route. This is an engineering proposal, not verified current behavior or approval
of a particular provider integration. Verify the necessary measurements and
management interface before replacing current runtime wiring. A tuner crash
should leave the pooler running at its last safe setting; that requires a real
process-kill test. Existing proxy backup routing remains an open gap while that
proxy is part of the deployment. A separate controller can reduce added traffic
path risk, but does not make the pooler itself highly available.

The highest implementation priority is actual managed integration for all three
providers, targeted fixed-versus-auto comparisons, safe tuner failure and
independent recovery. Dashboard, website, demo and packaging polish are paused
until managed feasibility and usefulness are established. Avoid another broad
benchmark; report provider-specific evidence and unresolved gates honestly. Rewrite affected
release/packaging/onboarding requirements around this narrower product before
claiming the original broad plan complete. Additional features can be considered
after the core demonstrates a useful measured benefit.
