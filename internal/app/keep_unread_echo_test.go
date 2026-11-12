package app

import (
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

func TestLiveKeepUnreadEchoBeforeSendPersistence(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "keep"}[keep], func(t *testing.T) {
			a := newTestApp(t)
			client := newFakeWA()
			a.wa = client
			chat := types.NewJID("5551230000", types.DefaultUserServer)
			feedLive(t, a, liveMessage(chat, "incoming", false, ownReadBase, textMessage("hello")))
			if keep {
				a.db.RegisterKeepUnreadIntent(t.Context(), "automatic")
			}
			feedLive(t, a, liveMessage(chat, "automatic", true, ownReadBase.Add(time.Minute), textMessage("reply")))
			wantCount := 0
			if keep {
				wantCount = 1
			}
			requireUnread(t, a, chat, keep, wantCount)
			// Only now does the accepted send persist its outbound row.
			if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: "automatic", Timestamp: ownReadBase.Add(time.Minute), FromMe: true, Text: "reply", KeepUnread: keep}); err != nil {
				t.Fatal(err)
			}
			requireUnread(t, a, chat, keep, wantCount)
			if !keep {
				// Even a subsequent explicit receipts command has no candidates
				// after #533 has cleared the local badge; zero is not a send trigger.
				if n, _, err := a.MarkChatReadWithReceipts(t.Context(), chat); err != nil || n != 0 {
					t.Fatalf("already-read receipt selection: n=%d err=%v", n, err)
				}
			}
			client.mu.Lock()
			defer client.mu.Unlock()
			if len(client.readReceiptCalls) != 0 || len(client.markReadCalls) != 0 {
				t.Fatalf("own echo unexpectedly sent a read transport: receipts=%d patches=%d", len(client.readReceiptCalls), len(client.markReadCalls))
			}
		})
	}
}
