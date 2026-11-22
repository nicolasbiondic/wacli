package wa

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"go.mau.fi/whatsmeow/store"
)

// ErrSignalWritesFenced is returned for a Signal session or sender-key write
// after FenceSessionWrites.
var ErrSignalWritesFenced = errors.New("signal session and sender-key writes are fenced: this command is closing its connection")

// sessionFence counts the Signal writes that decryptions commit and refuses
// them once closed.
//
// Decrypting a message commits the state it advanced with one write: a 1:1
// message (msg, pkmsg) its session with PutSession (and only then consumes a
// one-time prekey), a group message (skmsg) its sender-key chain with
// PutSenderKey (senderKeyFence). whatsmeow then hands the message to the
// event handlers. A message whose state is committed but which no handler
// stores is lost: the server delivers the same ciphertext again and it no
// longer decrypts. A command that runs by itself closes its connection while
// whatsmeow may still be decrypting the backlog. Once the socket is closed it
// fences these writes: a decryption that has not committed fails with the
// session, chain and prekeys untouched, so the message stays decryptable for
// the next connection; and the number of writes committed before the fence
// tells the command whether a decrypted message is still on its way to the
// handlers. Not counted: sends, which write sessions through whatsmeow's
// session cache (PutManySessions), and this device's own sender key, which a
// group send writes (see senderKeyFence.ownKey).
type sessionFence struct {
	store.SessionStore
	// Writes hold mu for reading; the fence takes it for writing, so it waits
	// for writes in progress and sees every write that committed.
	mu     sync.RWMutex
	closed bool
	writes atomic.Uint64
}

func (f *sessionFence) PutSession(ctx context.Context, address string, session []byte) error {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return ErrSignalWritesFenced
	}
	if err := f.SessionStore.PutSession(ctx, address, session); err != nil {
		return err
	}
	f.writes.Add(1)
	return nil
}

func (f *sessionFence) PutManySessions(ctx context.Context, sessions map[string][]byte) error {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return ErrSignalWritesFenced
	}
	return f.SessionStore.PutManySessions(ctx, sessions)
}

// fenceSessionStore wraps the device's session and sender-key stores once and
// returns the fence.
func fenceSessionStore(device *store.Device) *sessionFence {
	if device == nil || device.Sessions == nil {
		return nil
	}
	if fence, ok := device.Sessions.(*sessionFence); ok {
		return fence
	}
	fence := &sessionFence{SessionStore: device.Sessions}
	device.Sessions = fence
	if device.SenderKeys != nil {
		device.SenderKeys = &senderKeyFence{SenderKeyStore: device.SenderKeys, fence: fence, device: device}
	}
	return fence
}

// senderKeyFence does for group messages what sessionFence does for 1:1
// messages, under the same lock and count.
type senderKeyFence struct {
	store.SenderKeyStore
	fence  *sessionFence
	device *store.Device
}

func (s *senderKeyFence) PutSenderKey(ctx context.Context, group, user string, key []byte) error {
	f := s.fence
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return ErrSignalWritesFenced
	}
	if err := s.SenderKeyStore.PutSenderKey(ctx, group, user, key); err != nil {
		return err
	}
	if !s.ownKey(user) {
		f.writes.Add(1)
	}
	return nil
}

// ownKey reports whether user names this device's own sender key, which
// whatsmeow writes when this device sends to a group; no message follows
// that write. The pinned whatsmeow names it by this device's LID for every
// group, whether addressed by LID or by phone number (send.go sendGroup and
// retry.go use getOwnLID().SignalAddress(), also when the LID is empty); its
// Messenger path and older versions name it by this device's phone-number
// JID. Both are excluded: this device never receives a message under its own
// address, while the phone's key (device 0) is a decryption and is counted.
func (s *senderKeyFence) ownKey(user string) bool {
	return user == s.device.GetLID().SignalAddress().String() ||
		user == s.device.GetJID().SignalAddress().String()
}

// sessionFence looks up the fence installed when the client was built (or
// after pairing). It never wraps: after a logout whatsmeow replaces the
// device's stores (Device.Delete), and wrapping them again from here would
// write the device from an event goroutine.
func (c *Client) sessionFence() *sessionFence {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || cli.Store == nil {
		return nil
	}
	fence, _ := cli.Store.Sessions.(*sessionFence)
	return fence
}

// SessionWrites reports how many Signal session writes (decryptions) have
// been committed so far.
func (c *Client) SessionWrites() uint64 {
	fence := c.sessionFence()
	if fence == nil {
		return 0
	}
	return fence.writes.Load()
}

// FenceSessionWrites makes every later Signal session write of this client
// fail, after the writes in progress have finished, and returns how many were
// committed. Call it only after the socket is closed: a decryption it makes
// fail while connected would be answered with a retry receipt and an ack. It
// cannot be undone; the client is about to be closed.
func (c *Client) FenceSessionWrites() uint64 {
	fence := c.sessionFence()
	if fence == nil {
		return 0
	}
	fence.mu.Lock()
	defer fence.mu.Unlock()
	fence.closed = true
	return fence.writes.Load()
}
