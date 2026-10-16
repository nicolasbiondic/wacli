package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestChatArchiveUpgradeAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	jid := "15550000001@s.whatsapp.net"
	if err := db.UpsertChat(jid, "dm", "Synthetic", base); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatArchived(jid, true); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"ALTER TABLE chats DROP COLUMN archive_action_ms",
		"ALTER TABLE chats DROP COLUMN archive_incoming_ms",
		"ALTER TABLE chats DROP COLUMN archive_cleared_ms",
		"DROP TABLE chat_archive_setting",
		"DELETE FROM schema_migrations WHERE version=33",
	} {
		if _, err := db.sql.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// An unmigrated read-only store retains the old public query contract.
	db, err = OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := db.ListChats("", 5); err != nil || len(rows) != 1 || !rows[0].Archived {
		t.Fatalf("read-only rows=%+v, %v", rows, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if required, err := db.ChatArchiveRecoveryRequired(); err != nil || !required {
		t.Fatalf("upgrade recovery=%v, %v", required, err)
	}
	if err := db.RecordChatArchiveIncoming(jid, base.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatArchiveSetting(true, base); err != nil {
		t.Fatal(err)
	}
	chat, err := db.GetChat(jid)
	if err != nil || !chat.Archived {
		t.Fatalf("unknown archive boundary changed: %+v, %v", chat, err)
	}
	if err := db.ApplyChatArchive(jid, true, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetChatArchiveSetting(false, base.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyChatArchive(jid, true, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	chat, err = db.GetChat(jid)
	if err != nil || chat.Archived {
		t.Fatalf("replay after restart: %+v, %v", chat, err)
	}
}

func TestChatArchiveIdentityMergeKeepsNewerBoundary(t *testing.T) {
	for _, incomingOnLID := range []bool{false, true} {
		t.Run(map[bool]string{false: "phone incoming", true: "lid incoming"}[incomingOnLID], func(t *testing.T) {
			db := openTestDB(t)
			base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			lid, phone := "123@lid", "15550000001@s.whatsapp.net"
			for _, jid := range []string{lid, phone} {
				if err := db.UpsertChat(jid, "dm", "Synthetic", base); err != nil {
					t.Fatal(err)
				}
				if err := db.ApplyChatArchive(jid, true, base.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.SetChatArchiveSetting(true, base); err != nil {
				t.Fatal(err)
			}
			incoming := phone
			if incomingOnLID {
				incoming = lid
			}
			if err := db.RecordChatArchiveIncoming(incoming, base.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := db.MigrateLIDToPN(lid, phone); err != nil {
				t.Fatal(err)
			}
			if err := db.ApplyChatArchive(phone, true, base.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			chat, err := db.GetChat(phone)
			if err != nil || chat.Archived {
				t.Fatalf("merged archive=%+v, %v", chat, err)
			}
			if err := db.ApplyChatArchive(phone, true, base.Add(3*time.Second)); err != nil {
				t.Fatal(err)
			}
			chat, err = db.GetChat(phone)
			if err != nil || !chat.Archived {
				t.Fatalf("newer archive=%+v, %v", chat, err)
			}
		})
	}
}

func TestChatArchiveIdentityMergePreservesUnrecoveredArchive(t *testing.T) {
	db := openTestDB(t)
	lid, phone := "123@lid", "15550000001@s.whatsapp.net"
	if err := db.UpsertChat(lid, "dm", "Synthetic", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatArchived(lid, true); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateLIDToPN(lid, phone); err != nil {
		t.Fatal(err)
	}
	chat, err := db.GetChat(phone)
	if err != nil || !chat.Archived {
		t.Fatalf("unrecovered archive lost: %+v, %v", chat, err)
	}
	if required, err := db.ChatArchiveRecoveryRequired(); err != nil || !required {
		t.Fatalf("missing upgrade recovery=%v, %v", required, err)
	}
}
