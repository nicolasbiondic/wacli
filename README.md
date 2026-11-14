# Evidencia de wacli

Rama huérfana: solo artefactos revisados. El alcance y las limitaciones de cada prueba deben constar en el transcript. El escáner no hace OCR: revisar visualmente las imágenes antes de publicar.

# PR #560 data-model compatibility proof (13-nov-2026)

Kit: `upgrade-proof.sh NEW OLD` (SQLite backup copy, integrity/FK, per-table row+hash comparison, read-only and writable runs of NEW, then OLD v0.26.0 on the upgraded store, i.e. rollback). Result: **PASS**, 4 phases (see transcript.txt, results.json).

Store: one synthetic stock v0.26.0 store (created by the official v0.26.0 binary; 2 chats incl. unread count and pin, 4 messages, 1 contact). Migration 34 adds chats.read_through_ts/read_through_rowid; all original tables keep row counts and hashes; OLD v0.26.0 still opens the upgraded store.

## Not proven
- Production copies were NOT used for the pass: official v0.26.0 itself fails the kit's writable phase on them (change_state/message_changes rows change on a writable open), and their schema_migrations already hold fork-applied versions 34, 36, 38, 9033, 9035. Version 34 is therefore recorded, so this PR's migration 34 is skipped there. This matters only for fork-patched stores, not stock v0.26.0 stores; renumbering is an open item.
- No live WhatsApp account; no session.db touched.



