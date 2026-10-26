package store

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

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
		if err := db.MarkRegularFullSyncEventsSeen(); err != nil {
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
