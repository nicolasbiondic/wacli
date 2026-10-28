#!/usr/bin/env bash
# Offline upgrade proof on copies of our production wacli.db files.
# Only wacli.db is copied (SQLite online backup from a read-only connection);
# session.db is never copied, so no copy can connect to WhatsApp.
set -uo pipefail
R=$PROOF
FP=$PROOF/fingerprint-541.py
NEW=$R/wacli-541
STOCK=$HOME/.local/share/vendas-plan/tools/wacli-releases/0.25.1/wacli
T=$HOME/code/new-pedrito/vendas-chat-gateway/.tenants
OUT=$R/out
rm -rf "$OUT" && mkdir -p "$OUT"
i=0
for tenant in 964-694-693-58wb4p 922-682-374-fjwufn 905-448-520-anges8 notificaciones-lenz-dbpwff fazil-mgbuij; do
  i=$((i + 1)); name="line-$i"; S=$R/stores/$name
  rm -rf "$S" && mkdir -p "$S" && chmod 700 "$S"
  python3 - "$T/$tenant/store/wacli.db" "$S/wacli.db" <<'PY'
import sqlite3, sys
src = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True)
dst = sqlite3.connect(sys.argv[2])
src.backup(dst)
dst.close(); src.close()
PY
  chmod 600 "$S/wacli.db"
  test ! -e "$S/session.db" || { echo "session.db present, abort"; exit 1; }
  {
    echo "====== $name ======"
    python3 -c "import sqlite3;c=sqlite3.connect('file:$S/wacli.db?mode=ro',uri=True);print('schema_migrations max', c.execute('select max(version) from schema_migrations').fetchone()[0], '| messages', c.execute('select count(*) from messages').fetchone()[0], '| chats', c.execute('select count(*) from chats').fetchone()[0], '| contacts', c.execute('select count(*) from contacts').fetchone()[0])"
    python3 "$FP" capture "$S/wacli.db" > "$OUT/$name-before.json"
    echo "== new (#541) read-only before migration"
    "$NEW" --store "$S" --read-only --json quick-replies list 2>&1 | cut -c1-200
    echo "== new (#541) writable open applies migration 38"
    "$NEW" --store "$S" --json quick-replies list 2>&1 | cut -c1-200
    python3 -c "import sqlite3;c=sqlite3.connect('file:$S/wacli.db?mode=ro',uri=True);print('migration 38 row:', c.execute(\"select version,name from schema_migrations where version=38\").fetchall())"
    "$NEW" --store "$S" --read-only --json chats list --limit 1 >/dev/null 2>&1 && echo "new: chats list ok"
    "$NEW" --store "$S" --read-only --json messages search precio --limit 1 2>/dev/null | python3 -c "import json,sys;d=json.load(sys.stdin);print('new: messages search ok, fts=',d['data'].get('fts'))"
    python3 "$FP" compare "$S/wacli.db" "$OUT/$name-before.json" > "$OUT/$name-compare-upgrade.txt" 2>&1; echo "compare after upgrade: $(tail -1 "$OUT/$name-compare-upgrade.txt")"
    echo "== stock v0.25.1 reopens read-only and writable"
    "$STOCK" --store "$S" --read-only --json chats list --limit 1 >/dev/null 2>&1 && echo "stock read-only: chats list ok"
    "$STOCK" --store "$S" --json chats list --limit 1 >/dev/null 2>&1 && echo "stock writable: chats list ok"
    "$STOCK" --store "$S" --read-only --json messages search precio --limit 1 2>/dev/null | python3 -c "import json,sys;d=json.load(sys.stdin);print('stock: messages search ok, fts=',d['data'].get('fts'))"
    python3 "$FP" compare "$S/wacli.db" "$OUT/$name-before.json" > "$OUT/$name-compare-stock.txt" 2>&1; echo "compare after stock reopen: $(tail -1 "$OUT/$name-compare-stock.txt")"
    python3 -c "import sqlite3;c=sqlite3.connect('file:$S/wacli.db?mode=ro',uri=True);print('integrity:', c.execute('pragma integrity_check').fetchone()[0], '| foreign keys:', len(c.execute('pragma foreign_key_check').fetchall()), 'violations')"
    test ! -e "$S/session.db" && echo "no session.db was created by any command" || echo "NOTE: a session.db exists now (unauthenticated)"
  } 2>&1 | tee -a "$OUT/transcript.txt"
done
