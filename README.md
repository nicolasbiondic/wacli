# Evidencia de wacli

Rama huérfana: solo artefactos revisados. El alcance y las limitaciones de cada prueba deben constar en el transcript. El escáner no hace OCR: revisar visualmente las imágenes antes de publicar.

# PR #567 real-transport proof (14-nov-2026)

Head `0e769f0` (production code identical to reviewed `dc6a206`; only a `_test.go` was added). Test `TestContactCardsRealTransport` in `internal/wa/contact_card_transport_test.go`:

```
WACLI_PROOF_CLI=<built CLI> go test -tags sqlite_fts5 -run '^TestContactCardsRealTransport$' -count=1 -v .[LOCAL-PATH]
```

## What it exercises
Real `App.Sync` -> whatsmeow -> Noise XX/WebSocket -> in-process peer. A `ContactMessage` and a `ContactsArrayMessage` cross the socket Signal-encrypted. The vCards use `TEL;type=CELL;waid=<digits>:` with an empty value, and extra TEL parameters (`item1.TEL;type=CELL;type=VOICE;type=pref;waid=...:+55 0102`).
The log shows, from the production sync:
- display text with the waid-only number;
- `message_contacts` rows with the raw vCard, read with a separate SQLite connection;
- what the real `wacli messages list` command prints for those messages;
- store reopened (new `App`, new connection), same message IDs delivered again (fresh ciphertext): still one row per card, same text.

## Red control
`base-fail.txt`: the same test on openclaw main `2108fe7`: number missing from the display text and CLI output, `no such table: message_contacts`.
`head-pass.txt`: PR head.

## Not proven
Pairing/login, the remote service's policy, the phone UI. Identifiers are synthetic (short digit strings, invented names). Data-model compatibility is in branch `proof/567-compat-nov13`.

![Terminal head](shots/terminal-01.png)
![Terminal red control](shots/terminal-02.png)


![Terminal](shots/terminal-01.png)
![Terminal](shots/terminal-02.png)
