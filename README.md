# Evidencia de wacli

Rama huérfana: solo artefactos revisados. El alcance y las limitaciones de cada prueba deben constar en el transcript. El escáner no hace OCR: revisar visualmente las imágenes antes de publicar.

# PR #498: exact-head real-transport ordering proof

Exact source commit: `03293e26ed23`. Full SHA-1 is the concatenation of these two 20-character parts (split because the evidence scanner rejects unlabelled long hex strings):

```
03293e26ed23e2c23bf1
20b3112409b15218230a
```

Test: `TestListsAppliedOrderRealTransport`, in `internal/wa/lists_order_transport_test.go`.

Command (both green and counterfactual):

```sh
GOWORK=off GOFLAGS=-p=2 GOMAXPROCS=4 mise exec go@1.27.4 -- go test -tags sqlite_fts5 -run "^TestListsAppliedOrderRealTransport$" -count=1 -v github.com/openclaw/wacli/internal/wa
```

## Observed production path

Real `app.New`, `OpenWA`, `App.Sync`, SDK `ConnectContext`, certified Noise XX/WebSocket, encrypted `regular` app-state IQ responses, SDK patch/snapshot MAC and LTHash verification, the production ordered persistence owner, and ordinary SQLite storage are exercised. No fake WA client, event-handler injection, exchange substitution, or direct insertion of list/membership rows is used. Test devices receive an app-state key, and the peer encodes/verifies all five patches using a separate real SDK SQL store.

Wire v1 defines list 7; v2 renames it with a backwards timestamp; v3 adds a PN membership; v4 adds the LID alias at the same timestamp; v5 removes the LID membership with a lower timestamp. The stored PN row has order 1 and the LID removal has order 3. After the app, sockets and databases close, the app reopens the same store with a persisted synthetic PN/LID mapping. Real `App.Sync` reconciles historical identities before fetching the next collection. The trace inspects this precise boundary: the later removal wins despite its older device clock. A normal authenticated full replay then retains the final removal. The peer also returns the authentic historical v3 patch against current v5; the SDK rejects its incompatible LTHash, and the app performs its normal authenticated recovery replay. Final membership remains false and application order advances.

## Red control

A detached scratch worktree uses this same exact commit and test, with only the two-line PN/LID merge comparator restored to its pre-`applied_order` timestamp arbitration from `d43e74b`’s parent. The schema, wire patches, SDK, CLI and all other code remain unchanged; `red-control-diff.txt` is the complete production delta. `base-fail.txt` records membership=true instead of false at the restart-before-fetch boundary. This check deliberately runs before later replay could conceal the faulty merge. `head-pass.txt` records the unmodified head passing. Thus the red is a behavioral failure, not a missing-column or compile failure.

## Shipped CLI and limits

The test builds the actual current CLI with `sqlite_fts5`. On the final store, `lists list` returns the Transport definition with unknown completeness; `chats list --list 7` and the name-based equivalent return no removed member. `lists show 7` is also invoked: this PR has no show subcommand, so it displays group help rather than JSON; no successful show read is claimed. The socket ERROR lines are expected normal peer-close diagnostics while disconnect is explicitly drained.

Everything in this transport fixture is synthetic. This does not prove pairing/login, remote-service policy, phone UI, message/system-list membership coverage, or maintainer approval of the public contract. No production host, daemon or session store is used. Test-created session databases are deleted by `t.TempDir` cleanup.

Exact-head compatibility is on branch `proof/498-compat-nov13`, directory `round6`.


![Terminal](shots/terminal-01.png)
