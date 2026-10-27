package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	wmstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
)

// Use the SDK's real generic-event collector, not hand-built AppState events:
// losing FromFullSync here is precisely what made quick-reply-only replays unsafe.
func collectedQuickReplySnapshot(t *testing.T, values ...string) []any {
	t.Helper()
	client := whatsmeow.NewClient(&wmstore.Device{}, nil)
	var mutations []appstate.Mutation
	for i, value := range values {
		q := quickReplyEvent(fmt.Sprint(i+1), "fixture", value, time.Unix(int64(100+len(values)), 0), false)
		mutations = append(mutations, appstate.Mutation{Operation: waServerSync.SyncdMutation_SET, Index: q.Index, Action: q.SyncActionValue})
	}
	var collected []any
	if err := client.DangerousInternals().CollectEventsToDispatch(t.Context(), appstate.WAPatchRegular, mutations, true, &collected); err != nil {
		t.Fatal(err)
	}
	if len(collected) != len(values) {
		t.Fatalf("SDK events = %#v", collected)
	}
	return collected
}

func TestRegularLiveSnapshotCrashHelper(t *testing.T) {
	if os.Getenv("WACLI_SNAPSHOT_CRASH_HELPER") != "1" {
		t.Skip("subprocess helper only")
	}
	a, err := New(Options{StoreDir: os.Getenv("WACLI_SNAPSHOT_CRASH_STORE")})
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeWA()
	a.wa = f
	if os.Getenv("WACLI_SNAPSHOT_CRASH_HANDLER") == "one-shot" {
		if _, err := a.AddChatStatePersistenceHandler(t.Context()); err != nil {
			t.Fatal(err)
		}
	} else {
		var messages, last atomic.Int64
		a.addSyncEventHandler(t.Context(), SyncOptions{}, &messages, &last, make(chan struct{}, 1), make(chan struct{}, 1),
			make(chan staleReconnectRequest, 1), nil, nil, nil, &syncPresence{}, nil)
	}
	for _, evt := range collectedQuickReplySnapshot(t, "old") {
		f.emit(evt)
	}
	f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular})
	if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertRegularCompleteness(t, a, "complete")
	if mode := os.Getenv("WACLI_SNAPSHOT_CRASH_BOUNDARY"); mode != "collector-only" {
		f.emit(&events.AppStateSyncStart{Name: appstate.WAPatchRegular, Recovery: mode == "recovery"})
		if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertRegularCompleteness(t, a, "incomplete")
	}
	for i, evt := range collectedQuickReplySnapshot(t, "new-first", "new-second") {
		f.emit(evt)
		if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
			t.Fatal(err)
		}
		if i == 0 && os.Getenv("WACLI_SNAPSHOT_CRASH_PARTIAL") == "1" {
			// No App.Close, DB.Close or test cleanup: leave a committed partial
			// WAL and exit before the second mutation and completion sentinel.
			os.Exit(0)
		}
	}
	f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular})
	if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestRegularLiveSnapshotCrashBeforeSentinel(t *testing.T) {
	for _, handler := range []string{"follow", "one-shot"} {
		for _, boundary := range []string{"collector-only", "full", "recovery"} {
			for _, partial := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/partial=%t", handler, boundary, partial), func(t *testing.T) {
					dir := t.TempDir()
					ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRegularLiveSnapshotCrashHelper$")
					cmd.Env = append(os.Environ(), "WACLI_SNAPSHOT_CRASH_HELPER=1", "WACLI_SNAPSHOT_CRASH_STORE="+dir,
						"WACLI_SNAPSHOT_CRASH_HANDLER="+handler, "WACLI_SNAPSHOT_CRASH_BOUNDARY="+boundary, fmt.Sprintf("WACLI_SNAPSHOT_CRASH_PARTIAL=%d", map[bool]int{true: 1, false: 0}[partial]))
					if output, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("crash helper: %v %s", err, output)
					}
					ro, err := store.OpenReadOnly(filepath.Join(dir, "wacli.db"))
					if err != nil {
						t.Fatal(err)
					}
					defer ro.Close()
					got, err := ro.QuickReplies()
					want, count := "complete", 2
					if partial {
						want, count = "incomplete", 1
					}
					if err != nil || got.Completeness != want || len(got.QuickReplies) != count || got.QuickReplies[0].Message != "new-first" {
						t.Fatalf("reopened after abrupt exit = %+v, %v; want %s with %d replies", got, err, want, count)
					}
				})
			}
		}
	}
}

func TestRegularSnapshotStartIsDurableAndMustSucceedBeforeMutations(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(fmt.Sprint(recovery), func(t *testing.T) {
			a := newTestApp(t)
			a.wa = newFakeWA()
			emit := func(evt any) error {
				var persistErr error
				a.handleAppStatePersistenceEvent(t.Context(), evt, func(err error) { persistErr = err })
				return persistErr
			}
			for _, evt := range collectedQuickReplySnapshot(t, "old") {
				if err := emit(evt); err != nil {
					t.Fatal(err)
				}
			}
			if err := emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular}); err != nil {
				t.Fatal(err)
			}
			raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err := raw.Exec(`CREATE TRIGGER fail_snapshot_start BEFORE UPDATE OF full_sync_complete ON list_sync_state
				WHEN NEW.full_sync_complete=0 BEGIN SELECT RAISE(FAIL, 'injected start failure'); END`); err != nil {
				t.Fatal(err)
			}
			start := &events.AppStateSyncStart{Name: appstate.WAPatchRegular, Recovery: recovery}
			if err := emit(start); err == nil {
				t.Fatal("start invalidation unexpectedly succeeded")
			}
			if err := emit(collectedQuickReplySnapshot(t, "new-first", "new-second")[0]); err == nil {
				t.Fatal("mutation ran without durable invalidation")
			}
			if err := emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular, Recovery: recovery}); err == nil {
				t.Fatal("failed snapshot unexpectedly completed")
			}
			if got, err := a.db.QuickReplies(); err != nil || len(got.QuickReplies) != 1 || got.QuickReplies[0].Message != "old" {
				t.Fatalf("failed opening boundary changed the old snapshot: %+v %v", got, err)
			}
			if _, err := raw.Exec("DROP TRIGGER fail_snapshot_start"); err != nil {
				t.Fatal(err)
			}
			if err := emit(start); err != nil {
				t.Fatal(err)
			}
			// A separate connection must see the invalidation before ANY mutation.
			ro, err := store.OpenReadOnly(filepath.Join(a.StoreDir(), "wacli.db"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := ro.QuickReplies()
			ro.Close()
			if err != nil || got.Completeness != "incomplete" || got.QuickReplies[0].Message != "old" {
				t.Fatalf("start was not durable: %+v %v", got, err)
			}
			for _, evt := range collectedQuickReplySnapshot(t, "new-first", "new-second") {
				if err := emit(evt); err != nil {
					t.Fatal(err)
				}
			}
			if err := emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular, Recovery: recovery}); err != nil {
				t.Fatal(err)
			}
			assertRegularCompleteness(t, a, "complete")
			// Incremental generic events are not the beginning of a new snapshot.
			if err := emit(quickReplyEvent("1", "fixture", "incremental", time.Unix(200, 0), false)); err != nil {
				t.Fatal(err)
			}
			assertRegularCompleteness(t, a, "complete")
		})
	}
}

func TestRegularOverlappingSnapshotsWaitForBothCloses(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			a := newTestApp(t)
			a.wa = newFakeWA()
			emit := func(evt any) error {
				var persistErr error
				a.handleAppStatePersistenceEvent(t.Context(), evt, func(err error) { persistErr = err })
				return persistErr
			}
			if err := emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular}); err != nil {
				t.Fatal(err)
			}
			start := &events.AppStateSyncStart{Name: appstate.WAPatchRegular}
			end := &events.AppStateSyncComplete{Name: appstate.WAPatchRegular}
			if err := emit(start); err != nil {
				t.Fatal(err)
			}
			if failed {
				// Inject a real storage failure for snapshot A, then recover SQLite.
				raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				if _, err := raw.Exec(`CREATE TRIGGER fail_overlap BEFORE INSERT ON quick_replies BEGIN SELECT RAISE(FAIL, 'injected overlap failure'); END`); err != nil {
					t.Fatal(err)
				}
				if err := emit(collectedQuickReplySnapshot(t, "failed-A")[0]); err == nil {
					t.Fatal("storage failure ignored")
				}
				if _, err := raw.Exec("DROP TRIGGER fail_overlap"); err != nil {
					t.Fatal(err)
				}
			}
			if err := emit(&events.AppStateSyncStart{Name: appstate.WAPatchRegular, Recovery: true}); err != nil {
				t.Fatal(err)
			}
			if err := emit(collectedQuickReplySnapshot(t, "partial-B")[0]); err != nil {
				t.Fatal(err)
			}
			if err := emit(end); err != nil {
				t.Fatal(err)
			}
			assertRegularCompleteness(t, a, "incomplete")
			err := emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular, Recovery: true})
			if failed {
				if err == nil {
					t.Fatal("second opening erased the first snapshot's failure")
				}
				assertRegularCompleteness(t, a, "incomplete")
				if err := emit(start); err != nil {
					t.Fatal(err)
				}
				if err := emit(end); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			assertRegularCompleteness(t, a, "complete")
		})
	}
}

func TestRegularRecoveryCaptureKeepsSnapshotBoundariesBalanced(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	capture := a.newAppStateRecoveryCapture(appstate.WAPatchRegular)
	defer capture.stop()
	// A concurrent ordinary full fetch must not contribute an opening whose
	// non-recovery close the recovery capture deliberately ignores.
	f.emit(&events.AppStateSyncStart{Name: appstate.WAPatchRegular})
	for _, evt := range collectedQuickReplySnapshot(t, "prefix") {
		f.emit(evt)
	}
	for range 2 {
		f.emit(&events.AppStateSyncStart{Name: appstate.WAPatchRegular, Recovery: true})
	}
	f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular, Recovery: true})
	select {
	case <-capture.completed:
		t.Fatal("capture closed while a recovery snapshot was still open")
	default:
	}
	f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular})
	f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular, Recovery: true})
	completion := <-capture.completed
	starts, closes := 0, 0
	for _, evt := range completion.events {
		switch evt.(type) {
		case *events.AppStateSyncStart:
			starts++
		case *events.AppStateSyncComplete:
			closes++
		}
	}
	if starts != 2 || closes != 2 {
		t.Fatalf("captured starts/closes = %d/%d", starts, closes)
	}
	frontier := a.appStatePersist.complete(completion.ticket, func() {
		if err := a.persistFetchedAppStateEvents(t.Context(), completion.events, &appStatePersistenceTracker{}); err != nil {
			t.Error(err)
		}
	})
	if err := a.appStatePersist.waitThrough(t.Context(), frontier); err != nil {
		t.Fatal(err)
	}
	assertRegularCompleteness(t, a, "complete")
}
