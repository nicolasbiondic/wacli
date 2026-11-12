package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestKeepUnreadIntentEchoBeforePersistence(t *testing.T) {
	for _, placeholder := range []bool{false, true} {
		t.Run(map[bool]string{false: "content", true: "placeholder"}[placeholder], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wacli.db")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			seedUnreadChat(t, db, ownReadChat, 2, true)
			db.RegisterKeepUnreadIntent(t.Context(), "automatic")
			echo := UpsertMessageParams{ChatJID: ownReadChat, MsgID: "automatic", Timestamp: ownReadBase.Add(time.Minute), FromMe: true, Text: "automatic reply"}
			if placeholder {
				echo.Text, echo.DisplayText, echo.SkipOwnReadMark = "", "(message)", true
			}
			storeMsg(t, db, echo)
			assertUnread(t, db, ownReadChat, true, 2)
			if db.keepUnreadIntent("automatic") != nil {
				t.Fatal("successful persistence did not consume the intent")
			}
			var optedOut int
			if err := db.sql.QueryRow(`SELECT skip_own_read_mark FROM messages WHERE chat_jid=? AND msg_id=?`, ownReadChat, "automatic").Scan(&optedOut); err != nil || optedOut != 1 {
				t.Fatalf("policy was not saved atomically with the early echo: %d, %v", optedOut, err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			// ACK persistence, followed by unflagged replay/enrichment, must not
			// read the chat after the process forgets its in-flight intent.
			echo.SkipOwnReadMark, echo.DisplayText, echo.Text = false, "reply", "reply"
			storeMsg(t, db, echo)
			assertUnread(t, db, ownReadChat, true, 2)
			if err := db.sql.QueryRow(`SELECT skip_own_read_mark FROM messages WHERE msg_id=?`, "automatic").Scan(&optedOut); err != nil || optedOut != 1 {
				t.Fatalf("unflagged replay cleared policy: %d, %v", optedOut, err)
			}
			echo.MsgID = "human"
			storeMsg(t, db, echo)
			assertUnread(t, db, ownReadChat, false, 0)
		})
	}
}

func TestKeepUnreadIntentLifecycle(t *testing.T) {
	db := openTestDB(t)
	complete := db.RegisterKeepUnreadIntent(t.Context(), "failed")
	if db.keepUnreadIntent("failed") == nil {
		t.Fatal("intent not registered before dispatch")
	}
	complete(context.DeadlineExceeded)
	complete(context.DeadlineExceeded)
	if db.keepUnreadIntent("failed") != nil {
		t.Fatal("failure cleanup retained the intent")
	}
	before := time.Now()
	db.RegisterKeepUnreadIntent(t.Context(), "bounded")
	if expires := db.keepUnreadIntent("bounded").expires; expires.Before(before) || expires.After(time.Now().Add(keepUnreadIntentTTL)) {
		t.Fatalf("unbounded TTL: %s", expires)
	}
	// A stale cleanup cannot clear a newer attempt's intent for the same ID.
	stale := db.RegisterKeepUnreadIntent(t.Context(), "replacement")
	db.RegisterKeepUnreadIntent(t.Context(), "replacement")
	stale(context.Canceled)
	if db.keepUnreadIntent("replacement") == nil {
		t.Fatal("old attempt cleared replacement")
	}
}

func TestKeepUnreadIntentBoundedExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db := &DB{}
		db.RegisterKeepUnreadIntent(context.Background(), "lost-ack")
		time.Sleep(keepUnreadIntentTTL)
		synctest.Wait()
		db.sendIntentsMu.Lock()
		defer db.sendIntentsMu.Unlock()
		if len(db.sendIntents) != 0 {
			t.Fatal("TTL did not reclaim the intent")
		}
	})
}

func TestKeepUnreadIntentAttemptCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db := &DB{}
		ctx, cancel := context.WithCancel(context.Background())
		db.RegisterKeepUnreadIntent(ctx, "pending")
		cancel()
		// Effective expiry must not depend on when AfterFunc gets scheduled.
		if db.keepUnreadIntent("pending") != nil {
			t.Fatal("canceled pending send still has an effective intent")
		}
		synctest.Wait()
		ctx, cancel = context.WithCancel(context.Background())
		complete := db.RegisterKeepUnreadIntent(ctx, "accepted")
		complete(nil)
		cancel()
		synctest.Wait()
		intent := db.keepUnreadIntent("accepted")
		if intent == nil {
			t.Fatal("ACK cancellation erased protection before persistence")
		}
		db.clearKeepUnreadIntent("accepted", intent)
	})
}

func TestKeepUnreadIntentConcurrentEchoAndPersistence(t *testing.T) {
	db := openTestDB(t)
	seedUnreadChat(t, db, ownReadChat, 2, true)
	db.RegisterKeepUnreadIntent(t.Context(), "automatic")
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := db.UpsertMessage(UpsertMessageParams{ChatJID: ownReadChat, MsgID: "automatic", Timestamp: ownReadBase.Add(time.Minute), FromMe: true, Text: "reply"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	assertUnread(t, db, ownReadChat, true, 2)
}

func TestKeepUnreadPolicySurvivesLIDMerge(t *testing.T) {
	db := openTestDB(t)
	lid := "20001@lid"
	seedUnreadChat(t, db, lid, 2, true)
	storeMsg(t, db, UpsertMessageParams{ChatJID: lid, MsgID: "automatic", Timestamp: ownReadBase, FromMe: true, DisplayText: "(message)", KeepUnread: true})
	if err := db.MigrateLIDToPN(lid, ownReadChat); err != nil {
		t.Fatal(err)
	}
	storeMsg(t, db, UpsertMessageParams{ChatJID: ownReadChat, MsgID: "automatic", Timestamp: ownReadBase, FromMe: true, Text: "decoded"})
	assertUnread(t, db, ownReadChat, true, 2)
}
