# Traffic history with Tiger Data

History is optional. It stores transaction throughput and pool counts in Tiger Data, independently of the tuner. The private dashboard's **View history** button shows the last hour in one-minute buckets. Existing live metrics keep working without it.

## Enable

Create a Tiger Data development service with TimescaleDB enabled. Save its PostgreSQL connection URL in a private file outside the package and repository. Do not paste the URL into the dashboard, a command argument, or a public demo.

```sh
chmod 600 /absolute/private/tigerdata-url.txt
export POOLTUNE_HISTORY_URL_FILE=/absolute/private/tigerdata-url.txt
export POOLTUNE_HISTORY_STREAM=my-installation
```

Then start `pooltune-burst` or `pooltune-tuner` with your existing configuration. Use a stable, unique stream name for each installation: 1 to 64 letters, numbers, underscores or hyphens. The application appends `-managed` or `-local` so the two dashboard types remain separate. Reusing a stream name on multiple installations combines their readings, so avoid that.

The URL file must have no group or other permissions and contain one `postgres://` or `postgresql://` URL. Blank lines and comments beginning with `#` are allowed. TLS certificate and hostname verification are always required, even if the URL says `sslmode=require`. Plaintext connections are rejected.

If the macOS certificate verifier rejects the service certificate, use a trusted PEM CA bundle with full verification still enabled:

```sh
export POOLTUNE_HISTORY_CA_FILE=/etc/ssl/cert.pem
```

This path was verified on the development Mac. Other hosts should use their own trusted CA bundle. Do not disable certificate verification.

## What is saved

The exporter reads the existing dashboard state once per second. Managed Azure PostgreSQL, Google Cloud SQL and Supabase gateways use the same existing owned-pool transaction counter. Local PgBouncer uses its existing transaction counter. This feature does not poll a new cloud API, resize a pool, or change admission decisions.

Only timestamp, provider category, opaque counter epoch, exact cumulative transaction count, derived rate, active/queued counts, admission limit, warm count and the simulated-data flag are exported. SQL text, connection URLs, credentials, target names and diagnostic reasons are excluded.

The dedicated `pooltune_history.samples` Timescale hypertable is initialized automatically. The database role needs permission to create this schema/table and invoke `create_hypertable`, then read and write its rows. Use a database dedicated to this optional history feature.

Average tx/s is total measured transaction delta divided by the measured duration. Peak tx/s is the highest individual sampled rate in the minute. Capacity columns show each minute's maximum. The first reading, counter resets, epoch changes and gaps over three seconds do not produce a rate. No unavailable interval is filled with invented data.

## Failure and retention

History uses its own connection and worker with two-second network deadlines. Storage outages leave the tuner running, show an unavailable status, and retry after ten seconds. Unavailable intervals are dropped, so this is useful operational history rather than a lossless audit log.

The authenticated `/api/telemetry` endpoint reads only the installation's stream and last hour. It uses a separate bounded connection so dashboard reads do not block the writer. Raw rows remain stored until the database owner configures retention or removes them. There is no automatic deletion policy in this MVP.

Unsetting `POOLTUNE_HISTORY_URL_FILE` and restarting disables export. Existing history stays in Tiger Data. Configuration errors are intentionally generic and never return the URL.

## Verification

October 25, 2026: a live Tiger Data connection verified Timescale hypertable creation, idempotent inserts, one-minute aggregation, persistence through a new connection and isolation between streams. Test rows covered all four provider labels and were explicitly marked simulated. Those disposable integration-test rows were removed after verification.

The private managed and local browser previews use simulated transaction traffic. Saving that traffic in a real Tiger Data service does not turn it into live Azure, Google or Supabase performance evidence. The separate recorded provider trials remain documented in `managed-mvp-results.md`.
