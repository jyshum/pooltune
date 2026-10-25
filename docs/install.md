# Local development installation

The native package contains the managed burst gateway, its private service UI,
and the earlier local PostgreSQL/REST profiles. Start with
[the managed gateway guide](burst-gateway.md) for Azure, Google Cloud SQL or
Supabase. [Measured results](managed-mvp-results.md) describe the three successful
cloud spike tests and their limits. The downloaded build is for macOS Apple Silicon;
PgBouncer must be installed separately. It is a development deployment, not a
production-certified hosted service.

## Build a native package

Use the Go version in `go.mod`, Node/npm compatible with the lockfiles, Python 3,
and Git. From the application checkout:

```sh
./scripts/package-local.sh /absolute/new/pooltune-package
```

The destination and adjacent `.tar.gz` must not exist. The script builds the five
customer binaries, UI and SDK from the locked sources, copies an explicit list of
operation documents, and records `RELEASE.json` plus `SHA256SUMS`. Metadata states
the commit, dirty status, tool versions, native architecture and unverified gates.
It never archives the checkout, secret/state directories, environment files,
untracked files, hosted account tooling or development dependencies. Vite public
directory copying is disabled because there are no approved public-directory
assets; nested public-file sentinels are tested as well as checkout-root files. An explicit
payload test plants an untracked sentinel and verifies that it is absent.

Extract into a private new directory and verify every file against `SHA256SUMS`
using `sha256sum -c SHA256SUMS` or `shasum -a 256 -c SHA256SUMS` on macOS. These hashes
check integrity; release signing and reproducible container images remain pending.
Do not move an existing installation's state into the package or overwrite it.

## Configure the isolated local services

Create separate private directories for traffic state, control state and secrets.
Choose either `local-runtime.md` for PostgreSQL or `rest-runtime.md` for REST.
The REST profile also needs PostgREST with libpq. Those dependencies are installed
separately; they are not implicitly downloaded by the runtime or bundled here.

Supply the target endpoint, owner-approved bounds, initial registry/policy, TLS
certificate/private key, trusted PostgreSQL CA and private credential references.
No inline passwords or provider keys belong in JSON. Monitor visibility, fixed
allocations, exclusive ownership and absence of unrelated work are checked at
startup. These local profiles require literal loopback addresses. Public DNS,
customer-managed HTTPS deployment and hosted-provider profiles remain unverified.

Start traffic with the chosen explicit profile and its loopback health listener.
Then start control using `control-runtime.md`, its own health listener, private
control state, and the traffic Unix socket. Configure `Admin.Assets` to the
package's `ui` directory. Use the one-time private claim token to establish the
owner password. Keep admin loopback and separate from traffic. Restrict REST CORS
to exact approved HTTPS origins; the SDK sends only REST traffic to Pooltune and
leaves authentication/storage/realtime routes at their configured provider.

A fresh command without an explicit runtime profile is deliberately health-only:
`/healthz` is alive, `/readyz` is unavailable, and admin/admission routes are absent.
Passing a generic validation-only config does not start a runtime.

Preserve all state during updates. Control-only restart is tested. Independently
anchored local PG/REST restart is also tested, with actual backend checks and
separate confirmed control history. Configure the anchor at initial installation
and follow recovery.md. Unanchored restart, uncertain lineage and correlated
restore remain quarantined; do not erase state to bypass these checks.
Use `owner-recovery.md` for local password or unclaimed bootstrap recovery, not
state deletion. The separate persistent AI spending ledger must also be retained.

## Verification boundary

`go test -tags=deployment ./tests -run TestNativePackage -v -count=1` builds and
executes the native artifact, checks its manifest/allowlist, and verifies private
unconfigured startup and shutdown. Real configured traffic tests require local
PostgreSQL, plus PostgREST for REST. A missing dependency is a skip, not a pass.

Docker/Colima/Podman are unavailable on the current development host. No container
image, image-layer secret scan, Compose deployment or container restart result is
claimed. Native packaging advances B14 but does not complete its container gates.
