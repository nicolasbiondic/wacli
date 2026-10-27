package store

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestQuickRepliesUpsertOrderingAndResolution(t *testing.T) {
	db := openTestDB(t)
	at := time.Unix(100, 0)
	q := QuickReply{ID: "1700000000", Shortcut: "hola", Message: "Hola", Keywords: []string{"saludo"}, Count: listPtr(int32(2))}
	if err := db.UpsertQuickReply(q, at); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertQuickReply(QuickReply{ID: "1700000000", Shortcut: "viejo", Message: "older"}, at.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := db.QuickReplies()
	if err != nil || len(got.QuickReplies) != 1 || got.Completeness != "incomplete" {
		t.Fatalf("QuickReplies = %+v, %v", got, err)
	}
	if want := (QuickReply{ID: q.ID, Shortcut: "hola", Message: "Hola", Keywords: []string{"saludo"}, LabelIDs: []string{}, Count: listPtr(int32(2))}); !reflect.DeepEqual(got.QuickReplies[0], want) {
		t.Fatalf("stored = %+v, want %+v", got.QuickReplies[0], want)
	}
	for _, ref := range []string{"1700000000", "hola", "/hola"} {
		if r, err := db.ResolveQuickReply(ref); err != nil || r.ID != q.ID {
			t.Fatalf("ResolveQuickReply(%q) = %+v, %v", ref, r, err)
		}
	}
	// Equal timestamps keep a deletion; older replays cannot undo it.
	q.Deleted = true
	if err := db.UpsertQuickReply(q, at); err != nil {
		t.Fatal(err)
	}
	q.Deleted = false
	if err := db.UpsertQuickReply(q, at); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.QuickReplies(); len(got.QuickReplies) != 0 {
		t.Fatalf("deleted reply visible: %+v", got)
	}
	if r, found, err := db.QuickReplyByID(q.ID); err != nil || !found || !r.Deleted {
		t.Fatalf("tombstone = %+v %v %v", r, found, err)
	}
	if _, err := db.ResolveQuickReply("hola"); err == nil {
		t.Fatal("deleted reply resolved")
	}
	for _, id := range []string{"a", "b"} {
		if err := db.UpsertQuickReply(QuickReply{ID: id, Shortcut: "dup", Message: "x"}, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ResolveQuickReply("dup"); err == nil {
		t.Fatal("ambiguous shortcut resolved")
	}
}

func TestQuickRepliesCompletenessRequiresRegularCompletion(t *testing.T) {
	db := openTestDB(t)
	got, err := db.QuickReplies()
	if err != nil || got.Completeness != "incomplete" {
		t.Fatalf("initial = %+v, %v", got, err)
	}
	if err := db.UpsertList(ListUpdate{ID: "1", Name: listPtr("Seen"), FromFullSync: true}); err != nil {
		t.Fatal(err)
	}
	got, err = db.QuickReplies()
	if err != nil || got.Completeness != "incomplete" {
		t.Fatalf("partial full sync = %+v, %v", got, err)
	}
	if err := db.MarkRegularFullSyncComplete(); err != nil {
		t.Fatal(err)
	}
	got, err = db.QuickReplies()
	if err != nil || got.Completeness != "complete" {
		t.Fatalf("after full sync = %+v, %v", got, err)
	}
}

func TestQuickRepliesLegacySeenFlagDoesNotProveCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	if _, err := db.sql.Exec(`DROP TABLE list_sync_state;
		CREATE TABLE list_sync_state(id INTEGER PRIMARY KEY,full_sync_events_seen INTEGER NOT NULL DEFAULT 0);
		INSERT INTO list_sync_state VALUES(1,1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ro.QuickReplies(); err != nil || got.Completeness != "incomplete" {
		t.Fatalf("legacy read-only = %+v, %v", got, err)
	}
	ro.Close()
	db = mustOpen(t, path)
	defer db.Close()
	if got, err := db.QuickReplies(); err != nil || got.Completeness != "incomplete" {
		t.Fatalf("legacy upgrade = %+v, %v", got, err)
	}
}

func TestMaxNumericListIDCountsDeletedAndIgnoresOtherIDs(t *testing.T) {
	db := openTestDB(t)
	if got, err := db.MaxNumericListID(); err != nil || got != 0 {
		t.Fatalf("empty = %d, %v", got, err)
	}
	for _, p := range []ListUpdate{
		{ID: "3", Name: listPtr("a")},
		{ID: "12", Name: listPtr("gone"), Deleted: listPtr(true)},
		{ID: "abc", Name: listPtr("text")},
		{ID: "99x", Name: listPtr("mixed")},
	} {
		if err := db.UpsertList(p); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := db.MaxNumericListID(); err != nil || got != 12 {
		t.Fatalf("max = %d, %v", got, err)
	}
	if r, found, err := db.ListByID("12"); err != nil || !found || !r.Deleted {
		t.Fatalf("ListByID deleted = %+v %v %v", r, found, err)
	}
	if _, err := db.ResolveListForWrite("12"); err == nil {
		t.Fatal("deleted list resolved for write")
	}
}

// Another branch may own version 36; the schema guard must still create the
// table, and a read-only open of an older store must not fail.
func TestQuickRepliesMigrationSurvivesVersionCollision(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		owner   string
	}{
		{"lists at 36", 36, "WhatsApp lists and chat memberships"},
		{"message kinds at 37", 37, "messages semantic kind"},
		{"foreign row at the quick replies version", 38, "foreign"},
		{"recorded quick replies version without the table", 38, "WhatsApp Business quick replies"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wacli.db")
			db := mustOpen(t, path)
			if _, err := db.sql.Exec(`DROP TABLE quick_replies; DELETE FROM schema_migrations WHERE version IN (38, ?)`, tc.version); err != nil {
				t.Fatal(err)
			}
			if _, err := db.sql.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, 1)`, tc.version, tc.owner); err != nil {
				t.Fatal(err)
			}
			db.Close()
			ro, err := OpenReadOnly(path)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := ro.QuickReplies(); err != nil || len(got.QuickReplies) != 0 {
				t.Fatalf("read-only without table = %+v, %v", got, err)
			}
			ro.Close()
			db = mustOpen(t, path)
			defer db.Close()
			if err := db.UpsertQuickReply(QuickReply{ID: "1", Shortcut: "a", Message: "b"}, time.Unix(1, 0)); err != nil {
				t.Fatalf("table not recreated: %v", err)
			}
		})
	}
}
