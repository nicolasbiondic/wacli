# PR #567 data-model compatibility proof (14-nov-2026, renumbered head dc6a206)

Kit: `upgrade-proof.sh NEW OLD` (proof kit edec541). It takes SQLite backup copies of 5 real production stores (`wacli.db` only), checks integrity/FK and per-table row counts and hashes, runs NEW read-only and writable, then OLD (official v0.26.0) on the upgraded copy (rollback). Result: **PASS**, 4 phases on each of the 5 stores (transcript.txt, results.json).

Migration 40 adds the message_contacts table. On every store the migration is newly applied (added migration 40), no original table changes, and v0.26.0 still opens the upgraded copy. The stores already record fork migrations 34/36/38/9033/9035; the renumbered migration runs regardless, and the migration is idempotent for a shape that already exists (unit tests in the PR).

## Not proven
- No live WhatsApp account; no session.db touched; copies were deleted after the run.
- Row contents of the new column/table are not exercised by this proof (unit tests cover behaviour).
