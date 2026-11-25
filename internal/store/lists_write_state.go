package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// Typed lookup failures keep the human message while letting callers tell the
// cases apart without parsing it.
var (
	ErrListNotFound  = errors.New("list not found")
	ErrListDeleted   = errors.New("list deleted")
	ErrListAmbiguous = errors.New("list name ambiguous")
)

type listLookupError struct {
	kind error
	msg  string
}

func (e *listLookupError) Error() string { return e.msg }
func (e *listLookupError) Unwrap() error { return e.kind }

func newListLookupError(kind error, format string, args ...any) error {
	return &listLookupError{kind: kind, msg: fmt.Sprintf(format, args...)}
}

// listTombstoneExists reports whether a deleted definition has this ID or name.
func (d *DB) listTombstoneExists(value string) (bool, error) {
	exists, err := d.tableExists("lists")
	if err != nil || !exists {
		return false, err
	}
	var found bool
	err = d.readQuery().QueryRow(`SELECT EXISTS(SELECT 1 FROM lists WHERE deleted=1 AND (id=? OR name=?))`, value, value).Scan(&found)
	return found, err
}

// ListUpdatedAt returns the stored definition timestamp in Unix milliseconds.
func (d *DB) ListUpdatedAt(id string) (int64, error) {
	var at int64
	err := d.readQuery().QueryRow(`SELECT updated_at FROM lists WHERE id=?`, id).Scan(&at)
	return at, err
}

// ChatListUpdatedAt returns the stored membership timestamp in Unix milliseconds.
func (d *DB) ChatListUpdatedAt(chat, id string) (int64, error) {
	var at int64
	err := d.readQuery().QueryRow(`SELECT updated_at FROM chat_lists WHERE chat_jid=? AND list_id=?`, chat, id).Scan(&at)
	return at, err
}

// RegularIndexCompleteness describes whether the local label index may be
// trusted to show every existing ID.
type RegularIndexCompleteness struct {
	Complete bool
	// Broken is set while the latest full sync of `regular` is recorded as not
	// verifying. CompleteAfterBroken is true when the completion was recorded
	// later than that mark.
	Broken              bool
	CompleteAfterBroken bool
}

// UsableForNewIDs reports whether the next numeric ID can be derived locally.
func (c RegularIndexCompleteness) UsableForNewIDs() bool {
	return c.Complete && (!c.Broken || c.CompleteAfterBroken)
}

// RegularIndexCompleteness reads the completion bit, its timestamp and the
// broken-full-sync mark. Older stores without the timestamp column or the
// broken table (read-only opens skip schema maintenance) read as NULL/absent.
func (d *DB) RegularIndexCompleteness() (RegularIndexCompleteness, error) {
	var c RegularIndexCompleteness
	var err error
	c.Complete, err = d.RegularFullSyncComplete()
	if err != nil {
		return c, err
	}
	exists, err := d.tableExists("app_state_full_sync_broken")
	if err != nil || !exists {
		return c, err
	}
	var markedAt int64
	err = d.readQuery().QueryRow(`SELECT marked_at FROM app_state_full_sync_broken WHERE collection='regular'`).Scan(&markedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	c.Broken = true
	hasAt, err := d.tableHasColumn("list_sync_state", "full_sync_complete_at")
	if err != nil || !hasAt {
		return c, err
	}
	var at sql.NullInt64
	if err := d.readQuery().QueryRow(`SELECT full_sync_complete_at FROM list_sync_state WHERE id=1`).Scan(&at); err != nil {
		return c, err
	}
	// marked_at has second resolution; require a strictly later second.
	c.CompleteAfterBroken = c.Complete && at.Valid && at.Int64/1000 > markedAt
	return c, nil
}

// AppStateRecoveryPending reports whether any durable recovery intent exists
// for the collection, read inside the current view.
func (d *DB) AppStateRecoveryPending(collection string) (bool, error) {
	exists, err := d.tableExists("app_state_recovery_intents")
	if err != nil || !exists {
		return false, err
	}
	var pending bool
	err = d.readQuery().QueryRow(`SELECT EXISTS(SELECT 1 FROM app_state_recovery_intents WHERE collection=?)`, collection).Scan(&pending)
	return pending, err
}
