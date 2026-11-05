#!/usr/bin/env python3
"""Offline v0.25.1/new/stock round trips. Never pass a live store to wacli.

The referenced $TMP scripts were absent. Reconstructs
their mode=ro SQLite backup and per-table fingerprint pattern, with no column
exclusions: schema, migrations, logical rows and FTS shadow tables are checked.
Only anonymous hashes/counts are retained. All copied databases are deleted.
"""
import hashlib
from contextlib import closing
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import sys
import tempfile

PROOF = Path(__file__).resolve().parent
STOCK = Path('$HOME/.local/share/vendas-plan/tools/wacli-releases/0.25.1/wacli')
NEW = Path(sys.argv[1]).resolve()
ROOT = Path('$PRODUCTION_TENANTS')
SOURCES = [ROOT / tenant / 'store/wacli.db' for tenant in (
    '905-448-520-anges8', '922-682-374-fjwufn', '964-694-693-58wb4p',
    'fazil-mgbuij', 'notificaciones-lenz-dbpwff')]


def quote(name):
    return '"' + name.replace('"', '""') + '"'


def fingerprint(path):
    with sqlite3.connect(path.as_uri() + '?mode=ro', uri=True) as db:
        assert db.execute('PRAGMA integrity_check').fetchone() == ('ok',)
        schema = db.execute('SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name').fetchall()
        result = {'schema_sha256': hashlib.sha256(repr(schema).encode()).hexdigest(), 'tables': {}}
        for (table,) in db.execute("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name"):
            columns = db.execute(f'PRAGMA table_info({quote(table)})').fetchall()
            names = ','.join(quote(c[1]) for c in columns)
            digest = hashlib.sha256()
            count = 0
            for row in db.execute(f'SELECT {names} FROM {quote(table)} ORDER BY {names}'):
                digest.update(repr(row).encode())
                digest.update(b'\n')
                count += 1
            result['tables'][table] = {'columns': columns, 'rows': count, 'sha256': digest.hexdigest()}
        result['migration_versions'] = db.execute('SELECT version,name FROM schema_migrations ORDER BY version').fetchall()
        return result


def run_cli(binary, store, readonly=False):
    command = [str(binary), '--store', str(store), '--json']
    if readonly:
        command += ['--read-only']
    # These shipped local commands do not open a WhatsApp session or network.
    for args in (['chats', 'list', '--limit', '1'], ['messages', 'list', '--limit', '1'],
                 ['messages', 'search', 'proof533', '--limit', '1']):
        result = subprocess.run(command + args, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        if result.returncode:
            raise RuntimeError(f'CLI {args[:2]} failed: {result.stderr.decode()}')
    assert not (store / 'session.db').exists(), 'local retrieval must not open session.db'


def seed(store, variant):
    run_cli(STOCK, store)
    with sqlite3.connect(store / 'wacli.db') as db:
        for index, count in enumerate((2, 0)):
            chat = f'1555000000{index}@s.whatsapp.net'
            db.execute('INSERT INTO chats(jid,kind,name,last_message_ts,unread,unread_count) VALUES(?,?,?,?,?,?)',
                       (chat, 'dm', 'synthetic', 1700000060, 1, count))
            for n in range(3):
                db.execute('INSERT INTO messages(chat_jid,msg_id,ts,from_me,text,display_text) VALUES(?,?,?,?,?,?)',
                           (chat, f'proof-{n}', 1700000000 + n * 30, n == 2, f'proof533 {variant} {n}', f'proof533 {variant} {n}'))
            db.execute('INSERT INTO contacts(jid,full_name,updated_at) VALUES(?,?,?)', (chat, 'synthetic proof533', 1700000060))
        if variant == 'tombstones':
            db.execute("UPDATE messages SET revoked=1,deleted_at=1700000090,deletion_reason='whatsapp_revoke' WHERE msg_id='proof-2'")


def save(label, phase, fp):
    (PROOF / f'{label}-{phase}.json').write_text(json.dumps(fp, indent=2) + '\n')


def roundtrip(label, store):
    before = fingerprint(store / 'wacli.db')
    save(label, 'before', before)
    run_cli(NEW, store)
    after = fingerprint(store / 'wacli.db')
    save(label, 'after', after)
    assert before == after, f'{label}: new writable open changed schema/data'
    run_cli(STOCK, store, readonly=True)
    run_cli(STOCK, store)
    reopened = fingerprint(store / 'wacli.db')
    save(label, 'stock-reopened', reopened)
    assert before == reopened, f'{label}: stock reopen changed schema/data'
    tables = before['tables']
    print(f'{label}: PASS new writable retrieval + stock read-only/writable retrieval; '
          f'{len(tables)} tables unchanged, {tables["messages"]["rows"]} messages; '
          f'{len(before["migration_versions"])} migrations unchanged; integrity_check=ok', flush=True)


os.umask(0o077)
temp = Path(tempfile.mkdtemp(prefix='pr533-data-', dir='/tmp/opencode'))
try:
    print('stock v0.25.1:', subprocess.check_output([str(STOCK), '--version'], text=True).strip(), flush=True)
    print('stock binary sha256:', hashlib.file_digest(STOCK.open('rb'), 'sha256').hexdigest(), flush=True)
    for variant in ('unread', 'tombstones'):
        store = temp / ('stock-' + variant)
        store.mkdir()
        seed(store, variant)
        roundtrip('stock-' + variant, store)
    assert all(path.is_file() for path in SOURCES), 'expected exactly five production wacli.db sources'
    for index, source in enumerate(SOURCES, 1):
        label = f'production-{index}'
        store = temp / label
        store.mkdir()
        # The ONLY production database access: an explicitly read-only source
        # and SQLite's consistent backup API. Never copy sidecars or session.db.
        with closing(sqlite3.connect(source.as_uri() + '?mode=ro', uri=True)) as src:
            src.execute('PRAGMA query_only=ON')
            with closing(sqlite3.connect(store / 'wacli.db')) as dst:
                src.backup(dst, pages=256)
        print(f'{label}: mode=ro SQLite backup complete (wacli.db only)', flush=True)
        roundtrip(label, store)
        shutil.rmtree(store)
finally:
    shutil.rmtree(temp)
    print('CLEANUP: all disposable stores/copies removed; no session.db copied/opened; no daemon commands', flush=True)
