package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

func TestParseContactSaveTarget(t *testing.T) {
	got, err := parseContactSaveTarget("+55 11 99999-0000")
	if err != nil || got.String() != "5511999990000@s.whatsapp.net" {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := parseContactSaveTarget("123@lid"); err == nil {
		t.Fatal("LID accepted")
	}
	if _, err := parseContactSaveTarget("120363000000@g.us"); err == nil {
		t.Fatal("group accepted")
	}
	if _, err := parseContactSaveTarget("  "); err == nil {
		t.Fatal("empty accepted")
	}
}

func TestContactsSaveRequiresName(t *testing.T) {
	cmd := newContactsSaveCmd(&rootFlags{})
	cmd.SetArgs([]string{"--jid", "5511999990000"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil || err.Error() != "--name is required" {
		t.Fatalf("err = %v", err)
	}
}

// fakeContactSaver stands in for the follow process's connected session.
type fakeContactSaver struct {
	lid   types.JID
	err   error
	saved []string
}

func (f *fakeContactSaver) ResolvePNToLID(_ context.Context, jid types.JID) types.JID {
	if f.lid.IsEmpty() {
		return jid
	}
	return f.lid
}

func (f *fakeContactSaver) SaveContact(_ context.Context, target, lid types.JID, fullName, firstName string, phonebook bool) error {
	f.saved = append(f.saved, fmt.Sprintf("%s %s %q %q %v", target, lid, fullName, firstName, phonebook))
	return f.err
}

func openContactSaveTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestExecuteDelegatedContactsSaveSavesAndIndexes(t *testing.T) {
	db := openContactSaveTestDB(t)
	saver := &fakeContactSaver{lid: types.NewJID("99887766", types.HiddenUserServer)}
	resp, err := executeDelegatedContactsSave(context.Background(), saver, db, sendDelegateRequest{
		Kind: contactsSaveKind, To: "+1 555 000 1111", Name: "  Ada Lovelace ", Phonebook: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{`15550001111@s.whatsapp.net 99887766@lid "Ada Lovelace" "Ada" true`}; !slices.Equal(saver.saved, want) {
		t.Fatalf("saved = %q, want %q", saver.saved, want)
	}
	if !resp.OK || resp.Contact == nil {
		t.Fatalf("response = %+v", resp)
	}
	got := *resp.Contact
	want := contactSaveResult{JID: "15550001111@s.whatsapp.net", Name: "Ada Lovelace", FirstName: "Ada", Phonebook: true, LID: "99887766@lid"}
	if got != want {
		t.Fatalf("contact = %+v, want %+v", got, want)
	}
	c, err := db.GetContact("15550001111@s.whatsapp.net")
	if err != nil || c.Name != "Ada Lovelace" || c.Phone != "15550001111" {
		t.Fatalf("local index = %+v, %v", c, err)
	}
}

func TestExecuteDelegatedContactsSaveRefusesWithoutLID(t *testing.T) {
	db := openContactSaveTestDB(t)
	saver := &fakeContactSaver{}
	_, err := executeDelegatedContactsSave(context.Background(), saver, db, sendDelegateRequest{
		Kind: contactsSaveKind, To: "15550001111", Name: "Ada",
	})
	if err == nil || !strings.Contains(err.Error(), "LID") || !strings.Contains(err.Error(), "nothing was saved") {
		t.Fatalf("err = %v", err)
	}
	if saver.saved != nil {
		t.Fatalf("mutation sent without a LID: %q", saver.saved)
	}
	if _, err := db.GetContact("15550001111@s.whatsapp.net"); err == nil {
		t.Fatal("local index updated although nothing was saved")
	}
}

func TestExecuteDelegatedContactsSaveValidatesLikeTheCommand(t *testing.T) {
	db := openContactSaveTestDB(t)
	saver := &fakeContactSaver{lid: types.NewJID("99887766", types.HiddenUserServer)}
	for _, req := range []sendDelegateRequest{
		{Kind: contactsSaveKind, To: "123@lid", Name: "Ada"},
		{Kind: contactsSaveKind, To: "15550001111", Name: "   "},
		{Kind: contactsSaveKind, Name: "Ada"},
	} {
		if _, err := executeDelegatedContactsSave(context.Background(), saver, db, req); err == nil {
			t.Errorf("request %+v accepted", req)
		}
	}
	if saver.saved != nil {
		t.Fatalf("invalid requests reached the session: %q", saver.saved)
	}
}

func TestExecuteDelegatedContactsSaveReportsSendErrors(t *testing.T) {
	db := openContactSaveTestDB(t)
	saver := &fakeContactSaver{lid: types.NewJID("99887766", types.HiddenUserServer), err: errors.New("boom")}
	_, err := executeDelegatedContactsSave(context.Background(), saver, db, sendDelegateRequest{
		Kind: contactsSaveKind, To: "15550001111", Name: "Ada",
	})
	if err == nil || !strings.Contains(err.Error(), "save contact: boom") {
		t.Fatalf("err = %v", err)
	}
	if _, err := db.GetContact("15550001111@s.whatsapp.net"); err == nil {
		t.Fatal("local index updated after a failed send")
	}
}

func TestExplainContactsSaveDelegateErrorNamesRestartForOlderDaemons(t *testing.T) {
	old := errors.New(`unsupported send kind "contacts_save"`)
	if err := explainContactsSaveDelegateError(old); err == nil || !strings.Contains(err.Error(), "restart `wacli sync`") || !errors.Is(err, old) {
		t.Fatalf("error = %v, want restart hint wrapping the original", err)
	}
	other := errors.New("not connected")
	if err := explainContactsSaveDelegateError(other); err != other {
		t.Fatalf("error = %v, want unrelated error unchanged", err)
	}
}
