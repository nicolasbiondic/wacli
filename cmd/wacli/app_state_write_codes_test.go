package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/delegate"
	"github.com/openclaw/wacli/internal/lock"
)

type errorEnvelope struct {
	Success      bool    `json:"success"`
	Error        *string `json:"error"`
	ErrorCode    string  `json:"error_code"`
	WriteOutcome string  `json:"write_outcome"`
}

func parseErrorEnvelope(t *testing.T, stderr string) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	line := strings.TrimSpace(stderr)
	if i := strings.LastIndex(line, "\n"); i >= 0 {
		line = line[i+1:]
	}
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		t.Fatalf("stderr is not a JSON envelope: %q: %v", stderr, err)
	}
	if env.Success || env.Error == nil {
		t.Fatalf("envelope = %+v", env)
	}
	return env
}

// The test binary helper appends its own PASS line after the command output.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func lockedStore(t *testing.T) string {
	t.Helper()
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lk.Release() })
	return storeDir
}

func TestListWriteErrorEnvelopeCarriesCodeAndOutcome(t *testing.T) {
	t.Setenv("WACLI_REGULAR_WRITES", "")
	for _, tc := range []struct {
		args []string
		code string
	}{
		{[]string{"lists", "delete", "1"}, app.ListErrRegularWritesDisabled},
		{[]string{"chats", "label", "add", "--chat", "123@s.whatsapp.net", "--label", "1"}, app.ListErrRegularWritesDisabled},
		{[]string{"lists", "create", "--name", " "}, app.ListErrInvalidInput},
		{[]string{"lists", "edit", "1"}, app.ListErrInvalidInput},
		{[]string{"chats", "label", "add", "--chat", "123@s.whatsapp.net"}, app.ListErrInvalidInput},
	} {
		_, stderr, err := runPresenceDelegateHelper(t, append([]string{"--store", t.TempDir(), "--json"}, tc.args...))
		if err == nil {
			t.Fatalf("%v succeeded", tc.args)
		}
		env := parseErrorEnvelope(t, stderr)
		if env.ErrorCode != tc.code || env.WriteOutcome != app.WriteOutcomeNotSent {
			t.Fatalf("%v: envelope = %+v, want %s/not_sent", tc.args, env, tc.code)
		}
	}
	// Text output keeps the plain message.
	_, stderr, err := runPresenceDelegateHelper(t, []string{"--store", t.TempDir(), "lists", "delete", "1"})
	if err == nil || strings.Contains(stderr, "error_code") || !strings.Contains(stderr, "enable WACLI_REGULAR_WRITES=1") {
		t.Fatalf("text output = %q, %v", stderr, err)
	}
}

func TestOwnerSideGateRefusalIsTyped(t *testing.T) {
	t.Setenv("WACLI_REGULAR_WRITES", "")
	_, err := executeDelegatedAppStateWrite(t.Context(), nil, sendDelegateRequest{Kind: appStateWriteKind, AppState: &appStateWriteRequest{Action: labelDeleteAction, Ref: "1"}})
	var typed *app.ListWriteError
	if !errors.As(err, &typed) || typed.Code != app.ListErrRegularWritesDisabled || typed.Outcome != app.WriteOutcomeNotSent {
		t.Fatalf("err = %v", err)
	}
}

func TestDelegatedListWriteKeepsCodeOutcomeAndUpdatedAt(t *testing.T) {
	t.Setenv("WACLI_REGULAR_WRITES", "1")
	storeDir := lockedStore(t)
	fake := &fakeAppStateWriteApp{calls: make(chan string, 4)}
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		return executeDelegatedAppStateWrite(ctx, fake, req)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	run := func(args ...string) (string, errorEnvelope) {
		stdout, stderr, err := runPresenceDelegateHelper(t, append([]string{"--store", storeDir, "--timeout", "5s", "--json"}, args...))
		<-fake.calls
		if err == nil {
			return stdout, errorEnvelope{}
		}
		return stdout, parseErrorEnvelope(t, stderr)
	}

	// Results carry updated_at, over the socket too.
	stdout, _ := run("lists", "create", "--name", "Prueba")
	var created struct {
		Data struct {
			OK        bool           `json:"ok"`
			Label     map[string]any `json:"label"`
			UpdatedAt int64          `json:"updated_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(firstLine(stdout)), &created); err != nil || !created.Data.OK || created.Data.UpdatedAt != fakeUpdatedAt {
		t.Fatalf("create result = %q, %v", stdout, err)
	}
	stdout, _ = run("chats", "label", "add", "--chat", "123@s.whatsapp.net", "--label", "10")
	var chat struct {
		Data struct {
			Label     map[string]any `json:"label"`
			Chat      string         `json:"chat"`
			UpdatedAt int64          `json:"updated_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(firstLine(stdout)), &chat); err != nil || chat.Data.Chat != "123@s.whatsapp.net" || chat.Data.Label["id"] != "10" || chat.Data.UpdatedAt != fakeUpdatedAt {
		t.Fatalf("chat label result = %q, %v", stdout, err)
	}

	// Every owner-side code and outcome survives the socket.
	for _, tc := range []struct{ code, outcome string }{
		{app.ListErrDuplicateName, app.WriteOutcomeNotSent},
		{app.ListErrIndexIncomplete, app.WriteOutcomeNotSent},
		{app.ListErrCoordinatorDown, app.WriteOutcomeNotSent},
		{app.ListErrWriteOutcomeUnknown, app.WriteOutcomeUnknown},
	} {
		fake.err = app.NewListWriteError(tc.code, tc.outcome, fmt.Errorf("owner says %s", tc.code))
		_, env := run("lists", "create", "--name", "Prueba")
		if env.ErrorCode != tc.code || env.WriteOutcome != tc.outcome || *env.Error != "owner says "+tc.code {
			t.Fatalf("%s: envelope = %+v", tc.code, env)
		}
	}
}

func TestDelegatedNotSentCodeIsNotWrappedAsAfterDispatch(t *testing.T) {
	// A refusal before the send must not claim the write may have gone through,
	// even when the request deadline is what stopped it.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp := captureDelegateResult(t, ctx, sendDelegateRequest{Kind: appStateWriteKind},
		app.NewListWriteError(app.ListErrCoordinatorDown, app.WriteOutcomeNotSent, fmt.Errorf("wait for chat state synchronization: %w", context.Canceled)))
	if resp.ErrorCode != app.ListErrCoordinatorDown || resp.WriteOutcome != app.WriteOutcomeNotSent || strings.Contains(resp.Error, "may still have gone through") {
		t.Fatalf("resp = %+v", resp)
	}
	resp = captureDelegateResult(t, ctx, sendDelegateRequest{Kind: appStateWriteKind},
		app.NewListWriteError(app.ListErrWriteOutcomeUnknown, app.WriteOutcomeUnknown, context.Canceled))
	if resp.WriteOutcome != app.WriteOutcomeUnknown || !strings.Contains(resp.Error, "may still have gone through") {
		t.Fatalf("resp = %+v", resp)
	}
}

func captureDelegateResult(t *testing.T, ctx context.Context, req sendDelegateRequest, err error) sendDelegateResponse {
	t.Helper()
	srv, cli := net.Pipe()
	defer cli.Close()
	go func() {
		defer srv.Close()
		writeDelegateResult(srv, ctx, req, sendDelegateResponse{}, err)
	}()
	var resp sendDelegateResponse
	if err := json.NewDecoder(cli).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestOlderOwnerReplyMapsToGenericOrUnsupportedCode(t *testing.T) {
	t.Setenv("WACLI_REGULAR_WRITES", "1")
	storeDir := lockedStore(t)
	var failure error
	stop, err := startSendDelegateServerForStore(context.Background(), storeDir, sendSpacing{}, func(_ context.Context, _ sendDelegateRequest) (sendDelegateResponse, error) {
		return sendDelegateResponse{}, failure
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, tc := range []struct {
		name         string
		failure      error
		code         string
		outcome      string
		wantContains string
	}{
		// An owner built before these commands rejects the unknown kind.
		{"unsupported kind", fmt.Errorf("unsupported send kind %q", appStateWriteKind), app.ListErrOwnerUnsupported, app.WriteOutcomeNotSent, "changed nothing"},
		// An older owner that ran the write and failed sends no code.
		{"plain failure", errors.New("some older failure"), app.ListErrDelegateError, app.WriteOutcomeUnknown, "some older failure"},
	} {
		failure = tc.failure
		_, stderr, err := runPresenceDelegateHelper(t, []string{"--store", storeDir, "--timeout", "5s", "--json", "lists", "create", "--name", "x"})
		if err == nil {
			t.Fatalf("%s succeeded", tc.name)
		}
		env := parseErrorEnvelope(t, stderr)
		if env.ErrorCode != tc.code || env.WriteOutcome != tc.outcome || !strings.Contains(*env.Error, tc.wantContains) {
			t.Fatalf("%s: envelope = %+v (%s)", tc.name, env, *env.Error)
		}
	}
}

func TestLostDelegateReplyIsUnknownOutcome(t *testing.T) {
	t.Setenv("WACLI_REGULAR_WRITES", "1")
	storeDir := lockedStore(t)
	ln, err := delegate.Listen(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		var req sendDelegateRequest
		_ = json.NewDecoder(conn).Decode(&req)
		conn.Close() // dispatched, then the owner vanishes without a reply
	}()
	_, stderr, err := runPresenceDelegateHelper(t, []string{"--store", storeDir, "--timeout", "5s", "--json", "lists", "delete", "7"})
	if err == nil {
		t.Fatal("lost reply succeeded")
	}
	env := parseErrorEnvelope(t, stderr)
	if env.ErrorCode != app.ListErrWriteOutcomeUnknown || env.WriteOutcome != app.WriteOutcomeUnknown {
		t.Fatalf("envelope = %+v", env)
	}
}

func TestDirectUnavailableStoreIsNotSent(t *testing.T) {
	t.Setenv("WACLI_REGULAR_WRITES", "1")
	storeDir := lockedStore(t) // lock held, no socket: nothing can run the write
	_, stderr, err := runPresenceDelegateHelper(t, []string{"--store", storeDir, "--timeout", "5s", "--json", "lists", "delete", "7"})
	if err == nil {
		t.Fatal("write ran without a store")
	}
	env := parseErrorEnvelope(t, stderr)
	if env.ErrorCode != app.ListErrCoordinatorDown || env.WriteOutcome != app.WriteOutcomeNotSent {
		t.Fatalf("envelope = %+v", env)
	}
}

func TestExecuteAppStateWriteTypesValidationAndRecipientErrors(t *testing.T) {
	fake := &fakeAppStateWriteApp{calls: make(chan string, 1)}
	for _, req := range []appStateWriteRequest{
		{Action: "label_rename"},
		{Action: labelDeleteAction},
		{Action: chatLabelAddAction, Ref: "7"},
	} {
		_, err := executeAppStateWrite(t.Context(), fake, req)
		var typed *app.ListWriteError
		if !errors.As(err, &typed) || typed.Code != app.ListErrInvalidInput || typed.Outcome != app.WriteOutcomeNotSent {
			t.Fatalf("%+v: err = %v", req, err)
		}
	}
}
