# Evidencia de wacli

Rama huérfana: solo artefactos revisados. El alcance y las limitaciones de cada prueba deben constar en el transcript. El escáner no hace OCR: revisar visualmente las imágenes antes de publicar.

# PR #528: semantic unread real-transport proof

Head: `fc8eaa65364a`. Upstream main: `2108fe75b45c`.
`head.json` records the complete Git object ID as concatenated parts.

Command (Go 1.27.4):

```sh
go test -tags sqlite_fts5 -run '^TestSemanticUnreadRealTransport$' -count=1 -v github.com/openclaw/wacli/internal/wa
```

`TestSemanticUnreadRealTransport` reuses main's Noise XX/WebSocket harness and a
thin sender variant accepting arbitrary protobuf messages. The receiver runs
real `app.New`, `OpenWA`, `App.Sync`, whatsmeow and Signal decryption. No injected
message handler or fake WAClient drives production persistence.

## Observed paths

- Incoming protobuf poll, contact card, contact array and event messages have no
  conversation text on the wire. SQL stores `poll`, `contact`, `contacts`, `event`,
  respectively, without a downloadable media type. The named event and minimal
  card/array fixtures exercise semantic-only payloads, not fallback body parsing.
- A semantic-only event is stored once. Redelivery of the same message identity,
  sender and timestamp before reading leaves one row and one unread message.
  Fresh Signal encryption is intentional: replaying identical ciphertext would
  stop at the SDK's cryptographic replay guard and would not test live dedup.
- An incoming `read-self` stanza clears that event through `App.Sync`.
  Redelivery after this successful read retains one row and zero unread, without
  direct test mutation of unread metadata. The red control also completes this
  read successfully before replaying, isolating unread resurrection from receipt
  selection failures.
- The production `MarkChatReadWithReceiptsThrough` entry point selects semantic
  anchors, fetches privacy via the socket, sends a real read receipt through
  whatsmeow/Noise/WebSocket, and updates SQL.
- An older text followed by a later semantic-only event starts at two unread.
  A receipt bounded to the text selects only that ID and preserves one unread;
  an explicit receipt through the later event selects it and clears the last one.
- Independently, a real incoming `read-self` stanza goes through `App.Sync` and
  its production receipt-persistence handler. Its read-at timestamp is later
  than both messages, but its stored text ID still bounds the recount and leaves
  the later semantic-only event unread. This isolates bounded recount from
  outgoing receipt eligibility in the red control.

Production map: `handleLiveSyncMessage` ->
`shouldIncrementLiveUnread`/`liveUnreadPlaceholder` in
`internal/app/sync_events.go`; `markChatReadWithReceipts` ->
`ReadReceiptCandidatesThrough`/`receiptEligible` in
`internal/store/read_receipts.go`; and
`ClearChatUnreadThroughPreservingMarker`/`clearChatUnreadThrough` with
`unreadContentPredicate` in `internal/store/unread.go`. The independent incoming
receipt path is `handleReceiptPersistenceEvent` -> `handleReceiptEvent` ->
`receiptReadPosition` -> `clearChatUnreadFromEvent`.

## Red control

`base-fail.txt` runs the identical test on a detached copy of this head with only
the PR's three unread/receipt changes reversed. `production-reversal.txt` is the
complete production diff: remove the semantic-kind placeholder guard, restore
pre-fix receipt eligibility, and restore the pre-fix recount content predicate.
The schema and parser remain, so failures distinguish unread behavior from a
missing `kind` field or a compile failure. `head-pass.txt` is the full passing
verbose output at the published PR head.

All identities are synthetic; numeric identifiers in error output are redacted.
The complete verbose log bodies are retained. Public subtest paths are spaced
around their slash, and the head is abbreviated (full ID in `head.json`), solely
to avoid the scanner's long-token false positives. The displayed package import
path is equivalent to the relative package argument used by the recorded run.
The screenshot uses `terminal-excerpts.txt`: selected unchanged output lines,
with testing-framework source-location prefixes omitted to keep it readable.
No real account, production daemon or production session store is involved.
This proves neither Cobra/IPC command wiring nor pairing/login, the remote
service's policy or phone UI. The receipt API is the production implementation
shared by direct and delegated `chats mark-read --receipts --through-message-id`.

Nicolás (via agent)


![Terminal](shots/terminal-01.png)
