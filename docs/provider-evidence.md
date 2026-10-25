# Provider adapter evidence

October 24, 2026. The Go adapters are development components, not live-verified deployments. No cloud mutation or credential lookup was performed in this implementation run.

## Existing measurements inspected

Read the original Python Azure, Google Cloud and Supabase adapter code, detector/observer code, the result files below and corresponding JSONL events. Overall run status matters; individual successful repetitions do not make a failed suite successful.

- `azure-cold-20261024-012501-166a8483.json`: SHA-256 `e3458fd9059d5b4c77541cd5589d53bbd49e9e847b3854e872b1af5c4a864ffe`, overall `failed`, 2 completed result records.
- `google-cloud-cold-20261024-012502-9124e438.json`: SHA-256 `53e6416f9e574cd93d3f711c2784a8590c96c3238260e098f72a643e5a36922a`, overall `failed`, 4 completed result records.
- `supabase-warm-20261024-012502.json`: SHA-256 `57efdd6cb4aa1205eeb9adf3c6e44d1eeb6bb68d0950976f595ce6b04ef0efd6`, overall `failed`, 11 completed result records.

Azure's final cold run contains two successful repetitions then a URLError. Google contains four successful repetitions, cleanup warnings, then a failed run with 6,436 failures. Supabase's final warm run includes retained capacity at 0/60 seconds, lost retention at 180/270 seconds, then AdminShutdown. These are limited historical observations, not evidence that this Go ingress works with those services. Detector timestamps and throughput confirmation use different semantics, so this document makes no cross-provider speed ranking.

The logs record API response timings, backend counts and throughput, but do not contain complete HTTP management responses. Tests therefore use explicitly constructed API-contract fixtures based on the probe code and official documentation. They are not labeled captured provider responses. Capturing sanitized real API responses and staging before/after/readiness/restoration evidence remains G3 work.

## Implemented profile

- Azure reads default/min PgBouncer pool settings and PATCHes only one supported parameter. It requires a scoped asynchronous operation URL and separately confirms working capacity. No verified conditional-update contract was found for this endpoint, so writes require an explicitly verified single writer and a fresh configuration fingerprint. That does not protect against an out-of-band administrator racing the write.
- Google requires Enterprise Plus, pooling enabled, and explicit max/min flags in this first profile. PATCH preserves the complete existing flag list and uses settingsVersion. Missing defaults are discovery-incomplete; onboarding must establish a tested explicit configuration. Operation DONE is separate from capacity readiness.
- Supabase reads the transaction pool configuration and independently observes warm capacity. Cold configuration mutation is disabled in this package. Warm observations expire and retain their exact pool/role/replica scope. A REST permit is never equated with a Supavisor connection.

Every writer needs a verified scope inventory and a fixed physical allocation that already excludes REST allocation, unrouted consumers and reserved headroom. A changed pool value multiplied by the number of affected scopes must fit that allocation. Unknown inventories block writes. Ready capacity is measured for each scope against the configuration fingerprint; one pool cannot substitute for another. The runtime readiness observer and actual inventory discovery are later integration work, not simulated proof.

A durable journal must save intent before an API call and retain its asynchronous identity afterward. On restart the adapter loads incomplete operations and blocks compounding writes. Missing acknowledgements, failed operations and storage failures remain conservative; they require operator/control reconciliation, not an automatic replay. The control SQLite store now implements this journal with append-only provider lifecycle records. Adapter unit tests use a labeled memory journal; the main CLI has not enabled cloud operations.

Management clients use fixed provider origins, explicit timeouts, bounded bodies, no redirects, no environment proxy and generic errors that omit response bodies and credentials. Tokens come from a control-plane callback and are never persisted in operation state. No API runs inside request admission.

## Sources

[Azure configuration update](https://learn.microsoft.com/en-us/rest/api/postgresql/configurations/update?view=rest-postgresql-2025-08-01), [Cloud SQL managed pooling](https://docs.cloud.google.com/sql/docs/postgres/configure-mcp), and [Supabase pooler update](https://supabase.com/docs/reference/api/v1-update-pooler-config), checked October 24, 2026.

`go test -race ./internal/provider` verifies setting preservation, async completion versus readiness, scope separation, missing acknowledgement/restart fences, storage failure and error redaction. It makes no cloud requests.
