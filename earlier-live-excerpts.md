# Earlier phone-observed behavior (not a new live run)

Source: the locally preserved `lists-live/proof-stock-step1.md` and `proof-stock-step2.md`, 2026-10-16, build `0.22.0+394.2a9467e`, stock whatsmeow `c386243a72ba`, no dependency replacement. These excerpts summarize the recorded outputs; private names, account identities and all chat identifiers are omitted.

## Phone create/assign

The account owner created a custom test list on the phone and assigned one chat. The phone reused a previously deleted definition ID and sent a newer undelete/rename.

Recorded shipped-CLI results:

- `lists list --json`: success; Unread (type 1), Favorites (type 3), Groups (type 2), and one `<test-list>` (type 5). Only the custom list reported `chat_memberships_supported=true`.
- `full_sync_events_seen=true`; `completeness="unknown"`.
- `chats list --list <test-list> --json`: success, one assigned chat. Chat contents, name and identifier are withheld.

## Phone unassign/delete

The phone removed the chat and deleted the list six seconds later.

Recorded shipped-CLI results:

- `lists list --json`: success, the three generic system definitions remained; the deleted custom test definition was omitted.
- `chats list --list <deleted-test-list-id> --json`: failure, `list "<deleted-test-list-id>" was deleted on the phone`.
- `full_sync_events_seen=true`; `completeness="unknown"`.

`source-blobs.txt` records that `internal/app/lists.go`, `internal/store/lists.go` and `cmd/wacli/lists.go` are byte-identical at this demonstrated revision, the prior published PR head `bbd627e`, and the November tested head. Other shared code and dependencies have changed since that run. Identical files support relevance, not a claim that the current PR head was connected to the phone.

The November trace independently executes the current PR-head read-only CLI against five SQLite backups of fork-ingested real-account data. It does not demonstrate new phone actions, network ingestion by the PR binary, computed/system memberships or complete account coverage. Maintainer scope approval remains outstanding.
