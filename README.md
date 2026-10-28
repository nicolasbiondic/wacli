# Live proof: #502 phone-first archive recovery on an upgraded store (stock whatsmeow)

Real WhatsApp account whose `regular_low` server snapshot does not verify (the destructive case of #501), 2026-10-27/28. Temporary companion with its own store; one process at a time on that store.

1. `1-stock-0.22.0-pairing.png`: paired with the **official v0.22.0 release** (before the Keep-chats-archived recovery; `checksums-0.22.0.txt`). Its full sync of `regular_low` fails verification and the collection is flagged broken. Saved state: `regular_low` v63, 679 mutation MACs, 22 chats, 2 archived.
2. `2-502-first-start.png`: first `sync --follow` with #502 head `ff0d05e` built with `-tags sqlite_fts5` and the **pinned stock whatsmeow**. No full `regular_low` replay: background snapshot request to the primary, incremental sync and offline backlog, `settled legacy chat archive boundaries after incremental sync`. Saved state unchanged (same version, MACs, chats, archived chats); only the new archive-setting row and no recovery intents.
3. `3-502-restart.png`: restart. No new snapshot request, no replay, identical state.

`.txt` files are the same captures as text; `state.py` is the read-only state probe. Phone number and JIDs masked.
