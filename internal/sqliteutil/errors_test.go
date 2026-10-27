//go:build cgo

package sqliteutil

import (
	"errors"
	"fmt"
	"testing"

	"github.com/mattn/go-sqlite3"
)

func TestDescribeSQLiteReadErrors(t *testing.T) {
	for _, tc := range []struct {
		code      sqlite3.ErrNo
		extended  sqlite3.ErrNoExtended
		name      string
		transient bool
	}{
		{sqlite3.ErrReadonly, sqlite3.ErrReadonlyRecovery, "SQLITE_READONLY_RECOVERY", true},
		{sqlite3.ErrReadonly, sqlite3.ErrReadonly.Extend(5), "SQLITE_READONLY_CANTINIT", true},
		{sqlite3.ErrReadonly, 0, "SQLITE_READONLY", false},
		{sqlite3.ErrReadonly, sqlite3.ErrReadonlyCantLock, "SQLITE_READONLY_CANTLOCK", false},
		{sqlite3.ErrReadonly, sqlite3.ErrReadonlyRollback, "SQLITE_READONLY_ROLLBACK", false},
		{sqlite3.ErrReadonly, sqlite3.ErrReadonlyDbMoved, "SQLITE_READONLY_DBMOVED", false},
		{sqlite3.ErrReadonly, sqlite3.ErrReadonly.Extend(6), "SQLITE_READONLY_DIRECTORY", false},
		{sqlite3.ErrBusy, 0, "SQLITE_BUSY", false},
		{sqlite3.ErrBusy, sqlite3.ErrBusyRecovery, "SQLITE_BUSY_RECOVERY", false},
		{sqlite3.ErrIoErr, sqlite3.ErrIoErrSHMOpen, "SQLITE_IOERR_SHMOPEN", false},
		{sqlite3.ErrConstraint, sqlite3.ErrConstraintUnique, "SQLITE_CONSTRAINT_UNIQUE", false},
		{sqlite3.ErrNotADB, 0, "SQLITE_NOTADB", false},
		{sqlite3.ErrReadonly, sqlite3.ErrReadonly.Extend(99), "SQLITE_UNKNOWN", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := sqlite3.Error{Code: tc.code, ExtendedCode: tc.extended}
			for _, pointer := range []bool{false, true} {
				var err error = cause
				if pointer {
					err = &cause
				}
				for _, readOnly := range []bool{false, true} {
					wrapped := fmt.Errorf("read failed: %w", err)
					if readOnly {
						wrapped = MarkReadOnlyError(wrapped)
					}
					wrapped = fmt.Errorf("outer: %w", wrapped)
					got := DescribeError(wrapped)
					extended := int(tc.extended)
					if extended == 0 {
						extended = int(tc.code)
					}
					want := ErrorDetails{int(tc.code), extended, tc.name, readOnly && tc.transient}
					if got == nil || *got != want {
						t.Fatalf("pointer=%t readonly=%t: got %+v want %+v", pointer, readOnly, got, want)
					}
					if !errors.Is(wrapped, err) {
						t.Fatal("read-only provenance lost the cause")
					}
					if IsReadOnlyTransient(wrapped) != tc.transient {
						t.Fatal("retry classifier disagrees with JSON classifier")
					}
				}
			}
		})
	}
}

func TestSQLiteErrorProvenanceDoesNotLeakAcrossJoinedCauses(t *testing.T) {
	mutation := sqlite3.Error{Code: sqlite3.ErrReadonly, ExtendedCode: sqlite3.ErrReadonly.Extend(5)}
	joined := errors.Join(mutation, MarkReadOnlyError(errors.New("unrelated read failure")))
	if got := DescribeError(joined); got == nil || got.Retryable {
		t.Fatalf("read marker leaked to mutation: %+v", got)
	}
	joined = errors.Join(errors.New("context"), MarkReadOnlyError(fmt.Errorf("read: %w", mutation)))
	if got := DescribeError(joined); got == nil || !got.Retryable || got.ExtendedCode != 1288 {
		t.Fatalf("lost marked read cause: %+v", got)
	}
	for _, err := range []error{nil, errors.New("SQLITE_READONLY_CANTINIT"), MarkReadOnlyError(errors.New("attempt to write a readonly database"))} {
		if DescribeError(err) != nil || IsReadOnlyTransient(err) {
			t.Fatalf("non-SQLite error classified: %v", err)
		}
	}
}
