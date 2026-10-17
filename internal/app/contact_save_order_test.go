package app

import (
	"context"
	"errors"
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
				f.applyContactBatch(
					contactEvent(testContactLID, "Ada Lovelace", "Ada"),
					contactEvent(testContactLID, "Grace Hopper", "Grace"),
				)()
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

// whatsmeow dispatches every fetched batch from its own goroutine: the 409
// conflict patches, the post-send fetch of the retry that follows them, and
// the post-send fetch of an earlier save all run independently of each other
// and of SendAppState returning. An older batch can therefore reach wacli.db
// after a newer one. whatsmeow writes each batch to the session contact store
// before it dispatches it, so the mirror must follow that store, not the
// event payloads. (Schedules reproduced by the round-1 reviewers.)
func TestSaveContactDelayedOlderBatchDoesNotRegressTheMirror(t *testing.T) {
	t.Run("409 conflict retry", func(t *testing.T) {
		a, f := newContactSaveTestApp(t)
		f.hasLocalAppState = true
		installBoundaryReplayHandler(t, a, f)
		f.saveContactAfterSend = func(fakeSaveContactCall) {
			// sendAppState applies the conflicting older rename first and
			// then, after the retry, the fetch holding our own echo. The two
			// dispatch goroutines run in the opposite order.
			dispatchConflict := f.applyContactBatch(contactEvent(testContactPN, "Grace Hopper", "Grace"))
			dispatchRetry := f.applyContactBatch(contactEvent(testContactPN, "Ada Lovelace", "Ada"))
			dispatchRetry()
			dispatchConflict()
		}
		if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
			t.Fatal(err)
		}
		if got := storedContact(t, a, testContactPN); got.full != "Ada Lovelace" || got.first != "Ada" {
			t.Fatalf("local mirror = %+v, but the retry the server accepted saved Ada Lovelace", got)
		}
	})
	t.Run("two serialized saves", func(t *testing.T) {
		a, f := newContactSaveTestApp(t)
		f.hasLocalAppState = true
		installBoundaryReplayHandler(t, a, f)
		var dispatchFirstEcho func()
		f.saveContactAfterSend = func(call fakeSaveContactCall) {
			dispatch := f.applyContactBatch(contactEvent(call.target, call.fullName, call.firstName))
			if dispatchFirstEcho == nil {
				// The first save returns before its dispatch goroutine runs.
				dispatchFirstEcho = dispatch
				return
			}
			dispatch()
		}
		if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
			t.Fatal(err)
		}
		if _, err := a.SaveContact(t.Context(), testContactPN, "Grace Hopper", "", false); err != nil {
			t.Fatal(err)
		}
		dispatchFirstEcho()
		if got := storedContact(t, a, testContactPN); got.full != "Grace Hopper" || got.first != "Grace" {
			t.Fatalf("local mirror = %+v, but the server applied Grace Hopper last", got)
		}
	})
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
// before whatsmeow's dispatch goroutine runs, so the events of the post-send
// batch never reach wacli.db. whatsmeow wrote that batch to the session
// contact store before SendAppState returned, so the save mirrors the batch
// from there, and the debt recorded before the send is settled without a
// recovery snapshot.
func TestSaveContactOneShotMirrorsTheBatchFromTheSessionStore(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	other := types.NewJID("15550002222", types.DefaultUserServer)
	var dispatch func()
	f.saveContactAfterSend = func(fakeSaveContactCall) {
		dispatch = f.applyContactBatch(
			contactEvent(testContactLID, "Ada Lovelace", "Ada"),
			contactEvent(other, "Grace Hopper", "Grace"),
		)
	}
	remove, err := a.AddChatStatePersistenceHandler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
		t.Fatal(err)
	}
	remove()
	dispatch() // nobody is listening any more
	if got := storedContact(t, a, other); got.full != "Grace Hopper" {
		t.Fatalf("edit fetched with the save = %+v, want Grace Hopper mirrored from the session store", got)
	}
	if contactRecoveryRequired(t, a) {
		t.Fatal("critical_unblock_low debt kept after the save mirrored the batch from the session store")
	}
	if got := recoveredCollections(f); len(got) != 0 {
		t.Fatalf("recovery snapshots requested: %v", got)
	}
}

// A one-shot save can exit after whatsmeow consumed the post-send batch and
// before the save mirrored it; only the write-ahead debt survives. The pinned
// whatsmeow ignores a recovery snapshot whose version the session already
// holds (TestSDKIgnoresEqualVersionContactRecovery in internal/wa), so with
// no later contact edit the phone can never answer that debt. The next start
// must repair the mirror from the session contact store instead, and settle
// the debt only once that succeeded.
func TestContactDebtIsSettledFromTheSessionStoreWithoutASnapshot(t *testing.T) {
	a, f := newContactSaveTestApp(t)
	f.hasLocalAppState = true
	other := types.NewJID("15550002222", types.DefaultUserServer)
	f.saveContactAfterSend = func(fakeSaveContactCall) {
		// Consumed into the session store; the events are never dispatched.
		f.applyContactBatch(
			contactEvent(testContactLID, "Ada Lovelace", "Ada"),
			contactEvent(other, "Grace Hopper", "Grace"),
		)
	}
	// The process dies between the send and the mirror.
	f.getAllContactsErr = errors.New("process exited before the batch was mirrored")
	remove, err := a.AddChatStatePersistenceHandler(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveContact(t.Context(), testContactPN, "Ada Lovelace", "", false); err != nil {
		t.Fatal(err)
	}
	remove()
	if got := storedContact(t, a, other); got.found {
		t.Fatalf("test setup: the batch was mirrored before the exit: %+v", got)
	}
	if !contactRecoveryRequired(t, a) {
		t.Fatal("no durable critical_unblock_low debt after the cursor advanced past an unmirrored batch")
	}

	// Next start. The phone's snapshot is at the version the session already
	// consumed, so whatsmeow would ignore it: model a request nobody answers.
	f.getAllContactsErr = nil
	f.onAppStateRecovery = func(string) {}
	a.syncAppStateDeltas(t.Context(), &sync.Map{})
	if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := storedContact(t, a, other); got.full != "Grace Hopper" || got.first != "Grace" {
		t.Fatalf("edit consumed with the save = %+v, want Grace Hopper from the session store", got)
	}
	if contactRecoveryRequired(t, a) {
		t.Fatal("critical_unblock_low debt kept after the mirror was repaired from the session store")
	}
	if n := fullSyncFetches(f, contactCollection); n != 0 {
		t.Fatalf("startup debt ran %d destructive full syncs of %s", n, contactCollection)
	}
}

// An ordinary missing key is requested by whatsmeow when decoding fails and
// usually arrives before the next fetch. A write must keep retrying the
// (non-destructive) fetch until it does, as docs/sync.md promises, instead
// of waiting for a recovery snapshot the phone may never send: on the contact
// collection, which is never full-synced while local state exists, and on a
// flagged collection, which keeps main's behaviour. (Reproduced by all three
// round-1 reviewers.)
func TestOrdinaryMissingKeyRetriesWithoutSnapshot(t *testing.T) {
	cases := []struct {
		name       string
		collection appstate.WAPatchName
		flagged    bool
		debt       bool
	}{
		{name: "contacts", collection: contactCollection},
		{name: "contacts with debt", collection: contactCollection, debt: true},
		{name: "flagged regular_low", collection: appstate.WAPatchRegularLow, flagged: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, f := newContactSaveTestApp(t)
			f.hasLocalAppState = true
			if tc.flagged {
				a.setAppStateFullSyncResult(tc.collection, true)
			}
			if tc.debt {
				if _, err := a.db.MarkAppStateRecoveryGeneration(string(tc.collection)); err != nil {
					t.Fatal(err)
				}
			}
			f.appStateFetchErrs = []error{appstate.ErrKeyNotFound, nil}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if err := a.syncChatStateBeforeWrite(ctx, tc.collection); err != nil {
				t.Fatalf("the key available at the next fetch was never retried (recoveries %v): %v", recoveredCollections(f), err)
			}
			if got := recoveredCollections(f); len(got) != 0 {
				t.Fatalf("recovery snapshots requested for an ordinary missing key: %v", got)
			}
			if tc.collection == contactCollection {
				if n := fullSyncFetches(f, tc.collection); n != 0 {
					t.Fatalf("missing key ran %d destructive full syncs of %s", n, tc.collection)
				}
			}
		})
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
