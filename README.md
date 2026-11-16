# Proof for openclaw/wacli#573

Branch under test: `fix/redelivery-keeps-timestamp` at `078cab2`. Base: `main` at `2108fe7`.

- `logs/real-transport-*.log`: `TestRedeliveryKeepsOrderRealTransport` (real `App.Sync` over Noise XX/WebSocket to an in-process peer, Signal-encrypted messages, store reopened, redelivery with a stanza time 40 minutes later, real `wacli messages list`). `main` fails, head passes.
- `logs/store-tests-*.log`: the store regressions (`TestUpsertMessage...`) against `main`, against the previous head `ad8cced` (which the review showed loses the stale-media freshness check) and against the head.
- `field-evidence.md`: production aggregates.

Reproduce:

    go build -tags sqlite_fts5 -o /tmp/wacli ./cmd/wacli
    WACLI_PROOF_CLI=/tmp/wacli go test -tags sqlite_fts5 ./internal/wa -run TestRedeliveryKeepsOrderRealTransport -count=1 -v

Not proven: pairing/login, the remote service's policy, the phone UI. The peer is synthetic.
