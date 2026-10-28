#!/usr/bin/env bash
# Run under the agreed heavy lock/load guard. Entirely synthetic and offline.
# Example: flock $TMP bash -c 'bash $TMP 1400 >/dev/null; bash $PROOF/upgrade-proof.sh'
set -euo pipefail
ROOT=$(cd -- "$(dirname -- "$0")" && pwd)
REPO=${REPO:-$HOME/code/new-pedrito/wacli-fork/qr-v2}
NEW_REF=${NEW_REF:-HEAD}
RUN=$(mktemp -d "$ROOT/run-XXXXXX")
exec > >(tee "$RUN/transcript.txt") 2>&1
echo "Offline synthetic compatibility proof for PR #539; output: $RUN"
echo "No accounts, session databases, WhatsApp connections, or production data."
echo "Source commits, in order: stock v0.25.1, #498 bbd627e, reviewed implementation."
git -C "$REPO" rev-parse 'v0.25.1^{commit}' 'bbd627e^{commit}' "$NEW_REF^{commit}" | tee "$RUN/source-commits.txt"
git -C "$REPO" show --no-patch --format=fuller "$NEW_REF"
mkdir -p "$RUN/sources" "$RUN/bin"
for entry in 'stock:v0.25.1' 'lists498:bbd627e' "new:$NEW_REF"; do
    name=${entry%%:*}; ref=${entry#*:}
    mkdir -p "$RUN/sources/$name"
    git -C "$REPO" archive "$ref" | tar -x -C "$RUN/sources/$name"
done
for name in stock new; do
    echo "== Build $name CLI from the recorded committed source (FTS enabled; dependency network disabled)"
    (cd "$RUN/sources/$name"; GOWORK=off GOPROXY=off GOSUMDB=off CGO_ENABLED=1 CGO_CFLAGS=-Wno-error=missing-braces go build -tags sqlite_fts5 -o "$RUN/bin/wacli-$name" ./cmd/wacli)
done
for entry in stock:seed-stock.go lists498:seed-lists.go; do
    name=${entry%%:*}; seed=${entry#*:}
    mkdir -p "$RUN/sources/$name/.proof539-seed"
    cp "$ROOT/$seed" "$RUN/sources/$name/.proof539-seed/main.go"
    (cd "$RUN/sources/$name"; GOWORK=off GOPROXY=off GOSUMDB=off CGO_ENABLED=1 CGO_CFLAGS=-Wno-error=missing-braces go build -tags sqlite_fts5 -o "$RUN/bin/seed-$name" ./.proof539-seed)
done
sha256sum "$RUN/bin/"* | tee "$RUN/binary-sha256.txt"
OLD="$RUN/bin/wacli-stock"; NEW="$RUN/bin/wacli-new"
run() { printf '\n$'; printf ' %q' "$@"; printf '\n'; "$@"; }
assert_json() {
    python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); assert d["success"], d; assert len(d["data"]["quick_replies"])==0 and d["data"]["completeness"]=="incomplete", d; print("PASS: quick replies remain incomplete with no reply-aware replay")' "$1"
}
for scenario in stock lists498; do
    S="$RUN/$scenario"; mkdir -p "$S"
    echo; echo "====== Scenario: $scenario ======"
    run "$RUN/bin/seed-stock" "$S"
    if [ "$scenario" = lists498 ]; then run "$RUN/bin/seed-lists498" "$S"; fi
    run "$OLD" --store "$S" --read-only --json messages list --chat 15550000001@s.whatsapp.net --limit 3
    python3 "$ROOT/fingerprint.py" capture "$S/wacli.db" > "$S/before.json"
    echo "== New read-only CLI before upgrade must not claim reply coverage"
    "$NEW" --store "$S" --read-only --json quick-replies list | tee "$S/new-before-readonly.json"
    assert_json "$S/new-before-readonly.json"
    if [ "$scenario" = lists498 ]; then run "$NEW" --store "$S" --read-only --json lists list; fi
    echo "== New writable CLI applies migrations/guards (36 and 38)"
    "$NEW" --store "$S" --json quick-replies list | tee "$S/new-after-upgrade.json"
    assert_json "$S/new-after-upgrade.json"
    run "$NEW" --store "$S" --read-only --json messages show --chat 15550000001@s.whatsapp.net --id synthetic-1-1
    run "$NEW" --store "$S" --read-only --json contacts search Synthetic --limit 10
    run "$NEW" --store "$S" --read-only --json messages search Synthetic --limit 3
    run "$NEW" --store "$S" --read-only --json lists list
    python3 "$ROOT/fingerprint.py" capture "$S/wacli.db" > "$S/after-upgrade.json"
    python3 "$ROOT/fingerprint.py" compare "$S/wacli.db" "$S/before.json" | tee "$S/compare-upgrade.txt"
    echo "== Back to stock v0.25.1, read-only and writable"
    run "$OLD" --store "$S" --read-only --json messages show --chat 15550000001@s.whatsapp.net --id synthetic-1-1
    run "$OLD" --store "$S" --read-only --json contacts search Synthetic --limit 10
    run "$OLD" --store "$S" --json messages list --chat 15550000002@s.whatsapp.net --limit 3
    run "$OLD" --store "$S" --json chats list --limit 10
    run "$OLD" --store "$S" --json messages search Synthetic --limit 3
    python3 "$ROOT/fingerprint.py" capture "$S/wacli.db" > "$S/after-stock-reopen.json"
    python3 "$ROOT/fingerprint.py" compare "$S/wacli.db" "$S/before.json" | tee "$S/compare-stock-reopen.txt"
    "$NEW" --store "$S" --read-only --json quick-replies list | tee "$S/new-after-stock-reopen.json"
    assert_json "$S/new-after-stock-reopen.json"
done
echo "PASS: both synthetic stock-v0.25.1 and actual-#498 populated stores upgrade and reopen with stock read-only/writable; all original rows and settings preserved."
echo "$RUN" > "$ROOT/LATEST"
