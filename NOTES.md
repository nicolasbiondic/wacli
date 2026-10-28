# Regular app-state real-transport proof

Test: `internal/wa/regular_transport_test.go`, `TestRegularRealTransport`, identical on `upstream/quick-replies` and `upstream/quick-replies-v2`.

## Reproduce

From either worktree:

```bash
flock /tmp/opencode/heavy.lock bash -c 'bash /tmp/opencode/wait-load.sh 1400 >/dev/null; GOWORK=off go test ./internal/wa -run "^TestRegularRealTransport$" -v -count=1 -timeout=90s'
```

`qr-only.log` and `qr-v2.log` are the unedited verbose test outputs. The negative control is an assertion of the broken outcome, so the test suite passes while logging `EXPECTED CONTROL FAILURE` and the resurrected row. It is not a simulated `SendAppState` implementation.

Both syncd writes and fetches use IQ `type=set` in the pinned SDK. The trace prints the decoded type with `/write` or `/fetch` to distinguish their purposes.

## Boundaries

- Uses the real client and `ConnectContext`, with the SDK's exported `MessengerConfig.WebsocketURL` and websocket HTTP-client setting pointing at `httptest` on loopback.
- Performs Noise XX, including certificate signature verification. A generated test CA temporarily replaces the exported `whatsmeow.WACertPubKey` and is restored after this nonparallel test. There is no verification bypass, unsafe field access, SDK fork or production-code change.
- Uses a disposable paired device/session SQL store with synthetic app-state keys. The peer decrypts the client finish and both sides exchange real AES-GCM Noise frames. All IQ responses travel back over the websocket through the client's normal frame handler and response waiters; there is no direct `ReceiveResponse` injection.
- Public app create/edit/delete methods call the production WA boundary, `sdkRegularAppStateSender` and `sendRegularAppState`, with the real SDK `SendIQ`, encode, MAC verification, apply and fetch paths. The peer validates accepted patches against its own independent SQL-backed SDK processor and serves a patch log, including full replay from version zero instead of external snapshots.
- The app's real persistence owner writes `wacli.db`. An observational SQL trigger records each committed reply upsert; it does not replace persistence. SDK cursor logging delegates to the original session SQL store.
- This is app-layer proof, not an executed CLI/delegated follow-owner process, pairing/login-bootstrap proof or live WhatsApp/account acceptance. The test exercises the same app writer and persistence owner with a seeded paired device. No real account, production endpoint or credentials are used.

## Expected evidence

1. Ordered writes: accepted patches are create v1, A v2, B v3, deletion v4. The SDK and peer hashes/versions agree. SQL shows B then the tombstone; no later upsert resurrects it. The active persistence handler receives no detached quick-reply callbacks from the revised sender.
2. Conflict: the peer injects a foreign same-index v2 only after receiving the v1-based write, returns 409 with v2, then accepts the real retry as v3. SDK apply logs show v2 then v3. Owner SQL records optimistic retry value, foreign batch, retry batch, ending on the retry value. Exactly one conflict and matching final hashes are asserted.
3. Legacy control: real public `Client.SendAppState` applies A but a channel gate delays its detached callback before persistence-owner admission. B and delete persist first. Releasing A then stores A with `deleted=false`, while the peer/SDK still agree on v4's tombstone. This isolates callback ordering from protocol/hash correctness.

Full pre-write replay can show earlier historical values before the next write; those rows are labeled by SQL sequence and are not late send echoes. The ordered scenario specifically rejects any resurrection after its deletion.

Cleanup waits for a peer-initiated disconnect before manual client cleanup, avoiding the pinned SDK's separate manual-stop/read-pump race over `FrameSocket.OnDisconnect`.

## Verification artifacts

- `race-repeat.log`: five repetitions of all three scenarios under `go test -race` on the standalone branch.
- `qr-only.make-check.log` and `qr-v2.make-check.log`: full `make check` logs, using the specified GoReleaser 2.18.2 binary and heavy-command lock.
- Commit IDs and the final result are recorded in `../A4-TRANSPORT-REPORT.md`.
