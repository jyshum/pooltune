# Transaction throughput

Both dashboard modes display measured pooled transactions per second. Azure, Google Cloud and Supabase use the existing owned PgBouncer in the managed gateway. The local tuning mode reads its configured local PgBouncer. No provider-native resize or delayed cloud CPU metric is used to infer traffic volume.

| Mode | Counter scope |
| --- | --- |
| Azure | Exact database in the owned gateway pooler forwarding to Azure PostgreSQL |
| Google Cloud | Exact database in the owned gateway pooler forwarding to Cloud SQL |
| Supabase | Exact database in the owned gateway pooler forwarding to the stable Supabase session endpoint |
| Local pool tuning | Exact database in the configured PgBouncer |

The counter is `SHOW STATS.total_xact_count`. It counts pooled SQL transactions, including rollbacks and any internal work through that pooler. It does not count HTTP requests, individual SQL statements, or traffic bypassing the gateway. Reference: https://www.pgbouncer.org/usage.html#show-stats

Rates use cumulative counter differences divided by actual elapsed source seconds, with exact uint64 decimal strings in JSON. The first reading establishes a baseline. Process epoch changes, counter decreases, backward timestamps and gaps over three seconds rebaseline. Missing or stale readings show unavailable. Optional statistics queries use separate connections with a 300ms timeout, separate from safety readback. Managed polling runs independently once per second. No admission/controller decision uses this telemetry.

The browser keeps 60 seconds of history while the view is open. Grey pixels show normal measured volume. Cyan indicates more than 1.5 times the previous ten-reading mean after five readings. Each pixel column uses the peak observed rate in its time bucket on narrow screens. This is a relative traffic indicator, not a capacity alarm or an SLO. Zero is a measured zero, not missing data. There is no decorative animation or inferred historical traffic.

Validation: exact database scoping and uint64 parsing; real PostgreSQL/PgBouncer counted five standalone transactions and one explicit multi-query transaction as six; managed gateway counter propagation and clean-restart epoch verified locally; Azure/Google/Supabase dashboard mappings verified; cancelled optional polling leaves control connection usable. Browser tests cover both dashboards, stale state and mobile overflow. No new cloud benchmark was run for this telemetry addition.

Ports12164 and12165 are controlled browser previews. They carry `demo:true` and display Simulated traffic. Production readers never set this flag. The previews are not live cloud telemetry.
