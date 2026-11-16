# Managed memory metrics access

Status: approved on November 15, 2026. Jared selected **“Keep one encrypted project token for simpler setup”** in response to the explicit choice between retaining the project-scoped setup token with Analytics Read and using a separate read-only token. This supersedes discarding the installation token for the managed setup. No project API secret is collected.

## Verified mechanism

Supabase exposes actual database available/total memory through its metrics feed. The Management API `GET /v1/projects/{ref}/analytics/endpoints/metrics` requires `analytics_logs_read`. The existing database password cannot authenticate that HTTP endpoint. A Database Read-write-only setup token lacks this permission. Reading project secret API keys instead requires `api_gateway_keys_read` and `api_gateway_keys_secret_read` and would collect a broader credential unnecessarily.

Sources: https://supabase.com/docs/reference/api/v1-scrape-project-metrics and https://supabase.com/docs/reference/api/v1-get-project-api-keys . Existing customer plan access stores a separate encrypted `LogsToken`; this can supply metrics without adding a new credential type.

## Proposed automatic setup option

When function memory is selected, request Database Read-write plus Analytics Read on one project-scoped setup token. Disclose that Pooltune retains this token encrypted in the account/project-bound vault for ongoing read-only metrics requests; it keeps the token's underlying database setup permissions as well. No additional API key is collected. Stop granting extra memory if the token expires/revokes, metrics are missing/stale, or memory headroom is insufficient. Pause/disconnect invalidate allowances and disconnect removes the stored token. Do not invoke Management API mutation endpoints after setup through this credential.

This keeps one project token entry but changes the earlier promise to discard the setup token. Jared approved this change; setup must disclose retention and the underlying Database Write permission.

## Alternative

Discard the setup token as before. Request one additional, project-scoped Analytics Read-only token for runtime monitoring, stored in the existing LogsToken field. This limits retained API permissions but adds a one-time credential entry. If the owner already connected a valid LogsToken, reuse it without another request. Do not silently fetch a service-role API secret instead.
