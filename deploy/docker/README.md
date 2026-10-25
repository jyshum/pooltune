# Docker learning package

The first Linux ARM64 package is built and locally verified. This is not yet a complete managed-provider Compose deployment. The existing native demo remains unchanged.

## What each image does

- `pooltune:learning`: non-root Go gateway and owner CLI, PgBouncer 1.26.1, private dashboard assets and CA trust bundle. No test database or credentials.
- Compose `verify` target: disposable PostgreSQL 17.11 plus an integration-test executable. It is a verification image, not the application runtime.

## Docker CLI + Colima on macOS

Install the CLI tools, then start a dedicated ARM64 VM. On Intel Macs the AMD64 image path remains unverified.

```sh
brew install docker docker-compose docker-buildx colima
```

Follow Homebrew's printed caveats to add its Docker plugin directory to `cliPluginsExtraDirs` in `~/.docker/config.json`. On Apple Silicon that directory is usually `/opt/homebrew/lib/docker/cli-plugins`. Merge this key into your existing configuration; do not overwrite saved authentication or other settings. Confirm both `docker compose version` and `docker buildx version` work.

```sh
colima start pooltune --runtime docker --vm-type vz --cpu 2 --memory 3 --disk 25 --activate=false
export DOCKER_CONTEXT=colima-pooltune
```

This selects the dedicated engine for the current shell. The VM consumes local CPU, memory and disk. Review Colima's host mounts before making private files accessible to containers. The author's verification VM mounted only the source checkout; cloud credentials were not mounted.

## Build and verify

From the repository root, using your selected Docker engine:

```sh
# Build the actual runtime image.
docker build --target runtime -t pooltune:learning .

# Run real SQL integration in an isolated container with no network egress.
docker compose -f compose.verify.yaml run --build --rm verify

# Check the public command interface without credentials or a database.
docker run --rm --network none pooltune:learning -help

# Optional: check actual build-context filtering (requires Python 3).
python3 deploy/docker/check-context.py
```

Builds download base images and dependencies. The verification container itself has no network egress. It creates a disposable local PostgreSQL database, not an Azure, Google or Supabase database. It does not start a managed service or touch an existing native process.

Stop/start the dedicated VM when needed:

```sh
colima stop pooltune
colima start pooltune --activate=false
```

Stopping Colima does not stop a separate native dashboard. On another Docker installation, select its context instead. Do not stop the VM while an application depends on containers inside it.

## Verified October 25, 2026

- Runtime and verification images build for Linux ARM64.
- Real PostgreSQL/PgBouncer test passes in 3.35 seconds: actual SQL, active-transaction preservation while admission expands, ownership exclusion and clean in-process gateway restart.
- Test runner rejects missing dependencies, skipped tests, absent exact PASS output, failures and timeout. Pressure measurements in this test are controlled fixtures, not live cloud metrics.
- Runtime runs as UID/GID 10001, has required libraries and UI, and fails safely without a profile.
- Actual Docker context export excludes three synthetic secret sentinels and private/generated directories. Six final image layers were inspected for private runtime files, source payload and test sentinels; none found. This is a scoped packaging check, not a general vulnerability certification.
- Native demo still returns HTTP200. No tuner logic or native demo process changed. No cloud workloads started.

The Dockerfile and verification tools are published as source. Build the runtime locally; no prebuilt registry image is published. The native website download remains a separate artifact. Base tags and Debian package repositories remain mutable; this is not a bit-reproducible release. AMD64 is not yet verified.

## Remaining deployment work

Pooltune currently accepts loopback connections. Publishing a Docker port does not change that restriction. Application placement is still to be resolved; the recommended first setup places the application beside Pooltune in Compose with a deliberately tested network namespace.

Before a full deployment: verify application SQL and private dashboard access, mount credentials read-only, persist state, share `/tmp/pooltune-burst-owners-10001` among instances on the same Docker engine, and exercise container shutdown/crash/restart. The current test verifies clean runtime restart within one container, not container crash recovery. Never weaken listener checks or delete retained state to bypass failed startup. Containers do not provide distributed budget ownership.
