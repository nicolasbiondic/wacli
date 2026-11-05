#!/usr/bin/env python3
"""Counts/SHA-256 only. Adapted from upgrade-proof-539/fingerprint.py.

Hash every original logical column; physical FTS/SQLite tables are excluded.
No row contents, migration names, tenant paths or private identifiers emitted.
"""
import hashlib
import json
import sqlite3


def quote(name):
    return '"' + name.replace('"', '""') + '"'


def connect(path):
    return sqlite3.connect(path.as_uri() + '?mode=ro', uri=True, timeout=30)


def digest(db, table, columns, versions=None):
    names = ','.join(map(quote, columns))
    where, args = '', []
    if versions is not None:
        where = ' WHERE version IN (' + ','.join('?' for _ in versions) + ')'
        args = versions
    h, count = hashlib.sha256(), 0
    for row in db.execute(f'SELECT {names} FROM {quote(table)}{where} ORDER BY {names}', args):
        raw = json.dumps(row, ensure_ascii=True, separators=(',', ':'),
                         default=lambda v: {'blob': v.hex()}).encode()
        h.update(len(raw).to_bytes(8, 'big'))
        h.update(raw)
        count += 1
    return {'rows': count, 'sha256': h.hexdigest()}


def capture(path, baseline=None):
    with connect(path) as db:
        assert db.execute('PRAGMA integrity_check').fetchall() == [('ok',)], 'integrity check failed'
        assert not db.execute('PRAGMA foreign_key_check').fetchall(), 'foreign-key check failed'
        names = [r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'messages_fts%' ORDER BY name")]
        out = {'tables': {}, 'migration_versions': [r[0] for r in db.execute('SELECT version FROM schema_migrations ORDER BY version')], 'integrity': 'ok', 'foreign_key_errors': 0}
        for name in names:
            schema = db.execute(f'PRAGMA table_info({quote(name)})').fetchall()
            columns = [r[1] for r in schema]
            old = (baseline or {}).get('tables', {}).get(name)
            compared = old['columns'] if old else columns
            assert set(compared) <= set(columns), 'original column removed: ' + name
            versions = baseline['migration_versions'] if baseline and name == 'schema_migrations' else None
            original_schema = [r for r in schema if r[1] in compared]
            schema_hash = hashlib.sha256(json.dumps(original_schema, separators=(',', ':')).encode()).hexdigest()
            out['tables'][name] = {'columns': columns, 'original_column_schema_sha256': schema_hash,
                                   **digest(db, name, compared, versions)}
        return out


def compare(before, after):
    missing = sorted(set(before['tables']) - set(after['tables']))
    changed = [name for name, old in before['tables'].items()
               if name in after['tables'] and any(old[k] != after['tables'][name][k] for k in ('rows', 'sha256', 'original_column_schema_sha256'))]
    return {'missing_tables': missing, 'changed_original_tables': changed,
            'new_tables': sorted(set(after['tables']) - set(before['tables'])),
            'added_migration_versions': sorted(set(after['migration_versions']) - set(before['migration_versions']))}
