//go:build reviewprobe

// Reviewer's consequence probe for finding F1 (wacli-026-delta.md), kept as
// written apart from gofmt and the build tag. It hands the persistence owner
// two events for one key with identical timestamps and no batch, version or
// origin, "new" first and "old" second, and expects "new" to survive. At this
// boundary that input is the same as a legitimate later change, where the
// event applied last must win (internal/store/lists_test.go), so the owner
// cannot satisfy it. Run it with `-tags reviewprobe` to see the consequence.
//
// The defect is fixed where the order is known: the SDK delivers the batches
// of a collection in commit order, so this sequence no longer reaches the
// owner. TestAppStateProjectionFollowsSDKCommitOrder in internal/wa proves the
// label and the manual-unread case through the real SDK path.

package app

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// SDK probe TestReviewDeltaLabelEventsCannotRegressAfterRecovery produces this
// observable delivery order. Verify how the actual wacli persistence owner
// handles it, including the analogous newer manual-unread / older read case.
func TestReviewDeltaProjectionRejectsLateOldSDKBatch(t *testing.T) {
	t.Run("labels", func(t *testing.T) {
		a, _ := newLabelWriteApp(t)
		for _, name := range []string{"new", "old"} {
			a.handleAppStatePersistenceEvent(t.Context(), &events.LabelEdit{LabelID: "7", Action: &waSyncAction.LabelEditAction{Name: proto.String(name), Deleted: proto.Bool(false)}, Timestamp: time.Unix(1, 0)}, nil)
		}
		if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
			t.Fatal(err)
		}
		lists, err := a.db.Lists()
		if err != nil {
			t.Fatal(err)
		}
		stale := len(lists.Lists) == 1 && lists.Lists[0].Name == "old"
		t.Logf("stale_label_after_real_projection=%v", stale)
		if stale {
			t.Fatal("old SDK batch replaces newer label state")
		}
	})
	t.Run("manual_unread", func(t *testing.T) {
		a := newTestApp(t)
		a.wa = newFakeWA()
		chat := types.NewJID("15550000001", types.DefaultUserServer)
		if err := a.db.UpsertChat(chat.String(), "dm", "synthetic", time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
		for _, read := range []bool{false, true} {
			a.handleAppStatePersistenceEvent(t.Context(), &events.MarkChatAsRead{JID: chat, Action: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(read)}, Timestamp: time.Unix(1, 0)}, nil)
		}
		if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
			t.Fatal(err)
		}
		c, err := a.db.GetChat(chat.String())
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("manual_unread_survived=%v", c.Unread)
		if !c.Unread {
			t.Fatal("late old read event clears a newer manual unread marker")
		}
	})
}
