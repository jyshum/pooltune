# Pooltune

Pooltune's hosted query agent measures database performance, investigates slow queries, and tests whether a permitted repair actually helps.

- [How Pooltune works](docs/design/hosted-query-agent/overview.md) explains the problem, the stack, the main tests, and the tradeoffs in plain English.
- [Engineering walkthrough](docs/design/hosted-query-agent/index-slice.md) records the index trial, exact measurements, and how to reproduce it.
- [Implementation plan](docs/design/hosted-query-agent/implementation.md) tracks the build and review milestones.

| Folder | What to look for |
| --- | --- |
| `cmd/pooltune-query-lab/` | Entry point for the database trials |
| `internal/hostedquery/` | Query measurement, repair decisions, verification, and recovery |
| `investigator/` | The TypeScript and LangGraph investigation workflow |
| [`legacy/`](legacy/README.md) | Original traffic-control product, dashboard, and website |

From the repository root, `go test ./...` checks the hosted code. With PostgreSQL 17's binaries on your PATH, run `go test -tags=integration ./internal/hostedquery ./cmd/pooltune-query-lab -count=1` for the database tests. Run `npm --prefix investigator ci` and `npm --prefix investigator test` for the investigator. The original app has its own Go and Node setup inside `legacy/`.
