//go:build cgo

package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/mattn/go-sqlite3"
)

func TestReadOnlyOpenRetriesOnlyReadonlyRecovery(t *testing.T) {
	fixture := openTestDB(t)
	recovery := sqlite3.Error{Code: sqlite3.ErrReadonly, ExtendedCode: sqlite3.ErrReadonlyRecovery}
	for _, tc := range []struct {
		name                string
		injected            error
		failures, wantCalls int32
		mutation, wantError bool
	}{
		{name: "transient", injected: recovery, failures: 3, wantCalls: 4},
		{name: "wrapped", injected: fmt.Errorf("wrapped: %w", recovery), failures: 1, wantCalls: 2},
		{name: "bounded", injected: recovery, failures: 100, wantCalls: 4, wantError: true},
		{name: "plain readonly", injected: sqlite3.Error{Code: sqlite3.ErrReadonly}, failures: 1, wantCalls: 1, wantError: true},
		{name: "readonly rollback", injected: sqlite3.Error{Code: sqlite3.ErrReadonly, ExtendedCode: sqlite3.ErrReadonlyRollback}, failures: 1, wantCalls: 1, wantError: true},
		{name: "busy", injected: sqlite3.Error{Code: sqlite3.ErrBusy}, failures: 1, wantCalls: 1, wantError: true},
		{name: "message is not a code", injected: errors.New("SQLITE_READONLY_RECOVERY"), failures: 1, wantCalls: 1, wantError: true},
		{name: "mutation never retried", injected: recovery, failures: 1, wantCalls: 1, mutation: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			driver := fmt.Sprintf("sqlite3-readonly-recovery-%d", completenessDriverID.Add(1))
			sql.Register(driver, &sqlite3.SQLiteDriver{ConnectHook: func(*sqlite3.SQLiteConn) error {
				if calls.Add(1) <= tc.failures {
					return tc.injected
				}
				return nil
			}})
			raw, err := sql.Open(driver, sqliteURI(fixture.path, !tc.mutation))
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if tc.mutation {
				_, err = raw.Exec(`UPDATE chats SET name='must not retry'`)
			} else {
				// Production read-only opening SELECT, with the extended SQLite
				// error injected at connection open; no production hooks.
				err = (&DB{sql: raw}).validateReadable()
			}
			if (err != nil) != tc.wantError || calls.Load() != tc.wantCalls {
				t.Fatalf("error=%v calls=%d; want error=%t calls=%d", err, calls.Load(), tc.wantError, tc.wantCalls)
			}
		})
	}
}
