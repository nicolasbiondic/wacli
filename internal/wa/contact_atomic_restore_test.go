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
