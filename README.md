# Evidencia de wacli

Rama huérfana: solo artefactos revisados. El alcance y las limitaciones de cada prueba deben constar en el transcript. El escáner no hace OCR: revisar visualmente las imágenes antes de publicar.

# openclaw/wacli #561: real-transport proof for `profile business get|set` through the sync owner

Exact head: `6e77f29` (branch `feat/business-profile-catalog`; production code is identical to the previously reviewed `7d794ad`, only `_test.go` files were added).

Test file: `cmd/wacli/business_owner_transport_test.go`.

```
WACLI_PROOF_STABLE_WACLI=<official v0.26.0 binary> \
  go test -tags sqlite_fts5 -count=1 -v -run 'TestProfileBusinessViaOwner|TestCatalogListCursorsOverTLS'   # package cmd/wacli
```

## What `TestProfileBusinessViaOwner` does

1. The store (`wacli.db` and `session.db`) is first created by the official v0.26.0 release binary (`auth status` on an empty store; binary sha256=3750e566e88a3a7ae47e485c02392ad60b972160fc85e5bd789e896673245f76 ; extracted from `wacli_0.26.0_linux_amd64.tar.gz`, whose sha256 matches the release `checksums.txt`). A synthetic device is then paired into that `session.db`; the head opens both files.
2. The real `wacli sync --follow` command (real `App.Sync`, real send socket) connects over Noise XX/WebSocket to an in-process peer. The SDK dials its real URL `wss://web.whatsapp.com/ws/chat`; the default transport routes that host to the local TLS peer, which trusts only the test certificate.
3. The peer answers the `w:biz` get/set stanzas the production code sends from an in-memory profile and logs every stanza and connection.
4. The real Cobra commands `profile business get` and `profile business set ...` run in the same process against the same store. The store lock is held by the owner, so the CLI can only delegate over the owner's socket. All 10 `w:biz` stanzas of the 7 CLI calls arrive on connection #1 (the owner's); no CLI call opened a connection.

Shown in `head-pass.txt` and `shots/`:
- full read (description, address, email, 2 websites, category, hours);
- `set --description` sends only `<description>`; the next read shows address, email, websites and hours unchanged (omitted fields survive a delta);
- `set --website ""` sends one empty `<website/>`; the next read shows no websites and nothing else lost;
- re-read failure after an accepted update (peer returns iq error 500): exit 0, `Business profile updated.` on stdout, `Could not re-read the profile` plus the recovery hint on stderr only;
- the stable-created `wacli.db` has 33 migrations before and after the head opens it.

## Catalog cursors

- `TestCatalogListCursorsOverTLS`: the real `catalog list` command, URL `https://graph.whatsapp.com/graphql/catalog`, routed to a local TLS server (verified against the test certificate). Page 1 (2 products) returns a cursor; `--after <cursor>` returns a different final page (1 product, no cursor). No session, empty scratch store.
- Live (`live-catalog-facts.txt`): built head binary, empty scratch store (no `session.db` created), explicit business identifier, 1 request. The endpoint answered without a session, but the business has no catalog (`has_catalog=false`), so no second page was requested. Cursor paging is therefore **not** proven live; it is proven in-tree. Identifiers, names, prices and URLs were not recorded.

## Red controls (same test file)

- `base-fail.txt`: on openclaw `main` `2108fe7` both tests fail (`unknown command "get" for "wacli profile business"`; no `catalog` command).
- `before-stderr-fix-fail.txt`: on `6aa5edb` (feature before the stderr fix) the owner trace fails on the warning: it was printed on stdout, stderr empty.

## Not proven

Pairing/login, WhatsApp's own validation or moderation of profile fields, that the real service accepts an empty `<website/>` (the peer models the stanza), the phone UI, a real Business account, and live catalog pagination. Identifiers are synthetic. No data-model change in this PR (`internal/store` identical to main).

Nicolás (via agent)


![Terminal](shots/terminal-01.png)
![Terminal](shots/terminal-02.png)
![Terminal](shots/terminal-03.png)
![Terminal](shots/terminal-04.png)
![Terminal](shots/terminal-05.png)
![Terminal](shots/terminal-06.png)
