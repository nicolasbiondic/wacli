package wa

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/appstate"
	waStore "go.mau.fi/whatsmeow/store"
)

type failingAtomicAppStateStore struct {
	waStore.AppStateStore
	err error
}

func (s failingAtomicAppStateStore) ReplaceAppStateVersion(context.Context, string, uint64, [128]byte, []waStore.AppStateMutationMAC) error {
	return s.err
}

func TestContactStoreJournalAtomicReplacement(t *testing.T) {
	c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: newTestKeyStateStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	journal := c.ContactJournal()
	name := string(appstate.WAPatchCriticalUnblockLow)
	start := journal.Mark(name)
	if err := c.client.Store.AppState.ReplaceAppStateVersion(t.Context(), name, 10, [128]byte{}, nil); err != nil {
		t.Fatal(err)
	}
	if end := journal.Mark(name); end.CursorWrites-start.CursorWrites != 1 {
		t.Fatalf("atomic replacement must count exactly once: before=%+v after=%+v", start, end)
	}
	failure := errors.New("injected restore failure")
	failing := journal.AppStateStore(failingAtomicAppStateStore{AppStateStore: c.client.Store.AppState, err: failure})
	start = journal.Mark(name)
	if err := failing.ReplaceAppStateVersion(t.Context(), name, 11, [128]byte{}, nil); !errors.Is(err, failure) {
		t.Fatalf("restore error: %v", err)
	}
	if end := journal.Mark(name); end != start {
		t.Fatalf("failed replacement advanced journal: before=%+v after=%+v", start, end)
	}
}

// The SDK restores a leftover reset journal inside the store. The journal
// must count that cursor move, and only when there was one.
func TestContactStoreJournalRestoredReset(t *testing.T) {
	c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: newTestKeyStateStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	journal := c.ContactJournal()
	appState := c.client.Store.AppState
	name := string(appstate.WAPatchCriticalUnblockLow)
	if err := appState.ReplaceAppStateVersion(t.Context(), name, 10, [128]byte{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := appState.PutAppStateBackup(t.Context(), name, 10, [128]byte{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := appState.DeleteAppStateVersion(t.Context(), name); err != nil {
		t.Fatal(err)
	}
	for _, want := range []uint64{1, 0} {
		start := journal.Mark(name)
		if err := appState.RecoverAppStateBackup(t.Context(), name); err != nil {
			t.Fatal(err)
		}
		if version, _, err := appState.GetAppStateVersion(t.Context(), name); err != nil || version != 10 {
			t.Fatalf("reset was not restored: version=%d err=%v", version, err)
		}
		if end := journal.Mark(name); end.CursorWrites-start.CursorWrites != want {
			t.Fatalf("restore counted %d cursor writes, want %d", end.CursorWrites-start.CursorWrites, want)
		}
	}
}
