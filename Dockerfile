# syntax=docker/dockerfile:1
# Initial learning package. Full Compose application networking is separate.
FROM node:22.20.0-bookworm-slim AS dashboard
WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/index.html ui/tsconfig.json ui/vite.config.ts ./
COPY ui/src ./src
RUN npm run build

FROM debian:bookworm-slim AS pooler
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl build-essential pkg-config libevent-dev libssl-dev && rm -rf /var/lib/apt/lists/*
WORKDIR /build
RUN curl -fsSLO https://www.pgbouncer.org/downloads/files/1.26.1/pgbouncer-1.26.1.tar.gz \
 && echo '00886a2994b4844cd37110970df777772cfc42a74c400522c82fba509a3f42d9  pgbouncer-1.26.1.tar.gz' | sha256sum -c - \
 && tar -xzf pgbouncer-1.26.1.tar.gz \
 && cd pgbouncer-1.26.1 && ./configure --prefix=/usr/local --with-openssl --without-cares \
 && make -j2 pgbouncer && install -m 0755 pgbouncer /usr/local/bin/pgbouncer

FROM golang:1.27.3-bookworm AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/pooltune-burst ./cmd/pooltune-burst \
 && CGO_ENABLED=0 go build -trimpath -o /out/pooltune-owner ./cmd/pooltune-owner \
 && CGO_ENABLED=0 go test -c -tags=integration -o /out/burst-integration.test ./internal/burst

FROM postgres:17.11-bookworm AS verification
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libevent-2.1-7 libssl3 \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd -g 10001 pooltune && useradd -u 10001 -g pooltune -M -s /usr/sbin/nologin pooltune
COPY --from=pooler /usr/local/bin/pgbouncer /usr/local/bin/pgbouncer
COPY --from=backend /out/burst-integration.test /opt/pooltune/burst-integration.test
COPY --chmod=755 deploy/docker/verify.sh /opt/pooltune/verify.sh
USER 10001:10001
WORKDIR /tmp
ENTRYPOINT ["/opt/pooltune/verify.sh"]

FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libevent-2.1-7 libssl3 \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd -g 10001 pooltune && useradd -u 10001 -g pooltune -M -s /usr/sbin/nologin pooltune \
 && mkdir -p /var/lib/pooltune /tmp/pooltune-burst-owners-10001 \
 && chown -R 10001:10001 /var/lib/pooltune /tmp/pooltune-burst-owners-10001 \
 && chmod 700 /var/lib/pooltune /tmp/pooltune-burst-owners-10001
COPY --from=pooler /usr/local/bin/pgbouncer /usr/local/bin/pgbouncer
COPY --from=backend /out/pooltune-burst /out/pooltune-owner /usr/local/bin/
COPY --from=dashboard /src/ui/dist /opt/pooltune/ui
USER 10001:10001
WORKDIR /var/lib/pooltune
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/pooltune-burst"]

