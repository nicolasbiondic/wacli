# Proof for openclaw/wacli#574

Branch under test: `fix/own-message-chat-name` at `aca00fa`. Base: `main` at `2108fe7`.

- `logs/real-transport-*.log`: `TestOwnMessageNamesNewChatRealTransport` (real `App.Sync` over Noise XX/WebSocket to an in-process peer). A message from another device of the account, carrying the account's push name in the stanza, reaches a chat never seen before; then a customer message; then one more own message. Real `wacli chats list` after each step. `main` names the chat after the account, head does not.
- `logs/upgrade-store-written-by-main.log`: `TestOwnMessageUpgradedStoreRealTransport` opens the store `main` wrote after the first step (chat named after the account) with the head build.
- `logs/schema-*.txt`: schema fingerprint, migration count, row counts and integrity checks of that store before and after the head build ran on it. No schema change.

Reproduce:

    go build -tags sqlite_fts5 -o /tmp/wacli ./cmd/wacli
    WACLI_PROOF_CLI=/tmp/wacli go test -tags sqlite_fts5 ./internal/wa -run TestOwnMessageNamesNewChatRealTransport -count=1 -v

Not proven: pairing/login, the remote service's policy, the phone UI. The peer is synthetic.
