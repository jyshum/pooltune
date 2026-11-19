# Pooltune

<a href="https://pooltune.up.railway.app/#how"><img src="docs/images/pooltune-preview.jpg" alt="Pooltune’s live preview with its animated database workload" width="100%"></a>

<p align="center">
  <a href="https://go.dev/"><img alt="Worker: Go" src="https://img.shields.io/badge/Worker-Go-454d54?style=flat-square"></a>
  <a href="https://www.typescriptlang.org/"><img alt="Investigator: TypeScript" src="https://img.shields.io/badge/Investigator-TypeScript-454d54?style=flat-square"></a>
  <a href="https://docs.langchain.com/oss/javascript/langgraph/overview"><img alt="Workflow: LangGraph" src="https://img.shields.io/badge/Workflow-LangGraph-454d54?style=flat-square"></a>
  <a href="https://www.postgresql.org/"><img alt="Database: PostgreSQL" src="https://img.shields.io/badge/Database-PostgreSQL-454d54?style=flat-square"></a>
  <a href="https://supabase.com/"><img alt="Integration: Supabase" src="https://img.shields.io/badge/Integration-Supabase-454d54?style=flat-square"></a>
  <a href="https://railway.com/"><img alt="Hosting: Railway" src="https://img.shields.io/badge/Hosting-Railway-454d54?style=flat-square"></a>
</p>

<p align="center"><a href="https://pooltune.up.railway.app/#how"><b>Try the interactive walkthrough</b></a> · <a href="https://pooltune.up.railway.app/signin">Open workspace</a></p>

Pooltune helps developers find out why a PostgreSQL query is slow and whether a repair actually helped. Connect a Supabase project, choose which repairs the agent may run, or leave them all off to monitor only. The walkthrough is public. Connecting your own database requires a preview invite.

## One repair, measured

A lookup filtered by both customer and category. PostgreSQL touched **866 blocks** to find the matching rows. With index creation enabled, Pooltune built an index on those two columns while writes continued, then measured the same input again.

| Controlled PostgreSQL test | Before index | After index |
| --- | ---: | ---: |
| Block accesses | 866 | 4 |
| Execution time for one input | 16.605 ms | 0.070 ms |

In the workspace you can inspect the measurements, pause the agent, and revert an eligible index from its change history. [Recorded trial](docs/design/hosted-query-agent/evidence/mvp-simple-index-railway.json)

## Under the hood

Go serves the workspace, samples PostgreSQL statistics, and runs permitted repairs. A TypeScript investigator uses LangGraph and Claude to diagnose eligible queries. PostgreSQL records the observations and repair history. [Engineering overview](docs/design/hosted-query-agent/overview.md) · [Original connection-pool gateway](legacy/README.md)

<details>
<summary>Run the tests</summary>

```sh
go test ./...
npm --prefix investigator ci
npm --prefix investigator test
```

With PostgreSQL 17 binaries on your PATH, run the database integration tests with `go test -tags=integration ./internal/hostedquery ./cmd/pooltune-query-lab -count=1`.

</details>
