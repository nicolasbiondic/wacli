#!/usr/bin/env bash
# Live proof for openclaw/wacli #528 on a real account (a freshly linked companion running this branch's `sync --follow`).
# Output is redacted: phone numbers / JIDs / contact names / message bodies of third parties.
set -u
W=/tmp/opencode/kp/wacli-528; OLD=/tmp/opencode/kp/wacli-0.25.0; S=/tmp/opencode/kp/live
CHAT=51964694693@s.whatsapp.net
POLL=3EB0B44DD4510598D19622; VOTE=2A4B27CC33A63343C1D8; CARD=2A6663295B826F14514B; EVENT=2AE8D12330F13360A129
red() { sed -E 's/\+?[0-9][0-9 ()-]{6,}[0-9]/REDACTED/g; s/[0-9]{6,}@(s\.whatsapp\.net|lid|g\.us)/REDACTED@\1/g; s/Nexus Cloud/REDACTED NAME/g; s#/tmp/opencode/kp/##g'; }
show() { echo; echo "\$ $*" | red; }
rows() { python3 /tmp/opencode/kp/rows.py | red; }

echo "== Live account: companion linked by phone code, running \`sync --follow\` on this branch (stock whatsmeow pin)"
show wacli-528 version; "$W" version
show "wacli-528 --read-only messages list --chat CHAT --limit 4 --json   # a poll from another phone, then a vote, a contact card and an event sent from the primary"
"$W" --store "$S" --read-only --json messages list --chat "$CHAT" --limit 4 2>/dev/null | rows
show "wacli-528 --read-only search 'Prueba técnica' --json"
"$W" --store "$S" --read-only --json messages search "Prueba técnica" --chat "$CHAT" --limit 3 2>/dev/null | rows
show "wacli-528 --read-only messages show --chat CHAT --id $POLL --json"
"$W" --store "$S" --read-only --json messages show --chat "$CHAT" --id "$POLL" 2>/dev/null | rows
show "wacli-528 --read-only messages context --chat CHAT --id $VOTE --before 1 --after 2 --json   # neighbors carry their kind"
"$W" --store "$S" --read-only --json messages context --chat "$CHAT" --id "$VOTE" --before 1 --after 2 2>/dev/null | rows
echo; echo "== Webhook payloads received by a local listener (sync --webhook), message events for those IDs"
python3 - <<'PY' | red
import json
ids={"3EB0B44DD4510598D19622","2A4B27CC33A63343C1D8","2A6663295B826F14514B","2AE8D12330F13360A129"}
for line in open("/tmp/opencode/kp/webhook.ndjson"):
    try: e=json.loads(line)
    except Exception: continue
    m=e.get("message") or e.get("Message") or e
    mid=m.get("ID") or m.get("id")
    if mid in ids:
        print(f"{mid[:12]}…  Kind={m.get('Kind','(absent)'):10s}  MediaType={m.get('MediaType') or '':6s}")
PY
echo; echo "== Before this change: stock v0.25.0 reading the same store"
show "wacli-0.25.0 --read-only messages list --chat CHAT --limit 4 --json"
"$OLD" --store "$S" --read-only --json messages list --chat "$CHAT" --limit 4 2>/dev/null | rows
echo; echo "== Purge: the kind goes with the payload"
show "# stop sync --follow: delete --for-me and purge take the store lock"
pkill -INT -f "wacli-528 --store $S sync --follow"; for i in $(seq 1 30); do flock -n "$S/LOCK" true 2>/dev/null && break; sleep 1; done
show "wacli-528 messages delete --for-me --chat CHAT --id $CARD"
"$W" --store "$S" messages delete --for-me --chat "$CHAT" --id "$CARD" 2>&1 | tail -1 | red
sleep 3
show "wacli-528 messages purge --chat CHAT --id $CARD --confirm"
"$W" --store "$S" messages purge --chat "$CHAT" --id "$CARD" --confirm 2>&1 | tail -1 | red
show "wacli-528 --read-only messages show --chat CHAT --id $CARD --json"
"$W" --store "$S" --read-only --json messages show --chat "$CHAT" --id "$CARD" 2>/dev/null | rows
show "sqlite: SELECT kind, media_type, display_text, payload_purged_at IS NOT NULL FROM messages WHERE msg_id = CARD"
python3 /tmp/opencode/kp/q.py "$S/wacli.db" "select coalesce(kind,'NULL'), coalesce(media_type,'NULL'), coalesce(display_text,'NULL'), payload_purged_at is not null from messages where msg_id='$CARD'" | red
