package wa_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/appstate/lthash"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/proto/waSyncdSnapshotRecovery"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/keys"
	"google.golang.org/protobuf/proto"
)

// Review finding F1 at the wacli level, through the real SDK: App -> real
// whatsmeow client -> Noise socket -> syncd peer, with the owner's real
// persistence handler and wacli.db. A handler registered ahead of the owner
// holds one committed batch back, as a slow handler would, while a later
// batch of the same collection commits. The projection must end with the
// later batch's values.
//
// This replaces the consequence probe in
// internal/app/review_delta_projection_test.go, which hands the owner two
// events without any batch or version and so cannot tell stale from newer.
func TestAppStateProjectionFollowsSDKCommitOrder(t *testing.T) {
	// This test must stay nonparallel: the SDK exports a process-wide trust root.
	root := keys.NewKeyPair()
	previousRoot := whatsmeow.WACertPubKey
	whatsmeow.WACertPubKey = *root.Pub
	t.Cleanup(func() { whatsmeow.WACertPubKey = previousRoot })

	// The fetch commits label "old" and a read marker, and is held before the
	// owner sees them. A recovery snapshot then adopts label "new" and a manual
	// unread marker. The transport peer only serves `regular`, so the read
	// marker (a regular_low action on a phone) travels in the same collection.
	t.Run("recovery_after_fetch", func(t *testing.T) {
		e := newRegularTransport(t, root)
		entered, release := e.holdLabel("old")
		e.persistOwner()
		chat := types.NewJID("15550000001", types.DefaultUserServer)
		if err := e.app.DB().UpsertChat(chat.String(), "dm", "synthetic", time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
		e.server.foreignPatch(e.ctx, labelEdit("old"), appstate.BuildMarkChatAsRead(chat, true, time.Time{}, nil).Mutations[0])
		fetched := make(chan error, 1)
		go func() { fetched <- e.cli.FetchAppState(e.ctx, appstate.WAPatchRegular, false, false) }()
		e.wait(entered, "fetch never dispatched its label")
		e.adoptRecovery(
			&waSyncAction.SyncActionData{Index: []byte(`["label_edit","7"]`), Value: &waSyncAction.SyncActionValue{
				Timestamp: proto.Int64(time.Now().UnixMilli()), LabelEditAction: &waSyncAction.LabelEditAction{Name: proto.String("new")},
			}},
			&waSyncAction.SyncActionData{Index: []byte(fmt.Sprintf(`["markChatAsRead",%q]`, chat.String())), Value: &waSyncAction.SyncActionValue{
				Timestamp: proto.Int64(time.Now().UnixMilli()), MarkChatAsReadAction: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(false)},
			}},
		)
		release()
		if err := <-fetched; err != nil {
			t.Fatal(err)
		}
		e.projection("new", func() error {
			c, err := e.app.DB().GetChat(chat.String())
			if err != nil {
				return err
			}
			if !c.Unread {
				return fmt.Errorf("late older read event cleared the newer manual unread marker")
			}
			return nil
		})
	})

	// Same hold, but the later batch is a command's own captured fetch (the
	// pre-write sync of a quick reply), which the owner persists itself.
	t.Run("captured_fetch_after_fetch", func(t *testing.T) {
		e := newRegularTransport(t, root)
		entered, release := e.holdLabel("old")
		e.persistOwner()
		e.server.foreignPatch(e.ctx, labelEdit("old"))
		fetched := make(chan error, 1)
		go func() { fetched <- e.cli.FetchAppState(e.ctx, appstate.WAPatchRegular, false, false) }()
		e.wait(entered, "fetch never dispatched its label")
		e.server.foreignPatch(e.ctx, labelEdit("new"))
		created := make(chan error, 1)
		go func() {
			_, err := e.app.CreateQuickReply(e.ctx, app.QuickReplyInput{Shortcut: proto.String("transport"), Message: proto.String("created")})
			created <- err
		}()
		var createErr error
		select {
		case createErr = <-created:
			t.Log("the command persisted its fetch while the earlier batch was still undelivered")
			created = nil
		case <-time.After(500 * time.Millisecond):
		}
		release()
		if created != nil {
			createErr = <-created
		}
		if createErr != nil {
			t.Fatal(createErr)
		}
		if err := <-fetched; err != nil {
			t.Fatal(err)
		}
		e.projection("new", nil)
	})

	// A captured fetch that gives up while an earlier batch is still held must
	// fail without ever taking a write position: nobody would complete it.
	t.Run("captured_fetch_gives_up", func(t *testing.T) {
		e := newRegularTransport(t, root)
		entered, release := e.holdLabel("old")
		e.persistOwner()
		e.server.foreignPatch(e.ctx, labelEdit("old"))
		fetched := make(chan error, 1)
		go func() { fetched <- e.cli.FetchAppState(e.ctx, appstate.WAPatchRegular, false, false) }()
		e.wait(entered, "fetch never dispatched its label")
		e.server.foreignPatch(e.ctx, labelEdit("new"))
		short, cancel := context.WithTimeout(e.ctx, 300*time.Millisecond)
		defer cancel()
		var admitted atomic.Bool
		batch, err := e.app.WA().(*wa.Client).FetchAppStateEventsOrdered(short, "regular", false, false, func() { admitted.Store(true) })
		if !errors.Is(err, context.DeadlineExceeded) || batch != nil {
			t.Fatalf("batch=%v err=%v, want the wait to end with the context", batch, err)
		}
		release()
		if err := <-fetched; err != nil {
			t.Fatal(err)
		}
		if admitted.Load() {
			t.Fatal("an abandoned fetch still took a write position")
		}
		e.projection("old", nil)
	})
}

func labelEdit(name string) appstate.MutationInfo {
	return appstate.BuildLabelEdit("7", name, 1, false).Mutations[0]
}

// holdLabel blocks the SDK's dispatch of the first label event with that name
// until release, in a handler that runs ahead of the persistence owner.
func (e *regularTransport) holdLabel(name string) (<-chan struct{}, func()) {
	e.t.Helper()
	entered, released := make(chan struct{}), make(chan struct{})
	var enter, leave sync.Once
	release := func() { leave.Do(func() { close(released) }) }
	id := e.cli.AddEventHandler(func(evt any) {
		if label, ok := evt.(*events.LabelEdit); ok && label.Action.GetName() == name {
			held := false
			enter.Do(func() { held = true; close(entered) })
			if held {
				select {
				case <-released:
				case <-e.ctx.Done():
				}
			}
		}
	})
	e.t.Cleanup(func() { release(); e.cli.RemoveEventHandler(id) })
	return entered, release
}

func (e *regularTransport) persistOwner() {
	e.t.Helper()
	remove, err := e.app.AddChatStatePersistenceHandler(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(remove)
}

func (e *regularTransport) wait(ch <-chan struct{}, failure string) {
	e.t.Helper()
	select {
	case <-ch:
	case <-e.ctx.Done():
		e.t.Fatal(failure)
	}
}

// adoptRecovery hands the SDK a primary recovery snapshot nine versions ahead
// of the stored cursor, the way a peer data operation response does.
func (e *regularTransport) adoptRecovery(records ...*waSyncAction.SyncActionData) {
	e.t.Helper()
	keyID, err := e.cli.Store.AppStateKeys.GetLatestAppStateSyncKeyID(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	version, _, err := e.cli.Store.AppState.GetAppStateVersion(e.ctx, string(appstate.WAPatchRegular))
	if err != nil {
		e.t.Fatal(err)
	}
	var hash [128]byte
	plain := make([]*waSyncdSnapshotRecovery.SyncdPlainTextRecord, len(records))
	for i, record := range records {
		mac := make([]byte, 32)
		mac[0] = byte(i + 1)
		lthash.WAPatchIntegrity.SubtractThenAddInPlace(hash[:], nil, [][]byte{mac})
		plain[i] = &waSyncdSnapshotRecovery.SyncdPlainTextRecord{KeyID: keyID, Mac: mac, Value: record}
	}
	raw, err := proto.Marshal(&waSyncdSnapshotRecovery.SyncdSnapshotRecovery{
		CollectionName:   proto.String(string(appstate.WAPatchRegular)),
		CollectionLthash: hash[:],
		Version:          &waSyncdSnapshotRecovery.SyncdVersion{Version: proto.Uint64(version + 9)},
		MutationRecords:  plain,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	result := []*waE2E.PeerDataOperationRequestResponseMessage_PeerDataOperationResult{{
		SyncdSnapshotFatalRecoveryResponse: &waE2E.PeerDataOperationRequestResponseMessage_PeerDataOperationResult_SyncDSnapshotFatalRecoveryResponse{CollectionSnapshot: raw},
	}}
	if !e.cli.DangerousInternals().HandleAppStateRecovery(e.ctx, "synthetic-recovery", result) {
		e.t.Fatal("SDK rejected the recovery snapshot")
	}
	stored, _, err := e.cli.Store.AppState.GetAppStateVersion(e.ctx, string(appstate.WAPatchRegular))
	if err != nil || stored != version+9 {
		e.t.Fatalf("recovery was not adopted: version=%d err=%v", stored, err)
	}
}

// projection waits for the owner to settle and requires label 7 to carry the
// wanted name in wacli.db, plus whatever extra reports.
func (e *regularTransport) projection(label string, extra func() error) {
	e.t.Helper()
	var failure error
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		lists, err := e.app.DB().Lists()
		switch {
		case err != nil:
			failure = err
		case len(lists.Lists) != 1 || lists.Lists[0].ID != "7":
			failure = fmt.Errorf("label 7 is not projected: %+v", lists.Lists)
		case lists.Lists[0].Name != label:
			failure = fmt.Errorf("label 7 is %q, want %q: the older batch was applied last", lists.Lists[0].Name, label)
		case extra != nil:
			failure = extra()
		default:
			failure = nil
		}
		if failure == nil {
			e.t.Logf("PROJECTION label 7=%q", label)
			return
		}
	}
	e.t.Fatal(failure)
}

// foreignPatch stores a patch another device wrote, without the quick-reply
// bookkeeping of append.
func (s *transportSyncd) foreignPatch(ctx context.Context, mutations ...appstate.MutationInfo) {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.processor.EncodePatch(ctx, s.keyID, s.state, appstate.PatchInfo{Type: appstate.WAPatchRegular, Timestamp: time.Now(), Mutations: mutations})
	if err != nil {
		s.t.Fatal(err)
	}
	var patch waServerSync.SyncdPatch
	if err := proto.Unmarshal(raw, &patch); err != nil {
		s.t.Fatal(err)
	}
	patch.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(s.state.Version + 1)}
	_, state, err := s.processor.DecodePatches(ctx, &appstate.PatchList{Name: appstate.WAPatchRegular, Patches: []*waServerSync.SyncdPatch{&patch}}, s.state, true)
	if err != nil {
		s.t.Fatal(err)
	}
	s.state = state
	encoded, err := proto.Marshal(&patch)
	if err != nil {
		s.t.Fatal(err)
	}
	s.patches = append(s.patches, encoded)
	s.t.Logf("SERVER FOREIGN v%d mutations=%d", state.Version, len(mutations))
}
