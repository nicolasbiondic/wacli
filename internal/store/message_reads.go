package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func ensureMessageReadsTable(d *DB) error {
	if _, err := d.sql.Exec(`
		CREATE TABLE IF NOT EXISTS message_reads (
			chat_jid TEXT NOT NULL,
			msg_id TEXT NOT NULL,
			read_ts INTEGER NOT NULL,
			source TEXT NOT NULL CHECK(source IN ('other_device', 'this_device')),
			PRIMARY KEY (chat_jid, msg_id),
			FOREIGN KEY (chat_jid) REFERENCES chats(jid) ON DELETE CASCADE
		);
	`); err != nil {
		return fmt.Errorf("ensure message reads: %w", err)
	}
	// Read times are exported message state, just like recipient counts. Keep
	// pagination invalidation without changing journal checkpoints or identity.
	for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
		if _, err := d.sql.Exec(`CREATE TRIGGER IF NOT EXISTS retrieval_message_reads_` + strings.ToLower(op) + ` AFTER ` + op + ` ON message_reads BEGIN UPDATE retrieval_state SET revision=revision+1 WHERE id=1; END`); err != nil {
			return fmt.Errorf("ensure message read revisions: %w", err)
		}
	}
	return nil
}

// UpsertMessageReads retains the earliest observed read of each listed inbound
// ID. Unknown IDs in known chats may arrive before history; known outgoing IDs
// are excluded. A receipt's read time is not a message-position timestamp.
func (d *DB) UpsertMessageReads(chatJID string, ids []string, ts time.Time, source string) error {
	chatJID = strings.TrimSpace(chatJID)
	if chatJID == "" || (source != "other_device" && source != "this_device") {
		return fmt.Errorf("chat JID and valid read source are required")
	}
	if ts.IsZero() || len(ids) == 0 {
		return nil // Do not invent a read time for untimestamped events.
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO message_reads(chat_jid, msg_id, read_ts, source)
			SELECT ?1, ?2, ?3, ?4
			WHERE EXISTS (SELECT 1 FROM chats WHERE jid = ?1)
				AND NOT EXISTS (SELECT 1 FROM messages WHERE chat_jid = ?1 AND msg_id = ?2 AND from_me = 1)
			ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
				read_ts = excluded.read_ts, source = excluded.source
			WHERE excluded.read_ts < message_reads.read_ts
		`, chatJID, id, unix(ts), source); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) messageReadColumn() string {
	if !d.readsEnabled {
		return "NULL"
	}
	return `(SELECT read_ts FROM message_reads r WHERE m.from_me = 0 AND r.chat_jid = m.chat_jid AND r.msg_id = m.msg_id)`
}

func migrateLIDMessageReadsToPN(tx *sql.Tx, lidJID, pnJID string) error {
	if _, err := tx.Exec(`
		INSERT INTO message_reads(chat_jid, msg_id, read_ts, source)
		SELECT ?, msg_id, read_ts, source FROM message_reads WHERE chat_jid = ?
		ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
			read_ts = excluded.read_ts, source = excluded.source
		WHERE excluded.read_ts < message_reads.read_ts
	`, pnJID, lidJID); err != nil {
		return fmt.Errorf("migrate lid message reads: %w", err)
	}
	_, err := tx.Exec(`DELETE FROM message_reads WHERE chat_jid = ?`, lidJID)
	return err
}
