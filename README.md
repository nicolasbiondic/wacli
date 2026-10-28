# Live proof: WhatsApp Business quick replies (openclaw/wacli, `upstream/quick-replies-v2`)

Real WhatsApp Business account, 2026-10-27. Temporary companion paired by phone code with its own store; `sync --follow` owned the store, so writes went through `app_state_write`. The account owner confirmed each step on the phone. Companion logged out and store deleted at the end.

- `1-resync-list.png`: `app-state resync regular` → complete; 3 pre-existing replies.
- `2-create-edit.png`: create `/prueba` (appeared on the phone), edit it (new text on the phone).
- `3-delete.png`: delete `/prueba` (gone from the phone).
- `4-phone-created.png`: `/desdetel` created on the phone shows up in `quick-replies list`; deleted from wacli (gone from the phone).
- `5-final-unlink.png`: final list, logout.
- `live-transcript.md`: the same outputs as text.

Phone number masked; the shortcuts and text of the pre-existing replies are placeholders.
