#!/usr/bin/env python3
"""Shipped CLI proof. Raw private CLI output exists only in memory.

No source/tenant paths are published. The shell trap deletes ALL copied stores
on success or failure. Only mode=ro wacli.db backups touch production.
"""
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import traceback
import unicodedata

from fingerprint import capture, compare, connect, quote

ROOT = Path(os.environ['PROOF498_ROOT'])
PRIVATE = Path(os.environ['PROOF498_PRIVATE'])
BIN = PRIVATE / 'bin'
env = dict(os.environ, WACLI_READONLY='0', WACLI_STORE_DIR='', WACLI_ACCOUNT='', WACLI_CONFIG=str(PRIVATE / 'absent-config'))
results = []


def save(name, data):
    (ROOT / 'fingerprints' / (name + '.json')).write_text(json.dumps(data, indent=2, sort_keys=True) + '\n')


def cli(binary, store, args, readonly=True, expect_ok=True):
    mode = ['--read-only'] if readonly else []
    cmd = [str(BIN / binary), '--store', str(store), '--json', *mode, *args]
    p = subprocess.run(cmd, env=env, capture_output=True, text=True, timeout=90)
    try:
        doc = json.loads(p.stdout or p.stderr)
    except ValueError:
        raise AssertionError('CLI did not emit JSON; raw output suppressed') from None
    assert (p.returncode == 0 and doc.get('success') is True) == expect_ok, 'CLI result unexpected; raw output suppressed'
    assert not list(store.glob('session*')), 'unexpected session file'
    # Never print raw commands (they may contain an account list ID/name).
    print(f'  CLI {binary} {"read-only" if readonly else "writable"} {args[0]} {args[1]} exit={p.returncode} success={doc.get("success")}', flush=True)
    return doc, p.stderr


def seed(binary, store):
    store.mkdir(mode=0o700)
    p = subprocess.run([str(BIN / binary), str(store)], env=env, capture_output=True, timeout=30)
    assert p.returncode == 0, 'fixture seed failed'


def backup(source, target):
    # EXACT source filename only. Never glob/copy tenant directories or use
    # immutable=1 on live WAL databases. Snapshot is SQLite's online backup.
    assert source.name == 'wacli.db' and target.name == 'wacli.db'
    target.parent.mkdir(mode=0o700)
    with sqlite3.connect(source.as_uri() + '?mode=ro', uri=True, timeout=30) as src:
        src.execute('PRAGMA query_only=ON')
        with sqlite3.connect(target) as dst:
            src.backup(dst, pages=256, sleep=0.01)
    target.chmod(0o600)


def safe_name(name):
    name = ''.join(c for c in name if unicodedata.category(c) != 'Cf')
    return name if name in {'Unread', 'Favorites', 'Groups', 'All', 'Sin leer', 'Favoritos', 'Grupos', 'Todos'} else '<private-list>'


def behavior(store, label, phase):
    doc, _ = cli('head', store, ['lists', 'list'])
    data = doc['data']
    assert data['completeness'] == 'unknown'
    visible = data['lists']
    with connect(store / 'wacli.db') as db:
        exists = bool(db.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name='lists'").fetchone())
        expected = db.execute('SELECT count(*) FROM lists WHERE deleted=0').fetchone()[0] if exists else 0
        assert len(visible) == expected, 'visible definition count mismatch'
        print(f'  lists list: definitions={len(visible)}, completeness=unknown, full_sync_events_seen={data["full_sync_events_seen"]}', flush=True)
        filtered = []
        for number, item in enumerate(visible, 1):
            # Display list ordinal, never account IDs or private names.
            shown = {'ordinal': number, 'name': safe_name(item['name']), 'type': item['type'], 'color': item['color'], 'chat_memberships_supported': item['chat_memberships_supported']}
            if not item['chat_memberships_supported']:
                failed, _ = cli('head', store, ['chats', 'list', '--list', item['id'], '--limit', '1000000'], expect_ok=False)
                assert 'not supported' in (failed.get('error') or '') or 'unsupported' in (failed.get('error') or ''), 'unsupported membership error mismatch'
                shown['filter'] = 'unsupported system/computed membership (expected refusal)'
            else:
                chats, stderr = cli('head', store, ['chats', 'list', '--list', item['id'], '--limit', '1000000'])
                assert 'completeness is unknown' in stderr
                selected = db.execute('SELECT c.jid FROM chats c JOIN chat_lists cl ON cl.chat_jid=c.jid WHERE cl.list_id=? AND cl.labeled=1', (item['id'],)).fetchall()
                # Stock-shaped empty chat results encode as null, not [].
                returned = chats['data'] or []
                assert sorted(c['jid'] for c in returned) == sorted(r[0] for r in selected), 'CLI/SQL membership mismatch'
                shown['matched_chats'] = len(returned)
                # Check exact-name resolution too, except ambiguous names and
                # names equal to another visible ID (documented ID precedence).
                if sum(v['name'] == item['name'] for v in visible) == 1 and not any(v['id'] == item['name'] for v in visible) and item['name'].strip():
                    by_name, _ = cli('head', store, ['chats', 'list', '--list', item['name'], '--limit', '1000000'])
                    assert sorted(c['jid'] for c in (by_name['data'] or [])) == sorted(c['jid'] for c in returned)
                    shown['id_name_filter_agree'] = True
            print('  REDACTED ' + json.dumps(shown, sort_keys=True), flush=True)
            filtered.append(shown)
        if exists:
            deleted = db.execute('SELECT id FROM lists WHERE deleted=1 ORDER BY id').fetchall()
            if deleted:
                failed, _ = cli('head', store, ['chats', 'list', '--list', deleted[0][0]], expect_ok=False)
                assert 'deleted' in (failed.get('error') or '')
                print('  Deleted definition omitted; ID filter refuses tombstone (expected).', flush=True)
    return {'label': label, 'phase': phase, 'definitions': len(visible), 'completeness': 'unknown', 'full_sync_events_seen': data['full_sync_events_seen'], 'filters': filtered}


def read_data(binary, store, readonly):
    # Retrieval output is counted, not published. No contact names/text/JIDs.
    for args in [['chats', 'list', '--limit', '3'], ['contacts', 'search', 'Synthetic', '--limit', '3']]:
        doc, _ = cli(binary, store, args, readonly)
        assert doc['data'] is None or isinstance(doc['data'], list), 'retrieval result shape'
        print('  retained retrieval rows=' + str(len(doc['data'] or [])), flush=True)
    with connect(store / 'wacli.db') as db:
        row = db.execute('SELECT chat_jid,msg_id FROM messages ORDER BY chat_jid,msg_id LIMIT 1').fetchone()
    if row:
        doc, _ = cli(binary, store, ['messages', 'show', '--chat', row[0], '--id', row[1]], readonly)
        assert doc['data'], 'message no longer readable'
    doc, _ = cli(binary, store, ['messages', 'search', 'Synthetic', '--limit', '3'], readonly)
    print('  FTS search executed successfully (contents suppressed)', flush=True)


def scenario(label, store, stock, production):
    print('\n=== ' + label + ' ===', flush=True)
    dbpath = store / 'wacli.db'
    before = capture(dbpath)
    save(label + '-before', before)
    if production:
        assert {'lists', 'chat_lists', 'list_sync_state'} <= set(before['tables']), 'production lacks inherited Lists tables'
        registered36 = 36 in before['migration_versions']
        print(f'  Source boundary: real-account fork-ingested Lists snapshot, registered migration36={registered36}; not a stock-to-Lists upgrade.', flush=True)
        print('  Existing migration versions: ' + json.dumps(before['migration_versions']), flush=True)
    else:
        print('  Source boundary: synthetic fixture written by committed stock API (plus legacy #498 API for legacy36 fixture).', flush=True)
    behavior_before = behavior(store, label, 'before-upgrade')
    # Opening a reader must not migrate or mutate any logical row.
    ro = capture(dbpath, before)
    assert compare(before, ro)['changed_original_tables'] == []
    assert ro['migration_versions'] == before['migration_versions']
    print('  PASS read-only: fingerprints and migration versions unchanged', flush=True)
    cli('head', store, ['lists', 'list'], readonly=False)
    read_data('head', store, True)
    after = capture(dbpath, before)
    save(label + '-after-upgrade', after)
    diff = compare(before, after)
    save(label + '-upgrade-comparison', diff)
    print('  Upgrade comparison: ' + json.dumps(diff, sort_keys=True), flush=True)
    assert not diff['missing_tables'] and not diff['changed_original_tables'], 'upgrade changed original logical data'
    assert 36 in after['migration_versions'] and set(diff['added_migration_versions']) <= {36}, 'migration 36 missing/unexpected migration'
    behavior_after = behavior(store, label, 'after-upgrade')
    if production or label == 'synthetic-legacy36':
        assert behavior_before['filters'] == behavior_after['filters'], 'inherited Lists behavior changed'
    read_data(stock, store, True)
    read_data(stock, store, False)
    reopened = capture(dbpath, before)
    save(label + '-after-stock-reopen', reopened)
    reopen_diff = compare(before, reopened)
    save(label + '-reopen-comparison', reopen_diff)
    print('  Stock reopen comparison: ' + json.dumps(reopen_diff, sort_keys=True), flush=True)
    assert not reopen_diff['missing_tables'] and not reopen_diff['changed_original_tables'], 'stock reopen changed original data'
    behavior(store, label, 'after-stock-reopen')
    assert not list(store.glob('session*'))
    results.append({'scenario': label, 'stock_reopen': stock, 'production_lineage': production,
                    'preexisting_lists_migration36': 36 in before['migration_versions'],
                    'passed': True, 'before_behavior': behavior_before, 'after_behavior': behavior_after,
                    'upgrade': diff, 'stock_reopen_comparison': reopen_diff})
    print('PASS: original rows/settings/Lists retained; stock read-only+writable reopen; integrity/FKs clean.', flush=True)


def main():
    for label, stock in [('synthetic-stock250', 'stock250'), ('synthetic-stock251', 'stock251'), ('synthetic-legacy36', 'stock251')]:
        store = PRIVATE / label
        seed('seed-' + stock, store)
        if label == 'synthetic-legacy36':
            p = subprocess.run([str(BIN / 'seed-legacy'), str(store)], env=env, capture_output=True)
            assert p.returncode == 0, 'legacy seed failed'
        scenario(label, store, stock, False)
    sources = sorted(Path(os.environ['PROOF498_PRODUCTION_ROOT']).glob('*/store/wacli.db'))
    assert len(sources) == 5, 'expected exactly five production wacli.db paths'
    for n, source in enumerate(sources, 1):
        label = 'production-copy-' + str(n)
        store = PRIVATE / label
        backup(source, store / 'wacli.db')
        print('\nBackup ' + label + ': SQLite backup API, source mode=ro, ONLY wacli.db, source path withheld.', flush=True)
        scenario(label, store, 'stock251', True)
    (ROOT / 'results.json').write_text(json.dumps(results, indent=2, sort_keys=True) + '\n')


try:
    main()
except Exception as exc:
    # Suppress traceback locals/paths and raw SQL/CLI errors for publication.
    print('FAIL: ' + type(exc).__name__ + '; details suppressed to protect private data. Last completed step is above.', flush=True)
    print('Code locations: ' + ', '.join(f'{Path(f.filename).name}:{f.lineno}' for f in traceback.extract_tb(exc.__traceback__)), flush=True)
    sys.exit(1)
