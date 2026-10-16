package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestAutoUnarchivePreservesDispatchOrderAcrossQueuedActions(t *testing.T) {
	for _, settingQueued := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued archive", true: "queued setting"}[settingQueued], func(t *testing.T) {
			a := newTestApp(t)
			a.wa = newFakeWA()
			ctx := context.Background()
			chat := types.NewJID("15550000009", types.DefaultUserServer)
			base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			if err := a.db.UpsertChat(chat.String(), "dm", "Synthetic", base); err != nil {
				t.Fatal(err)
			}
			if err := a.db.SetChatArchiveSetting(!settingQueued, base.Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := a.db.ApplyChatArchive(chat.String(), true, base); err != nil {
				t.Fatal(err)
			}
			blocker := a.appStatePersist.reserve()
			if settingQueued {
				a.handleAppStatePersistenceEvent(ctx, &events.UnarchiveChatsSetting{Timestamp: base.Add(500 * time.Millisecond), Action: &waSyncAction.UnarchiveChatsSetting{UnarchiveChats: proto.Bool(true)}}, nil)
			} else {
				a.handleAppStatePersistenceEvent(ctx, &events.Archive{JID: chat, Timestamp: base.Add(500 * time.Millisecond), Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}}, nil)
			}
			var stored atomic.Int64
			a.handleLiveSyncMessage(ctx, SyncOptions{}, &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "new", Timestamp: base}, Message: &waE2E.Message{Conversation: proto.String("synthetic")}}, &stored, nil, nil)
			a.appStatePersist.completeOne(blocker, func() {})
			if err := a.appStatePersist.waitIdle(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := a.db.GetChat(chat.String())
			if err != nil || got.Archived {
				t.Fatalf("earlier queued action overrode later incoming: %+v, %v", got, err)
			}
		})
	}
}

func TestAutoUnarchiveQueuedDisableDoesNotUseOldEnabledSetting(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	ctx := context.Background()
	chat := types.NewJID("15550000010", types.DefaultUserServer)
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Synthetic", base); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetChatArchiveSetting(true, base.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := a.db.ApplyChatArchive(chat.String(), true, base); err != nil {
		t.Fatal(err)
	}
	blocker := a.appStatePersist.reserve()
	a.handleAppStatePersistenceEvent(ctx, &events.Archive{JID: chat, Timestamp: base.Add(500 * time.Millisecond), Action: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}}, nil)
	a.handleAppStatePersistenceEvent(ctx, &events.UnarchiveChatsSetting{Timestamp: base.Add(600 * time.Millisecond), Action: &waSyncAction.UnarchiveChatsSetting{UnarchiveChats: proto.Bool(false)}}, nil)
	var stored atomic.Int64
	a.handleLiveSyncMessage(ctx, SyncOptions{}, &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "new", Timestamp: base}, Message: &waE2E.Message{Conversation: proto.String("synthetic")}}, &stored, nil, nil)
	// Release even when the pre-drain assertion fails so cleanup can drain.
	released := false
	defer func() {
		if !released {
			a.appStatePersist.completeOne(blocker, func() {})
			_ = a.appStatePersist.waitIdle(ctx)
		}
	}()
	got, err := a.db.GetChat(chat.String())
	if err != nil || !got.Archived {
		t.Fatalf("pending disable used stale enabled preference: %+v, %v", got, err)
	}
	a.appStatePersist.completeOne(blocker, func() {})
	released = true
	if err := a.appStatePersist.waitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = a.db.GetChat(chat.String())
	if err != nil || !got.Archived {
		t.Fatalf("disabled preference unarchived chat: %+v, %v", got, err)
	}
}
