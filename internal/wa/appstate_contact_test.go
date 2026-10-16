package wa

import (
	"encoding/json"
	"testing"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
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
