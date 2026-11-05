# PR #533 verification

Reviewed baseline: `75ba671382b0d109f4833b50fd025a76ec672bb7`.
Main was fetched over HTTPS and merged (not rebased) at `616b348a0c74e96ef325016ae9db4894527c120f`; merge commit `c110b31`.

Repair commit: `a139ab1`, pushed without force to `nicolasbiondic/wacli:fix/own-message-marks-read`. No GitHub comments were posted. `pr.diff` contains the complete PR diff against merged main; `final.diff` contains only this repair after the main merge.

## Regression evidence

`regressions-before.log` runs the four new regressions on an exact `git archive 75ba671` source tree, with only the saved `baseline-tests/` test files overlaid. All four fail on their unread-state assertions (not compilation or environment errors):

- `TestHistoryStoredOwnReplyPreservesManualUnread`: duplicate history reply clears a later manual marker; newly decoded placeholders acknowledge only once.
- `TestHistoryOwnReplyPreservesUnresolvedSameSecond`: both history batch orders, with counted and marker-only unread state.
- `TestLiveTombstonedOwnReplyReplayPreservesUnread`: revoked and deleted-for-me live replay, then history replay. The final version additionally covers payload-purged tombstones.
- `TestPersistOutboundVotePreservesUnread`: counted and marker-only state at the producer shared by both vote paths.

`regressions-after.log` records the passing targeted tests, including the existing explicit-read boundary suite. `poll-paths.log` additionally executes the direct Cobra command and delegated vote executor with the existing fake send transport and real disposable SQLite stores. It checks a successful send and persisted vote without acknowledging either counted or marker-only unread state. Only application acquisition/authentication and network are fake in that command-path proof; delegation's IPC handshake is not under test.

## Real transport boundary

`internal/wa/own_message_transport_test.go` is a repeatable repository test. The Noise responder is adapted from `qr-only/internal/wa/regular_transport_test.go`.

The path is **App.Sync -> real WA client -> real whatsmeow -> certified Noise XX/WebSocket -> in-process peer**, in both directions. Messages use actual Signal prekey/session encryption and SDK decryption; there is no synthetic `events.Message` dispatch or socket response injection. Assertions wait for both the runtime event (after the app handler) and the peer's receipt of the SDK's real delivery (`inactive`) or own-device (`sender`) receipt. The peer returns a real wire acknowledgement for each receipt. Successful encrypted messages use receipts, not `ack class=message`; that distinction was verified against the pinned SDK during harness development.

The trace demonstrates unread before a reply, cleared state after a fresh own-device reply, replay preserving a newer manual marker, and insertion-order boundaries for same-second arrivals. This PR changes local unread metadata, not outbound read-receipt policy; the peer rejects unexpected read/read-self receipt types.

Limits: devices, identities, certificate authority and stores are freshly generated. Pairing/account login bootstrap, WhatsApp service behavior, a physical phone's UX, outbound CLI message encryption and full remote history downloads are **not** claimed. History replay is covered by the app regressions. No real WhatsApp connection or existing session database is used.

## Data-model compatibility

`compatibility.py` is the full offline reproduction. The specified `$TMP` and `fingerprint-541.py` were absent; this recreates their read-only SQLite-backup/per-table-fingerprint method, using the surviving `quick-replies/upgrade-proof-539` references for context.

- Two disposable populated stores are created by the unmodified stock **v0.25.1** CLI, including counted unread, manual markers, own replies, contacts, and tombstones.
- Exactly five production **wacli.db** files are read only through `mode=ro`, `query_only=ON` connections and SQLite's backup API. Each copy is isolated under a private temporary directory. Neither sidecars nor any `session.db` are copied.
- Only the copies are passed to the candidate and stock CLIs. Each executes writable `chats list`, `messages list` and FTS `messages search`; stock then reopens in both read-only and writable modes.
- Fingerprints include full table columns, per-table row counts and SHA-256 contents, migration rows/versions, the complete sqlite_master schema hash, FTS virtual/shadow tables, and `integrity_check`. No columns or logical tables are excluded. Private row contents are never logged.
- Before/after/stock-reopened fingerprint files and `compatibility.log` retain only anonymous evidence. All copied/synthetic databases are deleted in `finally`, including on failures. No daemon command is issued.
- `schema.diff` is empty: the schema, migrations and generated store queries are unchanged even relative to v0.25.1.

## Commands

Run from `~/code/new-pedrito/wacli-fork/own-message-marks-read`. Every build/test/compatibility command uses the shared heavy lock:

```sh
flock $TMP bash -c 'bash $TMP 1400 >/dev/null; go test -v ./internal/app ./internal/store ./cmd/wacli -run "Test(History.*Own|Live.*Own|OwnMessage|OwnPlaceholder|PersistOutboundVote|PollVoteExecutionPaths|ExplicitReadBoundary)" -count=1'
flock $TMP bash -c 'bash $TMP 1400 >/dev/null; go test -v ./internal/wa -run "^TestOwnReplyRealTransport$" -count=1'
flock $TMP bash -c 'bash $TMP 1400 >/dev/null; go build -tags sqlite_fts5 -o $TMP ./cmd/wacli && python3 ~/.local/share/vendas-plan/wacli-ronda-nov/proof-533/compatibility.py $TMP'
flock $TMP bash -c 'bash $TMP 1400 >/dev/null; make check GORELEASER=$HOME/.local/share/vendas-plan/tools/goreleaser-2.18.2/goreleaser'
```

Gate output is retained as `make-check.log`. `transcript.txt` assembles the key before/after and transport evidence. `final.diff` records the repair diff relative to the merged main/PR state; proof-only files live outside the repository.

## Final results

- Full `make check GORELEASER=.../goreleaser-2.18.2/goreleaser`: **PASS**, including both Go suites and release configuration checks.
- Targeted plain and FTS regressions: **PASS**. All four corresponding tests fail on `75ba671` (`regressions-before.log`). `reproduce-baseline.sh` rebuilds that exact test-only overlay and checks for all four failures.
- Real transport: **PASS**, three consecutive runs plus the FTS and targeted race runs. No unexpected read/read-self receipt was emitted.
- Targeted `-race` runtime/app/CLI tests: **PASS** (`race.log`; the selected names intentionally do not select store-only tests).
- Compatibility: **7/7 PASS**, all schema/table/migration fingerprints identical after new opens and stock read-only/writable reopens. Synthetic stock stores: 31 tables, 33 migrations, 6 messages each. Production copies: 334,676 messages total, preserving all preexisting 34/35 tables and 37/38 migrations. FTS shadow tables are included, not ignored.
- Compatibility temporary copies: **removed**, including the synthetic databases. Existing session databases and production daemons: **untouched**.
- Proof runner syntax (`bash -n`, Python AST parse) and `git diff --check`: **PASS**.

Stock binary SHA-256: `a211b0da0258858874ddd759a5357b21c581005d39bec64b326a4de7f7fef5cb` (`wacli 0.25.1`). Only generated test credentials and synthetic chat identities are used by the transport/command tests; terminal logs replace synthetic numeric JIDs with `<synthetic-chat>`.
