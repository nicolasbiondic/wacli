package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/lock"
	"go.mau.fi/whatsmeow/types"
)

type fakeContactSaveApp struct {
	calls chan fakeContactSaveAppCall
	saved app.SavedContact
	err   error
}

type fakeContactSaveAppCall struct {
	jid       types.JID
	fullName  string
	firstName string
}

func (f *fakeContactSaveApp) SaveContact(_ context.Context, jid types.JID, fullName, firstName string) (app.SavedContact, error) {
	f.calls <- fakeContactSaveAppCall{jid: jid, fullName: fullName, firstName: firstName}
	if f.err != nil {
		return app.SavedContact{}, f.err
	}
	saved := f.saved
	if saved.JID.IsEmpty() {
		saved.JID = jid
	}
	if saved.FullName == "" {
		saved.FullName = fullName
	}
	saved.FirstName = firstName
	return saved, nil
}

func TestContactSaveTargetValidation(t *testing.T) {
	tests := []struct {
		name string
		opts contactSaveOptions
		want string
	}{
		{name: "missing phone", opts: contactSaveOptions{name: "Ada"}, want: "--phone is required"},
		{name: "bad phone", opts: contactSaveOptions{phone: "abc", name: "Ada"}, want: "invalid phone number"},
		{name: "group jid", opts: contactSaveOptions{phone: "120363000000000000@g.us", name: "Ada"}, want: "unsupported --phone"},
		{name: "missing name", opts: contactSaveOptions{phone: "+1 555 000 1111"}, want: "--name requires a nonempty name"},
		{name: "long name", opts: contactSaveOptions{phone: "+15550001111", name: strings.Repeat("a", app.MaxContactNameLength+1)}, want: "longer than"},
		{name: "control name", opts: contactSaveOptions{phone: "+15550001111", name: "Ada\rLovelace"}, want: "control characters"},
		{name: "control first name", opts: contactSaveOptions{phone: "+15550001111", name: "Ada", firstName: "A\x00"}, want: "--first-name must not contain"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := contactSaveTarget(tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}

	jid, name, first, err := contactSaveTarget(contactSaveOptions{phone: "+1 (555) 000-1111", name: "  Ada Lovelace  ", firstName: " "})
	if err != nil {
		t.Fatal(err)
	}
	if jid.String() != "15550001111@s.whatsapp.net" || name != "Ada Lovelace" || first != "" {
		t.Fatalf("target = %s %q %q, want normalized phone, trimmed name, no derived first name", jid, name, first)
	}
	lid, _, _, err := contactSaveTarget(contactSaveOptions{phone: "987654321098765@lid", name: "Ada"})
	if err != nil || lid.Server != types.HiddenUserServer {
		t.Fatalf("LID input = %s, %v; want accepted for session mapping", lid, err)
	}
}

func TestExecuteDelegatedContactsSaveReturnsSavedContact(t *testing.T) {
	target := types.NewJID("15550001111", types.DefaultUserServer)
	lid := types.NewJID("987654321098765", types.HiddenUserServer)
	fake := &fakeContactSaveApp{calls: make(chan fakeContactSaveAppCall, 1), saved: app.SavedContact{JID: target, LID: lid, Registered: true, Responded: true}}
	resp, err := executeDelegatedContactsSave(context.Background(), fake, sendDelegateRequest{
		Kind: contactsSaveKind, To: "+1 555 000 1111", Name: " Ada Lovelace ", FirstName: "Ada",
	})
	if err != nil {
		t.Fatalf("executeDelegatedContactsSave: %v", err)
	}
	call := <-fake.calls
	if call.jid != target || call.fullName != "Ada Lovelace" || call.firstName != "Ada" {
		t.Fatalf("app call = %+v", call)
	}
	if !resp.OK || resp.Contact == nil {
		t.Fatalf("response = %+v, want ok with contact", resp)
	}
	c := resp.Contact
	if c.Action != "save" || c.JID != target.String() || c.Phone != "15550001111" || c.LID != lid.String() || c.FullName != "Ada Lovelace" || c.FirstName != "Ada" || !c.Registered || !c.Responded {
		t.Fatalf("contact result = %+v", *c)
	}
}

func TestExecuteDelegatedContactsSaveValidatesBeforeAppUse(t *testing.T) {
	fake := &fakeContactSaveApp{calls: make(chan fakeContactSaveAppCall, 1)}
	for _, req := range []sendDelegateRequest{
		{Kind: contactsSaveKind, To: "", Name: "Ada"},
		{Kind: contactsSaveKind, To: "+15550001111", Name: " "},
		{Kind: contactsSaveKind, To: "120363000000000000@g.us", Name: "Ada"},
	} {
		if _, err := executeDelegatedContactsSave(context.Background(), fake, req); err == nil {
			t.Fatalf("request %+v accepted", req)
		}
	}
	select {
	case call := <-fake.calls:
		t.Fatalf("invalid request reached the app: %+v", call)
	default:
	}
}

func TestExecuteDelegatedContactsSavePropagatesAppError(t *testing.T) {
	fake := &fakeContactSaveApp{calls: make(chan fakeContactSaveAppCall, 1), err: fmt.Errorf("%w: +15550001111", app.ErrContactNotOnWhatsApp)}
	_, err := executeDelegatedContactsSave(context.Background(), fake, sendDelegateRequest{Kind: contactsSaveKind, To: "+15550001111", Name: "Ada"})
	if !errors.Is(err, app.ErrContactNotOnWhatsApp) {
		t.Fatalf("error = %v, want app error", err)
	}
}

func TestExplainContactsSaveDelegateErrorNamesTheRestart(t *testing.T) {
	rejected := fmt.Errorf(`unsupported send kind %q`, contactsSaveKind)
	if err := explainContactsSaveDelegateError(rejected); err == nil || !strings.Contains(err.Error(), "restart `wacli sync`") || !errors.Is(err, rejected) {
		t.Fatalf("error = %v, want restart hint wrapping the original", err)
	}
	other := errors.New("server rejected patch")
	if err := explainContactsSaveDelegateError(other); err != other {
		t.Fatalf("error = %v, want unrelated error unchanged", err)
	}
	if err := explainContactsSaveDelegateError(nil); err != nil {
		t.Fatalf("nil error = %v", err)
	}
}

func TestContactsSaveDelegatesThroughProductionServerWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	target := types.NewJID("15550001111", types.DefaultUserServer)
	lid := types.NewJID("987654321098765", types.HiddenUserServer)
	fake := &fakeContactSaveApp{calls: make(chan fakeContactSaveAppCall, 1), saved: app.SavedContact{JID: target, LID: lid, Registered: true, Responded: true}}
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		if req.Version != sendDelegateVersion || req.Kind != contactsSaveKind {
			return sendDelegateResponse{}, fmt.Errorf("unexpected delegated request %d/%q", req.Version, req.Kind)
		}
		return executeDelegatedContactsSave(ctx, fake, req)
	})
	if err != nil {
		t.Fatalf("start production delegate server: %v", err)
	}
	defer stop()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "750ms",
		"contacts", "save", "--phone", "+1 555 000 1111", "--name", " Ada Lovelace ", "--first-name", "Ada",
	})
	if err != nil {
		t.Fatalf("contacts save failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if strings.Contains(stderr, "store is locked") {
		t.Fatalf("delegated command returned lock error: stderr=%q", stderr)
	}
	select {
	case call := <-fake.calls:
		if call.jid != target || call.fullName != "Ada Lovelace" || call.firstName != "Ada" {
			t.Fatalf("delegated call = %+v", call)
		}
	case <-contextWithTestTimeout(t).Done():
		t.Fatal("timed out waiting for the delegated save")
	}
	for _, want := range []string{`"action":"save"`, `"jid":"` + target.String() + `"`, `"phone":"15550001111"`, `"lid":"` + lid.String() + `"`, `"full_name":"Ada Lovelace"`, `"first_name":"Ada"`, `"registered":true`, `"responded":true`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout %q missing %s", stdout, want)
		}
	}
}

func TestContactsSaveRenameAliasDelegatesSameRequest(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()
	fake := &fakeContactSaveApp{calls: make(chan fakeContactSaveAppCall, 1)}
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		return executeDelegatedContactsSave(ctx, fake, req)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--timeout", "750ms", "contacts", "rename", "--phone", "15550001111", "--name", "New Name",
	})
	if err != nil {
		t.Fatalf("contacts rename: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	call := <-fake.calls
	if call.fullName != "New Name" {
		t.Fatalf("delegated call = %+v", call)
	}
	if !strings.Contains(stdout, `saved: 15550001111@s.whatsapp.net as "New Name"`) || !strings.Contains(stdout, "registration unknown") {
		t.Fatalf("stdout = %q, want text result with unknown registration note", stdout)
	}
}

func TestContactsSaveReadOnlyRejectsBeforeDelegation(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	for _, mode := range []string{"flag", "environment"} {
		t.Run(mode, func(t *testing.T) {
			storeDir := shortPresenceDelegateStoreDir(t)
			lk, err := lock.Acquire(storeDir)
			if err != nil {
				t.Fatal(err)
			}
			defer lk.Release()
			called := make(chan struct{}, 1)
			stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(context.Context, sendDelegateRequest) (sendDelegateResponse, error) {
				called <- struct{}{}
				return sendDelegateResponse{OK: true}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			args := []string{"--store", storeDir, "--json"}
			t.Setenv("WACLI_READONLY", "")
			if mode == "flag" {
				args = append(args, "--read-only")
			} else {
				t.Setenv("WACLI_READONLY", "1")
			}
			args = append(args, "contacts", "save", "--phone", "+15550001111", "--name", "Ada")
			stdout, stderr, err := runPresenceDelegateHelper(t, args)
			if err == nil || !strings.Contains(stdout+stderr, "read-only") {
				t.Fatalf("want read-only rejection, got %v stdout=%q stderr=%q", err, stdout, stderr)
			}
			select {
			case <-called:
				t.Fatal("read-only save reached the delegate")
			default:
			}
		})
	}
}

func TestContactsSaveValidationFailsBeforeDelegation(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	called := make(chan struct{}, 1)
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(context.Context, sendDelegateRequest) (sendDelegateResponse, error) {
		called <- struct{}{}
		return sendDelegateResponse{OK: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	stdout, stderr, err := runPresenceDelegateHelper(t, []string{"--store", storeDir, "contacts", "save", "--phone", "+15550001111", "--name", "  "})
	if err == nil || !strings.Contains(stdout+stderr, "--name requires a nonempty name") {
		t.Fatalf("want validation error, got %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	select {
	case <-called:
		t.Fatal("invalid request reached the delegate")
	default:
	}
}

func TestDelegatedContactsSaveDoesNotWaitForSendQueue(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	if err := clientConn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set client deadline: %v", err)
	}
	sendSlot := make(chan struct{}, 1) // empty means an earlier send owns it
	executed := make(chan sendDelegateRequest, 1)
	go handleSendDelegateConn(context.Background(), serverConn, func(_ context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		executed <- req
		return sendDelegateResponse{OK: true, Contact: &contactSaveResult{Action: "save", JID: req.To}}, nil
	}, sendSlot, newSendPacer(sendSpacing{min: time.Second, max: time.Second}))

	req := sendDelegateRequest{Version: sendDelegateVersion, Kind: contactsSaveKind, To: "15550001111@s.whatsapp.net", Name: "Ada"}
	if err := json.NewEncoder(clientConn).Encode(req); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	var resp sendDelegateResponse
	if err := json.NewDecoder(clientConn).Decode(&resp); err != nil {
		t.Fatalf("contact save waited behind the send queue: %v", err)
	}
	if !resp.OK || resp.Contact == nil || resp.Contact.JID != req.To {
		t.Fatalf("response = %+v", resp)
	}
	if got := <-executed; got.Kind != contactsSaveKind || got.Name != "Ada" {
		t.Fatalf("executed = %+v", got)
	}
}
