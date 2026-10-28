# #541 data-model compatibility on real stores

Five real production `wacli.db` files (one per WhatsApp line we run; 4,718 to 194,415 messages, 24 to 34,764 chats), copied with SQLite's online backup API from a read-only connection. `session.db` was never copied, so no copy could connect to WhatsApp, and no command created one. These stores were written by our patched 0.24.0 build: migration row 34 is already occupied by a fork migration and a fork-only row 9035 exists, which is the collision case the schema guard is for.

Per store (`run.sh`, `transcript.txt`):
1. #541 head `34c80b0` (`binary-sha256.txt`), read-only: `quick-replies list` → empty, `incomplete`, no migration.
2. Writable open: adds only migration 38 and the `quick_replies` / `quick_reply_sync_state` tables; `chats list` and FTS `messages search` work.
3. Every original table: same columns, row count and SHA-256 over its original columns (`*-compare-upgrade.txt`, `fingerprint-541.py`).
4. Official v0.25.1 (`checksums-0.25.1.txt`) reopens read-only **and** writable; `chats list` and FTS search work; tables still identical (`*-compare-stock.txt`); integrity and foreign-key checks clean.

Only table names, counts and hashes are published; no message, contact or chat content. Store names are replaced by `line-N`.
