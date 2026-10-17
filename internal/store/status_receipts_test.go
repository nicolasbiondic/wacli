package store

import (
	"path/filepath"
	"testing"
	"time"
)

func seedOwnStatus(t *testing.T, db *DB, msgID string, fromMe bool) {
	t.Helper()
	if err := db.UpsertStatusMessage(UpsertStatusMessageParams{
		MsgID: msgID, Timestamp: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC), FromMe: fromMe, Text: "hello",
	}); err != nil {
		t.Fatalf("UpsertStatusMessage: %v", err)
	}
}

func TestStatusReceiptsAreKeptPerParticipantAndOnlyMoveForward(t *testing.T) {
	db := openTestDB(t)
	seedOwnStatus(t, db, "s1", true)
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	first, second := "111@s.whatsapp.net", "222@s.whatsapp.net"

	for _, step := range []struct {
		who, status string
		stored      bool
	}{
		{first, "delivered", true},
		{second, "read", true},
		{first, "read", true},
		// A later, weaker report must not take the viewer back.
		{second, "delivered", false},
		{first, "read", false},
	} {
		stored, err := db.UpsertStatusReceipt("s1", step.who, step.status, at)
		if err != nil {
			t.Fatalf("UpsertStatusReceipt(%s,%s): %v", step.who, step.status, err)
		}
		if stored != step.stored {
			t.Fatalf("UpsertStatusReceipt(%s,%s) stored = %t, want %t", step.who, step.status, stored, step.stored)
		}
	}
	counts, err := db.StatusReceipts("s1")
	if err != nil {
		t.Fatalf("StatusReceipts: %v", err)
	}
	if counts.Delivered != 2 || counts.Read != 2 {
		t.Fatalf("counts = %+v, want two participants who both read", counts)
	}
}

func TestStatusReceiptsIgnoreStatusesWeDoNotOwn(t *testing.T) {
	db := openTestDB(t)
	seedOwnStatus(t, db, "theirs", false)
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	for _, id := range []string{"theirs", "unknown"} {
		stored, err := db.UpsertStatusReceipt(id, "111@s.whatsapp.net", "read", at)
		if err != nil || stored {
			t.Fatalf("UpsertStatusReceipt(%s) = %t, %v; want it dropped", id, stored, err)
		}
		counts, err := db.StatusReceipts(id)
		if err != nil || counts.Delivered != 0 {
			t.Fatalf("StatusReceipts(%s) = %+v, %v; want nothing", id, counts, err)
		}
	}
}

func TestStatusReceiptsRejectBadInput(t *testing.T) {
	db := openTestDB(t)
	seedOwnStatus(t, db, "s1", true)
	at := time.Now()
	if _, err := db.UpsertStatusReceipt("", "111@s.whatsapp.net", "read", at); err == nil {
		t.Fatal("empty status id accepted")
	}
	if _, err := db.UpsertStatusReceipt("s1", "", "read", at); err == nil {
		t.Fatal("empty participant accepted")
	}
	if _, err := db.UpsertStatusReceipt("s1", "111@s.whatsapp.net", "retry", at); err == nil {
		t.Fatal("unknown status accepted")
	}
}

func TestStatusReceiptsFollowTheirStatusOnDelete(t *testing.T) {
	db := openTestDB(t)
	seedOwnStatus(t, db, "s1", true)
	if _, err := db.UpsertStatusReceipt("s1", "111@s.whatsapp.net", "read", time.Now()); err != nil {
		t.Fatalf("UpsertStatusReceipt: %v", err)
	}
	if _, err := db.sql.Exec(`DELETE FROM status_messages WHERE msg_id = 's1'`); err != nil {
		t.Fatalf("delete status: %v", err)
	}
	if n := vendasCount(t, db, `SELECT count(*) FROM status_receipts`); n != 0 {
		t.Fatalf("status_receipts rows after deleting the status = %d, want 0", n)
	}
}

// Stores migrated before the table existed must get it on open, and the
// migration must be safe to run again under any number.
func TestOpeningAnOlderStoreAddsTheStatusReceiptTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	seedOwnStatus(t, db, "s1", true)
	if _, err := db.sql.Exec(`DROP TABLE status_receipts`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := db.sql.Exec(`DELETE FROM schema_migrations WHERE version = 9035`); err != nil {
		t.Fatalf("forget the migration: %v", err)
	}
	db.Close()

	reopened := mustOpen(t, path)
	if !reopened.hasTable("status_receipts") {
		t.Fatal("reopening an older store did not add the status receipt table")
	}
	if n := vendasCount(t, reopened, `SELECT count(*) FROM schema_migrations WHERE version = 9035 AND name = 'vendas: status receipts'`); n != 1 {
		t.Fatalf("local migration marker count = %d, want 1", n)
	}
	if stored, err := reopened.UpsertStatusReceipt("s1", "111@s.whatsapp.net", "read", time.Now()); err != nil || !stored {
		t.Fatalf("UpsertStatusReceipt = %t, %v; want stored", stored, err)
	}
	reopened.Close()

	// The migration body also runs when the marker exists but the table does
	// not (ensureCurrentSchema), and again on a healthy store: no error, data kept.
	again := mustOpen(t, path)
	defer again.Close()
	if err := ensureStatusReceiptsTable(again); err != nil {
		t.Fatalf("second ensureStatusReceiptsTable: %v", err)
	}
	counts, err := again.StatusReceipts("s1")
	if err != nil || counts.Read != 1 {
		t.Fatalf("counts = %+v, %v; want the receipt kept", counts, err)
	}
}

func TestStatusReceiptTableIsRecreatedWhenOnlyTheMarkerSurvived(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	if _, err := db.sql.Exec(`DROP TABLE status_receipts`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	db.Close()
	reopened := mustOpen(t, path)
	defer reopened.Close()
	if !reopened.hasTable("status_receipts") {
		t.Fatal("a store with the marker but not the table was not repaired")
	}
}
