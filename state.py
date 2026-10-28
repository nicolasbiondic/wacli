"""Read-only snapshot of the proof store: archive state and regular_low app state."""
import json
import sqlite3
import sys

store = sys.argv[1]


def ro(name):
    return sqlite3.connect(f"file:{store}/{name}?mode=ro", uri=True)


w = ro("wacli.db")
tables = {r[0] for r in w.execute("select name from sqlite_master where type='table'")}
out = {"wacli_tables_app_state": sorted(t for t in tables if "app_state" in t or "archive" in t)}
cols = [r[1] for r in w.execute("pragma table_info(chats)")]
out["chats"] = w.execute("select count(*) from chats").fetchone()[0]
if "archived" in cols:
    out["archived_chats"] = w.execute("select count(*) from chats where archived=1").fetchone()[0]
for t in sorted(tables):
    if "app_state" in t or "archive" in t:
        out[f"rows:{t}"] = w.execute(f"select count(*) from {t}").fetchone()[0]
s = ro("session.db")
st = {r[0] for r in s.execute("select name from sqlite_master where type='table'")}
if "whatsmeow_app_state_version" in st:
    out["regular_low_version"] = s.execute(
        "select version, hex(substr(hash,1,6)) from whatsmeow_app_state_version where name='regular_low'"
    ).fetchall()
if "whatsmeow_app_state_mutation_macs" in st:
    out["regular_low_mutation_macs"] = s.execute(
        "select count(*) from whatsmeow_app_state_mutation_macs where name='regular_low'"
    ).fetchone()[0]
print(json.dumps(out, indent=1))
