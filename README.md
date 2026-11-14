# Evidencia de wacli

Rama huérfana: solo artefactos revisados. El alcance y las limitaciones de cada prueba deben constar en el transcript. El escáner no hace OCR: revisar visualmente las imágenes antes de publicar.

# PR #560 real-transport proof (14-nov-2026)

Head `505ed6b` (production code identical to reviewed `5ccc0a0`; only a `_test.go` was added). Test `TestReadPositionRealTransport` in `internal/wa/read_position_transport_test.go`:

```
go test -tags sqlite_fts5 -run '^TestReadPositionRealTransport$' -count=1 -v .[LOCAL-PATH]
```

## What it exercises
Real `App.Sync` -> whatsmeow -> Noise XX/WebSocket -> in-process peer; every stanza is Signal-encrypted. The log prints unread state and the persisted `read_through_*` after each step (times relative to a base):
1. two incoming messages: unread 2;
2. own-device reply crosses the socket: unread 0, read position persisted;
3. `HistorySyncNotification` from the own primary device (inline bootstrap payload, no media download) reports the stale unread count 2: the chat stays at 0;
4. store reopened (second `App.Sync`), same history replayed: still 0;
5. a later incoming message beyond the boundary: 1; history reporting 1: still counted;
6. P2 case (chat B): a read-self receipt naming no stored message clears unread but persists no read position (`ts=0`), so a later history with an older unread message is counted.

## Red controls
- `base-fail.txt`: same test on openclaw main `2108fe7`: the stale count returns (2, then 3 after the new message).
- `p2-fail.txt`: same test on `c3925e6` (before the P2 fix): the receipt time becomes the read position and suppresses the older unread message in chat B.
- `head-pass.txt`: PR head.

## Not proven
Pairing/login, the remote service's history policy, the phone UI. Identifiers are synthetic. The compatibility proof (migration 39, v0.26.0 rollback) is in `proof/560-compat-nov13` and is unchanged: no production file differs from the proofed head.

![Terminal head](shots/terminal-01.png)
![Terminal red control](shots/terminal-02.png)


![Terminal](shots/terminal-01.png)
![Terminal](shots/terminal-02.png)
