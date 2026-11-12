package store

import (
	"context"
	"time"
)

const keepUnreadIntentTTL = 5 * time.Minute

type keepUnreadIntent struct {
	expires     time.Time
	timer       *time.Timer
	pending     context.Context
	stopContext func() bool
}

// RegisterKeepUnreadIntent must precede the send. IDs are account-wide and do
// not depend on the PN/LID chat alias selected by the sending device or echo.
// Call the returned completion with the send error: failures/timeouts clear
// the intent, while an ACK detaches attempt cancellation before the caller
// persists its row. Successful persistence consumes it; abandoned intents have
// a five-minute upper bound even if the transport never returns.
func (d *DB) RegisterKeepUnreadIntent(ctx context.Context, id string) func(error) {
	expires := time.Now().Add(keepUnreadIntentTTL)
	intent := &keepUnreadIntent{expires: expires, pending: ctx}
	clear := func() { d.clearKeepUnreadIntent(id, intent) }
	d.sendIntentsMu.Lock()
	if d.sendIntents == nil {
		d.sendIntents = make(map[string]*keepUnreadIntent)
	}
	if previous := d.sendIntents[id]; previous != nil {
		d.removeKeepUnreadIntentLocked(id, previous)
	}
	d.sendIntents[id] = intent
	intent.timer = time.AfterFunc(time.Until(expires), clear)
	intent.stopContext = context.AfterFunc(ctx, func() {
		d.sendIntentsMu.Lock()
		defer d.sendIntentsMu.Unlock()
		// A callback already scheduled when the ACK detaches cancellation
		// must not delete the accepted send's pre-persistence protection.
		if d.sendIntents[id] == intent && intent.pending != nil {
			d.removeKeepUnreadIntentLocked(id, intent)
		}
	})
	d.sendIntentsMu.Unlock()
	return func(sendErr error) {
		d.sendIntentsMu.Lock()
		defer d.sendIntentsMu.Unlock()
		if d.sendIntents[id] != intent {
			return
		}
		if sendErr != nil || ctx.Err() != nil {
			d.removeKeepUnreadIntentLocked(id, intent)
			return
		}
		intent.pending = nil
		intent.stopContext()
	}
}

func (d *DB) keepUnreadIntent(id string) *keepUnreadIntent {
	d.sendIntentsMu.Lock()
	defer d.sendIntentsMu.Unlock()
	intent := d.sendIntents[id]
	if intent != nil && (!time.Now().Before(intent.expires) || (intent.pending != nil && intent.pending.Err() != nil)) {
		d.removeKeepUnreadIntentLocked(id, intent)
		return nil
	}
	return intent
}

func (d *DB) clearKeepUnreadIntent(id string, intent *keepUnreadIntent) {
	d.sendIntentsMu.Lock()
	defer d.sendIntentsMu.Unlock()
	if intent != nil && d.sendIntents[id] == intent {
		d.removeKeepUnreadIntentLocked(id, intent)
	}
}

func (d *DB) removeKeepUnreadIntentLocked(id string, intent *keepUnreadIntent) {
	intent.timer.Stop()
	intent.stopContext()
	delete(d.sendIntents, id)
}

func migrateSkipOwnReadMark(d *DB) error {
	exists, err := d.tableExists("messages")
	if err != nil || !exists {
		return err
	}
	return addTextColumnIfMissing(d, "skip_own_read_mark", `ALTER TABLE messages ADD COLUMN skip_own_read_mark INTEGER NOT NULL DEFAULT 0`)
}
