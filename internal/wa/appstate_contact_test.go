package wa

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"github.com/openclaw/wacli/internal/sqliteutil"
)

// The patch must match what the primary device and WhatsApp Web write for a
// saved contact: the `contact` index keyed by the phone-number JID in the
// `critical_unblock_low` collection, action version 2, a SET with the names
// and the save-to-phone flag.
func TestBuildContactPatchMatchesClientShape(t *testing.T) {
	target := types.NewJID("15550001111", types.DefaultUserServer)
	target.Device = 7 // a device suffix must never leak into the index
	lid := types.NewJID("123456789012345", types.HiddenUserServer)

	patch := BuildContactPatch(target, "Ada Lovelace", "Ada", lid)

	if patch.Type != appstate.WAPatchCriticalUnblockLow {
		t.Fatalf("collection = %q, want %q", patch.Type, appstate.WAPatchCriticalUnblockLow)
	}
	if !patch.Timestamp.IsZero() {
		t.Fatal("timestamp should be left for EncodePatch to fill")
	}
	if len(patch.Mutations) != 1 {
		t.Fatalf("mutations = %d, want exactly one", len(patch.Mutations))
	}
	m := patch.Mutations[0]
	if m.Version != 2 {
		t.Fatalf("action version = %d, want 2", m.Version)
	}
	index, err := json.Marshal(m.Index)
	if err != nil {
		t.Fatal(err)
	}
	if string(index) != `["contact","15550001111@s.whatsapp.net"]` {
		t.Fatalf("index = %s, want contact index keyed by the non-AD phone JID", index)
	}
	action := m.Value.GetContactAction()
	if action == nil {
		t.Fatalf("value = %v, want a ContactAction", m.Value)
	}
	if action.GetFullName() != "Ada Lovelace" || action.GetFirstName() != "Ada" {
		t.Fatalf("names = %q/%q, want Ada Lovelace/Ada", action.GetFullName(), action.GetFirstName())
	}
	if action.GetLidJID() != "123456789012345@lid" {
		t.Fatalf("lidJID = %q, want the known LID", action.GetLidJID())
	}
	if action.SaveOnPrimaryAddressbook == nil || !action.GetSaveOnPrimaryAddressbook() {
		t.Fatal("saveOnPrimaryAddressbook must be set so the phone writes the device address book")
	}
	if action.PnJID != nil || action.Username != nil || action.Birthday != nil {
		t.Fatalf("unexpected extra fields set: %v", action)
	}
	// Only fields the SET actually carries may be present; whatsmeow fills the
	// timestamp while encoding.
	if m.Value.Timestamp != nil {
		t.Fatal("timestamp must be left for EncodePatch")
	}
}

func TestBuildContactPatchOmitsUnknownOptionalFields(t *testing.T) {
	target := types.NewJID("15550001111", types.DefaultUserServer)
	patch := BuildContactPatch(target, "Acme", "", types.JID{})
	action := patch.Mutations[0].Value.GetContactAction()
	if action.FirstName != nil {
		t.Fatalf("firstName = %q, want unset when not provided", action.GetFirstName())
	}
	if action.LidJID != nil {
		t.Fatalf("lidJID = %q, want unset without a known LID", action.GetLidJID())
	}
	if action.GetFullName() != "Acme" || !action.GetSaveOnPrimaryAddressbook() {
		t.Fatalf("action = %v, want full name and save flag", action)
	}
}

// The patch must survive whatsmeow's own encoder and decoder: the receiving
// side sees a SET contact mutation, version 2, with the names, LID, and save
// flag, and the encoder fills the timestamp.
func TestBuildContactPatchRoundTripsThroughAppStateProcessor(t *testing.T) {
	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(filepath.Join(t.TempDir(), "session.db"), "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Close() })
	device := container.NewDevice()
	device.ID = &types.JID{User: "15550000000", Device: 7, Server: types.DefaultUserServer}
	device.Account = testAccount()
	if err := device.Save(ctx); err != nil {
		t.Fatal(err)
	}
	keyID := []byte{1, 2, 3, 4}
	if err := device.AppStateKeys.PutAppStateSyncKey(ctx, keyID, store.AppStateSyncKey{Data: bytes.Repeat([]byte{7}, 32), Fingerprint: []byte{1}, Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	proc := appstate.NewProcessor(device, waLog.Noop)

	target := types.NewJID("15550001111", types.DefaultUserServer)
	lid := types.NewJID("123456789012345", types.HiddenUserServer)
	before := time.Now().UnixMilli()
	raw, err := proc.EncodePatch(ctx, keyID, appstate.HashState{}, BuildContactPatch(target, "Ada Lovelace", "", lid))
	if err != nil {
		t.Fatalf("EncodePatch: %v", err)
	}
	var patch waServerSync.SyncdPatch
	if err := proto.Unmarshal(raw, &patch); err != nil {
		t.Fatal(err)
	}
	patch.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(1)}
	mutations, _, err := proc.DecodePatches(ctx, &appstate.PatchList{
		Name:    appstate.WAPatchCriticalUnblockLow,
		Patches: []*waServerSync.SyncdPatch{&patch},
	}, appstate.HashState{}, true)
	if err != nil {
		t.Fatalf("DecodePatches: %v", err)
	}
	if len(mutations) != 1 {
		t.Fatalf("mutations = %d, want 1", len(mutations))
	}
	m := mutations[0]
	if m.Operation != waServerSync.SyncdMutation_SET || m.Version != 2 {
		t.Fatalf("operation/version = %s/%d, want SET/2", m.Operation, m.Version)
	}
	if len(m.Index) != 2 || m.Index[0] != appstate.IndexContact || m.Index[1] != target.String() {
		t.Fatalf("index = %v", m.Index)
	}
	action := m.Action.GetContactAction()
	if action.GetFullName() != "Ada Lovelace" || action.FirstName != nil || action.GetLidJID() != lid.String() || !action.GetSaveOnPrimaryAddressbook() {
		t.Fatalf("decoded action = %v", action)
	}
	if ts := m.Action.GetTimestamp(); ts < before {
		t.Fatalf("timestamp = %d, want filled by the encoder (>= %d)", ts, before)
	}
}

// KnownLIDForPN reads only the session's LID map, never the server.
func TestKnownLIDForPNReadsSessionLIDMap(t *testing.T) {
	c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: newTestKeyStateStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	ctx := context.Background()
	pn := types.NewJID("15550001111", types.DefaultUserServer)
	lid := types.NewJID("123456789012345", types.HiddenUserServer)
	if err := c.client.Store.LIDs.PutLIDMapping(ctx, lid, pn); err != nil {
		t.Fatal(err)
	}
	pnAD := pn
	pnAD.Device = 4
	if got := c.KnownLIDForPN(ctx, pnAD); got != lid {
		t.Fatalf("KnownLIDForPN(mapped) = %s, want %s", got, lid)
	}
	if got := c.KnownLIDForPN(ctx, types.NewJID("15550002222", types.DefaultUserServer)); !got.IsEmpty() {
		t.Fatalf("KnownLIDForPN(unmapped) = %s, want empty", got)
	}
	if got := c.KnownLIDForPN(ctx, lid); !got.IsEmpty() {
		t.Fatalf("KnownLIDForPN(lid) = %s, want empty", got)
	}
}
