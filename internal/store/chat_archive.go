package store

import (
	"database/sql"
	"fmt"
	"time"
)

func migrateChatAutoUnarchive(d *DB) error {
	hasChats, err := d.tableExists("chats")
	if err != nil || !hasChats {
		return err
	}
	for _, column := range []string{"archive_action_ms", "archive_incoming_ms", "archive_cleared_ms"} {
		exists, err := d.tableHasColumn("chats", column)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := d.sql.Exec("ALTER TABLE chats ADD COLUMN " + column + " INTEGER NOT NULL DEFAULT 0"); err != nil {
				return fmt.Errorf("add chat archive boundary: %w", err)
			}
		}
	}
	_, err = d.sql.Exec(`CREATE TABLE IF NOT EXISTS chat_archive_setting (
 id INTEGER PRIMARY KEY CHECK(id=1), enabled INTEGER NOT NULL, timestamp_ms INTEGER NOT NULL
)`)
	return err
}

// Missing preferences and archive timestamps are unknown, never the default
// phone preference. A full regular_low replay supplies them on upgraded stores.
func (d *DB) ChatArchiveRecoveryRequired() (bool, error) {
	var required bool
	err := d.sql.QueryRow(`SELECT NOT EXISTS(SELECT 1 FROM chat_archive_setting WHERE id=1)
 OR EXISTS(SELECT 1 FROM chats WHERE archived != 0 AND archive_action_ms=0)`).Scan(&required)
	return required, err
}

func (d *DB) SetChatArchiveSetting(enabled bool, timestamp time.Time) error {
	if timestamp.IsZero() {
		return nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO chat_archive_setting(id,enabled,timestamp_ms) VALUES(1,?,?)
 ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled,timestamp_ms=excluded.timestamp_ms
 WHERE excluded.timestamp_ms > chat_archive_setting.timestamp_ms`, boolToInt(enabled), timestamp.UnixMilli()); err != nil {
		return err
	}
	if err := reconcileChatArchive(tx, ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) ApplyChatArchive(jid string, archived bool, timestamp time.Time) error {
	if timestamp.IsZero() {
		return d.SetChatArchived(jid, archived)
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A replay at the same timestamp must not re-archive a newer arrival.
	if _, err := tx.Exec(`UPDATE chats SET
 archived=CASE WHEN ? AND ? > archive_cleared_ms THEN 1 ELSE 0 END,
 pinned=CASE WHEN ? AND ? > archive_cleared_ms THEN 0 ELSE pinned END, archive_action_ms=?
 WHERE jid=? AND ? >= archive_action_ms`, archived, timestamp.UnixMilli(), archived, timestamp.UnixMilli(), timestamp.UnixMilli(), jid, timestamp.UnixMilli()); err != nil {
		return err
	}
	if err := reconcileChatArchive(tx, jid); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordChatArchiveIncoming is called only for a first eligible live payload,
// never for history imports, duplicates, edits, reactions or tombstones.
func (d *DB) RecordChatArchiveIncoming(jid string, timestamp time.Time) error {
	if timestamp.IsZero() {
		return nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE chats SET archive_incoming_ms=MAX(archive_incoming_ms,?) WHERE jid=?`, timestamp.UnixMilli(), jid); err != nil {
		return err
	}
	if err := reconcileChatArchive(tx, jid); err != nil {
		return err
	}
	return tx.Commit()
}

func reconcileChatArchive(tx *sql.Tx, jid string) error {
	query := `UPDATE chats SET archived=0,
 archive_cleared_ms=MAX(archive_cleared_ms,archive_incoming_ms)
 WHERE archive_action_ms > 0
 AND archive_incoming_ms > archive_action_ms
 AND EXISTS(SELECT 1 FROM chat_archive_setting WHERE id=1 AND enabled=1
   AND archive_incoming_ms > timestamp_ms)`
	var args []any
	if jid != "" {
		query += " AND jid=?"
		args = append(args, jid)
	}
	_, err := tx.Exec(query, args...)
	return err
}

func migrateLIDChatArchive(tx *sql.Tx, lid, phone string) error {
	if _, err := tx.Exec(`UPDATE chats AS dst SET
 archived=CASE WHEN src.archive_action_ms > dst.archive_action_ms THEN src.archived
   WHEN dst.archive_action_ms=0 THEN MAX(src.archived,dst.archived) ELSE dst.archived END,
 archive_action_ms=MAX(src.archive_action_ms,dst.archive_action_ms),
 archive_incoming_ms=MAX(src.archive_incoming_ms,dst.archive_incoming_ms),
 archive_cleared_ms=MAX(src.archive_cleared_ms,dst.archive_cleared_ms)
 FROM chats AS src WHERE src.jid=? AND dst.jid=?`, lid, phone); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE chats SET archived=0 WHERE jid=? AND archive_action_ms > 0 AND archive_cleared_ms >= archive_action_ms`, phone); err != nil {
		return err
	}
	return reconcileChatArchive(tx, phone)
}
