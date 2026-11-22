package wa

import (
	"bytes"
	"errors"
	"testing"

	"go.mau.fi/libsignal/groups"
	"go.mau.fi/libsignal/protocol"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// The real client's session store goes through the fence: before it is
// closed a session write lands, after it the write fails and the stored
// session (the ratchet) is left as it was.
func TestFenceSessionWritesKeepsTheRatchet(t *testing.T) {
	c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: newTestKeyStateStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	ctx := t.Context()
	sessions := c.client.Store.Sessions
	if err := sessions.PutSession(ctx, "15551230000.0:1", []byte{1}); err != nil {
		t.Fatalf("session write before the fence: %v", err)
	}
	if got := c.SessionWrites(); got != 1 {
		t.Fatalf("session writes counted = %d, want 1: the client's session store must be wrapped from the start", got)
	}
	if got := c.FenceSessionWrites(); got != 1 {
		t.Fatalf("FenceSessionWrites = %d, want the 1 write committed before it", got)
	}
	if err := c.client.Store.Sessions.PutSession(ctx, "15551230000.0:1", []byte{2}); !errors.Is(err, ErrSignalWritesFenced) {
		t.Fatalf("session write after the fence = %v, want ErrSignalWritesFenced", err)
	}
	if err := c.client.Store.Sessions.PutManySessions(ctx, map[string][]byte{"15551230000.0:1": {3}}); !errors.Is(err, ErrSignalWritesFenced) {
		t.Fatalf("batch session write after the fence = %v, want ErrSignalWritesFenced", err)
	}
	got, err := c.client.Store.Sessions.GetSession(ctx, "15551230000.0:1")
	if err != nil || !bytes.Equal(got, []byte{1}) {
		t.Fatalf("stored session after the fence = %v, %v; want the one written before it", got, err)
	}
}

// A group message's decryption commits its sender-key chain with one
// PutSenderKey: it is counted, and refused after the fence with the stored
// chain left as it was.
func TestFenceSenderKeyWritesKeepTheChain(t *testing.T) {
	c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: newTestKeyStateStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	ctx := t.Context()
	const group, sender = "120363000000000001@g.us", "88880000001_1:0"
	if err := c.client.Store.SenderKeys.PutSenderKey(ctx, group, sender, []byte{1}); err != nil {
		t.Fatalf("sender-key write before the fence: %v", err)
	}
	if got := c.SessionWrites(); got != 1 {
		t.Fatalf("writes counted = %d, want 1: a group decryption commits through the sender-key store", got)
	}
	if got := c.FenceSessionWrites(); got != 1 {
		t.Fatalf("FenceSessionWrites = %d, want 1", got)
	}
	if err := c.client.Store.SenderKeys.PutSenderKey(ctx, group, sender, []byte{2}); !errors.Is(err, ErrSignalWritesFenced) {
		t.Fatalf("sender-key write after the fence = %v, want ErrSignalWritesFenced", err)
	}
	got, err := c.client.Store.SenderKeys.GetSenderKey(ctx, group, sender)
	if err != nil || !bytes.Equal(got, []byte{1}) {
		t.Fatalf("stored sender key after the fence = %v, %v; want the one written before it", got, err)
	}
}

// When this device sends to a group, whatsmeow creates or advances its own
// sender key (send.go sendGroup: named by this device's LID, also when the
// LID is empty; the Messenger path and older versions use its phone-number
// JID). No message follows that write, so it must not be counted: it would
// make every one-shot group send wait 1 s at exit and warn falsely. The
// phone's own key (same account, device 0) is a decryption and is counted.
func TestFenceDoesNotCountThisDevicesOwnSenderKey(t *testing.T) {
	c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: newTestKeyStateStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	ctx := t.Context()
	device := c.client.Store
	const group = "120363000000000001@g.us"
	var counted uint64
	create := func(label string, own *protocol.SignalAddress) {
		t.Helper()
		// What sendGroup does: GroupSessionBuilder.Create on its own key name.
		if _, err := groups.NewGroupSessionBuilder(device, waStore.SignalProtobufSerializer).Create(ctx, protocol.NewSenderKeyName(group, own)); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if got := c.SessionWrites() - counted; got != 0 {
			t.Errorf("%s: writes counted = %d, want 0 (this device's own sender key)", label, got)
		}
		counted = c.SessionWrites()
	}
	create("own key, LID unknown", device.GetLID().SignalAddress())
	device.LID = types.JID{User: "99990000001", Device: device.GetJID().Device, Server: types.HiddenUserServer}
	create("own key by LID", device.GetLID().SignalAddress())
	create("own key by phone number", device.GetJID().SignalAddress())
	phone := device.GetLID()
	phone.Device = 0
	if err := device.SenderKeys.PutSenderKey(ctx, group, phone.SignalAddress().String(), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if got := c.SessionWrites() - counted; got != 1 {
		t.Fatalf("writes counted for the phone's sender key = %d, want 1", got)
	}
}

// Looking the fence up never installs it: after a logout whatsmeow replaces
// every store of the device (Device.Delete), and the client must leave them.
func TestSessionWritesDoesNotWrapAReplacedStore(t *testing.T) {
	c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: newTestKeyStateStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	replaced := &waStore.NoopStore{Error: waStore.ErrDeviceDeleted}
	c.client.Store.SetAllStores(replaced)
	if got := c.SessionWrites(); got != 0 {
		t.Fatalf("SessionWrites = %d, want 0", got)
	}
	if got := c.FenceSessionWrites(); got != 0 {
		t.Fatalf("FenceSessionWrites = %d, want 0", got)
	}
	if c.client.Store.Sessions != waStore.SessionStore(replaced) || c.client.Store.SenderKeys != waStore.SenderKeyStore(replaced) {
		t.Fatal("looking the fence up wrapped the device's replaced stores")
	}
}
