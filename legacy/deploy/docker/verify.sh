#!/bin/sh
set -eu
# Refuse a false green caused by a missing PostgreSQL dependency.
command -v initdb >/dev/null
command -v postgres >/dev/null
command -v pgbouncer >/dev/null
[ "$(id -u)" = 10001 ]
pgbouncer --version
postgres --version
log=$(mktemp)
trap 'rm -f "$log"' EXIT
if ! /opt/pooltune/burst-integration.test -test.v -test.count=1 -test.timeout=60s -test.run '^TestOwnedPoolPreservesTransactionWhileReserveOpens$' >"$log" 2>&1; then
  cat "$log"
  exit 1
fi
cat "$log"
grep -q '^--- PASS: TestOwnedPoolPreservesTransactionWhileReserveOpens ' "$log"
if grep -q '^--- SKIP:' "$log"; then exit 1; fi
