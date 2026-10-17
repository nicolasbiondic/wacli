package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
)

// SavedContact reports the account contact that was saved.
type SavedContact struct {
	JID       types.JID
	LID       types.JID
	FullName  string
	FirstName string
	Phonebook bool
}

// SaveContact saves or renames a contact in the WhatsApp account through the
// `critical_unblock_low` app-state collection and mirrors the name into
// wacli.db. target must be a phone-number JID; the contact's LID is resolved
// over the connection and required, because current clients key contacts by
// it and show only the number for a contact saved without it. An empty
// firstName is derived from the first word of fullName.
//
// The write goes through the same coordinator as the chat-state writes: it
// waits for other app-state writes and recovery, synchronizes the collection
// first, records durable recovery debt before the send, and reserves the
// local mirror's place in the persistence order before whatsmeow can advance
// the collection. whatsmeow applies the batch it fetches after the send (our
// echo, a newer edit from the phone, other contacts' edits) to its contact
// store before SendAppState returns and only dispatches the events later, so
// the mirror is written from that store under the reserved ticket: the batch
// is covered even if this process exits before the events are dispatched,
// and the debt is settled here. The events themselves re-read the store when
// they arrive, so a batch dispatched late never regresses the mirror.
func (a *App) SaveContact(ctx context.Context, target types.JID, fullName, firstName string, phonebook bool) (SavedContact, error) {
	lid := a.wa.ResolvePNToLID(ctx, target)
	if lid.Server != types.HiddenUserServer || lid.User == "" {
		return SavedContact{}, fmt.Errorf("could not find the WhatsApp ID (LID) of %s; nothing was saved (is the number on WhatsApp?)", target.User)
	}
	lid = lid.ToNonAD()
	patch, err := wa.BuildSaveContact(target, lid, fullName, firstName, phonebook)
	if err != nil {
		return SavedContact{}, err
	}
	action := patch.Mutations[0].Value.GetContactAction()
	saved := SavedContact{JID: target, LID: lid, FullName: action.GetFullName(), FirstName: action.GetFirstName(), Phonebook: phonebook}

	release, err := a.beginChatStateWrite(ctx, appstate.WAPatchCriticalUnblockLow)
	if err != nil {
		return SavedContact{}, err
	}
	defer release()
	pending, err := a.beginLocalAppStateWrite(appstate.WAPatchCriticalUnblockLow)
	if err != nil {
		return SavedContact{}, err
	}
	postSendEvents, err := a.wa.SaveContact(ctx, target, lid, saved.FullName, saved.FirstName, phonebook, func() { pending.reserve(a) })
	if err != nil {
		return SavedContact{}, errors.Join(fmt.Errorf("save contact: %w", err), a.failLocalAppStateWrite(ctx, &pending, postSendEvents))
	}
	if !pending.reserved {
		return SavedContact{}, fmt.Errorf("WhatsApp app state send completed without an apply boundary")
	}
	persistCtx := context.WithoutCancel(ctx)
	var mirrorErr error
	err = a.completeLocalAppStateWrite(ctx, &pending, postSendEvents, func() error {
		if err := a.db.SetContactActionName(target.String(), target.User, saved.FullName, saved.FirstName); err != nil {
			return err
		}
		mirrorErr = a.mirrorContactsFromSessionStore(persistCtx)
		return nil
	})
	if err != nil {
		return SavedContact{}, fmt.Errorf("contact saved on WhatsApp, but updating the local index failed: %w", err)
	}
	if mirrorErr != nil {
		// The debt recorded before the send stays; the next start or write
		// settles it from the session store.
		a.warnAppStateRecovery(string(appstate.WAPatchCriticalUnblockLow), fmt.Errorf("mirror the contact batch after the save: %w", mirrorErr))
		return saved, nil
	}
	if _, err := a.db.ClearAppStateRecoveryGeneration(string(appstate.WAPatchCriticalUnblockLow), pending.generation); err != nil {
		a.warnAppStateRecovery(string(appstate.WAPatchCriticalUnblockLow), fmt.Errorf("clear the settled contact debt: %w", err))
	}
	return saved, nil
}

// sessionContactName returns the names whatsmeow's contact store holds for
// jid, the key a `contact` mutation is indexed by. whatsmeow writes that store
// when it applies the mutation, before it dispatches the event (and, for the
// batches fetched by SendAppState, before SendAppState returns), so it always
// holds the newest applied name. ok is false when the store has no row or
// cannot be read, in which case the caller falls back to the event payload.
func (a *App) sessionContactName(ctx context.Context, jid types.JID) (fullName, firstName string, ok bool) {
	info, err := a.wa.GetContact(ctx, jid)
	if err != nil || !info.Found {
		return "", "", false
	}
	return strings.TrimSpace(info.FullName), strings.TrimSpace(info.FirstName), true
}

// mirrorContactsFromSessionStore rewrites the contact names in wacli.db from
// whatsmeow's contact store, the SDK's own state for `critical_unblock_low`,
// which holds every batch whatsmeow consumed, including batches whose events
// never reached wacli.db. Only names that differ are written, in one
// transaction. Session rows without any name are left alone: they cannot be
// told apart from contacts that were never saved, and `contacts refresh`
// never clears names either. When two session rows (a LID and the phone
// number it maps to) disagree about one contact, that contact is left as the
// events established it, since the store keeps no order between them.
//
// Must run inside a persistence task, so that it is ordered with the contact
// events whatsmeow dispatches: those re-read the store too, so whichever runs
// last sees the newer state.
func (a *App) mirrorContactsFromSessionStore(ctx context.Context) error {
	session, err := a.wa.GetAllContacts(ctx)
	if err != nil {
		return fmt.Errorf("read session contact store: %w", err)
	}
	local, err := a.db.ContactActionNames()
	if err != nil {
		return fmt.Errorf("read local contact names: %w", err)
	}
	changed := map[string]store.ContactActionName{}
	conflicting := map[string]bool{}
	for jid, info := range session {
		fullName, firstName := strings.TrimSpace(info.FullName), strings.TrimSpace(info.FirstName)
		if fullName == "" && firstName == "" {
			continue
		}
		contact := a.canonicalStoreJID(ctx, jid)
		if contact.Server != types.DefaultUserServer && contact.Server != types.HiddenUserServer {
			continue
		}
		key := contact.String()
		if previous, seen := changed[key]; seen && (previous.FullName != fullName || previous.FirstName != firstName) {
			conflicting[key] = true
			continue
		}
		phone := ""
		if contact.Server == types.DefaultUserServer {
			phone = contact.User
		}
		changed[key] = store.ContactActionName{JID: key, Phone: phone, FullName: fullName, FirstName: firstName}
	}
	names := make([]store.ContactActionName, 0, len(changed))
	for key, name := range changed {
		if conflicting[key] {
			continue
		}
		if current, ok := local[key]; ok && current.FullName == name.FullName && current.FirstName == name.FirstName {
			continue
		}
		names = append(names, name)
	}
	slices.SortFunc(names, func(x, y store.ContactActionName) int { return strings.Compare(x.JID, y.JID) })
	if err := a.db.SetContactActionNames(names); err != nil {
		return fmt.Errorf("write %d contact names: %w", len(names), err)
	}
	return nil
}

// settleContactDebtFromSessionStore repairs `critical_unblock_low` replay debt
// without the phone: the collection is caught up incrementally, the session
// contact store is mirrored under a persistence ticket, and the debt up to
// generation is cleared once both succeeded. The pinned whatsmeow ignores a
// recovery snapshot whose version is not newer than the session cursor, so a
// snapshot could never cover a batch the session already consumed; the store
// can, because whatsmeow wrote it when it consumed the batch. Returns the
// error that kept the debt, if any.
func (a *App) settleContactDebtFromSessionStore(ctx context.Context, generation int64) error {
	collection := appstate.WAPatchCriticalUnblockLow
	fetchErr, persistenceErr := a.fetchAndPersistAppState(ctx, collection, false, &appStatePersistenceTracker{})
	if err := errors.Join(fetchErr, persistenceErr); err != nil {
		return fmt.Errorf("catch up %s before mirroring the session contact store: %w", collection, err)
	}
	if err := a.mirrorContactsUnderTicket(ctx); err != nil {
		return err
	}
	return a.clearCompletedAppStateRecovery(collection, generation)
}

// mirrorContactsUnderTicket runs mirrorContactsFromSessionStore as its own
// persistence task, ordered with the live contact events.
func (a *App) mirrorContactsUnderTicket(ctx context.Context) error {
	ticket := a.appStatePersist.reserve()
	persistCtx := context.WithoutCancel(ctx)
	result := make(chan error, 1)
	frontier := a.appStatePersist.complete(ticket, func() {
		result <- a.mirrorContactsFromSessionStore(persistCtx)
	})
	if err := a.appStatePersist.waitThrough(persistCtx, frontier); err != nil {
		return err
	}
	return <-result
}
