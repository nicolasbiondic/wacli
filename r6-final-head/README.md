# PR #528: final-head compatibility, round 6

Head: `fc8eaa65364a` (complete Git object ID in `head.json`).
NEW is built with Go 1.27.4 and `sqlite_fts5`.
OLD is the official v0.26.0 Linux amd64 release. Its archive matches the
published checksum:

sha256=2c28f65e2f9aaf32585288229db1f916c6c5fb5db9513715017d528513604b44

## Real-store inventory

PASS: five stores, all twenty strict phases. The expected inventory is five stores, with four phases each:
NEW read-only, NEW writable, OLD read-only and OLD writable. This uses the
unmodified kit's differential journal control: only `change_state` and
`message_changes` may differ, and only if OLD itself changes the same allowlisted
table on a separate fresh writable control. Every tolerance is reported.

## Populated stock v0.26.0 inventory

PASS: five populated stock-schema stores, all twenty phases with the explicit
exact-trigger-extension assertion below. These are not claimed to be untouched historical stock
databases. The official binary creates each empty stock schema. The shared
`stockify.py` then projects every ordinary table present in that schema from a
SQLite read-only backup of each real store, restricted to stock columns.
`schema_migrations` stays exactly as the official binary wrote it. Stock FTS is
rebuilt with stock's visible-message projection, and the official binary
successfully reads chats, messages and search before the upgrade run.

The strict kit intentionally rejects migration 37's extension of
`retrieval_messages_update`. `stock-strict/` retains the strict FAIL unchanged.
The separate `stock-trigger-assertion.json` proves the exact extension: removing
exactly one `OR old.kind IS NOT new.kind` token sequence reproduces the original
trigger. It checks all four NEW/OLD phases for all five populated stock stores;
all other original rows, settings, columns, constraints, indexes, triggers,
views, migrations, integrity, foreign keys and read-only writes retain the
strict kit's invariants, including its reported differential journal control.
This explicit assertion is not presented as a strict kit pass.

## Safety and limits

Only the SQLite read-only/query-only backup API accesses real `wacli.db` source
stores. No production daemon, service, host, deployment or session database is
accessed. Every disposable database and derived inventory is deleted on exit.
Binary commands are local chats/messages/search retrieval only. Published
reports contain counts and assertions, no source paths, private rows, identifiers,
SQL or private-row digests.

This proves the recorded upgrade/reopen/retrieval invariants for these
snapshots and stock-schema projections, not every possible historical schema
or remote WhatsApp behavior.

Nicolás (via agent)
