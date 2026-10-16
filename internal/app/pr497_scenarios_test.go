package app

// Black-box scenarios for #466 ported from an independent implementation
// (nicolasbiondic/wacli upstream/auto-unarchive-on-incoming). They exercise
// #497 only through its public behaviour: app-state events, live/history
// message ingestion, ArchiveChat, recovery and GetChat. Scenario names are
// prefixed with PR497Scenario so the whole set can be run with
// `-run PR497Scenario`.

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

var scnBase = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func scnArchive(chat types.JID, archived bool, at time.Time) *events.Archive {
	evt := &events.Archive{JID: chat, Timestamp: at, Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(archived)}}
	if !at.IsZero() {
		evt.Action.MessageRange = &waSyncAction.SyncActionMessageRange{LastMessageTimestamp: proto.Int64(at.Unix())}
	}
	return evt
}

func scnSetting(enabled bool, at time.Time) *events.UnarchiveChatsSetting {
	return &events.UnarchiveChatsSetting{Timestamp: at, Action: &waSyncAction.UnarchiveChatsSetting{UnarchiveChats: proto.Bool(enabled)}}
}

func scnText(s string) *waE2E.Message { return &waE2E.Message{Conversation: proto.String(s)} }

func scnLive(chat, sender types.JID, id string, at time.Time, fromMe bool, msg *waE2E.Message) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: fromMe, IsGroup: chat.Server == types.GroupServer},
			ID:            id,
			Timestamp:     at,
		},
		Message: msg,
	}
}

func scnDeliverLive(t *testing.T, a *App, evt *events.Message) {
	t.Helper()
	var stored atomic.Int64
	a.handleLiveSyncMessage(context.Background(), SyncOptions{}, evt, &stored, func(string, string) {}, nil)
}

func scnArchived(t *testing.T, a *App, chat types.JID) bool {
	t.Helper()
	c, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatalf("GetChat %s: %v", chat, err)
	}
	return c.Archived
}

func scnApply(t *testing.T, a *App, evt any) {
	t.Helper()
	if err := a.handleChatStateEvent(context.Background(), evt); err != nil {
		t.Fatalf("handleChatStateEvent %T: %v", evt, err)
	}
}

func scnArchiveWithBoundary(t *testing.T, a *App, chat types.JID, at time.Time) {
	t.Helper()
	if err := a.db.UpsertChat(chat.String(), chatKind(chat), "Archived", at); err != nil {
		t.Fatalf("UpsertChat: %v", err)
	}
	scnApply(t, a, scnArchive(chat, true, at))
	if !scnArchived(t, a, chat) {
		t.Fatalf("chat %s was not archived by the archive action", chat)
	}
}

// A legacy archive: written by a build without boundaries (plain archived=1).
func scnLegacyArchive(t *testing.T, a *App, chat types.JID) {
	t.Helper()
	if err := a.db.UpsertChat(chat.String(), chatKind(chat), "Legacy", scnBase); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetChatArchived(chat.String(), true); err != nil {
		t.Fatal(err)
	}
}

func scnHistoryText(chat types.JID, id string, at time.Time, fromMe bool, text string) *waWeb.WebMessageInfo {
	return &waWeb.WebMessageInfo{
		Key: &waCommon.MessageKey{
			RemoteJID: proto.String(chat.String()),
			FromMe:    proto.Bool(fromMe),
			ID:        proto.String(id),
		},
		MessageTimestamp: proto.Uint64(uint64(at.Unix())),
		Message:          scnText(text),
	}
}

func scnDeliverHistory(t *testing.T, a *App, chat types.JID, msgs ...*waWeb.WebMessageInfo) {
	t.Helper()
	items := make([]*waHistorySync.HistorySyncMsg, 0, len(msgs))
	for _, m := range msgs {
		items = append(items, &waHistorySync.HistorySyncMsg{Message: m})
	}
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{
		SyncType:      waHistorySync.HistorySync_RECENT.Enum(),
		Conversations: []*waHistorySync.Conversation{{ID: proto.String(chat.String()), Messages: items}},
	}}
	var stored, last atomic.Int64
	a.handleHistorySync(context.Background(), SyncOptions{}, history, &stored, &last, func(string, string) {})
}

// Preference as #497 stores it: chat_archive_setting(id=1, enabled, timestamp_ms).
func scnPreference(t *testing.T, a *App) (enabled, known bool) {
	t.Helper()
	raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var e int
	err = raw.QueryRow(`SELECT enabled FROM chat_archive_setting WHERE id=1`).Scan(&e)
	if err == sql.ErrNoRows {
		return false, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return e != 0, true
}

// S1
func TestPR497Scenario_LiveIncomingUnarchivesWhenPreferenceAllows(t *testing.T) {
	sender := types.JID{User: "999", Server: types.DefaultUserServer}
	cases := []struct {
		name         string
		fromMe       bool
		msg          *waE2E.Message
		wantArchived bool
	}{
		{"eligible incoming text", false, scnText("hello again"), false},
		{"own outgoing text", true, scnText("hello again"), true},
		{"reaction only", false, &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{
			Key:  &waCommon.MessageKey{ID: proto.String("target"), FromMe: proto.Bool(true)},
			Text: proto.String("👍"),
		}}, true},
		{"protocol only", false, &waE2E.Message{SenderKeyDistributionMessage: &waE2E.SenderKeyDistributionMessage{
			GroupID: proto.String("120363000000000001@g.us"),
		}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			a.wa = newFakeWA()
			scnApply(t, a, scnSetting(true, scnBase))
			chat := types.JID{User: "120363000000000001", Server: types.GroupServer}
			scnArchiveWithBoundary(t, a, chat, scnBase)
			scnDeliverLive(t, a, scnLive(chat, sender, "live-"+tc.name, scnBase.Add(time.Minute), tc.fromMe, tc.msg))
			if got := scnArchived(t, a, chat); got != tc.wantArchived {
				t.Fatalf("archived = %v after %s, want %v", got, tc.name, tc.wantArchived)
			}
		})
	}
}

// S2
func TestPR497Scenario_KeepsArchiveWhenPreferenceDisallowsOrUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setting *events.UnarchiveChatsSetting
	}{
		{"keep chats archived enabled", scnSetting(false, scnBase)},
		{"preference unknown on upgraded store", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			a.wa = newFakeWA()
			if tc.setting != nil {
				scnApply(t, a, tc.setting)
			}
			chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
			scnArchiveWithBoundary(t, a, chat, scnBase)
			scnDeliverLive(t, a, scnLive(chat, chat, "live-1", scnBase.Add(time.Minute), false, scnText("hi")))
			if !scnArchived(t, a, chat) {
				t.Fatalf("%s: incoming message cleared the archive flag", tc.name)
			}
		})
	}
}

// S3
func TestPR497Scenario_RecoveredPreferenceReconcilesKnownBoundaryOnly(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	known := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	unknown := types.JID{User: "15550000002", Server: types.DefaultUserServer}
	scnArchiveWithBoundary(t, a, known, scnBase)
	scnLegacyArchive(t, a, unknown)
	for _, chat := range []types.JID{known, unknown} {
		scnDeliverLive(t, a, scnLive(chat, chat, "before-pref", scnBase.Add(time.Minute), false, scnText("hi")))
		if !scnArchived(t, a, chat) {
			t.Fatalf("%s left the archive while the preference was unknown", chat)
		}
	}
	scnApply(t, a, scnSetting(true, scnBase))
	if scnArchived(t, a, known) {
		t.Fatal("recovered preference did not unarchive the chat with a newer incoming message")
	}
	if !scnArchived(t, a, unknown) {
		t.Fatal("recovered preference unarchived a chat whose archive boundary is unknown")
	}
}

// S4 (third subcase is a deliberate design deviation of our branch; see report)
func TestPR497Scenario_HistoryMessagesAgainstKnownBoundary(t *testing.T) {
	t.Run("older history message keeps the archive", func(t *testing.T) {
		a := newTestApp(t)
		a.wa = newFakeWA()
		scnApply(t, a, scnSetting(true, scnBase))
		chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
		scnArchiveWithBoundary(t, a, chat, scnBase)
		scnDeliverHistory(t, a, chat, scnHistoryText(chat, "old-1", scnBase.Add(-time.Hour), false, "before the archive"))
		if !scnArchived(t, a, chat) {
			t.Fatal("a backfilled message older than the archive cleared the flag")
		}
	})
	t.Run("history message with unknown boundary keeps the archive", func(t *testing.T) {
		a := newTestApp(t)
		a.wa = newFakeWA()
		scnApply(t, a, scnSetting(true, scnBase))
		chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
		scnLegacyArchive(t, a, chat)
		scnDeliverHistory(t, a, chat, scnHistoryText(chat, "new-1", scnBase.Add(time.Hour), false, "cannot be compared"))
		if !scnArchived(t, a, chat) {
			t.Fatal("a history message cleared an archive whose boundary is unknown")
		}
	})
	t.Run("history message at the boundary keeps the archive", func(t *testing.T) {
		a := newTestApp(t)
		a.wa = newFakeWA()
		scnApply(t, a, scnSetting(true, scnBase))
		chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
		scnArchiveWithBoundary(t, a, chat, scnBase)
		scnDeliverHistory(t, a, chat, scnHistoryText(chat, "edge", scnBase, false, "the archived message"))
		if !scnArchived(t, a, chat) {
			t.Fatal("a history message at the archive boundary cleared the flag")
		}
	})
	t.Run("newer history message after a known boundary unarchives (our deviation)", func(t *testing.T) {
		a := newTestApp(t)
		a.wa = newFakeWA()
		scnApply(t, a, scnSetting(true, scnBase))
		chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
		scnArchiveWithBoundary(t, a, chat, scnBase)
		scnDeliverHistory(t, a, chat,
			scnHistoryText(chat, "mine-1", scnBase.Add(time.Hour), true, "my later message"),
			scnHistoryText(chat, "old-2", scnBase.Add(-time.Minute), false, "older incoming"),
		)
		if !scnArchived(t, a, chat) {
			t.Fatal("own or older history messages cleared the archive")
		}
		scnDeliverHistory(t, a, chat, scnHistoryText(chat, "new-2", scnBase.Add(2*time.Hour), false, "newer incoming"))
		if scnArchived(t, a, chat) {
			t.Fatal("a history message newer than the known boundary did not unarchive")
		}
	})
}

// S5
func TestPR497Scenario_ReplayedArchiveOlderThanIncomingDoesNotRearchive(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	scnApply(t, a, scnSetting(true, scnBase))
	chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	scnArchiveWithBoundary(t, a, chat, scnBase)
	scnDeliverLive(t, a, scnLive(chat, chat, "live-1", scnBase.Add(time.Minute), false, scnText("hi")))
	if scnArchived(t, a, chat) {
		t.Fatal("incoming message did not unarchive")
	}
	scnApply(t, a, scnArchive(chat, true, scnBase))
	if scnArchived(t, a, chat) {
		t.Fatal("stale archive replay re-archived a chat with a newer incoming message")
	}
	scnApply(t, a, scnArchive(chat, true, scnBase.Add(time.Minute)))
	if !scnArchived(t, a, chat) {
		t.Fatal("archive with a boundary at the newest message did not archive")
	}
	scnApply(t, a, scnArchive(chat, false, scnBase.Add(2*time.Minute)))
	if scnArchived(t, a, chat) {
		t.Fatal("explicit unarchive was ignored")
	}
}

// S6
func TestPR497Scenario_ArchiveWithoutTimestampIsHonoured(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	scnApply(t, a, scnSetting(true, scnBase))
	chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	if err := a.db.UpsertChat(chat.String(), "dm", "Bob", scnBase); err != nil {
		t.Fatal(err)
	}
	scnDeliverLive(t, a, scnLive(chat, chat, "live-1", scnBase.Add(time.Minute), false, scnText("hi")))
	scnApply(t, a, &events.Archive{JID: chat, Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}})
	if !scnArchived(t, a, chat) {
		t.Fatal("an archive action without a timestamp was not honoured")
	}
}

// S7
func TestPR497Scenario_StaleArchiveKeepsPinAndUnread(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	scnApply(t, a, scnSetting(true, scnBase))
	chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	if err := a.db.UpsertChat(chat.String(), "dm", "Bob", scnBase); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetChatPinned(chat.String(), true); err != nil {
		t.Fatal(err)
	}
	scnApply(t, a, scnArchive(chat, true, scnBase))
	c, err := a.db.GetChat(chat.String())
	if err != nil || !c.Archived || c.Pinned {
		t.Fatalf("archive did not unpin: %+v, %v", c, err)
	}
	scnDeliverLive(t, a, scnLive(chat, chat, "live-1", scnBase.Add(time.Minute), false, scnText("hi")))
	if err := a.db.SetChatPinned(chat.String(), true); err != nil {
		t.Fatal(err)
	}
	scnApply(t, a, scnArchive(chat, true, scnBase))
	c, err = a.db.GetChat(chat.String())
	if err != nil {
		t.Fatal(err)
	}
	if c.Archived || !c.Pinned || !c.Unread || c.UnreadCount != 1 {
		t.Fatalf("stale archive replay changed pin or unread state: %+v", c)
	}
}

// S8
func TestPR497Scenario_RecoverySnapshotOlderArchiveDoesNotRearchive(t *testing.T) {
	a := newTestApp(t)
	scnApply(t, a, scnSetting(true, scnBase))
	chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	f := &appStateContextWA{fakeWA: newFakeWA()}
	a.wa = f
	scnArchiveWithBoundary(t, a, chat, scnBase)
	scnDeliverLive(t, a, scnLive(chat, chat, "live-1", scnBase.Add(time.Minute), false, scnText("hi")))
	if scnArchived(t, a, chat) {
		t.Fatal("incoming message did not unarchive")
	}
	snapshot := []any{scnSetting(true, scnBase), scnArchive(chat, true, scnBase)}
	f.fetchEvents = func(_ context.Context, name string, full, _ bool) ([]any, error) {
		if name != string(appstate.WAPatchRegularLow) || !full {
			t.Fatalf("recovery fetch = %s full=%v", name, full)
		}
		return nil, appstate.ErrMismatchingLTHash
	}
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) {
		go func() {
			for _, evt := range snapshot {
				f.emit(evt)
			}
			f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Recovery: true})
		}()
		return "recovery-1", nil
	}
	var budget sync.Map
	a.recoverAppStateCollection(context.Background(), string(appstate.WAPatchRegularLow), &budget, 5*time.Second)
	if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || required {
		t.Fatalf("recovery did not complete: required=%v err=%v", required, err)
	}
	if scnArchived(t, a, chat) {
		t.Fatal("recovery snapshot re-archived a chat with a newer incoming message")
	}
}

// S9 (our branch seeds the preference from the pairing history sync's
// GlobalSettings.autoUnarchiveChats; #497 does not read it)
func TestPR497Scenario_HistorySyncGlobalSettingsSeedPreference(t *testing.T) {
	seed := func(a *App, enabled bool) {
		history := &events.HistorySync{Data: &waHistorySync.HistorySync{
			SyncType:       waHistorySync.HistorySync_INITIAL_BOOTSTRAP.Enum(),
			GlobalSettings: &waHistorySync.GlobalSettings{AutoUnarchiveChats: proto.Bool(enabled)},
		}}
		var stored, last atomic.Int64
		a.handleHistorySync(context.Background(), SyncOptions{}, history, &stored, &last, func(string, string) {})
	}
	t.Run("seeded true lets a live arrival unarchive", func(t *testing.T) {
		a := newTestApp(t)
		a.wa = newFakeWA()
		chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
		scnArchiveWithBoundary(t, a, chat, scnBase)
		seed(a, true)
		scnDeliverLive(t, a, scnLive(chat, chat, "live-1", scnBase.Add(time.Minute), false, scnText("hi")))
		if scnArchived(t, a, chat) {
			t.Fatal("history sync global settings did not seed the unarchive preference")
		}
	})
	t.Run("seeded false keeps the archive", func(t *testing.T) {
		a := newTestApp(t)
		a.wa = newFakeWA()
		chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
		scnArchiveWithBoundary(t, a, chat, scnBase)
		seed(a, false)
		scnDeliverLive(t, a, scnLive(chat, chat, "live-1", scnBase.Add(time.Minute), false, scnText("hi")))
		if !scnArchived(t, a, chat) {
			t.Fatal("a seeded keep-archived preference let an incoming message clear the archive")
		}
	})
}

// S10
func TestPR497Scenario_LocalArchiveCommandRecordsItsOwnBoundary(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	f.connected = true
	a.wa = f
	scnApply(t, a, scnSetting(true, scnBase))
	chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	if err := a.db.UpsertChat(chat.String(), "dm", "Bob", scnBase); err != nil {
		t.Fatal(err)
	}
	scnDeliverLive(t, a, scnLive(chat, chat, "live-1", scnBase.Add(time.Minute), false, scnText("hi")))
	if err := a.ArchiveChat(context.Background(), chat, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	if !scnArchived(t, a, chat) {
		t.Fatal("local archive command was superseded by an older message")
	}
	// The echo of our own archive (same instant) must keep it archived.
	scnApply(t, a, scnArchive(chat, true, nowUTC()))
	if !scnArchived(t, a, chat) {
		t.Fatal("echo of the local archive undid it")
	}
	scnDeliverLive(t, a, scnLive(chat, chat, "live-2", nowUTC().Add(2*time.Second), false, scnText("again")))
	if scnArchived(t, a, chat) {
		t.Fatal("a message newer than the local archive did not unarchive")
	}
}

// S11
func TestPR497Scenario_UnarchiveChatsSettingIsRegularLowAndPersisted(t *testing.T) {
	got := appStateCollectionsForEvent(scnSetting(true, scnBase))
	if len(got) != 1 || got[0] != appstate.WAPatchRegularLow {
		t.Fatalf("collections = %v, want regular_low", got)
	}
	a := newTestApp(t)
	a.wa = newFakeWA()
	if _, known := scnPreference(t, a); known {
		t.Fatal("fresh store must start with an unknown preference")
	}
	if err := a.persistAppStateEvent(context.Background(), scnSetting(true, scnBase), nil); err != nil {
		t.Fatal(err)
	}
	if err := a.persistAppStateEvent(context.Background(), (*events.UnarchiveChatsSetting)(nil), nil); err != nil {
		t.Fatal(err)
	}
	if err := a.persistAppStateEvent(context.Background(), &events.UnarchiveChatsSetting{}, nil); err != nil {
		t.Fatal(err)
	}
	if enabled, known := scnPreference(t, a); !known || !enabled {
		t.Fatalf("preference after setting event = enabled %v known %v", enabled, known)
	}
}

// S12 (first assertion is our branch's choice: unknown→true reconciles
// arrivals from before the toggle; #497 only unarchives arrivals after the
// setting's own timestamp)
func TestPR497Scenario_KeepArchivedOffDoesNotUnarchiveRetroactively(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	before := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	kept := types.JID{User: "15550000002", Server: types.DefaultUserServer}
	scnArchiveWithBoundary(t, a, before, scnBase)
	scnArchiveWithBoundary(t, a, kept, scnBase)
	text := scnText("hi")
	scnDeliverLive(t, a, scnLive(before, before, "m-before", scnBase.Add(time.Minute), false, text))
	scnApply(t, a, scnSetting(false, scnBase.Add(10*time.Minute)))
	scnDeliverLive(t, a, scnLive(kept, kept, "m-kept", scnBase.Add(20*time.Minute), false, text))
	scnApply(t, a, scnSetting(true, scnBase.Add(30*time.Minute)))
	if !scnArchived(t, a, kept) {
		t.Fatal("turning keep-archived off unarchived a chat whose message arrived while it was on")
	}
	scnApply(t, a, scnSetting(true, scnBase.Add(30*time.Minute)))
	scnApply(t, a, scnArchive(kept, true, scnBase))
	if !scnArchived(t, a, kept) {
		t.Fatal("replayed setting or archive unarchived a chat kept archived on purpose")
	}
	scnDeliverLive(t, a, scnLive(kept, kept, "m-new", scnBase.Add(40*time.Minute), false, text))
	if scnArchived(t, a, kept) {
		t.Fatal("a new message after keep-archived was turned off did not unarchive")
	}
	if scnArchived(t, a, before) {
		t.Fatal("(our deviation) toggling the setting did not reconcile a message from before the toggle")
	}
}

// S13 (our deviation: history past a known boundary unarchives)
func TestPR497Scenario_HistoryMessageOneSecondPastBoundaryUnarchives(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	scnApply(t, a, scnSetting(true, scnBase))
	chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	scnArchiveWithBoundary(t, a, chat, scnBase)
	scnDeliverHistory(t, a, chat, scnHistoryText(chat, "after", scnBase.Add(time.Second), false, "one second later"))
	if scnArchived(t, a, chat) {
		t.Fatal("a history message after the boundary did not unarchive")
	}
}

// S14
func TestPR497Scenario_SettingRoutedByProductionHandlers(t *testing.T) {
	for name, install := range map[string]func(*testing.T, *App, *fakeWA){
		"sync": installBoundaryReplayHandler,
		"chat state command": func(t *testing.T, a *App, _ *fakeWA) {
			remove, err := a.AddChatStatePersistenceHandler(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(remove)
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
			scnArchiveWithBoundary(t, a, chat, scnBase)
			scnDeliverLive(t, a, scnLive(chat, chat, "m1", scnBase.Add(time.Minute), false, scnText("hi")))
			if !scnArchived(t, a, chat) {
				t.Fatal("chat left the archive while the preference was unknown")
			}
			install(t, a, f)
			f.emit(scnSetting(true, scnBase))
			if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
				t.Fatal(err)
			}
			if enabled, known := scnPreference(t, a); !known || !enabled {
				t.Fatalf("preference = enabled %v known %v", enabled, known)
			}
			if scnArchived(t, a, chat) {
				t.Fatal("the routed setting did not reconcile the stale archive")
			}
		})
	}
}

// S15
func TestPR497Scenario_LiveRedeliveryOfStoredMessageKeepsLegacyArchive(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	scnLegacyArchive(t, a, chat)
	old := scnLive(chat, chat, "old-1", scnBase.Add(-time.Hour), false, scnText("hi"))
	scnDeliverLive(t, a, old)
	scnApply(t, a, scnSetting(true, scnBase))
	if !scnArchived(t, a, chat) {
		t.Fatal("learning the preference unarchived a chat whose boundary is unknown")
	}
	scnDeliverLive(t, a, old)
	if !scnArchived(t, a, chat) {
		t.Fatal("a live redelivery of a stored message cleared a legacy archive")
	}
}

// S17
func TestPR497Scenario_LocalArchiveWinsOverServerClockSkew(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	f.connected = true
	a.wa = f
	now := scnBase.Add(time.Hour)
	previousNowUTC := nowUTC
	nowUTC = func() time.Time { return now }
	t.Cleanup(func() { nowUTC = previousNowUTC })
	scnApply(t, a, scnSetting(true, scnBase))
	chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	if err := a.db.UpsertChat(chat.String(), "dm", "Bob", scnBase); err != nil {
		t.Fatal(err)
	}
	ahead := now.Add(3 * time.Second)
	scnDeliverLive(t, a, scnLive(chat, chat, "ahead-1", ahead, false, scnText("hi")))
	if err := a.ArchiveChat(context.Background(), chat, true); err != nil {
		t.Fatalf("ArchiveChat: %v", err)
	}
	if !scnArchived(t, a, chat) {
		t.Fatal("local archive command was superseded by a message ahead of the local clock")
	}
	scnDeliverLive(t, a, scnLive(chat, chat, "later-1", ahead.Add(time.Second), false, scnText("again")))
	if scnArchived(t, a, chat) {
		t.Fatal("a message newer than the local archive did not unarchive")
	}
}

// S18 (our branch: the first live arrival of a never-stored message leaves
// a legacy archive once the preference is known; #497 waits for the archive
// boundary from a regular_low replay)
func TestPR497Scenario_LiveFirstArrivalUnarchivesLegacyArchive(t *testing.T) {
	for name, at := range map[string]time.Time{
		"new message":                scnBase.Add(time.Minute),
		"late offline-queue message": scnBase.Add(-time.Hour),
	} {
		t.Run(name, func(t *testing.T) {
			a := newTestApp(t)
			a.wa = newFakeWA()
			chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
			scnLegacyArchive(t, a, chat)
			scnApply(t, a, scnSetting(true, scnBase))
			scnDeliverHistory(t, a, chat, scnHistoryText(chat, "hist-1", scnBase.Add(time.Minute), false, "from history"))
			if !scnArchived(t, a, chat) {
				t.Fatal("a history message unarchived a legacy archive")
			}
			scnDeliverLive(t, a, scnLive(chat, chat, "live-1", at, false, scnText("hi")))
			if scnArchived(t, a, chat) {
				t.Fatal("the first live arrival did not unarchive a legacy archive")
			}
		})
	}
}

// S19 (mixed builds)
func TestPR497Scenario_ArchiveByBuildWithoutBoundariesSurvivesReplays(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	scnApply(t, a, scnSetting(true, scnBase))
	chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	scnArchiveWithBoundary(t, a, chat, scnBase)
	text := scnText("hi")
	scnDeliverLive(t, a, scnLive(chat, chat, "m1", scnBase.Add(time.Minute), false, text))
	if scnArchived(t, a, chat) {
		t.Fatal("incoming message did not unarchive")
	}
	// A build without boundaries (e.g. the rollback binary) archives the chat
	// with the plain flag write it has always used.
	if err := a.db.SetChatArchived(chat.String(), true); err != nil {
		t.Fatal(err)
	}
	scnDeliverHistory(t, a, chat, scnHistoryText(chat, "m1", scnBase.Add(time.Minute), false, "hi"))
	if !scnArchived(t, a, chat) {
		t.Fatal("a history replay of a covered message undid an archive applied by another build")
	}
	scnApply(t, a, scnSetting(true, scnBase))
	if !scnArchived(t, a, chat) {
		t.Fatal("a replayed preference undid an archive applied by another build")
	}
	scnDeliverLive(t, a, scnLive(chat, chat, "m2", scnBase.Add(time.Hour), false, text))
	if scnArchived(t, a, chat) {
		t.Fatal("a new live message did not unarchive")
	}
}

// S20 (new, #497-specific): a legacy archive the primary's snapshot does not
// mention, or an account whose snapshot carries no unarchiveChats mutation,
// must not make every sync start request another full regular_low replay.
func TestPR497Scenario_UpgradedStoreRecoveryIsRequestedOnce(t *testing.T) {
	for name, setup := range map[string]func(*testing.T, *App){
		"legacy archive absent from the snapshot": func(t *testing.T, a *App) {
			scnApply(t, a, scnSetting(true, scnBase))
			scnLegacyArchive(t, a, types.JID{User: "15550000001", Server: types.DefaultUserServer})
		},
		"snapshot without an unarchiveChats mutation": func(t *testing.T, a *App) {},
	} {
		t.Run(name, func(t *testing.T) {
			a := newTestApp(t)
			f := &appStateContextWA{fakeWA: newFakeWA()}
			a.wa = f
			setup(t, a)
			fullFetches := 0
			f.fetchEvents = func(_ context.Context, name string, full, _ bool) ([]any, error) {
				if name == string(appstate.WAPatchRegularLow) && full {
					fullFetches++
				}
				return nil, nil // snapshot carries nothing for this store
			}
			f.fetchAppState = func(ctx context.Context, name string, full, only bool) error {
				_, err := f.fetchEvents(ctx, name, full, only)
				return err
			}
			for i := range 3 {
				var recoveries sync.Map
				a.syncAppStateDeltas(context.Background(), &recoveries)
				if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || required {
					t.Fatalf("sync start %d: recovery still pending after a completed full replay: %v %v", i+1, required, err)
				}
			}
			if fullFetches > 1 {
				t.Fatalf("full regular_low replay requested on %d of 3 sync starts; want at most 1", fullFetches)
			}
		})
	}
}

// S21 (LID↔phone identity): an archive learned under a LID before the phone
// mapping is known must still be matched against a later arrival stored under
// the phone JID once the identities merge.
func TestPR497Scenario_ArchiveUnderLIDMergesWithPhoneArrival(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	scnApply(t, a, scnSetting(true, scnBase))
	lid := types.NewJID("15550000001", types.HiddenUserServer)
	pn := types.NewJID("15550000001", types.DefaultUserServer)
	// Archive persisted while only the LID is known.
	scnApply(t, a, scnArchive(lid, true, scnBase))
	if !scnArchived(t, a, lid) {
		t.Fatal("archive under the LID was not stored")
	}
	// Mapping learned afterwards; the arrival is stored under the phone JID.
	f.lids[lid] = pn
	scnDeliverLive(t, a, scnLive(pn, pn, "live-1", scnBase.Add(time.Minute), false, scnText("hi")))
	if err := a.migrateHistoricalLIDs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if scnArchived(t, a, pn) {
		t.Fatal("merged identity kept the chat archived although a newer message arrived")
	}
	// The merged archive still wins against a stale replay of the LID action.
	scnApply(t, a, scnArchive(lid, true, scnBase))
	if scnArchived(t, a, pn) {
		t.Fatal("stale LID archive replay re-archived the merged chat")
	}
}

// Codex P2 probes at the latest head.

// P2 #1 precision: a live arrival in the same second as a (millisecond) archive.
func TestPR497Probe_SameSecondArrivalAfterArchiveUnarchives(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	scnApply(t, a, scnSetting(true, scnBase.Add(-time.Hour)))
	chat := types.JID{User: "15550000001", Server: types.DefaultUserServer}
	scnArchiveWithBoundary(t, a, chat, scnBase.Add(500*time.Millisecond))
	scnDeliverLive(t, a, scnLive(chat, chat, "same-second", scnBase, false, scnText("hi")))
	if scnArchived(t, a, chat) {
		t.Fatal("second-precision arrival after a millisecond archive in the same second stayed archived")
	}
	// A duplicate of that message does not count again, and a later explicit
	// archive within the same second wins.
	scnApply(t, a, scnArchive(chat, true, scnBase.Add(900*time.Millisecond)))
	scnDeliverLive(t, a, scnLive(chat, chat, "same-second", scnBase, false, scnText("hi")))
	if !scnArchived(t, a, chat) {
		t.Fatal("later explicit archive in the same second lost to a duplicate")
	}
}

// P2 #4 missing chats: an archive replayed before the chat exists locally.
func TestPR497Probe_ArchiveForUnknownChatIsRetained(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	scnApply(t, a, scnSetting(true, scnBase))
	chat := types.JID{User: "15550000077", Server: types.DefaultUserServer}
	scnApply(t, a, scnArchive(chat, true, scnBase))
	if !scnArchived(t, a, chat) {
		t.Fatal("archive for a chat not yet present locally was dropped")
	}
	if required, err := a.db.ChatArchiveRecoveryRequired(); err != nil || required {
		t.Fatalf("retained archive still flags recovery: %v %v", required, err)
	}
	scnDeliverHistory(t, a, chat, scnHistoryText(chat, "h1", scnBase.Add(-time.Hour), false, "old"))
	if !scnArchived(t, a, chat) {
		t.Fatal("history creating the chat reset the archive")
	}
	scnDeliverLive(t, a, scnLive(chat, chat, "l1", scnBase.Add(time.Minute), false, scnText("new")))
	if scnArchived(t, a, chat) {
		t.Fatal("arrival after the retained archive did not unarchive")
	}
}

// P2 #3/#5 queued ordering with remapping: archive for the LID queued behind
// startup replay, mapping learned, same-second arrival under the phone JID.
func TestPR497Probe_QueuedLIDArchiveThenRemappedSameSecondArrival(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	ctx := context.Background()
	lid := types.NewJID("15550000009", types.HiddenUserServer)
	pn := types.NewJID("15550000009", types.DefaultUserServer)
	if err := a.db.UpsertChat(pn.String(), "dm", "Synthetic", scnBase); err != nil {
		t.Fatal(err)
	}
	scnApply(t, a, scnSetting(true, scnBase.Add(-time.Second)))
	scnApply(t, a, scnArchive(pn, true, scnBase.Add(-time.Minute)))
	blocker := a.appStatePersist.reserve()
	a.handleAppStatePersistenceEvent(ctx, scnArchive(lid, true, scnBase.Add(500*time.Millisecond)), nil)
	f.lids[lid] = pn
	scnDeliverLive(t, a, scnLive(pn, pn, "new", scnBase, false, scnText("hi")))
	a.appStatePersist.completeOne(blocker, func() {})
	if err := a.appStatePersist.waitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if scnArchived(t, a, pn) {
		t.Fatal("queued LID archive drained after the arrival re-archived the remapped chat")
	}
}
