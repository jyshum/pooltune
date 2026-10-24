# Pooltune

Pooltune is under construction. B01 provides typed configuration and a local, health-only executable. It does not forward traffic, control capacity, contact a model, or admit requests.

## Run

Use Go 1.27.3 or the local installed binary:

```sh
GO=/Users/jshum/Documents/Codex/2026-10-24/work-from-this-folder-users-jshum/work/toolchains/go1.27.3/go/bin/go
$GO test ./...
$GO run ./cmd/pooltune traffic -listen 127.0.0.1:8080
$GO run ./cmd/pooltune control -listen 127.0.0.1:8081 -config /absolute/path/config.json
$GO run ./cmd/pooltune gateway -listen '[::1]:8082'
```

Each mode serves only `GET /healthz` (200, process alive) and `GET /readyz` (503, unavailable). All other paths, including admission and administration, return 404. Bind addresses must be literal loopback IPs. Port 0 is available for tests. Supplying a valid config does not make a mode ready. Missing config is allowed for health checks. A supplied invalid config stops startup. No startup log is emitted. Errors go to stderr.

## Config

JSON is limited to 1 MiB. Fields must use the exact names shown below. Unknown fields, duplicate keys, trailing JSON, unsupported versions and invalid bounds fail startup. Durations are strings understood by Go `time.ParseDuration`, such as `"10s"` and `"5m"`. Numbers without units are rejected for durations. There are no calibrated production defaults.

This example is **synthetic test data**, not approved production limits:

```json
{
  "schema_version": "v1",
  "automation_enabled": true,
  "targets": [{
    "id": "db1", "owner": "node1",
    "postgres": {
      "admission_floor_transactions": 1,
      "admission_start_transactions": 2,
      "admission_ceiling_transactions": 4,
      "physical_ceiling_connections": 8,
      "critical_reserve_transactions": 1
    },
    "rest": {
      "max_in_flight_requests": 4,
      "physical_allocation_connections": 2,
      "critical_reserve_requests": 1
    },
    "guardrails": {
      "version": "v1",
      "cpu": {"grow": 0.5, "backoff": 0.7, "stop": 0.9},
      "memory": {"grow": 0.5, "backoff": 0.7, "stop": 0.9},
      "metric_max_age": {"cpu": "10s", "memory": "10s", "capacity": "10s", "connections": "10s"},
      "max_occupancy": {"critical": "2s", "normal": "3s", "background": "4s"}
    }
  }],
  "evaluation_profile": {
    "version": "v1", "min_window": "30s", "max_window": "5m",
    "min_samples_per_workload": 2000,
    "max_failure_rate_increase": 0.01, "max_latency_ratio": 1.05
  },
  "model_profile": {"gateway_profile_id": "approved-test-profile"},
  "secret_refs": {
    "postgres_credential_file": "/secrets/pg",
    "installation_token_file": "/secrets/installation-token"
  }
}
```

PostgreSQL admission numbers count transactions; its physical ceiling counts connections. REST concurrency counts HTTP requests; its fixed physical allocation counts connections. The two sections are distinct. CPU and memory thresholds are fractions. The program checks declared target IDs and owners, but it does not establish distributed ownership. Secret references are absolute file paths, not secret values. B01 does not open those files.

Traffic and control use customer config. The owned gateway uses a separate shape and cannot load customer settings:

```json
{"schema_version":"v1","gateway":{"provider":"anthropic","model":"test-model","provider_api_key_file":"/secrets/provider-key"}}
```

The provider key reference is accepted only in gateway mode. Customer config can name an approved gateway profile and reference an installation token. Neither reference is read by B01.

When `automation_enabled` is false, the evaluation and model profiles and secret references may be omitted. A supplied evaluation profile must still have valid version and bounds. Safety limits and ownership remain required for any supplied config. This flag cannot start automation in B01. No model or cloud calls occur.

Release gates G1 through G5 remain open. This foundation has no verified provider, driver, browser, recovery, performance, funded AI, or promotion capability. The authoritative design and build plan are in the parent `docs/superpowers` folder.
