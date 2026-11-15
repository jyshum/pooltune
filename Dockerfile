FROM golang:1.27.4-bookworm AS go-build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/ ./cmd/pooltune-worker ./cmd/pooltune-project ./cmd/pooltune-consumer ./cmd/pooltune-query-lab

FROM node:26.3.0-bookworm-slim
WORKDIR /app
COPY investigator/package*.json ./investigator/
RUN npm --prefix investigator ci --omit=dev
COPY investigator/*.ts ./investigator/
COPY --from=go-build /out/ /usr/local/bin/
COPY deploy/staging/start.sh /usr/local/bin/pooltune-staging
USER node
CMD ["/usr/local/bin/pooltune-staging"]
