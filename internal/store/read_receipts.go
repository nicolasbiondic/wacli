package store

import (
	"database/sql"
	"fmt"
	"strings"
)

const receiptEligible = "chat_jid = ? AND from_me = 0 AND revoked = 0 AND deleted_for_me = 0 AND deleted_at IS NULL AND COALESCE(reaction_to_id,'') = '' AND COALESCE(reaction_emoji,'') = '' AND (COALESCE(display_text,'') != '(message)' OR TRIM(COALESCE(text,'')) != '')"

// ReadBoundary resolves an exact inbound anchor before app-state recovery can
// start. The chat predicate prevents an ID from another chat becoming a cutoff.
func (d *DB) ReadBoundary(jid, id string) (MessageInfo, error) {
	m, _, err := readBoundary(d.sql, jid, id)
	return m, err
}

func readBoundary(db interface {
	QueryRow(string, ...any) *sql.Row
}, jid, id string) (MessageInfo, int64, error) {
	if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
		return MessageInfo{}, 0, fmt.Errorf("--through-message-id requires an exact nonempty message ID")
	}
	m := MessageInfo{ChatJID: jid}
	var rowID, ts int64
	err := db.QueryRow("SELECT rowid, msg_id, COALESCE(sender_jid,''), ts FROM messages WHERE "+receiptEligible+" AND msg_id = ?", jid, id).Scan(&rowID, &m.MsgID, &m.SenderJID, &ts)
	if err != nil {
		return MessageInfo{}, 0, fmt.Errorf("read boundary %q must be a stored inbound message in chat %s: %w", id, jid, err)
	}
	m.Timestamp = fromUnix(ts)
	if m.Timestamp.IsZero() {
		return MessageInfo{}, 0, fmt.Errorf("message %q in chat %s has no read boundary", id, jid)
	}
	return m, rowID, nil
}

// ReadReceiptCandidates takes one snapshot of the unread count and eligible
// messages, then returns the oldest bounded slice so repeated calls progress.
// missing is how many unread messages are older than anything stored (an
// unread count from history sync whose messages were never downloaded).
func (d *DB) ReadReceiptCandidates(jid string, limit int) (msgs []MessageInfo, missing int, err error) {
	return d.readReceiptCandidates(jid, "", limit)
}

// ReadReceiptCandidatesThrough intersects the current unread suffix with the
// handled prefix. Both are selected in one snapshot; newer same-second rows are
// excluded using the anchor's row position, and the existing batch cap remains.
func (d *DB) ReadReceiptCandidatesThrough(jid, id string, limit int) (msgs []MessageInfo, missing int, err error) {
	if strings.TrimSpace(id) == "" {
		return nil, 0, fmt.Errorf("--through-message-id requires an exact nonempty message ID")
	}
	return d.readReceiptCandidates(jid, id, limit)
}

func (d *DB) readReceiptCandidates(jid, throughID string, limit int) ([]MessageInfo, int, error) {
	tx, err := d.sql.BeginTx(storeCtx(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	boundary := ""
	var boundaryArgs []any
	if throughID != "" {
		m, rowID, err := readBoundary(tx, jid, throughID)
		if err != nil {
			return nil, 0, err
		}
		boundary = " WHERE ts < ? OR (ts = ? AND rowid <= ?)"
		boundaryArgs = []any{unix(m.Timestamp), unix(m.Timestamp), rowID}
	}
	var count int
	var marked bool
	if err := tx.QueryRow("SELECT unread_count, unread FROM chats WHERE jid = ?", jid).Scan(&count, &marked); err != nil {
		return nil, 0, err
	}
	if count == 0 && !marked {
		return nil, 0, tx.Commit()
	}
	var available int
	if err := tx.QueryRow("SELECT COUNT(*) FROM messages WHERE "+receiptEligible, jid).Scan(&available); err != nil {
		return nil, 0, err
	}
	// An unread count above the stored rows means every stored eligible row is
	// unread and the remainder predates the store (history sync reported the
	// count without the messages). Acknowledge what is stored; the caller's
	// boundary then lowers the count and reports the remainder.
	missing := 0
	if count > available {
		missing = count - available
	}
	want := count
	if want == 0 {
		want = 1
	}
	args := append([]any{jid, want}, boundaryArgs...)
	args = append(args, limit)
	rows, err := tx.Query("SELECT msg_id, COALESCE(sender_jid,''), ts FROM (SELECT msg_id, sender_jid, ts, rowid FROM messages WHERE "+receiptEligible+" ORDER BY ts DESC, rowid DESC LIMIT ?)"+boundary+" ORDER BY ts ASC, rowid ASC LIMIT ?", args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var messages []MessageInfo
	for rows.Next() {
		m := MessageInfo{ChatJID: jid}
		var ts int64
		if err := rows.Scan(&m.MsgID, &m.SenderJID, &ts); err != nil {
			return nil, 0, err
		}
		m.Timestamp = fromUnix(ts)
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := rows.Close(); err != nil {
		return nil, 0, err
	}
	return messages, missing, tx.Commit()
}

// ClearChatUnreadMarker clears only local unread metadata when there is nothing
// to acknowledge. Recheck eligibility atomically so an arrival after selection
// keeps its count or marker. No message read boundary or network state changes.
func (d *DB) ClearChatUnreadMarker(jid string) error {
	_, err := d.sql.ExecContext(storeCtx(), "UPDATE chats SET unread = 0, unread_count = 0 WHERE jid = ? AND NOT EXISTS (SELECT 1 FROM messages WHERE "+receiptEligible+")", jid, jid)
	return err
}
