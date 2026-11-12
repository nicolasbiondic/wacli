package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestStaleAppStateFetchPreservesCoverage(t *testing.T) {
	a, f := newLabelWriteApp(t)
	if err := a.db.MarkRegularFullSyncComplete(); err != nil {
		t.Fatal(err)
	}
	f.appStateFetchErr = fmt.Errorf("stale decode: %w", appstate.ErrStateChanged)
	fetchErr, persistErr := a.fetchAndPersistAppState(t.Context(), appstate.WAPatchRegular, true, &appStatePersistenceTracker{})
	if !errors.Is(fetchErr, appstate.ErrStateChanged) || persistErr != nil {
		t.Fatalf("fetch=%v persist=%v", fetchErr, persistErr)
	}
	if complete, err := a.db.RegularFullSyncComplete(); err != nil || !complete {
		t.Fatalf("stale fetch invalidated verified coverage: complete=%v err=%v", complete, err)
	}
	assertNoStaleFetchDebt(t, a)
}

func TestStaleAppStateBeforeWriteDoesNotCreateReplayDebt(t *testing.T) {
	a, f := newLabelWriteApp(t)
	f.appStateFetchErr = fmt.Errorf("stale decode: %w", appstate.ErrStateChanged)
	if err := a.syncChatStateBeforeWrite(t.Context(), appstate.WAPatchRegular); !errors.Is(err, appstate.ErrStateChanged) {
		t.Fatalf("caller must retry rather than write on a stale fetch: %v", err)
	}
	assertNoStaleFetchDebt(t, a)
	noPatch(t, f)
	f.appStateFetchErr = nil
	if err := a.syncChatStateBeforeWrite(t.Context(), appstate.WAPatchRegular); err != nil {
		t.Fatalf("next fetch did not recover: %v", err)
	}
	for _, fetch := range f.appStateFetches {
		if fetch.fullSync {
			t.Fatal("stale incremental fetch escalated to a full replay")
		}
	}
}

func TestStaleAppStateReplayPreservesOnlyPreexistingDebt(t *testing.T) {
	for _, priorDebt := range []bool{false, true} {
		t.Run(fmt.Sprint(priorDebt), func(t *testing.T) {
			a, f := newLabelWriteApp(t)
			var old int64
			if priorDebt {
				var err error
				old, err = a.db.MarkAppStateRecoveryGeneration("regular")
				if err != nil {
					t.Fatal(err)
				}
			}
			f.appStateFetchErr = appstate.ErrStateChanged
			if _, err := a.ResyncAppState(t.Context(), "regular"); !errors.Is(err, appstate.ErrStateChanged) {
				t.Fatalf("stale full fetch: %v", err)
			}
			if pending, err := a.db.AppStateRecoveryRequired("regular"); err != nil || pending != priorDebt {
				t.Fatalf("debt=%v want=%v err=%v", pending, priorDebt, err)
			}
			if priorDebt {
				if err := a.db.ClearAppStateRecoveryIntent("regular", old); err != nil {
					t.Fatal(err)
				}
			}
			assertNoStaleFetchDebt(t, a)
		})
	}
}

func TestStaleAppStateRecoveryDoesNotAskPrimaryOrConsumeRetry(t *testing.T) {
	a := newTestApp(t)
	f := &appStateContextWA{fakeWA: newFakeWA()}
	f.fetchEvents = func(context.Context, string, bool, bool) ([]any, error) { return nil, appstate.ErrStateChanged }
	requested := 0
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
		requested++
		return "", errors.New("unexpected primary recovery")
	}
	a.wa = f
	var recoveries sync.Map
	recoveries.Store("regular", struct{}{})
	a.recoverAppStateCollection(t.Context(), "regular", &recoveries, time.Second)
	if requested != 0 {
		t.Fatal("stale fetch requested a primary snapshot")
	}
	if _, retained := recoveries.Load("regular"); retained {
		t.Fatal("stale fetch consumed the next fetch's retry opportunity")
	}
	a.handleAppStateSyncError(t.Context(), &events.AppStateSyncError{Name: appstate.WAPatchRegular, FullSync: true, Error: fmt.Errorf("decode: %w", appstate.ErrStateChanged)}, &recoveries)
	a.appStateRecoveryWorkers.Wait()
	assertNoStaleFetchDebt(t, a)
}

func assertNoStaleFetchDebt(t *testing.T, a *App) {
	t.Helper()
	if pending, err := a.db.AppStateRecoveryRequired("regular"); err != nil || pending {
		t.Fatalf("stale fetch left replay debt: %v, %v", pending, err)
	}
	if a.isAppStateFullSyncBroken(appstate.WAPatchRegular) {
		t.Fatal("stale fetch marked full sync broken")
	}
}
