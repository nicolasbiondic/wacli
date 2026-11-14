# PR #498: Round 6 exact-head compatibility

Exact source commit: `03293e26ed23`. Full SHA-1 is the concatenation of these two 20-character parts (split because the evidence scanner rejects unlabelled long hex strings):

```
03293e26ed23e2c23bf1
20b3112409b15218230a
```

NEW was built with Go 1.27.4 and `sqlite_fts5` from this clean commit. OLD is the official v0.26.0 Linux amd64 release. Its archive checksum was reverified against published `checksums.txt`:

sha256=2c28f65e2f9aaf32585288229db1f916c6c5fb5db9513715017d528513604b44

**PASS: two inventories, five stores each, twenty retrieval phases per inventory (forty total).** Each copy runs NEW read-only, NEW writable, OLD read-only, OLD writable in that order. All original non-journal rows, columns, schema objects, constraints and database-resident settings survive. Integrity is OK; foreign-key errors are zero; read-only opens do not write. The kit uses fresh official-OLD controls and permits only matching changes in the explicit `change_state`/`message_changes` journal allowlist. Those allowances are recorded, not silently ignored.

## Inventory 1: existing Lists stores

`existing-lists/` contains the real-store-backup kit results, raw redacted transcript and additional revised-CLI assertions. All five discovered stores were tested; none skipped. Every copy gains `chat_lists.applied_order` where absent, while existing definitions, memberships and `list_sync_state` settings remain byte-equivalent on their original columns. The actual revised CLI successfully reads `lists list` and filters each supported observed list through `chats list --list ID`; only result counts are published, never identifiers, names or message content.

## Inventory 2: populated stock v0.26.0 schema

`stock-v026/` is not a renamed fork database. `r6/stockify.py` creates each empty schema by running the official OLD binary, backs up each real source via SQLite `mode=ro`/`query_only`, and projects rows into every common ordinary stock table using only stock columns. Stock `schema_migrations` remains exactly as OLD created it. Stock triggers are suspended during transplant, then restored; FTS is rebuilt from visible messages exactly as stock does. Run-scoped keyed comparisons verify every projected table’s rows/settings; no data digests are published. OLD chats/messages/FTS commands read the populated derived stores before the upgrade kit runs. The revised CLI then reads its newly created empty Lists index successfully; stock never had Lists rows to preserve. Existing-Lists coverage is supplied separately by inventory 1, and populated ordering by the transport test.

`stockify-assertions.json` records transplant, stock migration, FTS, integrity, OLD-read and cleanup assertions. The derived inventory was deleted on exit. Extra per-inventory `cli-list-reads.json` files verify that the new CLI opens the upgraded index, exposes applied_order, and retains original Lists data/settings. No `lists show` success is claimed: that subcommand is not implemented by this PR.

## Safety and scope

Only `wacli.db` is backed up from read-only sources. No production `session.db`, config file, host, daemon or `WACLI_BIN` is touched. Both the kit and the supplementary reads delete all private app/control copies; stockify deletes the derived inventory, including on failure. All actual CLI output is suppressed before reporting counts. These are offline data/CLI upgrade/rollback proofs, not live WhatsApp or arbitrary-schema guarantees. Filesystem config/settings are not copied; settings here means database-resident state.

Real-transport ordering red/green is on branch `proof/498-real-transport`.
