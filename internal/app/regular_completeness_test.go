package app

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func assertRegularCompleteness(t *testing.T, a *App, want string) {
	t.Helper()
	got, err := a.db.QuickReplies()
	if err != nil || got.Completeness != want {
		t.Fatalf("regular index = %+v, %v; want %s", got, err, want)
	}
}

func regularSnapshotFixture() []any {
	at := time.Unix(100, 0)
	fixture := listFixtureEvents(at, types.NewJID("123", types.DefaultUserServer))
	q := quickReplyEvent("1", "hello", "persist me", at, false)
	return append(fixture, q)
}

func TestRegularCompletenessOnlyAfterOrderedPersistedSentinel(t *testing.T) {
	for _, oneShot := range []bool{false, true} {
		t.Run(fmt.Sprint(oneShot), func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			if oneShot {
				remove, err := a.AddChatStatePersistenceHandler(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer remove()
			} else {
				// The live sync and one-shot handler use the same ordered writer.
				f.AddEventHandler(func(evt any) { a.handleAppStatePersistenceEvent(t.Context(), evt, nil) })
			}
			for _, evt := range regularSnapshotFixture() {
				f.emit(evt)
				assertRegularCompleteness(t, a, "incomplete")
			}
			blocker := a.appStatePersist.reserve()
			f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular})
			assertRegularCompleteness(t, a, "incomplete")
			a.appStatePersist.completeOne(blocker, func() {})
			if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertRegularCompleteness(t, a, "complete")
			// A new partial snapshot (or a crash here) must not keep the old bit.
			f.emit(regularSnapshotFixture()[0])
			assertRegularCompleteness(t, a, "incomplete")
		})
	}
}

func TestRegularSentinelCannotHidePreviousPersistenceFailure(t *testing.T) {
	for i, table := range []string{"lists", "chat_lists", "quick_replies"} {
		for _, fetched := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fetched=%t", table, fetched), func(t *testing.T) {
				a := newTestApp(t)
				a.wa = newFakeWA()
				raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				if _, err := raw.Exec("CREATE TRIGGER fail_regular BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(FAIL, 'injected regular write failure'); END"); err != nil {
					t.Fatal(err)
				}
				fixture := regularSnapshotFixture()
				sentinel := &events.AppStateSyncComplete{Name: appstate.WAPatchRegular}
				if fetched {
					if err := a.persistFetchedAppStateEvents(t.Context(), append(fixture, sentinel), &appStatePersistenceTracker{}); err == nil {
						t.Fatal("failed batch succeeded")
					}
				} else {
					var failed error
					a.handleAppStatePersistenceEvent(t.Context(), fixture[i], func(err error) { failed = err })
					if failed == nil {
						t.Fatal("injected write succeeded")
					}
					a.handleAppStatePersistenceEvent(t.Context(), sentinel, nil)
				}
				assertRegularCompleteness(t, a, "incomplete")
				if _, err := raw.Exec("DROP TRIGGER fail_regular"); err != nil {
					t.Fatal(err)
				}
				if err := a.persistFetchedAppStateEvents(t.Context(), append(fixture, sentinel), &appStatePersistenceTracker{}); err != nil {
					t.Fatal(err)
				}
				assertRegularCompleteness(t, a, "complete")
			})
		}
	}
}

func TestRegularSuccessfulFetchWithoutSentinelStaysIncomplete(t *testing.T) {
	a := newTestApp(t)
	f := &appStateContextWA{fakeWA: newFakeWA()}
	f.fetchEvents = func(context.Context, string, bool, bool) ([]any, error) { return regularSnapshotFixture(), nil }
	a.wa = f
	fetchErr, persistErr := a.fetchAndPersistAppState(t.Context(), appstate.WAPatchRegular, true, &appStatePersistenceTracker{})
	if fetchErr != nil || persistErr != nil {
		t.Fatalf("fetch=%v persist=%v", fetchErr, persistErr)
	}
	assertRegularCompleteness(t, a, "incomplete")
}
