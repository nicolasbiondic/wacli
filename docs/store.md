# store

Read when: inspecting local SQLite size/counts or pruning old local chat/group rows.

`wacli store` manages the selected account's local `wacli.db` mirror. Cleanup commands only delete local wacli cache/history rows; they do not delete WhatsApp chats, leave groups, or remove messages from WhatsApp servers.

## Commands

```bash
wacli store stats
wacli store cleanup [--days N] [--dry-run] [--confirm]
```

Related cleanup commands:

```bash
wacli chats cleanup [--days N] [--jid JID] [--dry-run] [--confirm]
wacli groups prune [--days N] [--left-only=false|--include-active] [--dry-run] [--confirm]
```

## Notes

- `store stats` reads local counts for chats, groups, left groups, and normal chat messages.
- Status broadcasts are persisted separately in `status_messages`; they are not chat rows and are not included in normal chat/message cleanup paths.
- What recipients report about your own messages is persisted in `message_receipts`, one row per recipient. The rows follow their chat through the SQLite cascade, so `store cleanup` and `chats cleanup` remove them with it.
- Location pins are persisted in `message_locations`. Coordinates follow the same lifecycle as the rest of a message's local payload: `messages purge` removes them with the retained payload, `store cleanup` and `chats cleanup` remove them with their chat, and a LID-to-phone identity migration moves them with the message rather than leaving them under the old identity.
- `store cleanup` removes chats whose known local activity is older than `--days` and deletes their messages through the SQLite chat/message cascade.
- `chats cleanup --jid JID` removes one local chat row and its local messages.
- `groups prune` removes local group metadata plus the matching local chat/messages for pruned group JIDs.
- `groups prune` defaults to groups you have left. `--days N` limits that to groups left more than `N` days ago.
- `groups prune --include-active --days N` also prunes active groups whose last known local message is older than `N` days. Groups with no known local activity timestamp are skipped.
- Destructive cleanup commands require confirmation unless `--confirm` is passed.
- If a row cannot be deleted, bulk cleanup continues with the other targets, then exits nonzero with the underlying errors and the number successfully deleted. In `--json` mode, failures use the error envelope on stderr and do not emit a success result on stdout. Successfully deleted rows stay deleted; failed deletions are rolled back individually.
- Use `--dry-run` first; it lists what would be deleted without changing the local store.
- Use `--read-only` or `WACLI_READONLY=1` to make cleanup commands fail before opening the store for writes.
- Read-only opening validation and read views retry only `SQLITE_READONLY_RECOVERY` (264) and `SQLITE_READONLY_CANTINIT` (1288), up to four attempts with 5/10/15 ms backoff. Both can occur during concurrent WAL/SHM initialisation. A read view rolls back and starts a fresh transaction before rerunning its read-only callback. Exhaustion and other errors remain errors. Writable connections, migrations and mutations are not retried; the read-only SHM policy is unchanged.
- Use `--account NAME` to target a named account store. Use `--store DIR` for manual stores or migration debugging; it cannot be combined with `--account`.

## Examples

SQLite failures in `--json` retain the existing string `error` and add typed
details on stderr (no success output on stdout):

```json
{"success":false,"data":null,"error":"attempt to write a readonly database","sqlite_error":{"primary_code":8,"extended_code":1288,"name":"SQLITE_READONLY_CANTINIT","retryable":true}}
```

`retryable` is true only for codes 264/1288 originating in a read-only opening
or read view. It describes that read failure, not permission to replay an entire
command or an uncertain mutation. Persistent SHM/permission failures can exhaust
the budget even for those codes. Other SQLite errors retain their codes with
`retryable:false`; unknown extensions use `SQLITE_UNKNOWN` rather than a guessed
symbol. Non-SQLite errors omit `sqlite_error`. The same details appear in
`--events` error events under `data.sqlite_error`.

```bash
wacli store stats
wacli store cleanup --days 365 --dry-run
wacli chats cleanup --jid 1234567890@s.whatsapp.net --dry-run
wacli groups prune --dry-run
wacli groups prune --days 180 --dry-run
wacli groups prune --include-active --days 365 --dry-run
```
