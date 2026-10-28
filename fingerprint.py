#!/usr/bin/env python3
"""Logical table fingerprints and strict original-column upgrade comparison.

Adapted from proof/528-upgrade's parent. No live database is ever opened here.
SQLite internals and physical FTS tables are excluded; CLI FTS search is checked
separately. No original logical data/settings columns are excluded.
"""
import hashlib
import json
import sqlite3
import sys


def quote(name):
    return '"' + name.replace('"', '""') + '"'


def digest(db, table, columns, versions=None):
    names = ','.join(quote(c) for c in columns)
    where = ''
    args = []
    if versions is not None:
        where = ' WHERE version IN (' + ','.join('?' for _ in versions) + ')'
        args = versions
    rows = db.execute(f'SELECT {names} FROM {quote(table)}{where} ORDER BY {names}', args).fetchall()
    encoded = json.dumps(rows, ensure_ascii=True, separators=(',', ':'),
                         default=lambda value: {'blob': value.hex()}).encode()
    return {'rows': len(rows), 'sha256': hashlib.sha256(encoded).hexdigest()}


def capture(db):
    result = {'tables': {}, 'migration_versions': []}
    tables = db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'messages_fts%' ORDER BY name").fetchall()
    for (table,) in tables:
        columns = [dict(zip(('cid', 'name', 'type', 'notnull', 'default', 'pk'), row))
                   for row in db.execute(f'PRAGMA table_info({quote(table)})')]
        result['tables'][table] = {'columns': columns, **digest(db, table, [c['name'] for c in columns])}
    result['migration_versions'] = [r[0] for r in db.execute('SELECT version FROM schema_migrations ORDER BY version')]
    return result


def compare(db, baseline):
    current = capture(db)
    for table, old in baseline['tables'].items():
        assert table in current['tables'], f'lost table {table}'
        actual_columns = {c['name']: c for c in current['tables'][table]['columns']}
        for column in old['columns']:
            assert actual_columns.get(column['name']) == column, f'changed original column {table}.{column["name"]}'
        projected = digest(db, table, [c['name'] for c in old['columns']],
                           baseline['migration_versions'] if table == 'schema_migrations' else None)
        assert projected['rows'] == old['rows'] and projected['sha256'] == old['sha256'], f'changed original rows in {table}: {projected} != {old}'
        print(f'IDENTICAL {table:34s} rows={old["rows"]:3d} sha256={old["sha256"]}')
    versions = set(current['migration_versions'])
    assert {36, 38} <= versions, 'missing Lists/quick-replies migrations'
    assert versions - set(baseline['migration_versions']) <= {36, 38}, 'unexpected migration'
    assert db.execute('SELECT count(*) FROM lists WHERE mute_end_time_ms IS NOT NULL').fetchone()[0] == 0
    assert db.execute('SELECT count(*) FROM chat_lists WHERE applied_order != 0').fetchone()[0] == 0
    assert db.execute('SELECT full_sync_events_seen FROM quick_reply_sync_state WHERE id=1').fetchone() == (0,)
    assert db.execute('PRAGMA integrity_check').fetchone() == ('ok',)
    assert not db.execute('PRAGMA foreign_key_check').fetchall()
    print('PASS: all original logical rows/settings preserved; only additive schema/migration rows; new coverage incomplete; SQLite integrity/FKs clean.')


with sqlite3.connect(f'file:{sys.argv[2]}?mode=ro', uri=True) as conn:
    if sys.argv[1] == 'capture':
        print(json.dumps(capture(conn), indent=2, sort_keys=True))
    elif sys.argv[1] == 'compare':
        with open(sys.argv[3]) as f:
            compare(conn, json.load(f))
    else:
        raise SystemExit('usage: fingerprint.py capture DB | compare DB BASELINE.json')
