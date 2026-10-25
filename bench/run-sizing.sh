#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
case "${1:-}" in /*) ;; *) echo 'usage: bench/run-sizing.sh /absolute/new/result.json' >&2; exit 2;; esac
for binary in go initdb pg_ctl pgbouncer; do
 command -v "$binary" >/dev/null || { echo "missing dependency: $binary" >&2; exit 1; }
done
[ ! -e "$1" ] || { echo 'output already exists' >&2; exit 1; }
export POOLTUNE_SIZING_OUTPUT="$1"
go test -tags=integration,performance ./internal/runtime -run '^TestPoolSizingComparison$' -count=1 -timeout=12m -v
