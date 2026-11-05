# PR #498 compatibility and read-only runtime proof

The reviewed ask has two parts: retained stored data/settings across upgrade and stock reopen, and shipped-CLI behavior on real-account Lists. This bundle keeps the boundaries separate. The prior review named v0.25.0; both v0.25.0 and the newer stable v0.25.1 are included as synthetic baseline writers and stock reopen binaries.

## Result

**PASS**, eight scenarios at PR head `fa41b76bfa5e83709a4d88565ed179c7b75a5cf1` (2026-11-05). Full `make check` also passes; see `make-check.log`. Both writable stock reopens and the PR's writable open preserve every original logical table's row count, data hash and original-column schema hash. Integrity/FK checks pass at each fingerprint stage. No original data/settings column is exempted.

| Scenario | Messages | Chats | Contacts | List definitions including tombstones | Membership rows | Added migration |
|---|---:|---:|---:|---:|---:|---|
| Synthetic stock v0.25.0 | 24 | 3 | 2 | 0 | 0 | 36 |
| Synthetic stock v0.25.1 | 24 | 3 | 2 | 0 | 0 | 36 |
| Synthetic stock + historical #498 | 24 | 3 | 2 | 3 | 3 | none |
| Production copy 1 | 206177 | 35511 | 28225 | 7 | 15 | none |
| Production copy 2 | 70083 | 7071 | 9761 | 4 | 0 | none |
| Production copy 3 | 42994 | 2895 | 6431 | 5 | 1 | none |
| Production copy 4 | 9950 | 457 | 900 | 3 | 0 | none |
| Production copy 5 | 5475 | 24 | 1149 | 4 | 1 | 36 |

Production copy 1's four supported filters return **9, 0, 3, 3** observed chats; copy 2 and copy 3 each have a supported empty filter. System filters refuse as documented. The supported historical synthetic filters each return one chat. Empty/deleted cases are also exercised. These counts do not imply complete account coverage. Public artifacts contain no database copies; the cleanup trap deleted all private copies/archives/binaries after the successful run.

## What is executed

`run.sh` builds FTS-enabled CLIs from committed archives: v0.25.0, v0.25.1 and the November PR head. `source-commits.txt` and `binary-sha256.txt` pin provenance. The synthetic seed is compiled against each stock version's own store API, writing 24 messages, three chats, two contacts, aliases/tags, archive preferences, pin/mute/unread settings and successful-sync metadata. A third fixture also uses the actual #498 `bbd627e` API to populate migration 36 with three definitions (one deleted), three memberships and historical full-sync metadata.

Five production `wacli.db` files are independently backed up through SQLite's online backup API from URI connections with `mode=ro` and `query_only=ON`. Only that filename is opened; no tenant directory, configuration, sidecar or session database is copied. Tenant paths and identities are never written to artifacts. Copies live in a private temporary directory, and an EXIT trap deletes all databases, binaries and source archives on success or failure. No production CLI, daemon, migration or mutation is invoked.

For each disposable fixture/copy:

1. Capture original logical table counts/SHA-256 and migration version numbers. Check SQLite integrity and foreign keys.
2. Run the PR CLI read-only: `lists list` and `chats list --list` by exact ID and unambiguous exact name. Compare returned chat identifiers to an independent SQL join in memory. Publish only generic list names, definition metadata and match counts, never chat identities/content. System/computed membership requests must refuse; deleted definitions must be hidden and their ID filter must refuse.
3. Verify the read-only run changed no logical fingerprint or migration version.
4. Open with the PR CLI writable on the copy to apply migration 36/schema guards, then exercise offline chat/contact/message retrieval and FTS search. Compare every original logical table and column to its baseline.
5. Reopen with the corresponding stock CLI read-only and writable, exercise retrieval/search, and compare the original fingerprints again. Recheck the PR's Lists reads afterwards.

`transcript.txt`, `results.json` and `fingerprints/*.json` record the outcome. The transcript is a deliberately transformed CLI trace: process exit/success, counts and redacted definition summaries; raw JSON containing private data is held only in memory. No list IDs, chat identifiers, contact names, phone numbers, message bodies, private endpoints or tenant paths are published. `fingerprint.py` hashes row streams; schema/column names are public metadata. Physical FTS and SQLite bookkeeping tables are excluded, while FTS functionality is separately exercised. Original migration rows are hashed with the original version set, allowing only the added Lists row 36.

## Production-lineage caveat

All five production stores already contain fork-ingested Lists tables/data. Four register migration **36**; the fifth still records the older **34** plus local rows **9033–9035**, without row 36. This differs from the initial assumption that all five already registered 36. The proof preserves every existing migration row and requires the missing 36 registration to be additive on that fifth copy. These cases prove compatibility and useful reads of previously ingested real-account Lists, not introduction of Lists into stock data and not network ingestion by this PR binary. The synthetic stock fixtures separately prove that upgrade path. Unknown completeness remains unknown; an empty filter cannot prove an empty phone list. Earlier phone-observed actions are summarized separately in `earlier-live-excerpts.md`, with exact source blob provenance.

The five local-only commits starting at `377ee6b` were inspected before merging. They contain the older equivalent Lists history and an earlier merge of main, including an obsolete migration-34 registration. The new history keeps them, merges published `bbd627e` to retain migration 36/collision tests and newer contact hooks, then merges main `616b348`. No rebase, cherry-pick or force push is used. The only residual difference from the published equivalent work is a pre-existing archive-upgrade test that deletes its migration row by name instead of hardcoding 33.

## Reproduce safely

Read `run.sh`, `proof.py`, `fingerprint.py` and the two seed files before running. It requires exactly five `*/store/wacli.db` paths under the private `PROOF498_PRODUCTION_ROOT`; set that environment variable to an authorized source root on another machine. These are the only live files the script opens, solely for SQLite backup. The head worktree must be clean and committed. Run the entire script under the agreed heavy-command wrapper:

```bash
flock $TMP bash -c 'bash $TMP 1400 >/dev/null; bash /path/to/proof-498/run.sh'
```

The older `$TMP` and `fingerprint-541.py` were no longer present. This implementation adapts the retained `quick-replies/upgrade-proof-539` seeds/fingerprint pattern, adds read-only online backup and counts-only redaction, and does not depend on transient scripts. The owner will publish this bundle separately; no proof branch or GitHub comment is created by this run.

This is offline read/data-model evidence, not a new phone test, authentication/recovery proof or maintainer approval of the public scope.
