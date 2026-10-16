package store

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func listPtr[T any](v T) *T { return &v }

func TestListsDefinitionsMembershipReplayAndDeletion(t *testing.T) {
	db := openTestDB(t)
	at := time.Unix(100, 0)
	chat := "123@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Fixture", at); err != nil {
		t.Fatal(err)
	}
	create := ListUpdate{ID: "7", Name: listPtr("Sales"), Color: listPtr(int32(3)),
		PredefinedID: listPtr(int32(0)), Type: listPtr(int32(5)), OrderIndex: listPtr(int32(2)),
		IsActive: listPtr(true), IsImmutable: listPtr(false), Deleted: listPtr(false), Timestamp: at, FromFullSync: true}
	for range 2 {
		if err := db.UpsertList(create); err != nil {
			t.Fatal(err)
		}
		if err := db.SetChatList(chat, "7", true, at, true); err != nil {
			t.Fatal(err)
		}
	}
	result, err := db.Lists()
	if err != nil || len(result.Lists) != 1 || !result.FullSyncEventsSeen || result.Completeness != "unknown" {
		t.Fatalf("Lists = %+v, %v", result, err)
	}
	want := List{ID: "7", Name: "Sales", Color: create.Color, PredefinedID: create.PredefinedID,
		Type: create.Type, OrderIndex: create.OrderIndex, IsActive: create.IsActive, IsImmutable: create.IsImmutable,
		ChatMembershipsSupported: true}
	if !reflect.DeepEqual(result.Lists[0], want) {
		t.Fatalf("definition = %+v, want %+v", result.Lists[0], want)
	}
	assertListChats(t, db, "7", chat)
	if err := db.UpsertList(ListUpdate{ID: "7", Name: listPtr("Renamed"), Timestamp: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	// A snapshot's older create must not undo a rename or a membership removal.
	if err := db.SetChatList(chat, "7", false, at.Add(time.Second), false); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.UpsertList(create); err != nil {
			t.Fatal(err)
		}
		if err := db.SetChatList(chat, "7", true, at, true); err != nil {
			t.Fatal(err)
		}
	}
	assertListChats(t, db, "7")
	result, err = db.Lists()
	if err != nil || result.Lists[0].Name != "Renamed" || *result.Lists[0].Color != 3 {
		t.Fatalf("rename/optional fields lost: %+v, %v", result, err)
	}
	if err := db.SetChatList(chat, "7", true, at.Add(2*time.Second), false); err != nil {
		t.Fatal(err)
	}
	assertListChats(t, db, "7", chat)
	if err := db.UpsertList(ListUpdate{ID: "7", Deleted: listPtr(true), Timestamp: at.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertList(create); err != nil {
		t.Fatal(err)
	}
	assertListChats(t, db, "7")
	result, err = db.Lists()
	if err != nil || len(result.Lists) != 0 {
		t.Fatalf("deleted list visible: %+v, %v", result, err)
	}
	if _, err := db.ResolveList("7"); err == nil {
		t.Fatal("deleted list resolved")
	}
}

func assertListChats(t *testing.T, db *DB, id string, jids ...string) {
	t.Helper()
	got, err := db.ListChatsFiltered(ChatListFilter{ListID: id})
	if err != nil {
		t.Fatal(err)
	}
	var actual []string
	for _, c := range got {
		actual = append(actual, c.JID)
	}
	if !reflect.DeepEqual(actual, jids) {
		t.Fatalf("list %s chats = %v, want %v", id, actual, jids)
	}
}

func TestListsIdentityMergeUsesNewestMembershipIncludingRemovals(t *testing.T) {
	for _, newerLID := range []bool{true, false} {
		db := openTestDB(t)
		at := time.Unix(100, 0)
		lid, pn := "999@lid", "123@s.whatsapp.net"
		if err := db.UpsertList(ListUpdate{ID: "1", Name: listPtr("Fixture"), Timestamp: at}); err != nil {
			t.Fatal(err)
		}
		if err := db.UpsertChat(pn, "dm", "Fixture", at); err != nil {
			t.Fatal(err)
		}
		older, newer := lid, pn
		if newerLID {
			older, newer = pn, lid
		}
		if err := db.SetChatList(older, "1", true, at, true); err != nil {
			t.Fatal(err)
		}
		if err := db.SetChatList(newer, "1", false, at.Add(time.Second), false); err != nil {
			t.Fatal(err)
		}
		lids, err := db.HistoricalLIDJIDs()
		if err != nil || !reflect.DeepEqual(lids, []string{lid}) {
			t.Fatalf("membership-only LID discovery: %v, %v", lids, err)
		}
		for range 2 {
			if err := db.MigrateLIDToPN(lid, pn); err != nil {
				t.Fatal(err)
			}
		}
		assertListChats(t, db, "1")
		var count int
		if err := db.sql.QueryRow(`SELECT count(*) FROM chat_lists WHERE chat_jid=?`, lid).Scan(&count); err != nil || count != 0 {
			t.Fatalf("LID rows remain: %d, %v", count, err)
		}
	}
}

func TestListsMigrationPopulatedOlderStoreAndReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	at := time.Unix(100, 0)
	if err := db.UpsertChat("123@s.whatsapp.net", "dm", "Fixture", at); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: "123@s.whatsapp.net", MsgID: "fixture", Timestamp: at, Text: "kept"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`DROP TABLE lists; DROP TABLE chat_lists; DROP TABLE list_sync_state;
		DELETE FROM schema_migrations WHERE version=33`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ro.Lists()
	if err != nil || result.FullSyncEventsSeen || result.Completeness != "unknown" || len(result.Lists) != 0 {
		t.Fatalf("older read-only Lists = %+v, %v", result, err)
	}
	if ro.hasTable("lists") {
		t.Fatal("read-only open migrated the schema")
	}
	if _, err := ro.ListChatsFiltered(ChatListFilter{ListID: "1"}); err != nil {
		t.Fatal(err)
	}
	ro.Close()
	for range 2 {
		w := mustOpen(t, path)
		if err := migrateLists(w); err != nil {
			t.Fatal(err)
		}
		if !w.hasTable("lists") || !w.hasTable("chat_lists") {
			t.Fatal("migration missing")
		}
		m, err := w.GetMessage("123@s.whatsapp.net", "fixture")
		if err != nil || m.Text != "kept" {
			t.Fatalf("populated store changed: %+v, %v", m, err)
		}
		w.Close()
	}
}

func TestResolveListRejectsAmbiguityAndSystemMemberships(t *testing.T) {
	db := openTestDB(t)
	for _, p := range []ListUpdate{
		{ID: "1", Name: listPtr("same"), Type: listPtr(int32(5))},
		{ID: "2", Name: listPtr("same"), Type: listPtr(int32(4))},
		{ID: "3", Name: listPtr("Favorites"), Type: listPtr(int32(3))},
		{ID: "4", Name: listPtr("legacy")},
		{ID: "5", Name: listPtr("none"), Type: listPtr(int32(0))},
	} {
		if err := db.UpsertList(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"same", "missing", "Favorites", "3"} {
		if _, err := db.ResolveList(name); err == nil {
			t.Fatalf("ResolveList(%q) should fail", name)
		}
	}
	for _, name := range []string{"1", "2", "4", "5", "legacy", "none"} {
		if _, err := db.ResolveList(name); err != nil {
			t.Fatalf("ResolveList(%q): %v", name, err)
		}
	}
}

func TestListsOverlappingMembershipsAndFilterBeforeLimit(t *testing.T) {
	db := openTestDB(t)
	at := time.Unix(100, 0)
	for _, id := range []string{"a", "b"} {
		if err := db.UpsertList(ListUpdate{ID: id, Name: listPtr(id), Type: listPtr(int32(5)), Timestamp: at}); err != nil {
			t.Fatal(err)
		}
	}
	for i, jid := range []string{"123@s.whatsapp.net", "456@s.whatsapp.net"} {
		if err := db.UpsertChat(jid, "dm", "Fixture", at.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a", "b"} {
		if err := db.SetChatList("123@s.whatsapp.net", id, true, at, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a", "b"} {
		chats, err := db.ListChatsFiltered(ChatListFilter{ListID: id, Limit: 1})
		if err != nil || len(chats) != 1 || chats[0].JID != "123@s.whatsapp.net" {
			t.Fatalf("filter before limit / overlapping list %s: %+v, %v", id, chats, err)
		}
	}
	// At equal timestamps, ambiguous replay must not undo a removal.
	if err := db.SetChatList("123@s.whatsapp.net", "a", false, at, false); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatList("123@s.whatsapp.net", "a", true, at, true); err != nil {
		t.Fatal(err)
	}
	assertListChats(t, db, "a")
	assertListChats(t, db, "b", "123@s.whatsapp.net")
}
