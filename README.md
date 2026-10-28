# Live proof: WhatsApp lists and chat labels (openclaw/wacli#539)

Same WhatsApp Business account as `proof/quick-replies-964` (iPhone), 2026-10-28, build of #539 at `99da71b`. Temporary companion with its own store; `sync --follow` owned the store, so every write went through `app_state_write`. The account owner watched the phone after each step.

- `1-lists-create.png`: existing lists (one CUSTOM), then `lists create` → appears in Settings > Lists.
- `2-lists-edit.png`: two `lists edit` (name and color) → renamed and recolored on the phone each time.
- `3-chat-label.png`: `chats label add` → the chat appears under the list filter with the list chip; `chats label remove` → the filter is empty.
- `4-lists-delete.png`: `lists delete` → gone from Settings > Lists; local index back to the original lists.

The new list was created without `orderIndex` and is not in a `label_reordering` action; the phone still listed it (after a short sync delay) and placed it after the existing custom list. `.txt` files are the same captures as text. Phone numbers masked; the chat is a test chat of the owner.
