package app

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

const contactCollection = appstate.WAPatchCriticalUnblockLow

func contactEvent(jid types.JID, fullName, firstName string) *events.Contact {
	return &events.Contact{JID: jid, Action: &waSyncAction.ContactAction{
		FullName: proto.String(fullName), FirstName: proto.String(firstName),
	}}
}

func contactRecoveryRequired(t *testing.T, a *App) bool {
	t.Helper()
	required, err := a.db.AppStateRecoveryRequired(string(contactCollection))
	if err != nil {
		t.Fatal(err)
	}
	return required
}

func fullSyncFetches(f *fakeWA, name appstate.WAPatchName) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, fetch := range f.appStateFetches {
		if fetch.name == string(name) && fetch.fullSync {
			n++
		}
	}
	return n
}

// whatsmeow's SendAppState fetches the collection after the send and
// dispatches the fetched batch from a goroutine before it returns. When that
// batch holds our save followed by a newer phone rename, the rename is the
// authoritative state; the optimistic local name must not overwrite it.
func TestSaveContactSameBatchRenameWinsOverOptimisticMirror(t *testing.T) {
	for _, handler := range []string{"sync-follow", "one-shot"} {
		t.Run(handler, func(t *testing.T) {
			a, f := newContactSaveTestApp(t)
			if handler == "sync-follow" {
				installBoundaryReplayHandler(t, a, f)
			} else {
				remove, err := a.AddChatStatePersistenceHandler(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(remove)
			}
			f.saveContactAfterSend = func(fakeSaveContactCall) {
				f.dispatchAsync(
					contactEvent(testContactLID, "Ada Lovelace", "Ada"),
					contactEvent(testContactLID, "Grace Hopper", "Grace"),
				)
			}
			if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
				t.Fatal(err)
			}
			if got := storedContact(t, a, testContactPN); got.full != "Grace Hopper" || got.first != "Grace" {
				t.Fatalf("local mirror = %+v, want the newer phone rename Grace Hopper", got)
			}
		})
	}
}

// Two concurrent saves of the same contact (two delegated requests) must
// leave the name of the save the server applied last.
func TestSaveContactConcurrentSavesMirrorTheLastSend(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	firstSent := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondSent := make(chan struct{})
	var calls int
	var mu sync.Mutex
	f.saveContactAfterSend = func(fakeSaveContactCall) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			close(firstSent)
			<-releaseFirst
			return
		}
		close(secondSent)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Go(func() {
		_, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false)
		errs <- err
	})
	<-firstSent
	wg.Go(func() {
		_, err := a.SaveContact(t.Context(), testContactPN, "Grace Hopper", "", false)
		errs <- err
	})
	// Unserialized, the second save sends and mirrors while the first is
	// still in flight; serialized, it waits for the first to finish.
	select {
	case <-secondSent:
	case <-time.After(300 * time.Millisecond):
	}
	close(releaseFirst)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	calls2 := append([]fakeSaveContactCall(nil), f.saveContactCalls...)
	f.mu.Unlock()
	if len(calls2) != 2 {
		t.Fatalf("calls = %+v", calls2)
	}
	if got, last := storedContact(t, a, testContactPN), calls2[1].fullName; got.full != last {
		t.Fatalf("local mirror = %q, but the server applied %q last", got.full, last)
	}
}

// App-state writes bypass the paced send queue because the app serializes
// them (#454). A contact save must wait for an in-flight app-state write and
// its pre-write synchronization instead of sending alongside it.
func TestSaveContactSerializesWithAppStateWrites(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	release, err := a.acquireChatStateSync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false)
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	f.mu.Lock()
	sent := len(f.saveContactCalls)
	f.mu.Unlock()
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatal("contacts save sent while another app-state write held the app-state coordinator")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.appStateFetches) == 0 || f.appStateFetches[0].name != string(contactCollection) {
		t.Fatalf("contacts save did not synchronize %s before writing: %+v", contactCollection, f.appStateFetches)
	}
}

// A one-shot save returns, disconnects and removes its persistence handler
// before whatsmeow's dispatch goroutine runs. Edits fetched in that batch
// never reach wacli.db, so recovery debt for the collection must already be
// durable, and a later recovery must deliver them.
func TestSaveContactOneShotKeepsDebtUntilTheBatchIsCovered(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	other := types.NewJID("15550002222", types.DefaultUserServer)
	batch := []any{
		contactEvent(testContactLID, "Ada Lovelace", "Ada"),
		contactEvent(other, "Grace Hopper", "Grace"),
	}
	remove, err := a.AddChatStatePersistenceHandler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
		t.Fatal(err)
	}
	remove()
	f.dispatchAsync(batch...) // nobody is listening any more
	if got := storedContact(t, a, other); got.found {
		t.Fatalf("test setup: the late batch was persisted: %+v", got)
	}
	if !contactRecoveryRequired(t, a) {
		t.Fatal("no durable critical_unblock_low recovery debt after the cursor advanced past an unpersisted batch")
	}

	// The next sync start settles the debt from the primary's snapshot.
	f.onAppStateRecovery = func(name string) {
		go func() {
			if name == string(contactCollection) {
				for _, evt := range batch {
					f.emit(evt)
				}
			}
			f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchName(name), Recovery: true})
		}()
	}
	a.syncAppStateDeltas(t.Context(), &sync.Map{})
	deadline := time.Now().Add(5 * time.Second)
	for contactRecoveryRequired(t, a) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if contactRecoveryRequired(t, a) {
		t.Fatal("the recovery snapshot did not settle the contact debt")
	}
	if got := storedContact(t, a, other); got.full != "Grace Hopper" {
		t.Fatalf("edit fetched with the save = %+v, want Grace Hopper after recovery", got)
	}
}

// With stock whatsmeow a full sync deletes the saved collection version
// before it decodes the snapshot, so a snapshot that does not verify leaves
// the contact collection empty until the primary answers. The contact
// collection is repaired from the primary's recovery snapshot instead,
// keeping local state, whenever local state exists.
func TestContactCollectionRecoveryKeepsLocalState(t *testing.T) {
	answerRecovery := func(f *fakeWA) {
		f.onAppStateRecovery = func(name string) {
			go f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchName(name), Recovery: true})
		}
	}
	t.Run("live mismatch", func(t *testing.T) {
		a, f := newContactSaveTestApp(t)
		f.hasLocalAppState = true
		answerRecovery(f)
		a.recoverAppStateCollection(t.Context(), string(contactCollection), &sync.Map{}, 2*time.Second)
		if n := fullSyncFetches(f, contactCollection); n != 0 {
			t.Fatalf("live LTHash mismatch ran %d destructive full syncs of %s", n, contactCollection)
		}
		if got := recoveredCollections(f); !slices.Equal(got, []string{string(contactCollection)}) {
			t.Fatalf("recoveries = %v", got)
		}
	})
	t.Run("pre-write mismatch", func(t *testing.T) {
		a, f := newContactSaveTestApp(t)
		f.hasLocalAppState = true
		answerRecovery(f)
		f.appStateFetchErrs = []error{appstate.ErrMismatchingLTHash}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := a.syncChatStateBeforeWrite(ctx, contactCollection); err != nil {
			t.Fatal(err)
		}
		if n := fullSyncFetches(f, contactCollection); n != 0 {
			t.Fatalf("pre-write LTHash mismatch ran %d destructive full syncs of %s", n, contactCollection)
		}
		if got := recoveredCollections(f); !slices.Equal(got, []string{string(contactCollection)}) {
			t.Fatalf("recoveries = %v", got)
		}
	})
	t.Run("startup debt", func(t *testing.T) {
		a, f := newContactSaveTestApp(t)
		f.hasLocalAppState = true
		answerRecovery(f)
		if _, err := a.db.MarkAppStateRecoveryGeneration(string(contactCollection)); err != nil {
			t.Fatal(err)
		}
		a.syncAppStateDeltas(t.Context(), &sync.Map{})
		if n := fullSyncFetches(f, contactCollection); n != 0 {
			t.Fatalf("startup replay debt ran %d destructive full syncs of %s", n, contactCollection)
		}
	})
	t.Run("write after a save", func(t *testing.T) {
		a, f := newContactSaveTestApp(t)
		f.hasLocalAppState = true
		answerRecovery(f)
		for _, name := range []string{"Ada Lovelace", "Grace Hopper"} {
			if _, err := a.SaveContact(t.Context(), testContactPN, name, "", false); err != nil {
				t.Fatal(err)
			}
		}
		if n := fullSyncFetches(f, contactCollection); n != 0 {
			t.Fatalf("the save after a save ran %d destructive full syncs of %s", n, contactCollection)
		}
	})
	t.Run("no local state", func(t *testing.T) {
		// Nothing to lose: a full sync is the only way to get the collection.
		a, f := newContactSaveTestApp(t)
		a.recoverAppStateCollection(t.Context(), string(contactCollection), &sync.Map{}, time.Second)
		if n := fullSyncFetches(f, contactCollection); n != 1 {
			t.Fatalf("full syncs = %d, want 1 without local state", n)
		}
	})
}

// A collection whose full sync is known not to verify keeps its local state
// on a live hash failure too, not only at startup and before writes (#497).
func TestKnownBrokenCollectionLiveMismatchKeepsLocalState(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	f.onAppStateRecovery = func(name string) {
		go f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchName(name), Recovery: true})
	}
	a.setAppStateFullSyncResult(appstate.WAPatchRegularLow, true)
	a.recoverAppStateCollection(t.Context(), string(appstate.WAPatchRegularLow), &sync.Map{}, 2*time.Second)
	if n := fullSyncFetches(f, appstate.WAPatchRegularLow); n != 0 {
		t.Fatalf("live LTHash mismatch on a known-broken collection ran %d full syncs", n)
	}
	if got := recoveredCollections(f); !slices.Equal(got, []string{string(appstate.WAPatchRegularLow)}) {
		t.Fatalf("recoveries = %v", got)
	}
}
