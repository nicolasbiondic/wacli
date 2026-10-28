# #541 / #539 runtime trace through the revised regular sender, real transport client

`go test -run TestRegularRealTransport -v ./internal/wa` at #541 `8c0d449` (identical test on #539 `ad4a713`). A real `*whatsmeow.Client` connects with `ConnectContext` over Noise XX to an in-process syncd peer (test CA replaces the SDK trust root only inside the test; the paired device is synthetic). Every write goes through the production `sendRegularAppState`/`sdkRegularAppStateSender`, encrypted IQs, SDK encoding, MAC verification, apply and fetch, and the real persistence owner. The peer validates each patch MAC against its own SQL-backed state.

- `1-ordered-edits-delete.png`: create → A → B → delete; server v4, SDK/server LTHash agree, owner ends at B/deleted.
- `2-injected-409.png`: a foreign same-index patch at v2 → one 409 → SDK applies v2 then the retry v3; the owner persists the conflict before the retry.
- `3-legacy-control.png`: the public `SendAppState` path with its detached callback held: late A resurrects the deleted row (expected control failure).
- `trace-*.txt`: full `-v` output on both branches; `race-repeat.txt`: five `-race` repetitions.
