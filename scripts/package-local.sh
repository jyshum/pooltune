#!/bin/bash
# Build an explicit native customer-development payload. Never archive the checkout.
set -euo pipefail
umask 077
if [ "$#" -ne 1 ] || [[ "$1" != /* ]]; then
  echo 'Usage: scripts/package-local.sh /absolute/new/output-directory' >&2
  exit 1
fi
output=$1
if [ -e "$output" ] || [ -L "$output" ] || [ -e "$output.tar.gz" ] || [ -L "$output.tar.gz" ]; then
  echo 'Output directory and archive must not already exist.' >&2
  exit 1
fi
repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"
for command in go npm python3 git; do command -v "$command" >/dev/null; done
stage=$(mktemp -d "${output}.tmp.XXXXXX")
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/bin" "$stage/ui" "$stage/sdk" "$stage/docs"
CGO_ENABLED=0 go build -trimpath -ldflags=-buildid= -o "$stage/bin/pooltune" ./cmd/pooltune
CGO_ENABLED=0 go build -trimpath -ldflags=-buildid= -o "$stage/bin/pooltune-owner" ./cmd/pooltune-owner
CGO_ENABLED=0 go build -trimpath -ldflags=-buildid= -o "$stage/bin/pooltune-tuner" ./cmd/pooltune-tuner
CGO_ENABLED=0 go build -trimpath -ldflags=-buildid= -o "$stage/bin/pooltune-provider" ./cmd/pooltune-provider
CGO_ENABLED=0 go build -trimpath -ldflags=-buildid= -o "$stage/bin/pooltune-burst" ./cmd/pooltune-burst
npm ci
npm --prefix ui ci
npm --prefix ui run build -- --outDir "$stage/ui"
npm exec -- tsc --outDir "$stage/sdk"
for doc in install operations controller local-runtime rest-runtime control-runtime owner-recovery release-gates build-status compatibility recovery managed-ai background-tuner burst-gateway burst-gateway-results quick-confirmation managed-mvp-results transaction-throughput traffic-history; do
  cp "docs/$doc.md" "$stage/docs/"
done
for evidence in burst-gateway-2026-10-25 quick-confirmation-2026-10-25 managed-mvp-2026-10-25; do
  mkdir -p "$stage/docs/evidence/$evidence"
  for name in summary azure-results google-results; do
    cp "docs/evidence/$evidence/$name.json" "$stage/docs/evidence/$evidence/"
  done
done
cp docs/evidence/burst-gateway-2026-10-25/supabase-results.json "$stage/docs/evidence/burst-gateway-2026-10-25/"
cp docs/evidence/managed-mvp-2026-10-25/supabase-results.json "$stage/docs/evidence/managed-mvp-2026-10-25/"
# Output metadata identifies dirty builds honestly. The manifest covers every
# shipped regular file; symlinks and unknown payload categories are rejected.
python3 - "$stage" <<'PY'
import hashlib,json,os,pathlib,subprocess,sys
from datetime import datetime,timezone
root=pathlib.Path(sys.argv[1])
def command(*args): return subprocess.check_output(args,text=True).strip()
metadata={"profile":"local-development","commit":command("git","rev-parse","HEAD"),"dirty":bool(command("git","status","--porcelain")),"go":command("go","version"),"node":command("node","--version"),"os":command("go","env","GOOS"),"arch":command("go","env","GOARCH"),"built_at":datetime.now(timezone.utc).isoformat(),"container_verified":False,"independent_traffic_recovery_verified":False}
(root/"RELEASE.json").write_text(json.dumps(metadata,indent=2)+"\n")
manifest=[]
for file in sorted(root.rglob("*")):
    if file.is_symlink(): raise SystemExit("Symlink in package payload")
    if not file.is_file(): continue
    relative=file.relative_to(root).as_posix()
    if file.parts[len(root.parts)] not in {"bin","ui","sdk","docs","RELEASE.json"}: raise SystemExit("Unexpected package payload")
    os.chmod(file,0o700 if relative.startswith("bin/") else 0o600)
    manifest.append(hashlib.sha256(file.read_bytes()).hexdigest()+"  "+relative)
(root/"SHA256SUMS").write_text("\n".join(manifest)+"\n")
PY
# The destination is new. Failed builds retain neither a partial package nor an
# archive that could be mistaken for success.
python3 - "$stage" "$output.tar.gz" <<'PY'
import os,pathlib,sys,tarfile
root=pathlib.Path(sys.argv[1]);output=pathlib.Path(sys.argv[2]);temporary=root/"payload.tar.gz"
with tarfile.open(temporary,"w:gz") as archive:
    for file in sorted(root.rglob("*")):
        if file==temporary: continue
        archive.add(file,arcname="pooltune/"+file.relative_to(root).as_posix(),recursive=False)
# Exclusive creation also protects against an output appearing during the build.
with output.open("xb") as destination,temporary.open("rb") as source:
    import shutil
    shutil.copyfileobj(source,destination)
    destination.flush();os.fsync(destination.fileno())
temporary.unlink()
PY
# Refuse a directory created concurrently rather than nesting the package in it.
if [ -e "$output" ] || [ -L "$output" ]; then
  rm -f "$output.tar.gz"
  echo 'Output appeared during build.' >&2
  exit 1
fi
mv "$stage" "$output"
trap - EXIT
printf 'Native development package: %s\nArchive: %s.tar.gz\n' "$output" "$output"
