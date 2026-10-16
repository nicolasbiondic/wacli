package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestLiveIncomingUnarchivesAndSurvivesArchiveReplay(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	ctx := context.Background()
	chat := types.NewJID("15550000001", types.DefaultUserServer)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Synthetic", base); err != nil {
		t.Fatal(err)
	}
	setting := func(enabled bool, ts time.Time) {
		a.handleAppStatePersistenceEvent(ctx, &events.UnarchiveChatsSetting{
			Timestamp: ts,
			Action:    &waSyncAction.UnarchiveChatsSetting{UnarchiveChats: proto.Bool(enabled)},
		}, nil)
	}
	archive := func(ts time.Time) {
		a.handleAppStatePersistenceEvent(ctx, &events.Archive{
			JID: chat, Timestamp: ts, FromFullSync: true,
			Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)},
		}, nil)
	}
	assertArchived := func(want bool) {
		t.Helper()
		got, err := a.db.GetChat(chat.String())
		if err != nil || got.Archived != want {
			t.Fatalf("archived = %v, want %v (error %v)", got.Archived, want, err)
		}
	}
	var stored atomic.Int64
	message := func(id string, ts time.Time, own bool) {
		a.handleLiveSyncMessage(ctx, SyncOptions{}, &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chat, Sender: chat, IsFromMe: own},
				ID:            id, Timestamp: ts,
			},
			Message: &waE2E.Message{Conversation: proto.String("synthetic arrival")},
		}, &stored, nil, nil)
	}
	setting(true, base)
	archive(base.Add(time.Second))
	message("new", base.Add(2*time.Second), false)
	assertArchived(false)
	archive(base.Add(time.Second))
	assertArchived(false)
	// A later explicit archive wins, and duplicate/own messages cannot undo it.
	archive(base.Add(3 * time.Second))
	message("new", base.Add(2*time.Second), false)
	message("own", base.Add(4*time.Second), true)
	assertArchived(true)
	// Keep-chats-archived wins even when an older preference is replayed.
	setting(false, base.Add(5*time.Second))
	setting(true, base)
	message("kept", base.Add(6*time.Second), false)
	assertArchived(true)
	setting(true, base.Add(7*time.Second))
	assertArchived(true)
	message("allowed", base.Add(8*time.Second), false)
	assertArchived(false)
	setting(false, base.Add(9*time.Second))
	archive(base.Add(3 * time.Second))
	assertArchived(false)
}

func TestAutoUnarchiveRecoveryCompletesWithoutBrokenFullReplay(t *testing.T) {
	a := newTestApp(t)
	f := &appStateContextWA{fakeWA: newFakeWA()}
	a.wa = f
	ctx := context.Background()
	chat := types.NewJID("15550000003", types.DefaultUserServer)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Synthetic", base); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetChatArchived(chat.String(), true); err != nil {
		t.Fatal(err)
	}
	a.setAppStateFullSyncResult(appstate.WAPatchRegularLow, true)
	var messagesStored atomic.Int64
	a.handleLiveSyncMessage(ctx, SyncOptions{}, &events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "pending", Timestamp: base.Add(2 * time.Second)},
		Message: &waE2E.Message{Conversation: proto.String("synthetic")},
	}, &messagesStored, nil, nil)
	requests := 0
	incremental := 0
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) { requests++; return "snapshot", nil }
	f.fetchEvents = func(_ context.Context, name string, full, _ bool) ([]any, error) {
		if name == string(appstate.WAPatchRegularLow) && full {
			t.Error("destructive full replay on known-broken collection")
		}
		if name == string(appstate.WAPatchRegularLow) && !full {
			incremental++
		}
		return nil, nil
	}
	f.fetchAppState = func(ctx context.Context, name string, full, onlyIfNotSynced bool) error {
		_, err := f.fetchEvents(ctx, name, full, onlyIfNotSynced)
		return err
	}
	var recovering sync.Map
	a.syncAppStateDeltas(ctx, &recovering)
	if requests != 1 {
		t.Fatalf("snapshot requests=%d, want 1", requests)
	}
	if incremental != 1 {
		t.Fatalf("incremental catch-ups=%d, want 1 while snapshot pending", incremental)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || !required {
		t.Fatalf("incremental incorrectly cleared snapshot debt: %v, %v", required, err)
	}
	f.emit(&events.UnarchiveChatsSetting{Timestamp: base, Action: &waSyncAction.UnarchiveChatsSetting{UnarchiveChats: proto.Bool(true)}, FromFullSync: true})
	f.emit(&events.Archive{JID: chat, Timestamp: base.Add(time.Second), Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}, FromFullSync: true})
	f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Recovery: true})
	waitForCondition(t, time.Second, func() bool {
		pending, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
		return err == nil && !pending
	})
	got, err := a.db.GetChat(chat.String())
	if err != nil || got.Archived {
		t.Fatalf("recovered arrival=%+v, %v", got, err)
	}
	var next sync.Map
	a.syncAppStateDeltas(ctx, &next)
	if requests != 1 {
		t.Fatalf("completed snapshot requested again: %d", requests)
	}
}

func TestAutoUnarchiveWaitsForPreferenceAndArchiveBoundary(t *testing.T) {
	for _, preferenceFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "archive first", true: "preference first"}[preferenceFirst], func(t *testing.T) {
			a := newTestApp(t)
			a.wa = newFakeWA()
			ctx := context.Background()
			chat := types.NewJID("15550000002", types.DefaultUserServer)
			base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			if err := a.db.UpsertChat(chat.String(), "dm", "Synthetic", base); err != nil {
				t.Fatal(err)
			}
			if err := a.db.SetChatArchived(chat.String(), true); err != nil {
				t.Fatal(err)
			}
			var stored atomic.Int64
			a.handleLiveSyncMessage(ctx, SyncOptions{}, &events.Message{
				Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "pending", Timestamp: base.Add(2 * time.Second)},
				Message: &waE2E.Message{Conversation: proto.String("pending preference recovery")},
			}, &stored, nil, nil)
			assert := func(want bool) {
				t.Helper()
				got, err := a.db.GetChat(chat.String())
				if err != nil || got.Archived != want {
					t.Fatalf("archived=%v, want %v; %v", got.Archived, want, err)
				}
			}
			assert(true)
			pref := &events.UnarchiveChatsSetting{Timestamp: base, Action: &waSyncAction.UnarchiveChatsSetting{UnarchiveChats: proto.Bool(true)}, FromFullSync: true}
			arch := &events.Archive{JID: chat, Timestamp: base.Add(time.Second), Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}, FromFullSync: true}
			first, second := any(arch), any(pref)
			if preferenceFirst {
				first, second = pref, arch
			}
			a.handleAppStatePersistenceEvent(ctx, first, nil)
			assert(true)
			a.handleAppStatePersistenceEvent(ctx, second, nil)
			assert(false)
			// Shared history ingestion must not act as a fresh arrival.
			a.handleAppStatePersistenceEvent(ctx, &events.Archive{JID: chat, Timestamp: base.Add(3 * time.Second), Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}}, nil)
			if err := a.storeParsedMessage(ctx, wa.ParsedMessage{Chat: chat, ID: "history", Timestamp: base.Add(4 * time.Second), Text: "history"}, messageStoreOptions{}); err != nil {
				t.Fatal(err)
			}
			assert(true)
		})
	}
}

// An upgraded store whose complete regular_low snapshot does not mention a
// locally archived chat, or carries no Keep chats archived mutation, must not
// request the full replay again on every start.
func TestAutoUnarchiveRecoverySettlesAfterCompleteReplay(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	chat := types.NewJID("15550000004", types.DefaultUserServer)
	preference := &events.UnarchiveChatsSetting{
		Timestamp: base, Action: &waSyncAction.UnarchiveChatsSetting{UnarchiveChats: proto.Bool(true)},
	}
	for name, preferenceKnown := range map[string]bool{
		"legacy archive absent from the snapshot": true,
		"snapshot without a preference mutation":  false,
	} {
		t.Run(name, func(t *testing.T) {
			a := newTestApp(t)
			f := &appStateContextWA{fakeWA: newFakeWA()}
			a.wa = f
			ctx := context.Background()
			if err := a.db.UpsertChat(chat.String(), "dm", "Legacy", base); err != nil {
				t.Fatal(err)
			}
			if err := a.db.SetChatArchived(chat.String(), true); err != nil {
				t.Fatal(err)
			}
			if preferenceKnown {
				a.handleAppStatePersistenceEvent(ctx, preference, nil)
			}
			settled := base.Add(time.Hour)
			previousNowUTC := nowUTC
			nowUTC = func() time.Time { return settled }
			t.Cleanup(func() { nowUTC = previousNowUTC })
			fullReplays := 0
			f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
				t.Error("verified full replay must not request a recovery snapshot")
				return "", nil
			}
			f.fetchEvents = func(_ context.Context, name string, full, _ bool) ([]any, error) {
				if name != string(appstate.WAPatchRegularLow) || !full {
					return nil, nil
				}
				fullReplays++
				// A verified snapshot with nothing for this store: whatsmeow
				// returns only the completion sentinel.
				return []any{&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Version: 7}}, nil
			}
			f.fetchAppState = func(ctx context.Context, name string, full, only bool) error {
				_, err := f.fetchEvents(ctx, name, full, only)
				return err
			}
			for start := 1; start <= 3; start++ {
				var recoveries sync.Map
				a.syncAppStateDeltas(ctx, &recoveries)
				if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || required {
					t.Fatalf("start %d: recovery intent kept after a completed full replay: %v %v", start, required, err)
				}
			}
			if fullReplays != 1 {
				t.Fatalf("full regular_low replays across 3 starts=%d, want 1", fullReplays)
			}
			got, err := a.db.GetChat(chat.String())
			if err != nil || !got.Archived {
				t.Fatalf("settled legacy archive=%+v, %v; want still archived", got, err)
			}
			var stored atomic.Int64
			message := func(id string, ts time.Time) {
				a.handleLiveSyncMessage(ctx, SyncOptions{}, &events.Message{
					Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: id, Timestamp: ts},
					Message: &waE2E.Message{Conversation: proto.String("synthetic arrival")},
				}, &stored, nil, nil)
			}
			// The settlement is the chat's boundary: a message older than the
			// snapshot never unarchives it.
			message("older", settled.Add(-time.Second))
			if got, err := a.db.GetChat(chat.String()); err != nil || !got.Archived {
				t.Fatalf("message older than the settled snapshot unarchived the chat: %+v, %v", got, err)
			}
			// A newer arrival unarchives with a known preference. The settled
			// preference is unknown, not "unarchive": the arrival waits for
			// the real one, which replaces it however old its timestamp is.
			message("newer", settled.Add(time.Second))
			got, err = a.db.GetChat(chat.String())
			if err != nil || got.Archived == preferenceKnown {
				t.Fatalf("archived=%v after a message newer than the settled snapshot (preference known: %v); %v", got.Archived, preferenceKnown, err)
			}
			if !preferenceKnown {
				a.handleAppStatePersistenceEvent(ctx, preference, nil)
				if got, err := a.db.GetChat(chat.String()); err != nil || got.Archived {
					t.Fatalf("real preference did not replace the settled one: %+v, %v", got, err)
				}
			}
		})
	}
}

// Known-broken collections never see the destructive full replay, request
// one background snapshot per start, and settle once the primary's snapshot
// arrives even when it does not mention the legacy archive.
func TestAutoUnarchiveBrokenCollectionSettlesAfterRecoverySnapshot(t *testing.T) {
	a := newTestApp(t)
	f := &appStateContextWA{fakeWA: newFakeWA()}
	a.wa = f
	ctx := context.Background()
	chat := types.NewJID("15550000005", types.DefaultUserServer)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Legacy", base); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetChatArchived(chat.String(), true); err != nil {
		t.Fatal(err)
	}
	a.setAppStateFullSyncResult(appstate.WAPatchRegularLow, true)
	requests := 0
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) { requests++; return "snapshot", nil }
	f.fetchEvents = func(_ context.Context, name string, full, _ bool) ([]any, error) {
		if name == string(appstate.WAPatchRegularLow) && full {
			t.Error("destructive full replay on known-broken collection")
		}
		return nil, nil
	}
	f.fetchAppState = func(ctx context.Context, name string, full, only bool) error {
		_, err := f.fetchEvents(ctx, name, full, only)
		return err
	}
	var first sync.Map
	a.syncAppStateDeltas(ctx, &first)
	if requests != 1 {
		t.Fatalf("background snapshot requests=%d, want 1", requests)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || !required {
		t.Fatalf("snapshot debt cleared before the primary answered: %v, %v", required, err)
	}
	// The primary answers with a snapshot that has neither the preference nor
	// an action for the legacy archive.
	f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Version: 9, Recovery: true})
	waitForCondition(t, time.Second, func() bool {
		pending, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
		return err == nil && !pending
	})
	if required, err := a.db.ChatArchiveRecoveryRequired(); err != nil || required {
		t.Fatalf("archive recovery still required after the primary's snapshot: %v, %v", required, err)
	}
	got, err := a.db.GetChat(chat.String())
	if err != nil || !got.Archived {
		t.Fatalf("legacy archive after settlement=%+v, %v; want archived", got, err)
	}
	for start := 2; start <= 3; start++ {
		var next sync.Map
		a.syncAppStateDeltas(ctx, &next)
		if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || required {
			t.Fatalf("start %d: settled store re-armed the recovery intent: %v, %v", start, required, err)
		}
	}
	if requests != 1 {
		t.Fatalf("background snapshot requests after settlement=%d, want 1", requests)
	}
}
