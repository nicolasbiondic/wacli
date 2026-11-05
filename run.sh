#!/usr/bin/env bash
# Run this ENTIRE script under flock $TMP and wait-load.sh.
# The only production access is Python SQLite mode=ro -> backup(wacli.db).
set -euo pipefail
umask 077
ROOT=$(cd -- "$(dirname -- "$0")" && pwd)
REPO=${REPO:-$REPO}
PRIVATE=$(mktemp -d $TMP)
trap 'rm -rf -- "$PRIVATE"' EXIT
mkdir -p "$PRIVATE/sources" "$PRIVATE/bin" "$ROOT/fingerprints"
exec > "$ROOT/transcript.txt" 2>&1
echo 'PR #498 offline proof; counts and hashes only; no session database or daemon access.'
printf 'PR source: '; git -C "$REPO" rev-parse HEAD
git -C "$REPO" rev-parse 'v0.25.0^{commit}' 'v0.25.1^{commit}' 'bbd627e^{commit}' HEAD > "$ROOT/source-commits.txt"
for spec in stock250:v0.25.0 stock251:v0.25.1 legacy:bbd627e head:HEAD; do
    name=${spec%%:*}; ref=${spec#*:}
    mkdir -p "$PRIVATE/sources/$name"
    git -C "$REPO" archive "$ref" | tar -x -C "$PRIVATE/sources/$name"
done
for name in stock250 stock251 head; do
    echo "Build FTS CLI: $name (committed source)"
    (cd "$PRIVATE/sources/$name"; GOWORK=off CGO_ENABLED=1 CGO_CFLAGS=-Wno-error=missing-braces go build -tags sqlite_fts5 -o "$PRIVATE/bin/$name" ./cmd/wacli) > "$PRIVATE/build.log" 2>&1 || { echo 'BUILD FAILED (private build log suppressed)'; exit 1; }
done
for spec in stock250:seed-stock.go stock251:seed-stock.go legacy:seed-lists.go; do
    name=${spec%%:*}; seed=${spec#*:}
    mkdir -p "$PRIVATE/sources/$name/.proof498"
    cp "$ROOT/$seed" "$PRIVATE/sources/$name/.proof498/main.go"
    (cd "$PRIVATE/sources/$name"; GOWORK=off CGO_ENABLED=1 CGO_CFLAGS=-Wno-error=missing-braces go build -tags sqlite_fts5 -o "$PRIVATE/bin/seed-$name" ./.proof498) > "$PRIVATE/build.log" 2>&1 || { echo 'SEED BUILD FAILED'; exit 1; }
done
(cd "$PRIVATE/bin"; sha256sum *) > "$ROOT/binary-sha256.txt"
export PROOF498_PRIVATE="$PRIVATE"
export PROOF498_ROOT="$ROOT"
export PROOF498_PRODUCTION_ROOT="${PROOF498_PRODUCTION_ROOT:-$PRODUCTION_TENANTS}"
python3 "$ROOT/proof.py"
echo 'PASS: all scenarios complete; deleting every private database copy, binary and source archive.'
