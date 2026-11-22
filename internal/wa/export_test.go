package wa

import (
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
)

// Hooks for the wa_test package, which drives the real App and the real SDK
// client together.

func RawClient(c *Client) *whatsmeow.Client { return c.client }

func PairedSessionStoreAt(t *testing.T, path string) { newPairedSessionStoreAt(t, path) }

// WrapFencedStores puts test stores between the fence and the device's own
// session and sender-key stores, so a test can act inside a write the fence
// is guarding.
func WrapFencedStores(c *Client, sessions func(store.SessionStore) store.SessionStore, senderKeys func(store.SenderKeyStore) store.SenderKeyStore) {
	fence := fenceSessionStore(c.client.Store)
	if sessions != nil {
		fence.SessionStore = sessions(fence.SessionStore)
	}
	if keys, ok := c.client.Store.SenderKeys.(*senderKeyFence); ok && senderKeys != nil {
		keys.SenderKeyStore = senderKeys(keys.SenderKeyStore)
	}
}
