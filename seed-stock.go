// Copied into an archived stock v0.25.1 module and built against its store API.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/openclaw/wacli/internal/store"
)

func must(err error) { if err != nil { panic(err) } }

func main() {
	db, err := store.Open(filepath.Join(os.Args[1], "wacli.db"))
	must(err)
	at := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	chats := []string{"15550000001@s.whatsapp.net", "15550000002@s.whatsapp.net", "123456789@g.us"}
	for i, jid := range chats {
		kind := "dm"
		if i == 2 { kind = "group" }
		must(db.UpsertChat(jid, kind, fmt.Sprintf("Synthetic chat %d", i+1), at))
		for n := 1; n <= 8; n++ {
			must(db.UpsertMessage(store.UpsertMessageParams{
				ChatJID: jid, MsgID: fmt.Sprintf("synthetic-%d-%d", i+1, n), SenderJID: chats[0],
				SenderName: "Synthetic sender", Timestamp: at.Add(time.Duration(n)*time.Second),
				FromMe: n%2==0, Text: fmt.Sprintf("Synthetic preserved body %d/%d", i+1,n),
				DisplayText: fmt.Sprintf("Synthetic preserved body %d/%d", i+1,n),
			}))
		}
	}
	must(db.UpsertContact(chats[0], "15550000001", "Synthetic push", "Synthetic Person A", "Synthetic", "Synthetic business"))
	must(db.UpsertContact(chats[1], "15550000002", "Synthetic push B", "Synthetic Person B", "Synthetic", ""))
	must(db.SetAlias(chats[:1], "Synthetic alias"))
	must(db.AddTag(chats[:1], "synthetic-tag"))
	must(db.SetChatArchiveSetting(false, at))
	must(db.ApplyChatArchive(chats[2], true, at.Add(time.Minute)))
	must(db.SetChatPinned(chats[0], true))
	must(db.SetChatMutedUntil(chats[1], 1900000000))
	must(db.SetChatUnreadCount(chats[1], 3))
	must(db.RecordSuccessfulSync(at.Add(2*time.Minute)))
	must(db.Close())
	fmt.Println("Stock v0.25.1 store API wrote 24 messages, 3 chats, 2 contacts, alias/tag, archive preference and chat/sync settings.")
}
