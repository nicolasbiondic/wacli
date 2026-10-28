#!/usr/bin/env python3
"""Per-table row count and content hash of a wacli.db, ignoring the new nullable messages.kind column."""
import hashlib, sqlite3, sys
c = sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True)
tables = [r[0] for r in c.execute("select name from sqlite_master where type='table' and name not like 'sqlite_%' and name not like 'messages_fts%' order by name")]
for t in tables:
    cols = [r[1] for r in c.execute(f"pragma table_info({t})") if not (t == "messages" and r[1] == "kind")]
    h = hashlib.sha256()
    n = 0
    for row in c.execute(f"select {','.join(cols)} from {t} order by {','.join(cols)}"):
        h.update(repr(row).encode()); n += 1
    extra = ""
    if t == "messages" and any(r[1] == "kind" for r in c.execute("pragma table_info(messages)")):
        extra = f"  (+kind: {c.execute('select count(*) from messages where kind is not null').fetchone()[0]} non-null)"
    print(f"{t:34s} {n:5d}  {h.hexdigest()[:16]}{extra}")
