package store

import (
	"fmt"
	"strings"
	"time"
)

// UpsertStatusReceipt records what one participant reported about a status of
// ours, keeping the furthest state reached. It reports whether the receipt
// belongs to a status we hold: a report about anything else says nothing about
// our own statuses and is dropped.
func (d *DB) UpsertStatusReceipt(msgID, participantJID, status string, ts time.Time) (bool, error) {
	msgID = strings.TrimSpace(msgID)
	participantJID = strings.TrimSpace(participantJID)
	status = strings.ToLower(strings.TrimSpace(status))
	if msgID == "" || participantJID == "" {
		return false, fmt.Errorf("status message ID and participant JID are required")
	}
	rank, ok := receiptRank[status]
	if !ok {
		return false, fmt.Errorf("unknown receipt status %q", status)
	}
	res, err := d.sql.ExecContext(storeCtx(), `
		INSERT INTO status_receipts(msg_id, participant_jid, status, ts)
		SELECT ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM status_messages WHERE msg_id = ? AND from_me = 1)
		ON CONFLICT(msg_id, participant_jid) DO UPDATE SET
			status = excluded.status,
			ts = excluded.ts
		WHERE ? > CASE status_receipts.status
			WHEN 'delivered' THEN 1 WHEN 'read' THEN 2 WHEN 'played' THEN 3 ELSE 0 END
	`, msgID, participantJID, status, unix(ts), msgID, rank)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// StatusReceiptCounts is how many participants have reached each state.
type StatusReceiptCounts struct {
	Delivered int
	Read      int
}

// StatusReceipts reports the counts for one status of ours.
func (d *DB) StatusReceipts(msgID string) (StatusReceiptCounts, error) {
	var counts StatusReceiptCounts
	err := d.sql.QueryRowContext(storeCtx(), `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN status IN ('read','played') THEN 1 ELSE 0 END), 0)
		FROM status_receipts WHERE msg_id = ?
	`, strings.TrimSpace(msgID)).Scan(&counts.Delivered, &counts.Read)
	if err != nil {
		return StatusReceiptCounts{}, err
	}
	return counts, nil
}
