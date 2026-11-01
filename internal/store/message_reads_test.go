package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSelfReadTimesMigration(t *testing.T) {
	for _, collision := range []bool{false, true} {
		t.Run(map[bool]string{false: "old", true: "collision"}[collision], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wacli.db")
			db := mustOpen(t, path)
			if _, err := db.sql.Exec(`DROP TABLE IF EXISTS message_reads; DELETE FROM schema_migrations WHERE version=39`); err != nil {
				t.Fatal(err)
			}
			if collision {
				if _, err := db.sql.Exec(`INSERT INTO schema_migrations VALUES(39,'another feature',1)`); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			db = mustOpen(t, path)
			defer db.Close()
			if !db.hasTable("message_reads") {
				t.Fatal("writable open did not ensure message_reads")
			}
			var name string
			if err := db.sql.QueryRow(`SELECT name FROM schema_migrations WHERE version=39`).Scan(&name); err != nil {
				t.Fatal(err)
			}
			if collision && name != "another feature" {
				t.Fatalf("overwrote migration owner: %s", name)
			}
		})
	}
}

func TestSelfReadTimesReadOnlyOldStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	chat := "123@s.whatsapp.net"
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	seedOutgoing(t, db, chat, "m1", at)
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "in-1", Timestamp: at, Text: "incoming"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`DROP TABLE IF EXISTS message_reads; DELETE FROM schema_migrations WHERE version=39`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	msgs, err := ro.ListMessages(ListMessagesParams{ChatJID: chat, Limit: 5})
	if err != nil || len(msgs) != 2 {
		t.Fatalf("ListMessages = %v, %v", msgs, err)
	}
	m, err := ro.GetMessage(chat, "in-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range append(msgs, m) {
		data, _ := json.Marshal(msg)
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(data, &fields)
		if string(fields["read_at"]) != "null" {
			t.Fatalf("read_at = %s; want null", fields["read_at"])
		}
	}
	if ro.hasTable("message_reads") {
		t.Fatal("read-only open migrated the store")
	}
	if _, err := ro.HistoricalLIDJIDs(); err != nil {
		t.Fatalf("old read-only identity discovery: %v", err)
	}
}

func TestSelfReadTimesEarliestSourceAndLateHistory(t *testing.T) {
	db := openTestDB(t)
	chat := "123@s.whatsapp.net"
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	seedOutgoing(t, db, chat, "outgoing", at)
	for _, r := range []struct {
		source string
		ts     time.Time
	}{
		{"this_device", at}, {"other_device", at.Add(time.Hour)},
		{"other_device", at.Add(-time.Minute)}, {"this_device", at.Add(-time.Minute)},
	} {
		if err := db.UpsertMessageReads(chat, []string{"late", "outgoing", ""}, r.ts, r.source); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "late", Timestamp: at.Add(-time.Hour), Text: "incoming"}); err != nil {
		t.Fatal(err)
	}
	var source string
	var ts int64
	if err := db.sql.QueryRow(`SELECT read_ts, source FROM message_reads WHERE chat_jid=? AND msg_id='late'`, chat).Scan(&ts, &source); err != nil {
		t.Fatal(err)
	}
	if source != "other_device" || ts != at.Add(-time.Minute).Unix() {
		t.Fatalf("earliest read = %d %s", ts, source)
	}
	if n := countRows(t, db.sql, `SELECT COUNT(*) FROM message_reads`); n != 1 {
		t.Fatalf("read rows = %d; want only incoming ID", n)
	}
	m, err := db.GetMessage(chat, "late")
	if err != nil || m.ReadAt == nil || m.ReadAt.Unix() != ts {
		t.Fatalf("GetMessage = %+v, %v", m, err)
	}
	if err := db.UpsertMessageReads(chat, []string{"zero"}, time.Time{}, "other_device"); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db.sql, `SELECT COUNT(*) FROM message_reads`); n != 1 {
		t.Fatalf("zero timestamp invented a read: %d rows", n)
	}
	if err := db.UpsertMessageReads(chat, []string{"late"}, at, "invalid"); err == nil {
		t.Fatal("invalid source accepted")
	}
}

func TestSelfReadTimesLIDMerge(t *testing.T) {
	db := openTestDB(t)
	lid, pn := "999@lid", "123@s.whatsapp.net"
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	for _, chat := range []string{lid, pn} {
		if err := db.UpsertChat(chat, "dm", "Alice", at); err != nil {
			t.Fatal(err)
		}
		if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "in-1", Timestamp: at, Text: "incoming"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.UpsertMessageReads(lid, []string{"in-1", "lid-only"}, at, "other_device"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMessageReads(pn, []string{"in-1", "pn-only"}, at.Add(time.Minute), "this_device"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.MigrateLIDToPN(lid, pn); err != nil {
			t.Fatal(err)
		}
	}
	if n := countRows(t, db.sql, `SELECT COUNT(*) FROM message_reads WHERE chat_jid=?`, lid); n != 0 {
		t.Fatalf("%d stale LID reads", n)
	}
	if n := countRows(t, db.sql, `SELECT COUNT(*) FROM message_reads WHERE chat_jid=?`, pn); n != 3 {
		t.Fatalf("%d PN reads; want 3", n)
	}
	var ts int64
	var source string
	if err := db.sql.QueryRow(`SELECT read_ts, source FROM message_reads WHERE chat_jid=? AND msg_id='in-1'`, pn).Scan(&ts, &source); err != nil {
		t.Fatal(err)
	}
	if ts != at.Unix() || source != "other_device" {
		t.Fatalf("merged read = %d %s", ts, source)
	}
	if _, err := db.sql.Exec(`DELETE FROM chats WHERE jid=?`, pn); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db.sql, `SELECT COUNT(*) FROM message_reads`); n != 0 {
		t.Fatalf("%d reads survived chat cleanup", n)
	}
}

func TestSelfReadTimesInvalidatePages(t *testing.T) {
	db := openTestDB(t)
	chat := "123@s.whatsapp.net"
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	seedOutgoing(t, db, chat, "outgoing", at)
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "incoming", Timestamp: at, Text: "incoming"}); err != nil {
		t.Fatal(err)
	}
	p := ListMessagesParams{ChatJID: chat, Limit: 1}
	page, err := db.ListMessagesPage("list", p, "")
	if err != nil || page.Page.NextCursor == nil {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	if err := db.UpsertMessageReads(chat, []string{"incoming"}, at, "this_device"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ListMessagesPage("list", p, *page.Page.NextCursor); err == nil || !strings.Contains(err.Error(), "archive changed") {
		t.Fatalf("stale cursor accepted: %v", err)
	}
	page, err = db.ListMessagesPage("list", p, "")
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ReadAt == nil || page.Page.NextCursor == nil {
		t.Fatalf("page read_at = %+v, %v", page, err)
	}
	if err := db.UpsertMessageReads(chat, []string{"incoming"}, at.Add(time.Hour), "other_device"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ListMessagesPage("list", p, *page.Page.NextCursor); err != nil {
		t.Fatalf("later read unnecessarily invalidated cursor: %v", err)
	}
}
