package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// MaxContactNameLength bounds each saved name in characters. It is a local
// safety policy; WhatsApp defines no protocol limit for contact names.
const MaxContactNameLength = 255

// SavedContact reports the address book entry that was written. JID is the
// phone-number JID WhatsApp reported for the number, which can differ from
// the typed one in regions where WhatsApp rewrites numbers; Requested then
// holds the typed number and is empty otherwise.
type SavedContact struct {
	JID       types.JID
	LID       types.JID
	Requested types.JID
	FullName  string
	FirstName string
}

// ErrContactNotOnWhatsApp is returned when the server confirms the number is
// not registered, so no address book entry is written.
var ErrContactNotOnWhatsApp = errors.New("number is not registered on WhatsApp")

// ErrContactRegistrationUnknown is returned when the server does not answer
// for the number. Without a confirmed registration there is no canonical
// phone number to index the entry by, so nothing is written.
var ErrContactRegistrationUnknown = errors.New("WhatsApp did not confirm the number is registered")

// NormalizeContactName trims a saved name and rejects empty, oversized, or
// control-character names before any network write.
func NormalizeContactName(flag, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%s requires a nonempty name", flag)
	}
	if utf8.RuneCountInString(name) > MaxContactNameLength {
		return "", fmt.Errorf("%s is longer than %d characters", flag, MaxContactNameLength)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%s must not contain line breaks or control characters", flag)
		}
	}
	return name, nil
}

// SaveContact saves or renames an address book entry through the
// `critical_unblock_low` app-state collection, with the same pre-write
// synchronization, durable intent, and ordered persistence as chat-state
// writes. The target must resolve to a phone-number JID; a LID is mapped
// through the local session and rejected when no mapping is known. The server
// must confirm the number is registered, and the entry is indexed by the
// phone number it reports (like send recipients, so a regional rewrite of the
// typed number attaches to the real chat). The LID is included so the phone
// links both identities. An empty first name is omitted from the mutation
// (WhatsApp Web sends only what was entered; the receiving side derives a
// short name itself).
func (a *App) SaveContact(ctx context.Context, jid types.JID, fullName, firstName string) (SavedContact, error) {
	fullName, err := NormalizeContactName("--name", fullName)
	if err != nil {
		return SavedContact{}, err
	}
	if strings.TrimSpace(firstName) == "" {
		firstName = ""
	} else if firstName, err = NormalizeContactName("--first-name", firstName); err != nil {
		return SavedContact{}, err
	}
	requested := canonicalJID(a.wa.ResolveLIDToPN(ctx, jid))
	if requested.Server != types.DefaultUserServer || requested.User == "" {
		return SavedContact{}, fmt.Errorf("contacts save needs a phone number or user JID, got %s", jid)
	}
	target, lid, err := a.contactRegistration(ctx, requested)
	if err != nil {
		return SavedContact{}, err
	}
	saved := SavedContact{JID: target, LID: lid, FullName: fullName, FirstName: firstName}
	if target != requested {
		saved.Requested = requested
	}

	release, err := a.beginChatStateWrite(ctx, appstate.WAPatchCriticalUnblockLow)
	if err != nil {
		return SavedContact{}, err
	}
	defer release()
	if saved.LID.IsEmpty() {
		saved.LID = a.wa.KnownLIDForPN(ctx, target)
	}
	pending, err := a.beginLocalAppStateWrite(appstate.WAPatchCriticalUnblockLow)
	if err != nil {
		return SavedContact{}, err
	}
	postSendEvents, err := a.wa.SaveContact(ctx, target, fullName, firstName, saved.LID, func() { pending.reserve(a) })
	if err != nil {
		return SavedContact{}, errors.Join(err, a.failLocalAppStateWrite(ctx, &pending, postSendEvents))
	}
	if !pending.reserved {
		return SavedContact{}, fmt.Errorf("WhatsApp app state send completed without an apply boundary")
	}
	err = a.completeLocalAppStateWrite(ctx, &pending, postSendEvents, func() error {
		return a.db.SetContactActionName(target.String(), target.User, fullName, firstName)
	})
	if err != nil {
		return SavedContact{}, err
	}
	return saved, nil
}

// contactRegistration asks the server whether the number is on WhatsApp
// before an address book entry is written for it, and returns the identity
// the server reports. IsOnWhatsApp queries with LID addressing, so the answer
// normally carries the LID as JID and the canonical phone number separately.
// A confirmed negative and a missing answer both stop the write.
func (a *App) contactRegistration(ctx context.Context, requested types.JID) (pn, lid types.JID, err error) {
	query := "+" + requested.User
	resps, err := a.wa.IsOnWhatsApp(ctx, []string{query})
	if err != nil {
		return types.JID{}, types.JID{}, fmt.Errorf("check WhatsApp registration for %s: %w", query, err)
	}
	for _, r := range resps {
		pn, lid = registrationIdentity(r)
		if r.Query != query && pn.User != requested.User {
			continue
		}
		if !r.IsIn {
			return types.JID{}, types.JID{}, fmt.Errorf("%w: %s", ErrContactNotOnWhatsApp, query)
		}
		if pn.IsEmpty() {
			pn = requested
		}
		return pn, lid, nil
	}
	return types.JID{}, types.JID{}, fmt.Errorf("%w: no answer for %s, nothing was saved", ErrContactRegistrationUnknown, query)
}

// registrationIdentity splits a registration answer into its phone-number and
// LID JIDs, whichever of JID and PhoneNumber carries them.
func registrationIdentity(r types.IsOnWhatsAppResponse) (pn, lid types.JID) {
	for _, jid := range []types.JID{r.PhoneNumber, r.JID} {
		jid = jid.ToNonAD()
		if jid.User == "" {
			continue
		}
		switch {
		case jid.Server == types.DefaultUserServer && pn.IsEmpty():
			pn = jid
		case jid.Server == types.HiddenUserServer && lid.IsEmpty():
			lid = jid
		}
	}
	return pn, lid
}

// handleContactEvent mirrors a `contact` app-state mutation (an echo of our
// own save or an edit made on the phone or another linked device) into
// wacli.db, so `contacts show` matches the whatsmeow contact store without a
// `contacts refresh`. The action is authoritative for the full and first
// name, as in whatsmeow's PutContactName: a name the action omits is cleared.
// A LID-indexed mutation is stored under the mapped phone number; without a
// mapping it is stored under the LID, as the whatsmeow store does.
func (a *App) handleContactEvent(ctx context.Context, evt *events.Contact) error {
	// TODO: the pinned whatsmeow emits events.Contact only for SET mutations.
	// Once it reports removals (whatsmeow#1247 adds events.Contact.Removed),
	// clear the names here instead of returning early on a nil Action.
	if evt == nil || evt.JID.IsEmpty() || evt.Action == nil {
		return nil
	}
	fullName := strings.TrimSpace(evt.Action.GetFullName())
	firstName := strings.TrimSpace(evt.Action.GetFirstName())
	contact := a.canonicalStoreJID(ctx, evt.JID)
	if contact.Server != types.DefaultUserServer && contact.Server != types.HiddenUserServer {
		return nil
	}
	phone := ""
	if contact.Server == types.DefaultUserServer {
		phone = contact.User
	}
	if err := a.db.SetContactActionName(contact.String(), phone, fullName, firstName); err != nil {
		a.emitWarning(
			"contact_store_failed",
			fmt.Sprintf("warning: failed to store contact name for %s: %v", evt.JID, err),
			map[string]any{"jid": evt.JID.String(), "error": err.Error()},
		)
		return err
	}
	return nil
}
