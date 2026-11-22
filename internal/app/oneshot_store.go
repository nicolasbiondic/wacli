package app

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types/events"
)

const (
	// oneShotStoreDrainMax bounds how long Close waits for events still being
	// stored when the connection closed, within the command's own deadline.
	oneShotStoreDrainMax = 10 * time.Second
	// oneShotStoreDrainNoDeadline is the bound for a command run without a
	// deadline (--timeout 0).
	oneShotStoreDrainNoDeadline = 2 * time.Second
	// oneShotDecryptedWait bounds the wait for a message whose decryption was
	// committed before the fence to reach the handlers. That takes
	// milliseconds; a counted write with no message to follow (a resend after
	// a retry receipt) costs this much at exit.
	oneShotDecryptedWait = time.Second
)

// A command that connects by itself (no `sync` holds the store) receives what
// WhatsApp queued for this device while it was offline, like any connection.
// oneShotStore keeps sync's storage handler registered for the life of such a
// connection:
//
//   - Messages and history chunks: whatsmeow sends the receipt only after the
//     handlers return, so they are stored before they are acknowledged. When
//     the connection closes, whatsmeow may still be decrypting one, 1:1 or
//     group. Signal session and sender-key writes are fenced first (see
//     wa.Client.FenceSessionWrites): a decryption that has not committed
//     fails and the message stays queued and decryptable for the next
//     connection; one that has committed is waited for (oneShotDecryptedWait)
//     and stored. Only if it does not reach the handlers in time is it lost;
//     the command then warns (oneshot_store_undispatched). Not covered: a
//     "stateless" prekey message, which consumes a one-time prekey without a
//     session write, and a version-3 (Armadillo) message, which whatsmeow
//     dispatches as another event; Signal's retry recovers both.
//   - Receipts, calls, app-state changes and channel messages: whatsmeow
//     acknowledges them on arrival, before any handler runs. They are stored
//     as they are handled; those handed over after the store closed are lost,
//     and reported only if they reach the command before it exits.
type oneShotStore struct {
	wa         WAClient
	handlerIDs []uint32
	gate       *mediaQueue
	limits     *syncStorageLimits
	cancel     context.CancelFunc
	deadline   time.Time // the command's own deadline; zero when it has none

	admitted sync.Map // event -> true while sync's handler may still be running it
	// Session writes (decryptions) committed before the last message dispatch
	// that reached admit.
	dispatchedWrites atomic.Uint64
	undispatched     bool
	closed           atomic.Bool
	drained          bool
	waitedFor        time.Duration
	// appStateQueued: app-state writes were still queued when Close gave up.
	appStateQueued bool
	// What the store did not keep, found when it closed and after.
	cutShort, late atomic.Int64
}

// startOneShotStore registers the store handler before the first connect of a
// command that connects by itself. Later reconnects keep the same handlers. A
// running Sync stores events itself, so commands it executes for others (the
// delegate socket) do not register a second handler.
func (a *App) startOneShotStore(ctx context.Context) {
	if a.syncRunning.Load() > 0 {
		return
	}
	a.waMu.Lock()
	defer a.waMu.Unlock()
	if a.oneShot != nil || a.wa == nil {
		return
	}
	storeCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s := &oneShotStore{
		wa:     a.wa,
		gate:   newMediaQueue(1),
		limits: &syncStorageLimits{app: a},
		cancel: cancel,
	}
	if deadline, ok := ctx.Deadline(); ok {
		s.deadline = deadline
	}
	// Download history chunks inside the handler, before whatsmeow sends the
	// message receipt, as sync does; otherwise whatsmeow downloads them in the
	// background and deletes the server copy whether or not they were stored.
	a.wa.SetManualHistorySyncDownload(true)
	// whatsmeow dispatches an event to its handlers in registration order:
	// admit runs before sync's handler and settle after it.
	s.handlerIDs = append(s.handlerIDs, a.wa.AddEventHandler(s.admit))
	var stored, lastEvent atomic.Int64
	storeID, _ := a.addSyncEventHandler(storeCtx, SyncOptions{PresenceMode: SyncPresenceModeQuiet, oneShot: true},
		&stored, &lastEvent, make(chan struct{}, 1), make(chan struct{}, 1), make(chan staleReconnectRequest, 1),
		func(string, string) {}, nil, s.limits, &syncPresence{}, s.gate)
	s.handlerIDs = append(s.handlerIDs, storeID, a.wa.AddEventHandler(func(evt any) { s.settle(a, evt) }))
	a.oneShot = s
}

// admit holds the gate open while sync's handler runs evt, so whether evt was
// stored is decided per event, not guessed from the gate's state afterwards.
func (s *oneShotStore) admit(evt any) {
	if s.gate.beginProducer() {
		s.admitted.Store(evt, true)
	}
	if _, ok := evt.(*events.Message); ok {
		// This message's decryption is among the writes counted now.
		for seen := s.wa.SessionWrites(); ; {
			old := s.dispatchedWrites.Load()
			if old >= seen || s.dispatchedWrites.CompareAndSwap(old, seen) {
				break
			}
		}
		s.gate.notify()
	}
}

// waitDispatched waits until every decryption committed before the fence has
// reached admit, or ctx ends.
func (s *oneShotStore) waitDispatched(ctx context.Context, committed uint64) bool {
	for s.dispatchedWrites.Load() < committed {
		select {
		case <-s.gate.changed:
		case <-ctx.Done():
			return s.dispatchedWrites.Load() >= committed
		}
	}
	return true
}

// settle runs after sync's handler: it releases the gate for an admitted
// event, and counts what reached the handlers after the store closed.
func (s *oneShotStore) settle(a *App, evt any) {
	if _, ok := s.admitted.LoadAndDelete(evt); ok {
		s.gate.endProducer()
		return
	}
	if s.closed.Load() && oneShotStoresKind(evt) && a.oneShotStores(evt) {
		s.late.Add(1)
	}
}

// oneShotStoresKind lists what sync's handler keeps in wacli.db.
func oneShotStoresKind(evt any) bool {
	switch evt.(type) {
	case *events.Message, *events.Receipt, *events.HistorySync,
		*events.CallOffer, *events.CallAccept, *events.CallPreAccept, *events.CallTransport,
		*events.CallOfferNotice, *events.CallRelayLatency, *events.CallTerminate, *events.CallReject,
		*events.AppState, *events.Star, *events.DeleteForMe,
		*events.Archive, *events.UnarchiveChatsSetting, *events.Pin, *events.Mute, *events.MarkChatAsRead,
		*events.Contact:
		return true
	}
	return false
}

// oneShotStores reports whether a one-shot connection hands evt to sync's
// handler: everything sync stores, nothing that runs sync's own connection
// loop, presence, freshness record or progress output.
func (a *App) oneShotStores(evt any) bool {
	switch evt.(type) {
	case *events.Connected, *events.Disconnected, *events.StreamReplaced, *events.LoggedOut,
		*events.KeepAliveTimeout, *events.PushNameSetting,
		*events.OfflineSyncPreview, *events.OfflineSyncCompleted,
		*events.AppStateSyncError, *events.ChatPresence, *events.Presence:
		return false
	case *events.AppState, *events.Star, *events.DeleteForMe,
		*events.Archive, *events.UnarchiveChatsSetting, *events.Pin, *events.Mute, *events.MarkChatAsRead,
		*events.Contact, *wa.AppStateKeyUnavailable:
		// Chat-state commands and contacts save persist app state themselves.
		return a.chatStatePersisters.Load() == 0
	default:
		return true
	}
}

// fenceOneShotStore runs once the socket is closed, so no receipt can follow.
//
// The bound of a command's exit, after its result is printed: the command's
// own deadline, which ends both waits below (at most 1 s for a decrypted
// message, then at most oneShotStoreDrainMax for events still being stored;
// 1 s and oneShotStoreDrainNoDeadline without a deadline). The one exception
// comes first: FenceSessionWrites waits for a Signal write already in
// progress, at most session.db's busy timeout (5 s, the go-sqlite3 default).
// When the wait is cut short Close leaves the handlers registered and does
// not wait for them, and closing the databases does not wait for a
// statement still in flight (database/sql closes a connection in use only
// when it is returned); the process exits under them.
// It fences Signal session and sender-key writes, waits for the events still being stored
// (within the command's deadline, see oneShotStoreDrainMax), then closes the
// store. The handlers stay registered until the session store is closed
// (release), so whatever reaches them later is still counted.
func (a *App) fenceOneShotStore() *oneShotStore {
	a.waMu.Lock()
	s, waClient := a.oneShot, a.wa
	a.oneShot = nil
	a.waMu.Unlock()
	if s == nil {
		return nil
	}
	committed := waClient.FenceSessionWrites()
	left := func(bound time.Duration) time.Duration {
		if s.deadline.IsZero() {
			return bound
		}
		return min(bound, max(time.Until(s.deadline), 0))
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), left(oneShotDecryptedWait))
	s.undispatched = !s.waitDispatched(ctx, committed)
	cancel()
	wait := left(oneShotStoreDrainMax)
	if s.deadline.IsZero() {
		wait = oneShotStoreDrainNoDeadline
	}
	s.waitedFor = time.Since(start) + wait
	// From here on, an event that finds the gate closed is counted.
	s.closed.Store(true)
	ctx, cancel = context.WithTimeout(context.Background(), wait)
	s.drained = s.gate.waitIdle(ctx)
	cancel()
	if !s.drained {
		s.gate.stopAccepting()
		s.admitted.Range(func(evt, _ any) bool {
			if oneShotStoresKind(evt) {
				s.cutShort.Add(1)
			}
			return true
		})
		// Stop a slow download; what it did not store is lost.
		s.cancel()
	}
	return s
}

// stopAccepting closes the gate without waiting for producers to finish.
func (q *mediaQueue) stopAccepting() {
	q.mu.Lock()
	q.accepting = false
	q.mu.Unlock()
	q.notify()
}

// finish reports what the connection could not store. It runs after pending
// app-state writes, so their failures are included.
func (s *oneShotStore) finish(a *App) {
	if s == nil {
		return
	}
	s.cancel()
	if n := s.cutShort.Load(); n > 0 || s.appStateQueued {
		queued := ""
		if s.appStateQueued {
			queued = ", and app-state writes were still queued"
		}
		a.emitWarning("oneshot_store_incomplete",
			fmt.Sprintf("warning: the connection closed while %d incoming message(s) or event(s) were still being stored (waited %s)%s; what was not stored is not in wacli.db and WhatsApp will not deliver it again", n, s.waitedFor.Round(time.Millisecond), queued),
			map[string]any{"count": n, "waited_ms": s.waitedFor.Milliseconds(), "app_state_queued": s.appStateQueued})
	}
	if s.undispatched && s.late.Load() == 0 {
		a.emitWarning("oneshot_store_undispatched",
			fmt.Sprintf("warning: whatsmeow had committed a decryption when the connection closed, and no message reached the store within %s; if it was a message, it is not in wacli.db and WhatsApp will not deliver it again", oneShotDecryptedWait),
			nil)
	}
	if n := s.late.Load(); n > 0 {
		a.emitWarning("oneshot_store_dropped",
			fmt.Sprintf("warning: %d incoming message(s) or event(s) reached this command after it closed its store; they are not in wacli.db and WhatsApp will not deliver them again", n),
			map[string]any{"count": n})
	}
	if s.limits.persistenceFailed.Load() {
		a.emitWarning("oneshot_store_failed",
			"warning: WhatsApp delivered messages or events to this command that could not be stored in wacli.db; they were acknowledged and will not be delivered again. Check the store (disk space, permissions) before the next command or sync.",
			nil)
	}
}

// release removes the handlers once the session store is closed. Manual
// history download is switched off only then: switched off earlier, a
// history notification still being decrypted would be downloaded by whatsmeow
// in the background and its server copy deleted, unstored.
func (s *oneShotStore) release(waClient WAClient) {
	if s == nil || waClient == nil {
		return
	}
	if s.handlersIdle() {
		for _, id := range s.handlerIDs {
			waClient.RemoveEventHandler(id)
		}
	}
	waClient.SetManualHistorySyncDownload(false)
}

// handlersIdle reports whether no handler is still running an event. Removing
// a handler waits for the event being dispatched (whatsmeow holds its handler
// lock while handlers run), so when the wait at exit was cut short the
// handlers are left registered: the process is about to exit.
func (s *oneShotStore) handlersIdle() bool { return s == nil || s.drained }

// abandonQueuedAppState does not wait for app-state writes still queued when
// the wait at exit was cut short: the handler that was cut short may hold a
// place in that queue. What is still queued is reported.
func (s *oneShotStore) abandonQueuedAppState(a *App) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.appStateQueued = a.appStatePersist.waitIdle(ctx) != nil
}
