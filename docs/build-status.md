# Build status

Updated October 24, 2026.

## Implemented: B01 foundation

- One Go module with shared domain types.
- Bounded configuration loading, strict field names, duplicate-key rejection, ownership declarations, separate PostgreSQL/REST units and safety/profile validation.
- Separate customer configuration and owned-gateway provider-key configuration.
- `traffic`, `control` and `gateway` command modes. They expose loopback health checks only, always report unready and perform no traffic forwarding, provider operations or model calls.

The configuration examples in the README are synthetic. Their numbers are not production defaults, calibrated thresholds, or an approval to automate.

## One repository, separate processes

The customer-local traffic and control processes share this Go codebase. The owned managed-AI gateway can use the same repository while deploying separately under Pooltune's control. Provider API keys stay on that gateway; customer installations receive revocable installation tokens. Dashboard and SDK source will also live here. A separate deployment does not require a separate source repository.

## Remaining build order

| Task | Scope | Status |
| --- | --- | --- |
| B01 | Domain, configuration and bootable modes | Implemented; see verification below |
| B02 | Durable history and secret files | Not started |
| B03 | Policy compiler and deterministic classifier | Not started |
| B04 | Bounded scheduler and permit accounting | Not started |
| B05 | Telemetry and capacity equations | Not started |
| B06 | PostgreSQL protocol path | Not started |
| B07 | REST ingress and selective browser SDK | Not started |
| B08 | Provider adapters and reconciliation | Not started |
| B09 | Revision activation and restart recovery | Not started |
| B10 | Owned AI gateway and spending enforcement | Not started |
| B11 | Bounded agent tool loop | Not started |
| B12 | Real canary, evaluation and rollback | Not started |
| B13 | Local administration and onboarding UI | Not started |
| B14 | Docker packaging and operations | Not started |
| B15 | Compatibility and performance evidence | Not started |
| B16 | Demonstration and replay | Not started |

## Verification

B01 was tested on macOS arm64 with Go 1.27.3:

```sh
go test ./internal/config ./cmd/pooltune
go test ./...
go test -race ./...
go vet ./...
go build -o bin/pooltune ./cmd/pooltune
```

Real-process checks covered all three modes: health returned 200, readiness returned 503, unrelated routes returned 404 and SIGTERM exited successfully. Wildcard, hostname and nonloopback bind addresses were rejected. These checks prove foundation behavior only.

## Release gates still open

Recovery and uncertain backend work; administration/public ingress security; provider, protocol and browser compatibility; calibrated evaluation and performance targets; and funded-AI authorization, quotas and data handling all remain unverified. Later tasks must satisfy their affected gates before enabling those capabilities. No customer-release readiness or latency claim follows from B01.
