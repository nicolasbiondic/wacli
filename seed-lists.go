// Copied into the archived #498 build (bbd627e), never linked to PR #539 code.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/openclaw/wacli/internal/store"
)

func must(err error) { if err != nil { panic(err) } }
func ptr[T any](v T) *T { return &v }

func main() {
	db, err := store.Open(filepath.Join(os.Args[1], "wacli.db"))
	must(err)
	at := time.Now().UTC().Truncate(time.Second)
	for _, p := range []store.ListUpdate{
		{ID:"7", Name:ptr("Synthetic sales"), Color:ptr(int32(3)), Type:ptr(int32(5)), IsActive:ptr(true), Timestamp:at, FromFullSync:true},
		{ID:"8", Name:ptr("Synthetic legacy label"), Color:ptr(int32(4)), Timestamp:at, FromFullSync:true},
		{ID:"9", Name:ptr("Synthetic retired"), Deleted:ptr(true), Timestamp:at, FromFullSync:true},
	} { must(db.UpsertList(p)) }
	must(db.SetChatList("15550000001@s.whatsapp.net", "7", true, at, true))
	must(db.SetChatList("15550000002@s.whatsapp.net", "7", false, at, true))
	must(db.SetChatList("15550000001@s.whatsapp.net", "8", true, at, true))
	must(db.Close())
	fmt.Println("Actual #498 store API wrote 3 definitions (one tombstone), 3 memberships, full_sync_events_seen=true.")
}
