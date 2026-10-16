package store

// Store-level black-box scenarios for #497 (upgrade path of an existing
// store, read-only opens, migration number collisions). Ported from
// nicolasbiondic/wacli upstream/auto-unarchive-on-incoming.

import (
	"path/filepath"
	"testing"
	"time"
)

var scnArchiveAt = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func scnMustOpen(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db
}

func scnArchived(t *testing.T, db *DB, jid string) bool {
	t.Helper()
	c, err := db.GetChat(jid)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	return c.Archived
}

// Produce the pre-#497 shape of a store that already has data.
func scnDowngrade(t *testing.T, db *DB) {
	t.Helper()
	for _, stmt := range []string{
		`DROP TABLE chat_archive_setting`,
		`ALTER TABLE chats DROP COLUMN archive_action_ms`,
		`ALTER TABLE chats DROP COLUMN archive_incoming_ms`,
		`ALTER TABLE chats DROP COLUMN archive_cleared_ms`,
		`ALTER TABLE chats DROP COLUMN archive_setting_min_ms`,
		`DELETE FROM schema_migrations WHERE name='ordered chat auto-unarchive'`,
	} {
		if _, err := db.sql.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if has, _ := db.tableHasColumn("chats", "archive_action_ms"); has || db.hasTable("chat_archive_setting") {
		t.Fatal("the older shape was not produced")
	}
}

func scnAssertSchema(t *testing.T, db *DB) {
	t.Helper()
	for _, col := range []string{"archive_action_ms", "archive_incoming_ms", "archive_cleared_ms", "archive_setting_min_ms"} {
		if has, err := db.tableHasColumn("chats", col); err != nil || !has {
			t.Fatalf("chats.%s missing after open: %v %v", col, has, err)
		}
	}
	if !db.hasTable("chat_archive_setting") {
		t.Fatal("chat_archive_setting missing after open")
	}
}

func TestPR497Store_OlderStoreGainsBoundariesWithFlagsIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := scnMustOpen(t, path)
	chat := "15550000001@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Bob", scnArchiveAt); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "m1", Timestamp: scnArchiveAt, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatArchived(chat, true); err != nil {
		t.Fatal(err)
	}
	scnDowngrade(t, db)
	db.Close()

	reopened := scnMustOpen(t, path)
	scnAssertSchema(t, reopened)
	if !scnArchived(t, reopened, chat) {
		t.Fatal("migration changed an existing archive flag")
	}
	// Legacy archives are unknown boundaries: the store asks for a replay.
	if required, err := reopened.ChatArchiveRecoveryRequired(); err != nil || !required {
		t.Fatalf("recovery required after upgrade = %v, %v; want true", required, err)
	}
	var action int64
	if err := reopened.sql.QueryRow(`SELECT archive_action_ms FROM chats WHERE jid=?`, chat).Scan(&action); err != nil || action != 0 {
		t.Fatalf("upgraded archive boundary = %d, %v; want 0 (unknown)", action, err)
	}
	reopened.Close()
	again := scnMustOpen(t, path)
	defer again.Close()
	scnAssertSchema(t, again)
	if err := again.ApplyChatArchive(chat, true, scnArchiveAt); err != nil {
		t.Fatalf("ApplyChatArchive after upgrade: %v", err)
	}
}

// Read-only opens never migrate; the chat read paths must not depend on the
// new columns or table.
func TestPR497Store_ReadOnlyOlderStoreListsChats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := scnMustOpen(t, path)
	chat := "15550000001@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Bob", scnArchiveAt); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatArchived(chat, true); err != nil {
		t.Fatal(err)
	}
	scnDowngrade(t, db)
	db.Close()

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()
	archived := true
	chats, err := ro.ListChatsFiltered(ChatListFilter{Archived: &archived})
	if err != nil || len(chats) != 1 || !chats[0].Archived {
		t.Fatalf("ListChatsFiltered = %+v, %v", chats, err)
	}
	if c, err := ro.GetChat(chat); err != nil || !c.Archived {
		t.Fatalf("GetChat = %+v, %v", c, err)
	}
	if has, _ := ro.tableHasColumn("chats", "archive_action_ms"); has {
		t.Fatal("read-only open migrated the store")
	}
}

// Other open PRs (#494, #496) also claim version 33. A store that recorded
// 33 for another migration must still gain this one's objects on the next
// writable open, and a store that already has them must not break when a
// build records 33 for something else.
func TestPR497Store_SurvivesMigrationNumberCollision(t *testing.T) {
	chat := "15550000001@s.whatsapp.net"
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := scnMustOpen(t, path)
	if err := db.UpsertChat(chat, "dm", "Bob", scnArchiveAt); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatArchived(chat, true); err != nil {
		t.Fatal(err)
	}
	scnDowngrade(t, db)
	if _, err := db.sql.Exec(`INSERT INTO schema_migrations(version,name,applied_at) VALUES(33,'another contribution',0)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	reopened := scnMustOpen(t, path)
	defer reopened.Close()
	scnAssertSchema(t, reopened)
	if !scnArchived(t, reopened, chat) {
		t.Fatal("reopen changed an existing archive flag")
	}
	var name string
	if err := reopened.sql.QueryRow(`SELECT name FROM schema_migrations WHERE version=33`).Scan(&name); err != nil || name != "another contribution" {
		t.Fatalf("version 33 row = %q, %v; the other migration's record must stay", name, err)
	}
	if err := reopened.SetChatArchiveSetting(true, scnArchiveAt); err != nil {
		t.Fatal(err)
	}
}

// A chat archived by a build without boundaries after this build cleared it
// (binary rollback and forward). Any reconcile of the account preference
// re-evaluates the old boundary against the old arrival.
func TestPR497Store_LegacyRearchiveAfterClearSurvivesSettingReplay(t *testing.T) {
	db := openTestDB(t)
	chat := "15550000001@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Bob", scnArchiveAt); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatArchiveSetting(true, scnArchiveAt.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyChatArchive(chat, true, scnArchiveAt); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "m1", Timestamp: scnArchiveAt.Add(time.Minute), Text: "hi", ArchiveIncoming: true}); err != nil {
		t.Fatal(err)
	}
	if scnArchived(t, db, chat) {
		t.Fatal("arrival did not clear the archive")
	}
	if err := db.SetChatArchived(chat, true); err != nil { // the other build
		t.Fatal(err)
	}
	if err := db.SetChatArchiveSetting(true, scnArchiveAt.Add(-time.Hour)); err != nil { // replayed preference
		t.Fatal(err)
	}
	if !scnArchived(t, db, chat) {
		t.Fatal("a replayed preference undid an archive applied by another build")
	}
}
