package app

import (
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func assertMessageReadAt(t *testing.T, a *App, chat, id string, want *time.Time) {
	t.Helper()
	m, err := a.db.GetMessage(chat, id)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	raw, ok := fields["read_at"]
	if !ok {
		t.Fatal("message JSON has no read_at")
	}
	var got *time.Time
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if (got == nil) != (want == nil) || (got != nil && !got.Equal(*want)) {
		t.Fatalf("%s read_at = %s; want %v", id, raw, want)
	}
}

func TestSelfReadTimesOtherDevices(t *testing.T) {
	for _, kind := range []types.ReceiptType{types.ReceiptTypeReadSelf, types.ReceiptTypeRead} {
		t.Run(string(kind), func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			pn := types.NewJID("456", types.DefaultUserServer)
			lid := types.NewJID("999", types.HiddenUserServer)
			f.lids[lid] = pn
			seedReceiptChat(t, a, pn, "dm", []store.UpsertMessageParams{
				{MsgID: "earlier"}, {MsgID: "listed-1"}, {MsgID: "listed-2"}, {MsgID: "outgoing", FromMe: true},
			}, 3)
			at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
			evt := &events.Receipt{
				MessageSource: types.MessageSource{Chat: lid, IsFromMe: true},
				Type:          kind, Timestamp: at,
				MessageIDs: []types.MessageID{"listed-1", "listed-2", "outgoing"},
			}
			a.handleReceiptPersistenceEvent(t.Context(), evt)
			evt.Timestamp = at.Add(time.Hour)
			a.handleReceiptPersistenceEvent(t.Context(), evt)
			evt.Timestamp = at.Add(-time.Minute)
			a.handleReceiptPersistenceEvent(t.Context(), evt)
			earliest := evt.Timestamp
			assertMessageReadAt(t, a, pn.String(), "listed-1", &earliest)
			assertMessageReadAt(t, a, pn.String(), "listed-2", &earliest)
			assertMessageReadAt(t, a, pn.String(), "earlier", nil)
			assertMessageReadAt(t, a, pn.String(), "outgoing", nil)
			msgs, err := a.db.ListMessages(store.ListMessagesParams{ChatJID: pn.String(), Limit: 10})
			if err != nil || len(msgs) != 4 {
				t.Fatalf("ListMessages = %v, %v", msgs, err)
			}
			for _, m := range msgs {
				data, _ := json.Marshal(m)
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(data, &fields)
				if m.MsgID == "listed-1" && string(fields["read_at"]) != `"`+earliest.Format(time.RFC3339)+`"` {
					t.Fatalf("listed read_at = %s", fields["read_at"])
				}
			}
		})
	}
}

func TestSelfReadTimesThisDeviceMarkRead(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.NewJID("456", types.DefaultUserServer)
	lid := types.NewJID("999", types.HiddenUserServer)
	f.lids[lid] = chat
	seedReceiptChat(t, a, chat, "dm", []store.UpsertMessageParams{{MsgID: "in-1"}, {MsgID: "in-2"}}, 2)
	if _, _, err := a.MarkChatReadWithReceipts(t.Context(), lid); err != nil {
		t.Fatal(err)
	}
	calls := readReceiptCallsOf(f)
	if len(calls) != 1 {
		t.Fatalf("calls = %v", calls)
	}
	at := calls[0].timestamp.Truncate(time.Second)
	assertMessageReadAt(t, a, chat.String(), "in-1", &at)
	assertMessageReadAt(t, a, chat.String(), "in-2", &at)
}

func TestSelfReadTimesPartialDispatch(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	group := types.NewJID("999", types.GroupServer)
	f.groups[group] = &types.GroupInfo{JID: group}
	seedReceiptChat(t, a, group, "group", []store.UpsertMessageParams{
		{MsgID: "sent", SenderJID: "111@s.whatsapp.net"},
		{MsgID: "failed", SenderJID: "222@s.whatsapp.net"},
	}, 2)
	f.onReadReceipt = func(index int) (types.ReceiptType, error) {
		if index == 1 {
			return "", errors.New("send failed")
		}
		return types.ReceiptTypeReadSelf, nil
	}
	n, _, err := a.MarkChatReadWithReceipts(t.Context(), group)
	if n != 1 || err == nil {
		t.Fatalf("mark read = %d, %v; want partial failure", n, err)
	}
	at := readReceiptCallsOf(f)[0].timestamp.Truncate(time.Second)
	assertMessageReadAt(t, a, group.String(), "sent", &at)
	assertMessageReadAt(t, a, group.String(), "failed", nil)
}

func TestSelfReadTimesIgnorePeerReads(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()
	chat := types.NewJID("456", types.DefaultUserServer)
	seedReceiptChat(t, a, chat, "dm", []store.UpsertMessageParams{{MsgID: "in-1"}}, 1)
	a.handleReceiptPersistenceEvent(t.Context(), &events.Receipt{
		MessageSource: types.MessageSource{Chat: chat}, Type: types.ReceiptTypeRead,
		Timestamp: time.Now(), MessageIDs: []types.MessageID{"in-1"},
	})
	assertMessageReadAt(t, a, chat.String(), "in-1", nil)
}

func TestSelfReadTimesStoreFailureBlocksFreshness(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	var stored, last atomic.Int64
	limits := &syncStorageLimits{app: a}
	a.addSyncEventHandler(t.Context(), SyncOptions{}, &stored, &last, make(chan struct{}, 1), make(chan struct{}, 1), make(chan staleReconnectRequest, 1), nil, nil, limits, &syncPresence{}, nil)
	if err := a.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.emit(&events.Receipt{
		MessageSource: types.MessageSource{Chat: types.NewJID("456", types.DefaultUserServer), IsFromMe: true},
		Type:          types.ReceiptTypeRead, Timestamp: time.Now(), MessageIDs: []types.MessageID{"incoming"},
	})
	if !limits.persistenceFailed.Load() || limits.canRecordFreshness() {
		t.Fatal("self read persistence failure did not block freshness")
	}
}
