package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

const contactsSaveKind = "contacts_save"

type contactSaveOptions struct {
	phone     string
	name      string
	firstName string
}

type contactSaveResult struct {
	Action    string `json:"action"`
	JID       string `json:"jid"`
	Phone     string `json:"phone"`
	LID       string `json:"lid,omitempty"`
	FullName  string `json:"full_name"`
	FirstName string `json:"first_name"`
	// RequestedJID is the typed number when WhatsApp reported a different
	// canonical phone number for it (regional rewrites); empty otherwise.
	RequestedJID string `json:"requested_jid,omitempty"`
}

func newContactsSaveCmd(flags *rootFlags) *cobra.Command {
	opts := contactSaveOptions{}
	cmd := &cobra.Command{
		Use:     "save",
		Aliases: []string{"rename"},
		Short:   "Save or rename a contact in the WhatsApp address book",
		Long: "Save a phone number under a name in the WhatsApp address book of the linked\n" +
			"account, or rename an existing contact. The change is sent as a WhatsApp\n" +
			"contact mutation, so the phone and other linked devices receive it; the phone\n" +
			"is asked to store the entry in its device address book. The number must be\n" +
			"confirmed as registered on WhatsApp; the entry is saved under the phone number\n" +
			"WhatsApp reports for it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContactsSave(flags, opts)
		},
	}
	cmd.Flags().StringVar(&opts.phone, "phone", "", "phone number (+E.164 or common formatting) or user JID")
	cmd.Flags().StringVar(&opts.name, "name", "", "full name to save")
	cmd.Flags().StringVar(&opts.firstName, "first-name", "", "first name; omitted from the mutation when not given")
	return cmd
}

// contactSaveTarget validates the flags before any lock, connection, or
// delegation so mistakes fail fast with the same errors in both paths.
func contactSaveTarget(opts contactSaveOptions) (types.JID, string, string, error) {
	if strings.TrimSpace(opts.phone) == "" {
		return types.JID{}, "", "", fmt.Errorf("--phone is required")
	}
	jid, err := wa.ParseUserOrJID(opts.phone)
	if err != nil {
		return types.JID{}, "", "", err
	}
	if jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer {
		return types.JID{}, "", "", fmt.Errorf("unsupported --phone %q: pass a phone number or user JID", opts.phone)
	}
	name, err := app.NormalizeContactName("--name", opts.name)
	if err != nil {
		return types.JID{}, "", "", err
	}
	firstName := ""
	if strings.TrimSpace(opts.firstName) != "" {
		if firstName, err = app.NormalizeContactName("--first-name", opts.firstName); err != nil {
			return types.JID{}, "", "", err
		}
	}
	return jid, name, firstName, nil
}

type contactSaveApp interface {
	SaveContact(context.Context, types.JID, string, string) (app.SavedContact, error)
}

func runContactsSave(flags *rootFlags, opts contactSaveOptions) error {
	jid, name, firstName, err := contactSaveTarget(opts)
	if err != nil {
		return err
	}
	if err := flags.requireWritable(); err != nil {
		return err
	}

	ctx, cancel := withTimeout(context.Background(), flags)
	defer cancel()

	a, lk, err := newApp(ctx, flags, true, false)
	if err != nil {
		resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{
			Kind:      contactsSaveKind,
			To:        jid.String(),
			Name:      name,
			FirstName: firstName,
		})
		if delegated {
			if delegateErr != nil {
				return explainContactsSaveDelegateError(delegateErr)
			}
			if resp.Contact == nil {
				return fmt.Errorf("the running sync process did not report the saved contact for %s; restart `wacli sync` after upgrading", jid)
			}
			return writeContactSaveResult(flags, *resp.Contact)
		}
		return err
	}
	defer closeApp(a, lk)

	if err := a.EnsureAuthed(ctx); err != nil {
		return err
	}
	removePersistenceHandler, err := a.AddChatStatePersistenceHandler(ctx)
	if err != nil {
		return err
	}
	defer func() {
		// Keep the handler active until the socket is closed, then let App.Close
		// drain every persistence task before it closes the local database.
		a.WA().Disconnect()
		removePersistenceHandler()
	}()
	if err := a.Connect(ctx, false, nil); err != nil {
		return err
	}

	result, err := saveContact(ctx, a, jid, name, firstName)
	if err != nil {
		return err
	}
	return writeContactSaveResult(flags, result)
}

func saveContact(ctx context.Context, a contactSaveApp, jid types.JID, name, firstName string) (contactSaveResult, error) {
	saved, err := a.SaveContact(ctx, jid, name, firstName)
	if err != nil {
		return contactSaveResult{}, err
	}
	result := contactSaveResult{
		Action:    "save",
		JID:       saved.JID.String(),
		Phone:     saved.JID.User,
		FullName:  saved.FullName,
		FirstName: saved.FirstName,
	}
	if !saved.LID.IsEmpty() {
		result.LID = saved.LID.String()
	}
	if !saved.Requested.IsEmpty() {
		result.RequestedJID = saved.Requested.String()
	}
	return result, nil
}

// executeDelegatedContactsSave runs the write on the follow process, which
// owns the store lock and the connected session.
func executeDelegatedContactsSave(ctx context.Context, a contactSaveApp, req sendDelegateRequest) (sendDelegateResponse, error) {
	jid, name, firstName, err := contactSaveTarget(contactSaveOptions{phone: req.To, name: req.Name, firstName: req.FirstName})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	result, err := saveContact(ctx, a, jid, name, firstName)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	return sendDelegateResponse{OK: true, Contact: &result}, nil
}

// explainContactsSaveDelegateError turns an older daemon's rejection into the action to take.
func explainContactsSaveDelegateError(err error) error {
	if err == nil || !strings.Contains(err.Error(), "unsupported send kind") || !strings.Contains(err.Error(), contactsSaveKind) {
		return err
	}
	return fmt.Errorf("the running sync process does not support contacts save; restart `wacli sync` after upgrading, then run this again: %w", err)
}

func writeContactSaveResult(flags *rootFlags, result contactSaveResult) error {
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, result)
	}
	note := ""
	if result.RequestedJID != "" {
		note = fmt.Sprintf(" (WhatsApp's number for %s)", sanitize(result.RequestedJID))
	}
	fmt.Fprintf(os.Stdout, "saved: %s as %q%s\n", sanitize(result.JID), sanitize(result.FullName), note)
	return nil
}
