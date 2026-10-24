#!/usr/bin/env bash
# Upgrade a populated v0.25.0 store to this branch and back. Offline: no account, no network.
set -u
OLD=/tmp/opencode/kp/wacli-0.25.0; NEW=/tmp/opencode/kp/wacli-branch; S=/tmp/opencode/kp/store
run() { echo; echo "\$ $*" | sed "s#/tmp/opencode/kp/##g"; "$@" 2>&1 | sed "s#/tmp/opencode/kp/##g"; }
mig() { python3 /tmp/opencode/kp/q.py "$S/wacli.db" "select version||' '||name from schema_migrations order by version desc limit 2"; }
echo "== 1. Populated store written by stock v0.25.0"
run python3 /tmp/opencode/kp/q.py "$S/wacli.db" "select count(*) from messages"; echo "migrations:"; mig
python3 /tmp/opencode/kp/fingerprint.py "$S/wacli.db" > /tmp/opencode/kp/fp-before.txt; cat /tmp/opencode/kp/fp-before.txt
echo; echo "== 2. A paging cursor taken with v0.25.0"
CUR=$("$OLD" --store "$S" --read-only --json messages list --chat 15550000001@s.whatsapp.net --limit 3 | python3 -c "import json,sys; d=json.load(sys.stdin); p=d['data']['page']; print(p.get('next_cursor') or p.get('cursor') or '')")
echo "cursor: ${CUR:0:24}…"
echo; echo "== 3. Open with this branch (writable: runs migration 35)"
run "$NEW" --store "$S" --json messages list --chat 15550000001@s.whatsapp.net --limit 1
echo "migrations:"; mig
run python3 /tmp/opencode/kp/q.py "$S/wacli.db" "select sql like '%kind%' from sqlite_master where name='retrieval_messages_update'"
echo; echo "== 4. The v0.25.0 cursor resumes on this branch"
run "$NEW" --store "$S" --read-only messages list --chat 15550000001@s.whatsapp.net --limit 3 --cursor "$CUR"
echo; echo "== 5. Every table unchanged (kind column excluded and reported)"
python3 /tmp/opencode/kp/fingerprint.py "$S/wacli.db" > /tmp/opencode/kp/fp-after.txt; cat /tmp/opencode/kp/fp-after.txt
diff <(grep -v "^schema_migrations" /tmp/opencode/kp/fp-before.txt | sed "s/  (+kind.*//") <(grep -v "^schema_migrations" /tmp/opencode/kp/fp-after.txt | sed "s/  (+kind.*//") && echo "IDENTICAL to v0.25.0 (schema_migrations: +1 row, 35 message semantic kind)"
echo; echo "== 6. Back to stock v0.25.0 (read-only and writable)"
run "$OLD" --store "$S" --read-only messages list --chat 15550000001@s.whatsapp.net --limit 3
run "$OLD" --store "$S" --json messages show --chat 15550000001@s.whatsapp.net --id poll1
run "$OLD" --store "$S" messages list --chat 15550000002@s.whatsapp.net --limit 2
python3 /tmp/opencode/kp/fingerprint.py "$S/wacli.db" > /tmp/opencode/kp/fp-downgrade.txt
diff <(grep -v "^schema_migrations" /tmp/opencode/kp/fp-before.txt | sed "s/  (+kind.*//") <(grep -v "^schema_migrations" /tmp/opencode/kp/fp-downgrade.txt | sed "s/  (+kind.*//") && echo "IDENTICAL after downgrade"
