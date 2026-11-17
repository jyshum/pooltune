# Repair availability copy review

Reviewed exact range `2b8154b951464854920133e5b46602be5d4279d4..08d6fd3b0499d31478298a51c3e5583ad712a3aa`.

**Verdict: no findings.** The three revised messages match the existing managed repair wiring and preserve readiness limits. The diff changes strings and expected test wording only, with no authorization, enrollment, dispatch or repair behavior changes.

- `internal/hostedapp/views.go:225`: vacuum copy now acknowledges cleanup-setting checks. This matches `managed_project_index.go:70` dispatching `runCleanupSettings`, whose capability, lookup shape, settings and measured-evidence gates remain active. “A repair still needs measured evidence” avoids promising immediate changes.
- `internal/hostedapp/views.go:232`: memory copy requires a chosen function, memory/concurrency limits and measured disk spill. The supervisor still builds exact memory enrollments and supplies memory-headroom observations; `runMemory` still rejects absent enrollment, headroom or measurement windows and checks the selected function. The message does not claim arbitrary functions are eligible.
- `internal/hostedapp/views.go:234`: cron copy requires a chosen job, permitted schedule window and measured job history. The supervisor still constructs exact job enrollments with windows, and managed cron uses those windows and measured periods. Selection alone remains insufficient.
- Existing enrolled-target messaging still says automatic repair waits for supported measurements and safety limits. Saved repair choices remain selected.

Independent focused check passed: `go test ./internal/hostedapp -run TestRepairReadiness -count=1`, 0.535s. Log: `/tmp/pooltune-supabase-copy-review-test.log`. It ran in the prior exact-base temporary archive with the two changed files read directly from the reviewed commit. The checkout was unchanged.

The parent reports live onboarding, strict provider/native setup and advancing monitoring checks succeeded on 2b8154b. This focused review does not independently establish those external observations or any repair success. No query-window, investigation or six-family repair success is inferred from the new copy. No external operations or customer-state changes were performed.
