#!/usr/bin/env bash
# Live proof for openclaw/wacli #527 on a real account (the same freshly linked companion).
# URLs, direct paths, hashes, picture IDs, numbers and JIDs are redacted.
set -u
NEW=/tmp/opencode/kp/wacli-527; OLD=/tmp/opencode/kp/wacli-0.25.0; S=/tmp/opencode/kp/live
PEER=51964694693@s.whatsapp.net
OWN=51905448512@s.whatsapp.net
red() { sed -E 's#https://[^" ]+#REDACTED-URL#g; s#"direct_?[pP]ath":"[^"]*"#"direct_path":"REDACTED"#g; s#oh=[^&" ]+#oh=REDACTED#g; s#oe=[^&" ]+#oe=REDACTED#g; s#"hash":"[^"]*"#"hash":"REDACTED"#g; s#"id":"[^"]*"#"id":"REDACTED"#g; s#(ID|Direct path|Hash): +[^ ]+#\1: REDACTED#g; s/[0-9]{6,}(:[0-9]+)?@(s\.whatsapp\.net|lid|g\.us)/REDACTED@\2/g; s/\+?[0-9][0-9 ()-]{6,}[0-9]/REDACTED/g; s#/tmp/opencode/kp/##g'; }
show() { echo; echo "\$ $*" | red; }
stop_follow() { pkill -INT -f "wacli-(527|0.25.0) --store $S sync --follow"; for i in $(seq 1 30); do flock -n "$S/LOCK" true 2>/dev/null && break; sleep 1; done; }
start_follow() { nohup "$1" --store "$S" sync --follow > /tmp/opencode/kp/follow-527.log 2>&1 & for i in $(seq 1 40); do grep -q "Connected" /tmp/opencode/kp/follow-527.log 2>/dev/null && break; sleep 1; done; sleep 2; }

echo "== 1. Latest stable daemon (v0.25.0) owns the store; the patched CLI explains the upgrade"
stop_follow; start_follow "$OLD"
show "wacli-0.25.0 --store STORE sync --follow   # running"
show "wacli-527 --store STORE profile picture-info --jid PEER"
"$NEW" --store "$S" profile picture-info --jid "$PEER" 2>&1 | red
show "wacli-0.25.0 --store STORE profile picture-info --jid PEER   # the stock CLI, for comparison"
"$OLD" --store "$S" profile picture-info --jid "$PEER" 2>&1 | red
stop_follow

echo; echo "== 2. Patched daemon owns the store; lookups are answered on its live connection"
start_follow "$NEW"
show "wacli-527 --store STORE sync --follow   # running"
show "wacli-527 --store STORE profile picture-info --jid OWN   # the linked account, text output"
"$NEW" --store "$S" profile picture-info --jid "$OWN" 2>&1 | red
show "wacli-527 --store STORE --json profile picture-info --jid PEER"
OUT=$("$NEW" --store "$S" --json profile picture-info --jid "$PEER" 2>&1); echo "$OUT" | red
PIC=$(echo "$OUT" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(((d.get("data") or {}).get("id")) or "")' 2>/dev/null)
show "wacli-527 --store STORE --json profile picture-info --jid PEER --existing-id ID   # same picture"
"$NEW" --store "$S" --json profile picture-info --jid "$PEER" --existing-id "$PIC" 2>&1 | red
show "wacli-527 --store STORE --json profile picture-info --jid status@broadcast"
"$NEW" --store "$S" --json profile picture-info --jid status@broadcast 2>&1 | red
DELEGATED=$("$NEW" --store "$S" --json profile picture-info --jid "$PEER" 2>/dev/null)
stop_follow

echo; echo "== 3. Same lookup without a daemon (direct connection): identical result"
show "wacli-527 --store STORE --json profile picture-info --jid PEER   # nothing running"
DIRECT=$("$NEW" --store "$S" --json profile picture-info --jid "$PEER" 2>/dev/null); echo "$DIRECT" | red
python3 - "$DELEGATED" "$DIRECT" <<'PY'
import json, sys
a, b = (json.loads(x) for x in sys.argv[1:3])
print("delegated == direct:", a == b)
PY
