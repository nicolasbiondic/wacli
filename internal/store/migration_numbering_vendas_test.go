package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The ad_referral migration from upstream PR #480 is recorded locally as
// version 9033 ("vendas: messages ad_referral (#480)") instead of the PR's 33,
// so that whatever number upstream finally assigns is still unapplied on our
// stores. These tests prove both directions of that compatibility story, plus
// that the runner tolerates the resulting numbering gap.

func seedAdReferralFixture(t *testing.T, db *DB) {
	t.Helper()
	chat := "lead@s.whatsapp.net"
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if err := db.UpsertChat(chat, "dm", "Lead", base); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	if err := db.UpsertMessage(UpsertMessageParams{ChatJID: chat, MsgID: "fx1", SenderJID: chat, Timestamp: base, Text: "hola"}); err != nil {
		t.Fatalf("UpsertMessage: %v", err)
	}
}

func updateTriggerSQL(t *testing.T, db *DB) string {
	t.Helper()
	var sqlText string
	if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='retrieval_messages_update'`).Scan(&sqlText); err != nil {
		t.Fatalf("read retrieval_messages_update: %v", err)
	}
	return sqlText
}

func vendasCount(t *testing.T, db *DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// Direction 1: a pre-ad_referral store upgraded by our build (which records
// 9033) later sees upstream's own version of the migration (33 in the PR).
// The body must be a no-op: same triggers, no duplicates, journal untouched.
func TestUpstreamAdReferralMigrationIsNoOpAfterVendasNumbering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	seedAdReferralFixture(t, db)
	downgradeToBeforeAdReferral(t, db)
	db.Close()

	// Our build upgrades the store and records the local number.
	db = mustOpen(t, path)
	defer db.Close()
	if n := vendasCount(t, db, `SELECT count(*) FROM schema_migrations WHERE version=9033 AND name='vendas: messages ad_referral (#480)'`); n != 1 {
		t.Fatalf("local migration marker count = %d; want 1", n)
	}
	if n := vendasCount(t, db, `SELECT count(*) FROM schema_migrations WHERE version=33`); n != 0 {
		t.Fatalf("unexpected upstream marker before upstream migration: %d", n)
	}
	triggerBefore := updateTriggerSQL(t, db)
	journalBefore := vendasCount(t, db, `SELECT count(*) FROM message_changes`)

	// Upstream lands #480: its migration runs the same idempotent body and
	// records its own marker.
	if err := migrateMessagesAdReferralColumn(db); err != nil {
		t.Fatalf("upstream migration body on vendas-migrated store: %v", err)
	}
	if _, err := db.sql.Exec(`INSERT INTO schema_migrations(version,name,applied_at) VALUES(33,'messages ad_referral column',?)`, time.Now().Unix()); err != nil {
		t.Fatalf("record upstream marker: %v", err)
	}

	if got := updateTriggerSQL(t, db); got != triggerBefore {
		t.Fatalf("upstream migration rebuilt the update trigger:\nbefore: %s\nafter: %s", triggerBefore, got)
	}
	if n := vendasCount(t, db, `SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'retrieval_messages_%'`); n != 3 {
		t.Fatalf("message change trigger count = %d; want 3", n)
	}
	if n := strings.Count(updateTriggerSQL(t, db), "old.ad_referral IS NOT new.ad_referral"); n != 1 {
		t.Fatalf("ad_referral comparison appears %d times in update trigger; want 1", n)
	}
	if n := vendasCount(t, db, `SELECT count(*) FROM message_changes`); n != journalBefore {
		t.Fatalf("journal reseeded: %d entries, was %d", n, journalBefore)
	}
}

// Direction 2: a store that upstream's numbering already migrated (marker 33,
// column and triggers present) must open under our build without error; our
// 9033 run must be a harmless no-op.
func TestStoreMigratedByUpstreamNumberingOpensUnderVendasNumbering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	seedAdReferralFixture(t, db)
	// Column and triggers already exist; only the bookkeeping differs.
	if _, err := db.sql.Exec(`DELETE FROM schema_migrations WHERE version=9033`); err != nil {
		t.Fatalf("drop local marker: %v", err)
	}
	if _, err := db.sql.Exec(`INSERT INTO schema_migrations(version,name,applied_at) VALUES(33,'messages ad_referral column',?)`, time.Now().Unix()); err != nil {
		t.Fatalf("record upstream marker: %v", err)
	}
	triggerBefore := updateTriggerSQL(t, db)
	journalBefore := vendasCount(t, db, `SELECT count(*) FROM message_changes`)
	db.Close()

	db = mustOpen(t, path)
	defer db.Close()
	if n := vendasCount(t, db, `SELECT count(*) FROM schema_migrations WHERE version IN (33, 9033)`); n != 2 {
		t.Fatalf("expected both markers after reopen, found %d", n)
	}
	if got := updateTriggerSQL(t, db); got != triggerBefore {
		t.Fatalf("reopen rebuilt the update trigger:\nbefore: %s\nafter: %s", triggerBefore, got)
	}
	if n := vendasCount(t, db, `SELECT count(*) FROM message_changes`); n != journalBefore {
		t.Fatalf("journal reseeded on reopen: %d entries, was %d", n, journalBefore)
	}
	rows, err := db.ListMessages(ListMessagesParams{ChatJID: "lead@s.whatsapp.net", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListMessages = %d rows, err = %v; want 1", len(rows), err)
	}
}

// The runner applies migrations by version-set membership, so the jump from
// 32 to 9033 must neither error nor re-run anything on reopen.
func TestMigrationRunnerAcceptsNumberingGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	db := mustOpen(t, path)
	if n := vendasCount(t, db, `SELECT count(*) FROM schema_migrations WHERE version=32`); n != 1 {
		t.Fatalf("migration 32 missing")
	}
	if n := vendasCount(t, db, `SELECT count(*) FROM schema_migrations WHERE version=9033`); n != 1 {
		t.Fatalf("migration 9033 missing")
	}
	if n := vendasCount(t, db, `SELECT count(*) FROM schema_migrations WHERE version>32 AND version<9033`); n != 0 {
		t.Fatalf("unexpected migrations inside the gap: %d", n)
	}
	total := vendasCount(t, db, `SELECT count(*) FROM schema_migrations`)
	db.Close()

	db = mustOpen(t, path)
	defer db.Close()
	if n := vendasCount(t, db, `SELECT count(*) FROM schema_migrations`); n != total {
		t.Fatalf("reopen changed migration count from %d to %d", total, n)
	}
}
