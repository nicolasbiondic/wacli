# PR #567 data-model compatibility proof (14-nov-2026)

Head `0e769f0`; production code identical to `dc6a206` (renumbered migration 40, `message_contacts`). Kit `upgrade-proof.sh` (edec541), NEW = PR head `go build -tags sqlite_fts5`, OLD = official v0.26.0 (release tarball checked against `checksums.txt`).

## Populated stock v0.26.0 stores
Five inventories were derived like this: the schema of each store was created by the **official v0.26.0 binary** on an empty store; ordinary rows and settings (23 stock tables) were transplanted from SQLite backups of five real stores (`mode=ro`, `query_only`; only `wacli.db`, never `session.db`), restricted to the stock columns; `schema_migrations` left as stock wrote it; FTS rebuilt as stock does; v0.26.0 then read chats, messages and search back. Per-store assertions: `stock-inventory.json`. The inventories were deleted after the run.

## Result: PASS
Per store, four phases (`transcript.txt`, `results.json`): NEW read-only (nothing changes), NEW writable (migration 40 applied, table `message_contacts` added, no original table changed, integrity/FK ok), OLD v0.26.0 read-only and writable on the upgraded copy (rollback ok).

## Not proven
Rows are copies of real data transplanted into a stock schema, not a store written by a v0.26.0 daemon. No contact rows exist before the upgrade (the table is new). Earlier fork-written real-store copies also passed (previous commit of this branch).
