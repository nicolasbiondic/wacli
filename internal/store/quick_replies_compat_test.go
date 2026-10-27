package store

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Reproducible rollback gate using the actual two executables. Never accepts a
// store path: both databases, the pre-upgrade snapshot and all writes are local
// disposable fixtures. There is deliberately no authenticated device here.
func TestQuickRepliesRollbackPreviousBinaryOffline(t *testing.T) {
	oldBin, newBin := os.Getenv("WACLI_COMPAT_BIN"), os.Getenv("WACLI_NEW_BIN")
	if oldBin == "" || newBin == "" {
		t.Skip("set WACLI_COMPAT_BIN and WACLI_NEW_BIN to the two executables")
	}
	if probe := openTestDB(t); !probe.HasFTS() {
		t.Skip("the actual release-binary fixture uses FTS5 triggers; run this compatibility gate with -tags sqlite_fts5")
	}
	dir := t.TempDir()
	run := func(bin string, args ...string) json.RawMessage {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, append([]string{"--store", dir, "--json"}, args...)...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		raw, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s %v: %v stdout=%s stderr=%s", bin, args, err, raw, stderr.String())
		}
		var envelope struct {
			Success bool
			Data    json.RawMessage
			Error   any
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || !envelope.Success || envelope.Error != nil {
			t.Fatalf("%v invalid result: %s %v", args, raw, err)
		}
		t.Logf("PASS %s %v: %s", filepath.Base(bin), args, envelope.Data)
		return envelope.Data
	}
	// Start with both databases initialized by the rollback binary, without
	// linking a device. Snapshot them while every CLI is closed.
	run(oldBin, "chats", "list")
	run(oldBin, "doctor")
	snapshot := make(map[string][]byte)
	for _, name := range []string{"wacli.db", "session.db"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		snapshot[name] = raw
	}
	path := filepath.Join(dir, "wacli.db")
	assertMigrationRow(t, path, 36, false)
	assertMigrationRow(t, path, 38, false)
	// Migrate through the NEW EXECUTABLE, not only the Go store API.
	run(newBin, "chats", "list")
	for _, version := range []int{36, 38, 9035} {
		assertMigrationRow(t, path, version, true)
	}
	db := mustOpen(t, path)
	chat := "123@s.whatsapp.net"
	seedOutgoing(t, db, chat, "m1", time.Unix(1700000000, 0))
	seedOutgoing(t, db, chat, "purge-me", time.Unix(1700000001, 0))
	if err := db.MarkMessageDeletedForMe(chat, "purge-me", chat, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertQuickReply(QuickReply{ID: "1", Shortcut: "fixture", Message: "preserve across rollback"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertList(ListUpdate{ID: "7", Name: listPtr("Fixture")}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatList(chat, "7", true, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkRegularFullSyncComplete(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, args := range [][]string{
		{"chats", "list", "--limit", "10"},
		{"--read-only", "chats", "list", "--limit", "10"},
		{"messages", "list", "--chat", chat, "--limit", "10"},
		{"--read-only", "messages", "list", "--chat", chat, "--limit", "10"},
		{"messages", "show", "--chat", chat, "--id", "m1"},
		{"messages", "changes", "--checkpoint"},
		{"groups", "list"}, {"contacts", "search", "fixture"}, {"auth", "status"},
		{"messages", "purge", "--chat", chat, "--id", "purge-me", "--confirm"},
	} {
		run(oldBin, args...)
	}
	var doctor struct {
		StoreError    string `json:"store_error"`
		Authenticated bool   `json:"authenticated"`
		Connected     bool   `json:"connected"`
	}
	if err := json.Unmarshal(run(oldBin, "doctor"), &doctor); err != nil || doctor.StoreError != "" || doctor.Authenticated || doctor.Connected {
		t.Fatalf("offline doctor = %+v, %v", doctor, err)
	}
	// --once is NOT an offline flag. With an unlinked fixture the auth guard
	// stops before Connect; never claim this proves live sync/reconnection.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, oldBin, "--store", dir, "--json", "sync", "--once").CombinedOutput()
	if err == nil || !strings.Contains(string(output), "not authenticated") {
		t.Fatalf("offline sync auth guard: %v %s", err, output)
	}
	t.Log("PASS old sync --once opens migrated session/index and stops before Connect (not authenticated)")
	db = mustOpen(t, path)
	if got, err := db.QuickReplies(); err != nil || len(got.QuickReplies) != 1 || got.Completeness != "complete" {
		t.Fatalf("rollback lost new data: %+v %v", got, err)
	}
	if _, err := db.GetMessage(chat, "m1"); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetMessage(chat, "purge-me"); err != nil || got.Text != "" {
		t.Fatalf("old local purge: %+v %v", got, err)
	}
	db.Close()
	// Exercise the documented fallback: closed processes, restore BOTH DBs,
	// remove only fixture WAL/SHM sidecars, then open with the old binary.
	for name, raw := range snapshot {
		for _, suffix := range []string{"-wal", "-shm"} {
			if err := os.Remove(filepath.Join(dir, name+suffix)); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	run(oldBin, "chats", "list")
	run(oldBin, "doctor")
	assertMigrationRow(t, path, 36, false)
	assertMigrationRow(t, path, 38, false)
	t.Log("PASS restored pre-upgrade session.db + wacli.db snapshot, old binary opens both")
}

// Optional old-binary compatibility check for the quick reply migrations
// (36 lists, 38 quick replies). The only stores it touches are disposable
// temp directories.
//
// Forward: a store migrated by this build is still readable by the previous
// binary in --read-only mode (binary rollback without restoring a snapshot),
// and that binary reports `quick-replies` as an unknown command, which is the
// signal the gateway expects from an older daemon.
//
// Backward: a store created by the previous binary opens with this build,
// gets both migrations, and lists no quick replies with `incomplete`
// completeness until a full sync of `regular` is observed.
func TestQuickRepliesStoreCompatibleWithPreviousBinary(t *testing.T) {
	bin := os.Getenv("WACLI_COMPAT_BIN")
	if bin == "" {
		t.Skip("set WACLI_COMPAT_BIN to the previous binary")
	}

	t.Run("forward", func(t *testing.T) {
		dir := t.TempDir()
		db := mustOpen(t, filepath.Join(dir, "wacli.db"))
		chat := "123@s.whatsapp.net"
		seedOutgoing(t, db, chat, "m1", time.Now())
		if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "m1", Timestamp: time.Now(), Kind: "contact", Text: "fixture contact"}); err != nil {
			t.Fatal(err)
		}
		count := int32(1)
		if err := db.UpsertQuickReply(QuickReply{ID: "1700000000", Shortcut: "hola", Message: "Hola", Keywords: []string{"k"}, Count: &count}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := db.MarkRegularFullSyncComplete(); err != nil {
			t.Fatal(err)
		}
		db.Close()
		for _, version := range []int{36, 38, 9035} {
			assertMigrationRow(t, filepath.Join(dir, "wacli.db"), version, true)
		}
		output, err := exec.Command(bin, "--store", dir, "--read-only", "--json", "messages", "list", "--chat", chat, "--limit", "10").CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte("fixture contact")) {
			t.Fatalf("old reader: %s (%v)", output, err)
		}
		output, err = exec.Command(bin, "--store", dir, "--read-only", "--json", "quick-replies", "list").CombinedOutput()
		if err == nil || !bytes.Contains(output, []byte("unknown command")) {
			t.Fatalf("old binary must reject quick-replies: %s (%v)", output, err)
		}
		// The old binary still only knows its own rows; the new ones survive.
		db = mustOpen(t, filepath.Join(dir, "wacli.db"))
		defer db.Close()
		got, err := db.QuickReplies()
		if err != nil || len(got.QuickReplies) != 1 || got.Completeness != "complete" {
			t.Fatalf("quick replies after old reader = %+v, %v", got, err)
		}
	})

	t.Run("backward", func(t *testing.T) {
		dir := t.TempDir()
		// A non read-only command of the previous binary creates and
		// migrates the store with its own migration set.
		output, err := exec.Command(bin, "--store", dir, "--json", "chats", "list", "--limit", "1").CombinedOutput()
		if err != nil {
			t.Fatalf("old writer: %s (%v)", output, err)
		}
		path := filepath.Join(dir, "wacli.db")
		assertMigrationRow(t, path, 9035, true)
		assertMigrationRow(t, path, 36, false)
		assertMigrationRow(t, path, 38, false)
		db := mustOpen(t, path)
		defer db.Close()
		assertMigrationRow(t, path, 36, true)
		assertMigrationRow(t, path, 38, true)
		got, err := db.QuickReplies()
		if err != nil || len(got.QuickReplies) != 0 || got.Completeness != "incomplete" {
			t.Fatalf("quick replies on upgraded store = %+v, %v", got, err)
		}
		lists, err := db.Lists()
		if err != nil || len(lists.Lists) != 0 || lists.FullSyncEventsSeen {
			t.Fatalf("lists on upgraded store = %+v, %v", lists, err)
		}
	})
}

func assertMigrationRow(t *testing.T, path string, version int, want bool) {
	t.Helper()
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	var got bool
	if err := ro.sql.QueryRow(`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=?)`, version).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("schema_migrations row %d present = %v, want %v", version, got, want)
	}
}
