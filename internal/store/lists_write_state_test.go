package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func completeAt(t *testing.T, db *DB) (at *int64) {
	t.Helper()
	var v *int64
	if err := db.sql.QueryRow(`SELECT full_sync_complete_at FROM list_sync_state WHERE id=1`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRegularFullSyncCompleteAtIsSetAndCleared(t *testing.T) {
	db := openTestDB(t)
	if completeAt(t, db) != nil {
		t.Fatal("fresh store has a completion time")
	}
	defer func(prev func() time.Time) { nowUTC = prev }(nowUTC)
	nowUTC = func() time.Time { return time.UnixMilli(1_700_000_123_456) }
	if err := db.MarkRegularFullSyncComplete(); err != nil {
		t.Fatal(err)
	}
	if at := completeAt(t, db); at == nil || *at != 1_700_000_123_456 {
		t.Fatalf("complete_at = %v", at)
	}
	if err := db.InvalidateRegularFullSync(); err != nil {
		t.Fatal(err)
	}
	if completeAt(t, db) != nil {
		t.Fatal("invalidate kept the completion time")
	}
	if err := db.MarkRegularFullSyncComplete(); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertList(ListUpdate{ID: "1", Name: listPtr("x"), FromFullSync: true}); err != nil {
		t.Fatal(err)
	}
	if completeAt(t, db) != nil {
		t.Fatal("a new full-sync snapshot kept the completion time")
	}
}

func TestRegularIndexCompletenessBrokenOrdering(t *testing.T) {
	defer func(prev func() time.Time) { nowUTC = prev }(nowUTC)
	for _, tc := range []struct {
		name     string
		brokenAt int64 // seconds; 0 = not broken
		doneAtMS int64
		complete bool
		usable   bool
	}{
		{"never complete", 0, 0, false, false},
		{"complete, not broken", 0, 5_000, true, true},
		{"broken after complete", 6, 5_000, true, false},
		{"broken in the same second", 5, 5_999, true, false},
		{"complete after broken", 5, 6_000, true, true},
		{"broken and never complete", 5, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			if tc.complete {
				nowUTC = func() time.Time { return time.UnixMilli(tc.doneAtMS) }
				if err := db.MarkRegularFullSyncComplete(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.brokenAt != 0 {
				nowUTC = func() time.Time { return time.Unix(tc.brokenAt, 0) }
				if err := db.SetAppStateFullSyncBroken("regular", true); err != nil {
					t.Fatal(err)
				}
			}
			got, err := db.RegularIndexCompleteness()
			if err != nil || got.UsableForNewIDs() != tc.usable || got.Complete != tc.complete || got.Broken != (tc.brokenAt != 0) {
				t.Fatalf("completeness = %+v, %v; want usable=%v", got, err, tc.usable)
			}
			// Marking broken never lowers the completion bit.
			if full, err := db.RegularFullSyncComplete(); err != nil || full != tc.complete {
				t.Fatalf("full_sync_complete = %v, %v", full, err)
			}
		})
	}
}

func TestRegularIndexCompletenessToleratesMissingColumnAndTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	if err := db.MarkRegularFullSyncComplete(); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAppStateFullSyncBroken("regular", true); err != nil {
		t.Fatal(err)
	}
	// An older store: no completion time column.
	if _, err := db.sql.Exec(`ALTER TABLE list_sync_state DROP COLUMN full_sync_complete_at`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	err = ro.ReadView(func(view *DB) error {
		got, err := view.RegularIndexCompleteness()
		if err != nil || !got.Complete || !got.Broken || got.CompleteAfterBroken || got.UsableForNewIDs() {
			t.Fatalf("missing column = %+v, %v", got, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ro.Close()
	// A writable open adds the column back, idempotently.
	for range 2 {
		db = mustOpen(t, path)
		if completeAt(t, db) != nil {
			t.Fatal("legacy completion gained a time")
		}
		db.Close()
	}
	// Without the broken table at all, the index is simply not broken.
	db = mustOpen(t, path)
	if _, err := db.sql.Exec(`DROP TABLE app_state_full_sync_broken`); err != nil {
		t.Fatal(err)
	}
	if got, err := db.RegularIndexCompleteness(); err != nil || !got.UsableForNewIDs() || got.Broken {
		t.Fatalf("no broken table = %+v, %v", got, err)
	}
	db.Close()
}

func TestResolveListForWriteDistinguishesCauses(t *testing.T) {
	db := openTestDB(t)
	for _, p := range []ListUpdate{
		{ID: "1", Name: listPtr("a")},
		{ID: "2", Name: listPtr("dup")},
		{ID: "3", Name: listPtr("dup")},
		{ID: "4", Name: listPtr("gone"), Deleted: listPtr(true)},
	} {
		if err := db.UpsertList(p); err != nil {
			t.Fatal(err)
		}
	}
	for ref, want := range map[string]error{"nope": ErrListNotFound, "4": ErrListDeleted, "gone": ErrListDeleted, "dup": ErrListAmbiguous} {
		if _, err := db.ResolveListForWrite(ref); !errors.Is(err, want) {
			t.Fatalf("%q: err = %v, want %v", ref, err, want)
		}
	}
	if l, err := db.ResolveListForWrite("a"); err != nil || l.ID != "1" {
		t.Fatalf("live = %+v, %v", l, err)
	}
}

func TestAppStateRecoveryPendingReadsInsideTheView(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.MarkAppStateRecoveryGeneration("regular"); err != nil {
		t.Fatal(err)
	}
	err := db.ReadView(func(view *DB) error {
		for collection, want := range map[string]bool{"regular": true, "regular_low": false} {
			if got, err := view.AppStateRecoveryPending(collection); err != nil || got != want {
				t.Fatalf("%s pending = %v, %v", collection, got, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
