package app

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/appstate/lthash"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type rotatingContactSDKWA struct {
	*sdkSessionWA
	sendPatch func(context.Context, appstate.PatchInfo) ([]any, error)
}

func (w *rotatingContactSDKWA) SendAppStatePatch(ctx context.Context, patch appstate.PatchInfo, beforeApply func()) ([]any, error) {
	beforeApply()
	return w.sendPatch(ctx, patch)
}

// Exercise App.SaveContact with the fork's actual expansion, encoder, MAC
// verification, SQLStore and journal. Only the server exchange is replaced by
// its encoded echo. Multiple mutations in one patch must count as one cursor.
func TestSaveContactRotationCleanupKeepsCoverageAndNames(t *testing.T) {
	for _, scenario := range []string{"rotated PN", "latest PN", "new contact", "distinct stale LID", "rotated PN and distinct LID"} {
		t.Run(scenario, func(t *testing.T) {
			a, f := newContactSaveTestApp(t)
			f.hasLocalAppState = true
			oldKey, latestKey := []byte{1, 2, 3}, []byte{4, 5, 6}
			device, sdk, proc := newSDKSession(t, f, oldKey, 80)
			if err := device.AppStateKeys.PutAppStateSyncKey(t.Context(), latestKey, waStore.AppStateSyncKey{Data: bytes.Repeat([]byte{8}, 32), Fingerprint: []byte{2}, Timestamp: 2}); err != nil {
				t.Fatal(err)
			}
			state := appstate.HashState{Version: 80}
			encode := func(ctx context.Context, key []byte, patch appstate.PatchInfo) *waServerSync.SyncdPatch {
				t.Helper()
				raw, err := proc.EncodePatch(ctx, key, state, patch)
				if err != nil {
					t.Fatal(err)
				}
				encoded := &waServerSync.SyncdPatch{}
				if err := proto.Unmarshal(raw, encoded); err != nil {
					t.Fatal(err)
				}
				encoded.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(state.Version + 1)}
				return encoded
			}
			apply := func(ctx context.Context, patch *waServerSync.SyncdPatch) ([]any, error) {
				var evts []any
				next, err := sdk.DangerousInternals().ApplyAppStatePatches(ctx, contactCollection, state, &appstate.PatchList{Name: contactCollection, Patches: []*waServerSync.SyncdPatch{patch}}, false, &evts)
				if err == nil {
					state = next
				}
				return evts, err
			}
			seed := func(lid bool, key []byte) []byte {
				t.Helper()
				patch, err := wa.BuildSaveContact(testContactPN, testContactLID, "Old Name", "Old", false)
				if err != nil {
					t.Fatal(err)
				}
				if lid {
					patch.Mutations[0].Index[1] = testContactLID.String()
				}
				encoded := encode(t.Context(), key, patch)
				if _, err := apply(t.Context(), encoded); err != nil {
					t.Fatal(err)
				}
				return encoded.Mutations[0].Record.Index.Blob
			}
			var stalePNIndex, staleLIDIndex []byte
			switch scenario {
			case "rotated PN", "rotated PN and distinct LID":
				stalePNIndex = seed(false, oldKey)
			case "latest PN":
				seed(false, latestKey)
			}
			if scenario == "distinct stale LID" || scenario == "rotated PN and distinct LID" {
				staleLIDIndex = seed(true, oldKey)
			}
			if _, err := a.drainContactJournal(t.Context()); err != nil {
				t.Fatal(err)
			}
			w := &rotatingContactSDKWA{sdkSessionWA: &sdkSessionWA{fakeWA: f, device: device}}
			a.wa = w
			w.sendPatch = func(ctx context.Context, patch appstate.PatchInfo) ([]any, error) {
				start := f.journal.Mark(string(contactCollection))
				expanded, err := proc.ExpandPatchForLID(ctx, patch)
				if err != nil {
					return nil, err
				}
				wantMutations := 1
				if stalePNIndex != nil {
					wantMutations = 2
				}
				if len(patch.Mutations) != 1 || patch.Mutations[0].Index[1] != testContactPN.String() || len(expanded.Mutations) != wantMutations {
					return nil, fmt.Errorf("wrong outgoing mutation shape: expected %d mutations, got %d", wantMutations, len(expanded.Mutations))
				}
				encoded := encode(ctx, latestKey, expanded)
				if len(encoded.Mutations) != wantMutations || encoded.Mutations[0].GetOperation() != waServerSync.SyncdMutation_SET {
					return nil, fmt.Errorf("encoder changed outgoing SET/REMOVE shape")
				}
				if stalePNIndex != nil {
					remove := encoded.Mutations[1]
					if remove.GetOperation() != waServerSync.SyncdMutation_REMOVE || !bytes.Equal(remove.GetRecord().GetKeyID().GetID(), oldKey) || !bytes.Equal(remove.GetRecord().GetIndex().GetBlob(), stalePNIndex) {
						return nil, fmt.Errorf("encoded REMOVE did not target exactly the old-key contact index")
					}
				}
				evts, err := apply(ctx, encoded)
				if err != nil {
					return nil, err
				}
				covered, ownWritten, end := f.journal.Coverage(string(contactCollection), testContactPN, start)
				writes, _ := f.journal.Snapshot()
				if !covered || !ownWritten || end.CursorWrites-start.CursorWrites != 1 || len(writes) != 1 || writes[0].JID != testContactPN || writes[0].FullName != "New Name" {
					return evts, fmt.Errorf("one SET+REMOVE patch must remain covered with one name write: covered=%v own=%v writes=%+v", covered, ownWritten, writes)
				}
				contacts := 0
				for _, evt := range evts {
					if contact, ok := evt.(*events.Contact); ok {
						contacts++
						if contact.Action.GetFullName() != "New Name" {
							return evts, fmt.Errorf("REMOVE emitted a contact clear")
						}
					}
				}
				if contacts != 1 {
					return evts, fmt.Errorf("contact events = %d, want one SET only", contacts)
				}
				return evts, nil
			}
			if _, err := a.SaveContact(t.Context(), testContactPN, "New Name", "New", false); err != nil {
				t.Fatal(err)
			}
			if contactRecoveryRequired(t, a) || storedContact(t, a, testContactPN).full != "New Name" {
				t.Fatal("normal rotation save did not settle its own intent and mirror its name")
			}
			if stalePNIndex != nil {
				if mac, err := device.AppState.GetAppStateMutationMAC(t.Context(), string(contactCollection), stalePNIndex); err != nil || mac != nil {
					t.Fatalf("old-key row survived removal: %x %v", mac, err)
				}
			}
			if staleLIDIndex != nil {
				mac, err := device.AppState.GetAppStateMutationMAC(t.Context(), string(contactCollection), staleLIDIndex)
				contact, contactErr := device.Contacts.GetContact(t.Context(), testContactLID)
				if err != nil || contactErr != nil || mac == nil || contact.FullName != "Old Name" {
					t.Fatal("PN-key rotation cleanup incorrectly deleted or cleared a distinct LID entry")
				}
			}
			reader := device.AppState.(interface {
				GetAllAppStateMutationMACs(context.Context, string) ([]waStore.AppStateMutationMAC, error)
			})
			macs, err := reader.GetAllAppStateMutationMACs(t.Context(), string(contactCollection))
			if err != nil {
				t.Fatal(err)
			}
			var values [][]byte
			for _, mac := range macs {
				values = append(values, mac.ValueMAC)
			}
			var hash [128]byte
			lthash.WAPatchIntegrity.SubtractThenAddInPlace(hash[:], nil, values)
			version, storedHash, err := device.AppState.GetAppStateVersion(t.Context(), string(contactCollection))
			if err != nil || version != state.Version || storedHash != state.Hash || storedHash != hash {
				t.Fatal("MAC-verified decoder, stored cursor/hash and current MAC rows disagree")
			}
		})
	}
}
