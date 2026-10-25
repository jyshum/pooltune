# Docker deployment: first learning increment

User intent: begin shipping Docker packaging and learn the deployment mechanics. Existing native demo, website and original dirty checkout must stay intact.

## Approach

Start by packaging the actual Go managed gateway, PgBouncer 1.26.1 and built private dashboard into a non-root Linux image. A separate verification target runs the existing real PostgreSQL/PgBouncer integration test in a network-isolated container. It is a packaging check, not a new managed-provider benchmark.

Compose application placement is awaiting the user's choice. Recommended next increment: application alongside Pooltune in Compose, initially sharing its network namespace to retain loopback restrictions. A normal bridge-network client would require an explicit, tested listener change. Host-to-container dashboard access also needs deliberate origin, authentication and network handling; no public bind workaround is approved or implemented.

User selected Docker CLI plus Colima. Installed and running in dedicated profile pooltune; no default context change. Initial ARM64 packaging verification is complete; application topology remains open.

## Required boundaries

- No tuner/controller algorithm changes, cloud starts, provider mutations or website changes.
- Runtime image excludes cloud credentials, source/test fixtures and development dependencies. Deny-by-default build context. Secrets are mounted at runtime.
- Persist state. Share the fixed ownership-lock directory among instances using one Docker engine. This is not distributed fencing.
- No automatic restart after uncertain shutdown. Preserve existing conservative reconciliation behavior.
- Do not call an image verified until actually built and exercised.

## First completion evidence

Build both image targets; verify non-root execution and expected PgBouncer/binary versions; run the real owned-pool integration test with no network egress. That test covers transaction preservation during admission growth, ownership exclusion, clean restart; container crash/restart remains a later check. Later test application/network and dashboard integration separately before calling Docker a complete deployment method.
