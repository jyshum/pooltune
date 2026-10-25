# Docker learning package

The first Linux ARM64 package is built and locally verified. This is not yet a complete managed-provider Compose deployment. The existing native demo remains unchanged.

## What each image does

- `pooltune:learning`: non-root Go gateway and owner CLI, PgBouncer 1.26.1, private dashboard assets and CA trust bundle. No test database or credentials.
- Compose `verify` target: disposable PostgreSQL 17.11 plus an integration-test executable. It is a verification image, not the application runtime.

## Run on this Mac

Docker CLI, Compose, Buildx and Colima are installed. The separate `pooltune` VM uses 2 CPUs, 3 GiB RAM and a 25 GiB data disk. It mounts only this source checkout, not the private cloud-credentials directory. The default Docker context was not changed.

From the repository root:

```sh
# Build the actual runtime image.
docker --context colima-pooltune build --target runtime -t pooltune:learning .

# Run real SQL integration in an isolated container with no network egress.
docker --context colima-pooltune compose -f compose.verify.yaml run --build --rm verify

# Check the public command interface without credentials or a database.
docker --context colima-pooltune run --rm --network none pooltune:learning -help

# Check the actual Docker build-context filtering.
DOCKER_CONTEXT=colima-pooltune python3 deploy/docker/check-context.py
```

Stop/start the dedicated VM when needed:

```sh
colima stop pooltune
colima start pooltune --activate=false
```

Stopping Colima does not stop the separate native dashboard. On another Docker installation, omit `--context colima-pooltune` or use its own context.

## Verified October 25, 2026

- Runtime and verification images build for Linux ARM64.
- Real PostgreSQL/PgBouncer test passes in 3.35 seconds: actual SQL, active-transaction preservation while admission expands, ownership exclusion and clean in-process gateway restart.
- Test runner rejects missing dependencies, skipped tests, absent exact PASS output, failures and timeout. Pressure measurements in this test are controlled fixtures, not live cloud metrics.
- Runtime runs as UID/GID 10001, has required libraries and UI, and fails safely without a profile.
- Actual Docker context export excludes three synthetic secret sentinels and private/generated directories. Six final image layers were inspected for private runtime files, source payload and test sentinels; none found. This is a scoped packaging check, not a general vulnerability certification.
- Native demo still returns HTTP200. No tuner logic, UI design or website changes. No cloud workloads started.

The runtime image is local only. No image registry publication or website download replacement occurred. Base tags and Debian package repositories remain mutable; this is not a bit-reproducible release. AMD64 is not yet verified.

## Remaining deployment work

Pooltune currently accepts loopback connections. Publishing a Docker port does not change that restriction. Application placement is still to be resolved; the recommended first setup places the application beside Pooltune in Compose with a deliberately tested network namespace.

Before a full deployment: verify application SQL and private dashboard access, mount credentials read-only, persist state, share `/tmp/pooltune-burst-owners-10001` among instances on the same Docker engine, and exercise container shutdown/crash/restart. The current test verifies clean runtime restart within one container, not container crash recovery. Never weaken listener checks or delete retained state to bypass failed startup. Containers do not provide distributed budget ownership.
