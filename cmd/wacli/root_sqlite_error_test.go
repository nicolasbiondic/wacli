//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattn/go-sqlite3"
	"github.com/openclaw/wacli/internal/sqliteutil"
	"github.com/openclaw/wacli/internal/store"
)

func TestRootJSONPreservesSQLiteReadErrorDetails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	rw, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	for _, extended := range []int{264, 1288} {
		t.Run(fmt.Sprint(extended), func(t *testing.T) {
			cause := sqlite3.Error{Code: sqlite3.ErrReadonly, ExtendedCode: sqlite3.ErrNoExtended(extended)}
			calls := 0
			err := ro.ReadView(func(*store.DB) error { calls++; return cause })
			if err == nil || calls != 4 {
				t.Fatalf("retry exhaustion = %v calls=%d", err, calls)
			}
			for _, events := range []bool{false, true} {
				wrapped := fmt.Errorf("read fixture: %w", err)
				stderr := captureRootStderr(t, func() { writeRootError(rootFlags{asJSON: true, events: events}, wrapped) })
				var got struct {
					Success bool                     `json:"success"`
					Error   string                   `json:"error"`
					SQLite  *sqliteutil.ErrorDetails `json:"sqlite_error"`
					Data    struct {
						Message string                   `json:"message"`
						SQLite  *sqliteutil.ErrorDetails `json:"sqlite_error"`
					} `json:"data"`
				}
				if err := json.Unmarshal([]byte(stderr), &got); err != nil {
					t.Fatal(err)
				}
				details, message := got.SQLite, got.Error
				if events {
					details, message = got.Data.SQLite, got.Data.Message
				}
				wantName := "SQLITE_READONLY_RECOVERY"
				if extended == 1288 {
					wantName = "SQLITE_READONLY_CANTINIT"
				}
				if got.Success || message != wrapped.Error() || details == nil || details.PrimaryCode != 8 || details.ExtendedCode != extended || details.Name != wantName || !details.Retryable {
					t.Fatalf("JSON error: %s", stderr)
				}
			}
			stderr := captureRootStderr(t, func() { writeRootError(rootFlags{asJSON: true}, cause) })
			var mutation struct {
				SQLite *sqliteutil.ErrorDetails `json:"sqlite_error"`
			}
			if err := json.Unmarshal([]byte(stderr), &mutation); err != nil {
				t.Fatal(err)
			}
			if mutation.SQLite == nil || mutation.SQLite.Retryable {
				t.Fatalf("unmarked mutation advertised retryable: %s", stderr)
			}
		})
	}
}

func TestSQLiteReadFailureJSONThroughCLI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wacli.db"), []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr string
	var commandErr error
	stdout := captureRootStdout(t, func() {
		stderr = captureRootStderr(t, func() {
			commandErr = execute([]string{"--store", dir, "--read-only", "--json", "chats", "list"})
		})
	})
	var got struct {
		Success bool                     `json:"success"`
		Error   string                   `json:"error"`
		SQLite  *sqliteutil.ErrorDetails `json:"sqlite_error"`
	}
	if err := json.Unmarshal([]byte(stderr), &got); err != nil {
		t.Fatalf("JSON: %v %s", err, stderr)
	}
	if commandErr == nil || stdout != "" || got.Success || got.Error != commandErr.Error() || got.SQLite == nil || got.SQLite.PrimaryCode != 26 || got.SQLite.ExtendedCode != 26 || got.SQLite.Name != "SQLITE_NOTADB" || got.SQLite.Retryable {
		t.Fatalf("CLI stdout=%q stderr=%s error=%v", stdout, stderr, commandErr)
	}
}
