package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/openclaw/wacli/internal/store/storedb"
)

var completenessDriverID atomic.Uint64

type completenessReadBarrier struct {
	seen, written chan struct{}
	once          atomic.Bool
}

func TestListResolutionAndMembershipsShareOneWALSnapshot(t *testing.T) {
	t.Run("deleted ID precedes reused name", func(t *testing.T) {
		writer := openTestDB(t)
		for _, l := range []ListUpdate{
			{ID: "7", Name: listPtr("original"), Deleted: listPtr(true)},
			{ID: "8", Name: listPtr("7")},
		} {
			if err := writer.UpsertList(l); err != nil {
				t.Fatal(err)
			}
		}
		// Pause the active-definition SELECT while another WAL connection
		// reactivates ID 7. A later tombstone SELECT must retain the old view.
		if _, err := writer.sql.Exec(`ALTER TABLE lists RENAME TO resolution_lists;
			CREATE VIEW lists AS SELECT id,round4_name(name) AS name,color,predefined_id,type,
			order_index,is_active,is_immutable,deleted,updated_at FROM resolution_lists`); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		gate := &completenessReadBarrier{seen: make(chan struct{}), written: make(chan struct{})}
		driver := fmt.Sprintf("sqlite3-list-resolution-%d", completenessDriverID.Add(1))
		sql.Register(driver, &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return conn.RegisterFunc("round4_name", func(name string) (string, error) {
				if name == "7" && gate.once.CompareAndSwap(false, true) {
					close(gate.seen)
					select {
					case <-gate.written:
					case <-ctx.Done():
						return "", ctx.Err()
					}
				}
				return name, nil
			}, false)
		}})
		raw, err := sql.Open(driver, sqliteURI(writer.path, true))
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		raw.SetMaxOpenConns(1)
		reader := &DB{path: writer.path, sql: raw, q: storedb.New(raw)}
		written := make(chan error, 1)
		go func() {
			select {
			case <-gate.seen:
			case <-ctx.Done():
				written <- ctx.Err()
				return
			}
			_, err := writer.sql.ExecContext(ctx, `UPDATE resolution_lists SET deleted=0 WHERE id='7'`)
			written <- err
			close(gate.written)
		}()
		id, resolveErr := reader.ResolveList("7")
		if err := <-written; err != nil {
			t.Fatal(err)
		}
		if id != "" || resolveErr == nil || !strings.Contains(resolveErr.Error(), "was deleted") {
			t.Fatalf("mixed definitions/tombstones: id=%q error=%v; want deleted ID, never name ID 8", id, resolveErr)
		}
		if id, err := reader.ResolveList("7"); err != nil || id != "7" {
			t.Fatalf("next snapshot = %q, %v", id, err)
		}
	})
	t.Run("membership load retains resolution view", func(t *testing.T) {
		writer := openTestDB(t)
		at := time.Unix(100, 0)
		if err := writer.UpsertList(ListUpdate{ID: "7", Name: listPtr("chosen")}); err != nil {
			t.Fatal(err)
		}
		for _, jid := range []string{"old@s.whatsapp.net", "new@s.whatsapp.net"} {
			if err := writer.UpsertChat(jid, "dm", jid, at); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.SetChatList("old@s.whatsapp.net", "7", true, at, false); err != nil {
			t.Fatal(err)
		}
		reader, err := OpenReadOnly(writer.path)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		err = reader.ReadView(func(view *DB) error {
			id, err := view.ResolveList("chosen")
			if err != nil {
				return err
			}
			if _, err := writer.sql.Exec(`UPDATE chat_lists SET labeled=0 WHERE list_id='7';
				INSERT INTO chat_lists(chat_jid,list_id,labeled,updated_at) VALUES('new@s.whatsapp.net','7',1,200)`); err != nil {
				return err
			}
			chats, err := view.ListChatsFiltered(ChatListFilter{ListID: id})
			if err != nil {
				return err
			}
			if len(chats) != 1 || chats[0].JID != "old@s.whatsapp.net" {
				return fmt.Errorf("membership escaped read view: %+v", chats)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		chats, err := reader.ListChatsFiltered(ChatListFilter{ListID: "7"})
		if err != nil || len(chats) != 1 || chats[0].JID != "new@s.whatsapp.net" {
			t.Fatalf("next snapshot memberships = %+v, %v", chats, err)
		}
	})
}

// The view's scalar function pauses the actual flag SELECT after SQLite has
// read its value. A second WAL connection then commits new rows before the
// reader continues. No production hooks, sleeps or timing luck are needed.
func TestCompletenessAndRowsShareOneWALSnapshot(t *testing.T) {
	for _, lists := range []bool{false, true} {
		t.Run(fmt.Sprintf("lists=%t", lists), func(t *testing.T) {
			writer := openTestDB(t)
			if _, err := writer.sql.Exec(`ALTER TABLE list_sync_state RENAME TO snapshot_state;
				CREATE VIEW list_sync_state AS SELECT id,
				round3_flag(full_sync_events_seen) AS full_sync_events_seen,
				round3_flag(full_sync_complete) AS full_sync_complete FROM snapshot_state`); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			var gate atomic.Pointer[completenessReadBarrier]
			driver := fmt.Sprintf("sqlite3-completeness-%d", completenessDriverID.Add(1))
			sql.Register(driver, &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
				return conn.RegisterFunc("round3_flag", func(value int64) (int64, error) {
					if b := gate.Load(); b != nil && b.once.CompareAndSwap(false, true) {
						close(b.seen)
						select {
						case <-b.written:
						case <-ctx.Done():
							return 0, ctx.Err()
						}
					}
					return value, nil
				}, false)
			}})
			raw, err := sql.Open(driver, sqliteURI(writer.path, true))
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			raw.SetMaxOpenConns(1)
			reader := &DB{path: writer.path, sql: raw, q: storedb.New(raw)}
			type request struct {
				barrier *completenessReadBarrier
				n       int
			}
			requests, failures, stopped := make(chan request), make(chan error, 1), make(chan struct{})
			go func() {
				defer close(stopped)
				for r := range requests {
					select {
					case <-r.barrier.seen:
					case <-ctx.Done():
						close(r.barrier.written)
						return
					}
					var err error
					if lists {
						var tx *sql.Tx
						tx, err = writer.sql.BeginTx(ctx, nil)
						if err == nil {
							_, err = tx.Exec(`UPDATE snapshot_state SET full_sync_events_seen=1 WHERE id=1`)
							if err == nil {
								_, err = tx.Exec(`UPDATE lists SET name=? WHERE id='7'`, fmt.Sprintf("partial-%d", r.n))
							}
							if err == nil {
								err = tx.Commit()
							} else {
								_ = tx.Rollback()
							}
						}
					} else {
						_, err = writer.sql.ExecContext(ctx, `UPDATE snapshot_state SET full_sync_complete=0 WHERE id=1`)
						if err == nil {
							err = writer.UpsertQuickReply(QuickReply{ID: "1", Shortcut: "fixture", Message: fmt.Sprintf("partial-%d", r.n)}, time.Unix(int64(2*r.n+2), 0))
						}
					}
					if err != nil {
						failures <- err
					}
					close(r.barrier.written)
					if err != nil {
						return
					}
				}
			}()
			defer func() { close(requests); cancel(); <-stopped }()
			for n := range 12 {
				full := fmt.Sprintf("full-%d", n)
				if lists {
					if err := writer.UpsertList(ListUpdate{ID: "7", Name: listPtr(full), Timestamp: time.Unix(int64(2*n+1), 0)}); err != nil {
						t.Fatal(err)
					}
				} else if err := writer.UpsertQuickReply(QuickReply{ID: "1", Shortcut: "fixture", Message: full}, time.Unix(int64(2*n+1), 0)); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.sql.Exec(`UPDATE snapshot_state SET full_sync_complete=1,full_sync_events_seen=0 WHERE id=1`); err != nil {
					t.Fatal(err)
				}
				b := &completenessReadBarrier{seen: make(chan struct{}), written: make(chan struct{})}
				gate.Store(b)
				requests <- request{barrier: b, n: n}
				if lists {
					got, err := reader.Lists()
					if err != nil || got.FullSyncEventsSeen || len(got.Lists) != 1 || got.Lists[0].Name != full {
						t.Fatalf("mixed list metadata/rows at read %d: %+v %v", n, got, err)
					}
				} else {
					got, err := reader.QuickReplies()
					if err != nil || got.Completeness != "complete" || len(got.QuickReplies) != 1 || got.QuickReplies[0].Message != full {
						t.Fatalf("complete with partial rows at read %d: %+v %v", n, got, err)
					}
				}
				select {
				case err := <-failures:
					t.Fatal(err)
				default:
				}
				// Once the original read ends, a NEW read must see the committed
				// partial state, not an accidentally retained snapshot or cache.
				if !lists {
					got, err := reader.QuickReplies()
					if err != nil || got.Completeness != "incomplete" || got.QuickReplies[0].Message != fmt.Sprintf("partial-%d", n) {
						t.Fatalf("next read = %+v %v", got, err)
					}
				}
			}
		})
	}
}
