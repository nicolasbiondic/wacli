# contacts

Read when: finding synced contacts, saving or renaming a contact in the WhatsApp address book, importing macOS Contacts names, or managing local contact metadata.

`wacli contacts` works with contact metadata stored locally. Aliases and tags are local to `wacli`; they do not edit WhatsApp contacts on the phone. `save` (alias `rename`) is the one command that writes to the WhatsApp address book.

## Commands

```bash
wacli contacts search <query> [--limit N]
wacli contacts show --jid JID
wacli contacts resolve <lid|phone|jid> [...]
wacli contacts check <phone> [phone...]
wacli contacts save --phone <phone|jid> --name "Full Name" [--first-name NAME]
wacli contacts rename --phone <phone|jid> --name "New Name"
wacli contacts refresh
wacli contacts import-system [--input FILE] [--dry-run] [--clear]
wacli contacts alias set --jid JID --alias NAME
wacli contacts alias rm --jid JID
wacli contacts tags add --jid JID --tag TAG
wacli contacts tags rm --jid JID --tag TAG
```

## Notes

- `search` matches alias, full name, push name, first name, business name, phone, and JID.
- `search` and `show` combine phone-number and `@lid` contact rows only when the local WhatsApp session has a verified mapping. Matching names alone never merge contacts. The combined result uses the phone-number JID and its actual phone number; an unmapped `@lid` stays separate with an empty `phone` field.
- Search matches metadata and stored IDs on either row, as well as the resolved phone number. `--limit` applies after combining duplicates. A contact stored only as `@lid` is also searchable by its mapped phone number, including partial numbers.
- For combined contacts, local aliases and system names retain their display precedence, with the phone-number row winning conflicts within each field. `show` accepts either stored JID or the resolved phone-number JID and combines tags. Reading contacts never rewrites their stored rows or metadata.
- Alias and tag commands accept either verified identity, including the JID returned by `search` or `show`. Changes apply atomically to both identities so removing metadata cannot reveal an older copy on the other row. These local commands read the session mapping without connecting to WhatsApp or modifying its session database; an unreadable session database fails the command before a metadata write.
- `resolve` maps each LID to its phone number and each phone number to its LID, using the local session's verified mapping. It reads local state only and takes no store lock, so it works while `sync --follow` is running. Every input gets an entry: an identity without a known pair has `resolved: false` (never dropped), and a group, channel or malformed input carries an `error`. JSON entries have `input`, `jid` (phone JID), `phone`, `lid`, `name`, `resolved` and `error` (#420).
- `check` connects with the account session and asks WhatsApp's servers whether each number is registered (accepts +E164, common formatting, or user JIDs). Results are reported per query and not stored locally; use `--json` for scripting. A number the server did not answer for is reported as `no response` (JSON `"responded": false`) — treat it as unknown, not as a confirmed negative.
- While a same-store `sync --follow` process owns the store lock, `check` is delegated to its already-connected session instead of failing on the lock. Restart an older `sync --follow` process after upgrading; older daemons return an unsupported `contacts_check` kind error. Delegated checks run in the follow process's serialized delegate queue, so a large batch can hold a queued send until the send's own `--timeout` expires; split long lists or run them when nothing else is sending.
- `save` writes a contact to the WhatsApp address book of the linked account, and `rename` is the same command: saving an existing number under a new name renames it. `--phone` accepts +E164, common formatting, or a user JID; an `@lid` JID is accepted only when the local session already maps it to a phone number (an unmapped LID is rejected, never saved as-is). `--name` is required; `--first-name` is optional and is omitted from the mutation when not given (WhatsApp Web sends only what was entered and the receiving device derives its own short name). Names are trimmed and must be nonempty, at most 255 characters, and free of line breaks or control characters; this limit is a local safety policy, not a WhatsApp protocol limit.
- `save` sends one `contact` app-state mutation (collection `critical_unblock_low`, version 2, indexed by the phone-number JID) with the full name, the optional first name, the LID (from WhatsApp's registration answer, or else the local session mapping), and `saveOnPrimaryAddressbook` set, which asks the phone to store the entry in its device address book. A successful command means WhatsApp accepted the mutation; whether and how the phone applies it (inside WhatsApp, in the device address book) depends on the phone's WhatsApp build and settings.
- Before writing, `save` asks WhatsApp whether the number is registered (the same lookup as `check`). Only a confirmed registered number is saved: a confirmed unregistered number and a number the server did not answer for are both rejected before anything is written. The entry is indexed by the phone number WhatsApp reports for the typed one, like send recipients, so regional rewrites (for example a missing or extra mobile digit) attach to the real chat; the LID from that answer is included. JSON output has `action`, `jid`, `phone`, `lid` (when known), `full_name`, `first_name`, and `requested_jid` when WhatsApp's number differs from the typed one (text output adds `WhatsApp's number for ...`).
- `save` uses the same app-state discipline as `chats archive` and the other chat-state writes: it fetches the collection's pending deltas first, keeps a durable recovery intent across the send, replays the collection when its local state does not verify, and persists the server's echo in order. After a successful write the saved names are stored in `wacli.db` immediately, so `contacts show` reflects them without `contacts refresh`; whatsmeow's own contact store is updated by the post-send fetch. Contact mutations made on the phone or another linked device while `sync --follow` runs are now mirrored into `wacli.db` the same way. A contact mutation is authoritative for the full and first name, as in whatsmeow's session store: a rename without a first name clears the old one, while push, business, and macOS Contacts names are kept. A mutation indexed by a LID the session cannot map is stored under that LID, as whatsmeow stores it.
- Like the other app-state writes, a `save` keeps the collection's recovery intent after it succeeds, so the next `save` normally replays the whole `critical_unblock_low` collection (the full contact list) before writing, and the next `sync` startup replays it too. Each replayed entry's names are written into `wacli.db`. With the pinned whatsmeow, a replay whose state does not verify discards the local collection version and waits for the phone to resend it.
- While a same-store `sync --follow` process owns the store lock, `save` is delegated to it like the `chats` state commands: it skips the send queue and pacing but keeps the caller's `--timeout` budget. Older daemons return an unsupported `contacts_save` kind error; restart `sync --follow` after upgrading. `save` is refused in read-only mode before any connection or delegation.
- Removing a contact from the address book is not supported: it needs a REMOVE app-state operation that the pinned whatsmeow cannot encode.
- `refresh` imports contacts from the whatsmeow session store into `wacli.db`.
- `import-system` imports display names from macOS Contacts by matching phone numbers against already-synced wacli contacts. Run `contacts refresh` first.
- `import-system --input FILE` reads a JSON array or newline-delimited JSON contacts file with `full_name` and `phones` fields instead of opening macOS Contacts.
- Imported system names are local wacli metadata. They do not edit WhatsApp contacts or macOS Contacts.
- Display precedence is local alias, imported system name, then WhatsApp names.
- Use `import-system --dry-run` before writing. Use `import-system --clear` to remove imported system names.
- See [contacts import-system](contacts-import-system.md) for the full import workflow, JSON shape, file format, and verification steps.
- Tags are local grouping metadata for scripts and future workflows.

## Examples

```bash
wacli contacts search Alice
wacli contacts show --jid 1234567890@s.whatsapp.net
wacli contacts resolve 123456789@lid 987654321@lid --json
wacli contacts check +43 664 12345678 --json
wacli contacts save --phone "+43 664 12345678" --name "Alice Example" --json
wacli contacts rename --phone 4366412345678@s.whatsapp.net --name "Alice E."
wacli contacts refresh
wacli contacts import-system --dry-run
wacli contacts alias set --jid 1234567890@s.whatsapp.net --alias mom
wacli contacts tags add --jid 1234567890@s.whatsapp.net --tag family
```
