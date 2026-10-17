package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func startStatusReceiptApp(t *testing.T) (*App, *fakeWA) {
	t.Helper()
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	var messagesStored, lastEvent atomic.Int64
	a.addSyncEventHandler(
		context.Background(), SyncOptions{}, &messagesStored, &lastEvent,
		make(chan struct{}, 1), make(chan struct{}, 1), make(chan staleReconnectRequest, 1),
		func(string, string) {}, nil, nil, &syncPresence{}, nil,
	)
	return a, f
}

func statusReceipt(viewer types.JID, kind types.ReceiptType, at time.Time, ids ...types.MessageID) *events.Receipt {
	return &events.Receipt{
		MessageSource: types.MessageSource{Chat: types.StatusBroadcastJID, Sender: viewer},
		MessageIDs:    ids,
		Timestamp:     at,
		Type:          kind,
	}
}

// A status of ours lives in status_messages, so the per-chat receipt table never
// saw its views. The real sync event path must now keep them per viewer.
func TestSyncEventHandlerStoresStatusViewsPerViewer(t *testing.T) {
	a, f := startStatusReceiptApp(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if err := a.db.UpsertStatusMessage(store.UpsertStatusMessageParams{MsgID: "st1", Timestamp: at, FromMe: true, Text: "hi"}); err != nil {
		t.Fatalf("UpsertStatusMessage: %v", err)
	}
	first := types.JID{User: "111", Server: types.DefaultUserServer}
	second := types.JID{User: "222", Server: types.DefaultUserServer}

	f.emit(statusReceipt(first, types.ReceiptTypeDelivered, at.Add(time.Minute), "st1"))
	f.emit(statusReceipt(second, types.ReceiptTypeRead, at.Add(2*time.Minute), "st1"))
	// The same viewer opening it again must not count twice.
	f.emit(statusReceipt(second, types.ReceiptTypeRead, at.Add(3*time.Minute), "st1"))
	f.emit(statusReceipt(first, types.ReceiptTypePlayed, at.Add(4*time.Minute), "st1"))

	counts, err := a.db.StatusReceipts("st1")
	if err != nil {
		t.Fatalf("StatusReceipts: %v", err)
	}
	if counts.Delivered != 2 || counts.Read != 2 {
		t.Fatalf("counts = %+v, want two viewers and two who saw it", counts)
	}
}

func TestSyncEventHandlerIgnoresStatusReceiptsForStatusesWeDoNotHold(t *testing.T) {
	a, f := startStatusReceiptApp(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	// Someone else's status, and an id we never stored.
	if err := a.db.UpsertStatusMessage(store.UpsertStatusMessageParams{MsgID: "theirs", Timestamp: at, FromMe: false, Text: "hi"}); err != nil {
		t.Fatalf("UpsertStatusMessage: %v", err)
	}
	viewer := types.JID{User: "111", Server: types.DefaultUserServer}
	f.emit(statusReceipt(viewer, types.ReceiptTypeRead, at, "theirs", "unknown"))
	// Sender/retry bookkeeping is no view either.
	if err := a.db.UpsertStatusMessage(store.UpsertStatusMessageParams{MsgID: "mine", Timestamp: at, FromMe: true, Text: "hi"}); err != nil {
		t.Fatalf("UpsertStatusMessage: %v", err)
	}
	f.emit(statusReceipt(viewer, types.ReceiptTypeRetry, at, "mine"))

	for _, id := range []string{"theirs", "unknown", "mine"} {
		counts, err := a.db.StatusReceipts(id)
		if err != nil || counts.Delivered != 0 {
			t.Fatalf("StatusReceipts(%s) = %+v, %v; want nothing", id, counts, err)
		}
	}
}

// Status receipts must not leak into the chat receipt table, and chat receipts
// must keep working next to them.
func TestStatusReceiptsDoNotTouchChatReceipts(t *testing.T) {
	a, f := startStatusReceiptApp(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if err := a.db.UpsertStatusMessage(store.UpsertStatusMessageParams{MsgID: "st1", Timestamp: at, FromMe: true, Text: "hi"}); err != nil {
		t.Fatalf("UpsertStatusMessage: %v", err)
	}
	viewer := types.JID{User: "111", Server: types.DefaultUserServer}
	f.emit(statusReceipt(viewer, types.ReceiptTypeRead, at, "st1"))

	counts, err := a.db.MessageReceipts(types.StatusBroadcastJID.String(), "st1")
	if err != nil || counts.Delivered != 0 {
		t.Fatalf("MessageReceipts for the status = %+v, %v; want none", counts, err)
	}
}
