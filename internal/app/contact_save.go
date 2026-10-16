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

// MaxContactNameLength bounds each saved name in characters. WhatsApp clients
// accept long names, but device address books truncate well before this.
const MaxContactNameLength = 255

// SavedContact reports the address book entry that was written.
type SavedContact struct {
	JID       types.JID
	LID       types.JID
	FullName  string
	FirstName string
	// Registered and Responded carry the pre-write registration lookup:
	// Responded=false means the server did not answer for the number, which
	// is unknown rather than a confirmed negative.
	Registered bool
	Responded  bool
}

// ErrContactNotOnWhatsApp is returned when the server confirms the number is
// not registered, so no address book entry is written.
var ErrContactNotOnWhatsApp = errors.New("number is not registered on WhatsApp")

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
// through the local session and rejected when no mapping is known. The known
// LID is included so the phone links both identities. An empty first name is
// omitted from the mutation (WhatsApp Web sends only what was entered; the
// receiving side derives a short name itself).
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
	target := canonicalJID(a.wa.ResolveLIDToPN(ctx, jid))
	if target.Server != types.DefaultUserServer || target.User == "" {
		return SavedContact{}, fmt.Errorf("contacts save needs a phone number or user JID, got %s", jid)
	}
	saved := SavedContact{JID: target, FullName: fullName, FirstName: firstName}
	if saved.Registered, saved.Responded, err = a.contactRegistered(ctx, target); err != nil {
		return SavedContact{}, err
	}
	if saved.Responded && !saved.Registered {
		return SavedContact{}, fmt.Errorf("%w: +%s", ErrContactNotOnWhatsApp, target.User)
	}

	release, err := a.beginChatStateWrite(ctx, appstate.WAPatchCriticalUnblockLow)
	if err != nil {
		return SavedContact{}, err
	}
	defer release()
	saved.LID = a.wa.KnownLIDForPN(ctx, target)
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
		return a.db.UpsertContact(target.String(), target.User, "", fullName, firstName, "")
	})
	if err != nil {
		return SavedContact{}, err
	}
	return saved, nil
}

// contactRegistered asks the server whether the number is on WhatsApp before
// an address book entry is written for it. The server may omit a number from
// its answer; that is reported as responded=false, not as unregistered.
func (a *App) contactRegistered(ctx context.Context, target types.JID) (registered, responded bool, err error) {
	query := "+" + target.User
	resps, err := a.wa.IsOnWhatsApp(ctx, []string{query})
	if err != nil {
		return false, false, fmt.Errorf("check WhatsApp registration for %s: %w", query, err)
	}
	for _, r := range resps {
		if r.Query == query || (!r.JID.IsEmpty() && r.JID.ToNonAD().User == target.User) {
			return r.IsIn, true, nil
		}
	}
	return false, false, nil
}

// handleContactEvent mirrors a `contact` app-state mutation (an echo of our
// own save or an edit made on the phone or another linked device) into
// wacli.db, so `contacts show` matches the whatsmeow contact store without a
// `contacts refresh`.
func (a *App) handleContactEvent(ctx context.Context, evt *events.Contact) error {
	if evt == nil || evt.JID.IsEmpty() || evt.Action == nil {
		return nil
	}
	fullName := strings.TrimSpace(evt.Action.GetFullName())
	firstName := strings.TrimSpace(evt.Action.GetFirstName())
	if fullName == "" && firstName == "" {
		return nil
	}
	contact := a.canonicalStoreJID(ctx, evt.JID)
	if contact.Server != types.DefaultUserServer && contact.Server != types.HiddenUserServer {
		return nil
	}
	phone := ""
	if contact.Server == types.DefaultUserServer {
		phone = contact.User
	}
	if err := a.db.UpsertContact(contact.String(), phone, "", fullName, firstName, ""); err != nil {
		a.emitWarning(
			"contact_store_failed",
			fmt.Sprintf("warning: failed to store contact name for %s: %v", evt.JID, err),
			map[string]any{"jid": evt.JID.String(), "error": err.Error()},
		)
		return err
	}
	return nil
}
