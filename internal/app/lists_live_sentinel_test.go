package app

import (
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types/events"
)

// TestLiveFullSyncSentinelRouting pins how this fork merges the upstream quick
// reply completeness flag with its archive recovery settlement (#502): a live
// `regular` sentinel marks the quick reply index complete, while a live
// `regular_low` sentinel is not persisted, so it cannot settle the archive
// recovery out of order with the snapshot it closes.
func TestLiveFullSyncSentinelRouting(t *testing.T) {
	for _, oneShot := range []bool{false, true} {
		a := newTestApp(t)
		f := newFakeWA()
		a.wa = f
		ctx := t.Context()
		if oneShot {
			remove, err := a.AddChatStatePersistenceHandler(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer remove()
		} else {
			var messages, last atomic.Int64
			a.addSyncEventHandler(ctx, SyncOptions{}, &messages, &last, make(chan struct{}, 1), make(chan struct{}, 1),
				make(chan staleReconnectRequest, 1), nil, nil, nil, &syncPresence{}, nil)
		}
		required, err := a.db.ChatArchiveRecoveryRequired()
		if err != nil || !required {
			t.Fatalf("fresh store archive recovery required = %v, %v", required, err)
		}
		if got, err := a.db.QuickReplies(); err != nil || got.Completeness != "incomplete" {
			t.Fatalf("fresh store completeness = %+v, %v", got, err)
		}

		f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Version: 7})
		f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegular, Version: 3})

		deadline := time.Now().Add(5 * time.Second)
		for {
			got, err := a.db.QuickReplies()
			if err != nil {
				t.Fatal(err)
			}
			if got.Completeness == "complete" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("oneShot=%v: live regular sentinel did not mark completeness: %+v", oneShot, got)
			}
			time.Sleep(10 * time.Millisecond)
		}
		// The regular sentinel was emitted after the regular_low one and both
		// share the ordered persistence queue, so the regular_low sentinel has
		// already been handled (or, as expected, ignored) by now.
		if required, err := a.db.ChatArchiveRecoveryRequired(); err != nil || !required {
			t.Fatalf("oneShot=%v: live regular_low sentinel settled archive recovery: required=%v, %v", oneShot, required, err)
		}
		if pending, err := a.db.AppStateRecoveryCollections(); err != nil || len(pending) != 0 {
			t.Fatalf("oneShot=%v: live recovery intents = %v, %v", oneShot, pending, err)
		}
	}
}
