# Docker publication and architecture

> Execute inline with Superpowers. User authorized publishing the current Docker package and updating repo/homepage while preserving the live judging demo.

## Scope and decisions
Publish the existing verified Linux ARM64 learning package as source. It is not yet a full managed Compose deployment. Keep the native release and active fixture unchanged. No backend runtime edits, fixture restart, credentials, cloud tests or registry publication. Update the old architecture illustration to the current deterministic gateway, owned PgBouncer, provider checks, SQLite, private UI and optional Tiger Data history. Mana remains a demo tool outside the request path. Correct only factual homepage statements that conflict with that architecture, retaining layout, styling and animation.

## Execution
- [x] Add one accessible architecture SVG shared by README and website; document native/Docker boundaries.
- [x] Add Docker source/build/verification instructions to setup and link from Get started. Keep native path intact.
- [x] Build website; inspect desktop setup/diagram and copy feedback. Source review caught mobile diagram scaling; added readable route summary and full-size link. No dedicated mobile viewport test in this turn. Docker SQL test passed 3.36 seconds; native PID64015 and HTTP200 unchanged.
- [ ] Fresh whole-change review. Commit, fast-forward main, deploy explicit static output only, verify production and demo.

## Review focus
No implied AI/priority, REST gateway, native managed resize, complete Docker deployment, distributed ownership or cost savings. No changes to native demo or private files. No broken installation commands or unreadable diagram on mobile. No secret-bearing upload directory.
