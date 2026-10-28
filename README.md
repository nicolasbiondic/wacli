# PR #539 offline compatibility proof

Result: **PASS**, implementation `99da71b7646dff893a8d0aac04e0c5e2edc02ac3`.
No WhatsApp account, session database, live store, daemon or production data was used.
All phone-shaped IDs, contact names and message bodies in the transcript are synthetic.

## Source and fixture ownership

- Stock v0.25.1: `b7603b697e69be591afa83379ced1e73177604c6`.
- Actual #498 Lists implementation: `bbd627ebc4960002ae840238befe827c27beb3c0`.
- Stock and new CLIs were compiled from unmodified committed archives with FTS5.
- `seed-stock.go` was compiled against stock's own store API. It wrote 24 messages,
  3 chats, 2 contacts, alias/tag rows, the Keep chats archived preference, pinned,
  muted and unread chat settings, and successful-sync metadata.
- A second fixture then ran `seed-lists.go`, compiled against #498's actual API:
  3 definitions (including a tombstone), 3 memberships, and historical Lists
  `full_sync_events_seen=true`. This was not SQL emulation of #498's schema.
- Module/network fetching was disabled during builds (`GOPROXY=off`, `GOSUMDB=off`).

## Results, both fixtures

1. New read-only quick-reply listing before migration: empty, `incomplete`.
   New read-only Lists listing also worked against #498 without the new mute column.
2. New writable quick-reply listing: migrations 36 and 38 present, still `incomplete`.
   #498's existing migration 36 and every original list/membership row were preserved.
3. Message show/list/search, contact search and Lists listing work on the new CLI.
4. Stock v0.25.1 reopens both upgraded stores read-only **and writable**. Messages,
   contacts, chat settings and FTS search still work.
5. Every original logical table retains exactly the original row count and SHA-256
   over its original columns at upgrade and after stock reopens. Existing column
   definitions are also checked. No original data/settings column is excluded.
6. Only additive schema is introduced; legacy mute values are NULL, legacy
   membership order defaults to zero, and independent reply coverage is false.
   SQLite integrity and foreign-key checks pass.

`schema_migrations` is compared over all original version rows, permitting only
the requested new 36/38 rows. Physical FTS internals and SQLite bookkeeping are
excluded from logical fingerprints; successful FTS CLI searches are in the transcript.
This proves offline data-model compatibility, not phone-side label behavior or
broken-collection recovery. No proof branch was created or pushed.

## Files

- `upgrade-proof.sh`, `seed-stock.go`, `seed-lists.go`, `fingerprint.py`: reproducible proof.
- `transcript.txt`: full successful run.
- `source-commits.txt`, `binary-sha256.txt`: source and binary provenance.
- `fingerprint-{stock,lists498}-{before,after-upgrade,after-stock-reopen}.json`:
  full original/current column metadata, logical counts and SHA-256s.
- `compare-{stock,lists498}-{upgrade,stock-reopen}.txt`: human-readable comparisons.
- `reference-528-*`: the upstream proof pattern, fetched over HTTPS from the parent
  of `nicolasbiondic/wacli:proof/528-upgrade`.
- `LATEST` points to `run-vgyUzC`, which additionally retains synthetic databases,
  binaries and source archives. Those runtime directories are not needed for a
  proof-only publication; publish the scripts, transcript and fingerprints above.

## Rerun

Requires the recorded Git objects and module/toolchain cache. The script creates
a fresh private fixture directory on every run and never reuses a real store.

```bash
flock $TMP bash -c 'bash $TMP 1400 >/dev/null; bash $PROOF/upgrade-proof.sh'
```

Use `NEW_REF=<commit>` to verify another committed implementation. Re-running
updates `LATEST`; the root-level transcript/fingerprint copies above are the
recorded successful `99da71b` run, not automatically replaced by a later rerun.
