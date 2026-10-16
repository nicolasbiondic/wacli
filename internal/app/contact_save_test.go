package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func registeredChecker(jid types.JID) func([]string) ([]types.IsOnWhatsAppResponse, error) {
	return func(phones []string) ([]types.IsOnWhatsAppResponse, error) {
		return []types.IsOnWhatsAppResponse{{Query: "+" + jid.User, JID: jid, IsIn: true}}, nil
	}
}

// lidRegisteredChecker answers like whatsmeow's IsOnWhatsApp, which queries
// with LID addressing: JID is the LID and PhoneNumber the canonical PN.
func lidRegisteredChecker(query string, pn, lid types.JID) func([]string) ([]types.IsOnWhatsAppResponse, error) {
	return func(phones []string) ([]types.IsOnWhatsAppResponse, error) {
		return []types.IsOnWhatsAppResponse{{Query: query, JID: lid, PhoneNumber: pn, IsIn: true}}, nil
	}
}

// rawContactNames reads the stored name columns directly: the display name
// precedence in store.Contact could hide which column a write filled.
func rawContactNames(t *testing.T, a *App, jid string) (fullName, firstName, phone string) {
	t.Helper()
	raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var full, first, ph sql.NullString
	if err := raw.QueryRow(`SELECT full_name, first_name, phone FROM contacts WHERE jid = ?`, jid).Scan(&full, &first, &ph); err != nil {
		t.Fatalf("read contact %s: %v", jid, err)
	}
	return full.String, first.String, ph.String
}

func TestSaveContactWritesContactCollectionAndMirrorsLocally(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.NewJID("15550001111", types.DefaultUserServer)
	lid := types.NewJID("987654321098765", types.HiddenUserServer)
	f.lids[lid] = target
	f.isOnWhatsApp = registeredChecker(target)

	saved, err := a.SaveContact(context.Background(), target, "  Ada Lovelace ", "")
	if err != nil {
		t.Fatalf("SaveContact: %v", err)
	}
	if saved.JID != target || saved.LID != lid || saved.FullName != "Ada Lovelace" || saved.FirstName != "" {
		t.Fatalf("saved = %+v, want trimmed name, PN target and known LID", saved)
	}
	if !saved.Requested.IsEmpty() {
		t.Fatalf("saved = %+v, want no requested rewrite", saved)
	}

	f.mu.Lock()
	calls := append([]fakeSaveContactCall(nil), f.saveContactCalls...)
	fetches := append([]fakeAppStateFetch(nil), f.appStateFetches...)
	checks := append([][]string(nil), f.isOnWhatsAppCalls...)
	f.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("save calls = %+v, want one", calls)
	}
	if calls[0].target != target || calls[0].fullName != "Ada Lovelace" || calls[0].firstName != "" || calls[0].lid != lid {
		t.Fatalf("save call = %+v, want PN target, trimmed full name, no derived first name, known LID", calls[0])
	}
	if len(fetches) != 1 || fetches[0].name != string(appstate.WAPatchCriticalUnblockLow) || fetches[0].fullSync {
		t.Fatalf("pre-write fetches = %+v, want one incremental critical_unblock_low sync", fetches)
	}
	if len(checks) != 1 || len(checks[0]) != 1 || checks[0][0] != "+15550001111" {
		t.Fatalf("registration checks = %v, want one lookup of +15550001111", checks)
	}

	full, first, phone := rawContactNames(t, a, target.String())
	if full != "Ada Lovelace" || first != "" || phone != "15550001111" {
		t.Fatalf("stored names = %q/%q phone=%q, want the saved full name only", full, first, phone)
	}
	c, err := a.db.GetContact(target.String())
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Ada Lovelace" {
		t.Fatalf("display name = %q, want saved name", c.Name)
	}
	// The write's post-send intent stays durable, like chat-state writes, so a
	// process exit before whatsmeow dispatches the post-send batch is replayed.
	required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchCriticalUnblockLow))
	if err != nil {
		t.Fatal(err)
	}
	if !required {
		t.Fatal("post-send recovery intent for critical_unblock_low was dropped")
	}
}

func TestSaveContactSendsExplicitFirstName(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.NewJID("15550001111", types.DefaultUserServer)
	f.isOnWhatsApp = registeredChecker(target)

	saved, err := a.SaveContact(context.Background(), target, "Ada Lovelace", " Ada ")
	if err != nil {
		t.Fatalf("SaveContact: %v", err)
	}
	if saved.FirstName != "Ada" || !saved.LID.IsEmpty() {
		t.Fatalf("saved = %+v, want trimmed first name and no LID without a mapping", saved)
	}
	f.mu.Lock()
	call := f.saveContactCalls[0]
	f.mu.Unlock()
	if call.firstName != "Ada" || !call.lid.IsEmpty() {
		t.Fatalf("save call = %+v", call)
	}
	full, first, _ := rawContactNames(t, a, target.String())
	if full != "Ada Lovelace" || first != "Ada" {
		t.Fatalf("stored names = %q/%q", full, first)
	}
}

func TestSaveContactRenamesExistingEntry(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.NewJID("15550001111", types.DefaultUserServer)
	f.isOnWhatsApp = registeredChecker(target)
	if err := a.db.UpsertContact(target.String(), target.User, "Push", "Old Name", "Old", "Biz"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveContact(context.Background(), target, "New Name", ""); err != nil {
		t.Fatalf("SaveContact: %v", err)
	}
	// The action is authoritative for both names, like whatsmeow's
	// PutContactName in session.db: the old first name must not survive.
	full, first, _ := rawContactNames(t, a, target.String())
	if full != "New Name" || first != "" {
		t.Fatalf("stored names = %q/%q, want renamed full name and cleared first name", full, first)
	}
	c, err := a.db.GetContact(target.String())
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "New Name" {
		t.Fatalf("display name = %q", c.Name)
	}
	if found, err := a.db.SearchContacts("Old", 10); err != nil || len(found) != 0 {
		t.Fatalf("search for the replaced names = %+v, %v; want no match", found, err)
	}
	raw, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var push, biz string
	if err := raw.QueryRow(`SELECT push_name, business_name FROM contacts WHERE jid = ?`, target.String()).Scan(&push, &biz); err != nil || push != "Push" || biz != "Biz" {
		t.Fatalf("push/business = %q/%q, %v; want untouched", push, biz, err)
	}
}

// IsOnWhatsApp queries with LID addressing, so the answer carries the LID as
// JID and the canonical phone number apart. The save must be indexed by that
// phone number even when it differs from the typed one (regional rewrites),
// and the LID must come from the answer when the session had no mapping.
func TestSaveContactIndexesByServerCanonicalPhoneNumber(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	typed := types.NewJID("15550001111", types.DefaultUserServer)
	canonical := types.NewJID("15550009999", types.DefaultUserServer)
	lid := types.NewJID("987654321098765", types.HiddenUserServer)
	canonicalAD := canonical
	canonicalAD.Device = 3
	f.isOnWhatsApp = lidRegisteredChecker("+"+typed.User, canonicalAD, lid)

	saved, err := a.SaveContact(context.Background(), typed, "Ada Lovelace", "")
	if err != nil {
		t.Fatalf("SaveContact: %v", err)
	}
	if saved.JID != canonical || saved.LID != lid || saved.Requested != typed {
		t.Fatalf("saved = %+v, want canonical PN, LID from the answer, typed number as requested", saved)
	}
	f.mu.Lock()
	calls := append([]fakeSaveContactCall(nil), f.saveContactCalls...)
	f.mu.Unlock()
	if len(calls) != 1 || calls[0].target != canonical || calls[0].lid != lid {
		t.Fatalf("save calls = %+v, want one indexed by the canonical PN with the answered LID", calls)
	}
	full, _, phone := rawContactNames(t, a, canonical.String())
	if full != "Ada Lovelace" || phone != canonical.User {
		t.Fatalf("canonical row = %q phone=%q", full, phone)
	}
	if _, err := a.db.GetContact(typed.String()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("typed-number row created: %v", err)
	}
}

func TestSaveContactUsesAnsweredLIDWithoutRewrite(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.NewJID("15550001111", types.DefaultUserServer)
	lid := types.NewJID("987654321098765", types.HiddenUserServer)
	f.isOnWhatsApp = lidRegisteredChecker("+"+target.User, target, lid)

	saved, err := a.SaveContact(context.Background(), target, "Ada", "")
	if err != nil {
		t.Fatalf("SaveContact: %v", err)
	}
	if saved.JID != target || saved.LID != lid || !saved.Requested.IsEmpty() {
		t.Fatalf("saved = %+v, want typed PN, answered LID, no requested rewrite", saved)
	}
}

// Events the post-send fetch returns are persisted in order after the local
// mirror, through the same contact handler as live events.
func TestSaveContactPersistsPostSendContactEvents(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.NewJID("15550001111", types.DefaultUserServer)
	other := types.NewJID("15550003333", types.DefaultUserServer)
	f.isOnWhatsApp = registeredChecker(target)
	f.saveContactEvent = func() any {
		return &events.Contact{
			JID:    other,
			Action: &waSyncAction.ContactAction{FullName: proto.String("Edited On Phone")},
		}
	}
	if _, err := a.SaveContact(context.Background(), target, "Ada", ""); err != nil {
		t.Fatalf("SaveContact: %v", err)
	}
	if full, _, _ := rawContactNames(t, a, target.String()); full != "Ada" {
		t.Fatalf("saved row = %q", full)
	}
	if full, _, phone := rawContactNames(t, a, other.String()); full != "Edited On Phone" || phone != other.User {
		t.Fatalf("post-send event row = %q phone=%q", full, phone)
	}
}

func TestSaveContactMapsKnownLIDToPhoneNumber(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.NewJID("15550001111", types.DefaultUserServer)
	lid := types.NewJID("987654321098765", types.HiddenUserServer)
	f.lids[lid] = target
	f.isOnWhatsApp = registeredChecker(target)

	saved, err := a.SaveContact(context.Background(), lid, "Ada Lovelace", "")
	if err != nil {
		t.Fatalf("SaveContact: %v", err)
	}
	if saved.JID != target || saved.LID != lid {
		t.Fatalf("saved = %+v, want the mapped PN as target", saved)
	}
	f.mu.Lock()
	call := f.saveContactCalls[0]
	f.mu.Unlock()
	if call.target != target {
		t.Fatalf("index target = %s, want PN %s", call.target, target)
	}
}

func TestSaveContactRejectsUnmappedLIDAndNonUserTargets(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	for _, jid := range []types.JID{
		types.NewJID("987654321098765", types.HiddenUserServer),
		types.NewJID("120363000000000000", types.GroupServer),
	} {
		if _, err := a.SaveContact(context.Background(), jid, "Ada", ""); err == nil || !strings.Contains(err.Error(), "needs a phone number") {
			t.Fatalf("SaveContact(%s) error = %v, want phone number requirement", jid, err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.saveContactCalls) != 0 || len(f.isOnWhatsAppCalls) != 0 || len(f.appStateFetches) != 0 {
		t.Fatalf("rejected targets reached the network: saves=%d checks=%d fetches=%d", len(f.saveContactCalls), len(f.isOnWhatsAppCalls), len(f.appStateFetches))
	}
}

func TestSaveContactRejectsConfirmedUnregisteredNumber(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.NewJID("15550001111", types.DefaultUserServer)
	f.isOnWhatsApp = func(phones []string) ([]types.IsOnWhatsAppResponse, error) {
		return []types.IsOnWhatsAppResponse{{Query: "+15550001111", IsIn: false}}, nil
	}
	_, err := a.SaveContact(context.Background(), target, "Ada", "")
	if !errors.Is(err, ErrContactNotOnWhatsApp) {
		t.Fatalf("error = %v, want ErrContactNotOnWhatsApp", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.saveContactCalls) != 0 || len(f.appStateFetches) != 0 {
		t.Fatal("unregistered number still reached the app-state write")
	}
	if _, err := a.db.GetContact(target.String()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("local contact row after rejected save: %v", err)
	}
}

// Without an answer there is no confirmed registration and no canonical
// phone number to index by, so the save fails closed before any write.
func TestSaveContactRejectsUnansweredNumber(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.NewJID("15550001111", types.DefaultUserServer)
	for name, answer := range map[string][]types.IsOnWhatsAppResponse{
		"no entries":   nil,
		"other number": {{Query: "+15550002222", JID: types.NewJID("15550002222", types.DefaultUserServer), IsIn: true}},
	} {
		t.Run(name, func(t *testing.T) {
			f.isOnWhatsApp = func([]string) ([]types.IsOnWhatsAppResponse, error) { return answer, nil }
			_, err := a.SaveContact(context.Background(), target, "Ada", "")
			if !errors.Is(err, ErrContactRegistrationUnknown) {
				t.Fatalf("error = %v, want ErrContactRegistrationUnknown", err)
			}
		})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.saveContactCalls) != 0 || len(f.appStateFetches) != 0 {
		t.Fatal("unanswered number still reached the app-state write")
	}
	if _, err := a.db.GetContact(target.String()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("local contact row after rejected save: %v", err)
	}
}

func TestSaveContactFailsClosedOnRegistrationLookupError(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.NewJID("15550001111", types.DefaultUserServer)
	f.isOnWhatsApp = func([]string) ([]types.IsOnWhatsAppResponse, error) { return nil, errors.New("usync timeout") }
	if _, err := a.SaveContact(context.Background(), target, "Ada", ""); err == nil || !strings.Contains(err.Error(), "usync timeout") {
		t.Fatalf("error = %v, want lookup failure", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.saveContactCalls) != 0 {
		t.Fatal("lookup failure still wrote app state")
	}
}

func TestSaveContactValidatesNames(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.NewJID("15550001111", types.DefaultUserServer)
	tests := []struct {
		name      string
		full      string
		first     string
		wantError string
	}{
		{name: "empty", full: "   ", wantError: "--name requires a nonempty name"},
		{name: "too long", full: strings.Repeat("é", MaxContactNameLength+1), wantError: "longer than 255 characters"},
		{name: "line break", full: "Ada\nLovelace", wantError: "control characters"},
		{name: "first name control", full: "Ada", first: "A\tda", wantError: "--first-name must not contain"},
		{name: "first name too long", full: "Ada", first: strings.Repeat("a", MaxContactNameLength+1), wantError: "--first-name is longer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := a.SaveContact(context.Background(), target, tc.full, tc.first)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
		})
	}
	// A name of exactly the limit is accepted.
	f.isOnWhatsApp = registeredChecker(target)
	if _, err := a.SaveContact(context.Background(), target, strings.Repeat("é", MaxContactNameLength), ""); err != nil {
		t.Fatalf("limit-length name rejected: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.saveContactCalls) != 1 {
		t.Fatalf("save calls = %d, want only the valid name", len(f.saveContactCalls))
	}
}

func TestSaveContactRecoversCollectionBeforeWrite(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.NewJID("15550001111", types.DefaultUserServer)
	f.isOnWhatsApp = registeredChecker(target)
	f.appStateFetchErrs = []error{appstate.ErrMismatchingLTHash, nil}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := a.SaveContact(ctx, target, "Ada", ""); err != nil {
		t.Fatalf("SaveContact: %v", err)
	}
	f.mu.Lock()
	fetches := append([]fakeAppStateFetch(nil), f.appStateFetches...)
	f.mu.Unlock()
	if len(fetches) != 2 || fetches[0].fullSync || !fetches[1].fullSync || fetches[1].name != string(appstate.WAPatchCriticalUnblockLow) {
		t.Fatalf("fetches = %+v, want incremental then full replay of critical_unblock_low", fetches)
	}
}

func TestSaveContactSendFailureKeepsLocalStoreUnchanged(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.NewJID("15550001111", types.DefaultUserServer)
	f.isOnWhatsApp = registeredChecker(target)
	f.saveContactErr = errors.New("server rejected patch")
	if _, err := a.SaveContact(context.Background(), target, "Ada", ""); err == nil || !strings.Contains(err.Error(), "server rejected patch") {
		t.Fatalf("error = %v", err)
	}
	if _, err := a.db.GetContact(target.String()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("local contact after failed send: %v", err)
	}
}

// contactEventRegistrations installs the production handler that receives
// whatsmeow events: the `sync --follow` handler or the one-shot persistence
// handler used by write commands.
var contactEventRegistrations = map[string]func(t *testing.T, a *App, f *fakeWA){
	"sync follow handler": func(t *testing.T, a *App, f *fakeWA) {
		installBoundaryReplayHandler(t, a, f)
	},
	"chat state persistence handler": func(t *testing.T, a *App, f *fakeWA) {
		remove, err := a.AddChatStatePersistenceHandler(t.Context())
		if err != nil {
			t.Fatalf("AddChatStatePersistenceHandler: %v", err)
		}
		t.Cleanup(remove)
	},
}

// Contact edits made on the phone or another linked device, and the echo of
// our own save, reach wacli.db through the production event handlers.
func TestContactEventPersistsThroughProductionHandlers(t *testing.T) {
	if got := appStateCollectionsForEvent(&events.Contact{}); len(got) != 1 || got[0] != appstate.WAPatchCriticalUnblockLow {
		t.Fatalf("collections for events.Contact = %v", got)
	}
	for name, install := range contactEventRegistrations {
		t.Run(name, func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			target := types.NewJID("15550002222", types.DefaultUserServer)
			lid := types.NewJID("111222333444555", types.HiddenUserServer)
			f.lids[lid] = target
			if err := a.db.UpsertContact(target.String(), target.User, "Push", "", "", ""); err != nil {
				t.Fatal(err)
			}
			install(t, a, f)

			// A LID-indexed echo is stored under the mapped phone-number row.
			f.emit(&events.Contact{
				JID:    lid,
				Action: &waSyncAction.ContactAction{FullName: proto.String("Grace Hopper"), FirstName: proto.String("Grace")},
			})
			if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
				t.Fatal(err)
			}
			full, first, phone := rawContactNames(t, a, target.String())
			if full != "Grace Hopper" || first != "Grace" || phone != target.User {
				t.Fatalf("stored = %q/%q phone=%q", full, first, phone)
			}
			required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchCriticalUnblockLow))
			if err != nil {
				t.Fatal(err)
			}
			if required {
				t.Fatal("live contact event left its recovery marker behind")
			}

			// A rename on the phone without a first name clears the old one,
			// as whatsmeow does in session.db.
			f.emit(&events.Contact{JID: target, Action: &waSyncAction.ContactAction{FullName: proto.String("Rear Admiral Hopper")}})
			if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
				t.Fatal(err)
			}
			full, first, _ = rawContactNames(t, a, target.String())
			if full != "Rear Admiral Hopper" || first != "" {
				t.Fatalf("after phone rename = %q/%q, want new full name and no first name", full, first)
			}
			c, err := a.db.GetContact(target.String())
			if err != nil || c.Name != "Rear Admiral Hopper" {
				t.Fatalf("display = %+v, %v", c, err)
			}
		})
	}
}

func TestContactEventIgnoresNonUserAndMalformedEvents(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	target := types.NewJID("15550002222", types.DefaultUserServer)
	if err := a.db.UpsertContact(target.String(), target.User, "", "Grace Hopper", "Grace", ""); err != nil {
		t.Fatal(err)
	}
	for _, evt := range []*events.Contact{
		nil,
		{JID: target},
		{JID: types.NewJID("120363000000000000", types.GroupServer), Action: &waSyncAction.ContactAction{FullName: proto.String("Group")}},
		{JID: types.NewJID("15550004444", types.DefaultUserServer), Action: &waSyncAction.ContactAction{}},
	} {
		if err := a.handleContactEvent(context.Background(), evt); err != nil {
			t.Fatalf("handleContactEvent(%+v) = %v", evt, err)
		}
	}
	if full, first, _ := rawContactNames(t, a, target.String()); full != "Grace Hopper" || first != "Grace" {
		t.Fatalf("event without action changed names: %q/%q", full, first)
	}
	for _, jid := range []string{"120363000000000000@g.us", "15550004444@s.whatsapp.net"} {
		if _, err := a.db.GetContact(jid); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("contact row %s created: %v", jid, err)
		}
	}

	// An action without names clears the existing names, like whatsmeow.
	if err := a.handleContactEvent(context.Background(), &events.Contact{JID: target, Action: &waSyncAction.ContactAction{}}); err != nil {
		t.Fatal(err)
	}
	if full, first, _ := rawContactNames(t, a, target.String()); full != "" || first != "" {
		t.Fatalf("empty action left names %q/%q", full, first)
	}
}
