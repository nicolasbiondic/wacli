package app

import (
	"context"
	"fmt"

	"github.com/openclaw/wacli/internal/wa"
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
	if err := a.wa.SaveContact(ctx, target, lid, saved.FullName, saved.FirstName, phonebook); err != nil {
		return SavedContact{}, fmt.Errorf("save contact: %w", err)
	}
	if err := a.db.UpsertContact(target.String(), target.User, "", saved.FullName, saved.FirstName, ""); err != nil {
		return SavedContact{}, fmt.Errorf("contact saved on WhatsApp, but updating the local index failed: %w", err)
	}
	return saved, nil
}
