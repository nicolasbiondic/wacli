package main

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/lock"
	"go.mau.fi/whatsmeow/types"
)

type recordingChatStateApp struct {
	calls []string
}

func (r *recordingChatStateApp) ArchiveChat(_ context.Context, jid types.JID, archive bool) error {
	r.calls = append(r.calls, "archive="+jid.String()+"/"+boolString(archive))
	return nil
}

func (r *recordingChatStateApp) PinChat(_ context.Context, jid types.JID, pin bool) error {
	r.calls = append(r.calls, "pin="+jid.String()+"/"+boolString(pin))
	return nil
}

func (r *recordingChatStateApp) MuteChat(_ context.Context, jid types.JID, mute bool, d time.Duration) error {
	r.calls = append(r.calls, "mute="+jid.String()+"/"+boolString(mute)+"/"+d.String())
	return nil
}

func (r *recordingChatStateApp) MarkChatRead(_ context.Context, jid types.JID, read bool) error {
	r.calls = append(r.calls, "read="+jid.String()+"/"+boolString(read))
	return nil
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestChatStateRunnerMapsEveryAction(t *testing.T) {
	jid := types.NewJID("123", types.DefaultUserServer)
	cases := []struct {
		action   string
		duration time.Duration
		want     string
	}{
		{"archive", 0, "archive=123@s.whatsapp.net/true"},
		{"unarchive", 0, "archive=123@s.whatsapp.net/false"},
		{"pin", 0, "pin=123@s.whatsapp.net/true"},
		{"unpin", 0, "pin=123@s.whatsapp.net/false"},
		{"mute", 8 * time.Hour, "mute=123@s.whatsapp.net/true/8h0m0s"},
		{"unmute", 0, "mute=123@s.whatsapp.net/false/0s"},
		{"mark-read", 0, "read=123@s.whatsapp.net/true"},
		{"mark-unread", 0, "read=123@s.whatsapp.net/false"},
	}
	for _, tc := range cases {
		run, err := chatStateRunner(tc.action, tc.duration)
		if err != nil {
			t.Fatalf("%s: %v", tc.action, err)
		}
		app := &recordingChatStateApp{}
		if err := run(context.Background(), app, jid); err != nil {
			t.Fatalf("%s: run: %v", tc.action, err)
		}
		if len(app.calls) != 1 || app.calls[0] != tc.want {
			t.Fatalf("%s: calls = %v, want [%s]", tc.action, app.calls, tc.want)
		}
	}
	if _, err := chatStateRunner("delete", 0); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestExecuteDelegatedSendRejectsUnknownChatStateAction(t *testing.T) {
	_, err := executeDelegatedSend(context.Background(), nil, sendDelegateRequest{
		Version:         sendDelegateVersion,
		Kind:            "chat_state",
		ChatStateAction: "delete",
		To:              "123@s.whatsapp.net",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported chat state action") {
		t.Fatalf("err = %v, want unsupported chat state action", err)
	}
}

func TestChatsArchiveDelegatesThroughSendSocketWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
		return sendDelegateResponse{OK: true, To: "123@s.whatsapp.net"}
	})
	defer server.stop()

	before := time.Now()
	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "5s",
		"chats", "archive", "--chat", "123@s.whatsapp.net",
	})
	if err != nil {
		t.Fatalf("chats archive failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	after := time.Now()

	req := server.nextRequest(t)
	if req.Version != sendDelegateVersion || req.Kind != "chat_state" {
		t.Fatalf("delegate version/kind = %d/%q", req.Version, req.Kind)
	}
	if req.ChatStateAction != "archive" || req.To != "123@s.whatsapp.net" {
		t.Fatalf("delegate chat state target = %+v", req)
	}
	if req.TimeoutMS != 5000 {
		t.Fatalf("delegate timeout_ms = %d, want 5000 (caller's --timeout 5s)", req.TimeoutMS)
	}
	if req.DeadlineUnixMS == 0 {
		t.Fatalf("delegate deadline_unix_ms = 0, want a caller-derived deadline")
	}
	wantMin, wantMax := before.Add(4500*time.Millisecond).UnixMilli(), after.Add(5500*time.Millisecond).UnixMilli()
	if req.DeadlineUnixMS < wantMin || req.DeadlineUnixMS > wantMax {
		t.Fatalf("delegate deadline_unix_ms = %d, want within [%d, %d] for --timeout 5s", req.DeadlineUnixMS, wantMin, wantMax)
	}
	if strings.Contains(stderr, "store is locked") {
		t.Fatalf("delegated command tried the direct store path: stderr=%q", stderr)
	}
	for _, want := range []string{`"ok":true`, `"action":"archive"`, `"chat":"123@s.whatsapp.net"`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout %q missing %s", stdout, want)
		}
	}
}

func TestChatsMuteDelegatesDurationThroughSendSocket(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
		return sendDelegateResponse{OK: true, To: "123@s.whatsapp.net"}
	})
	defer server.stop()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "750ms",
		"chats", "mute", "--chat", "123@s.whatsapp.net", "--duration", "8h",
	})
	if err != nil {
		t.Fatalf("chats mute failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	req := server.nextRequest(t)
	if req.Kind != "chat_state" || req.ChatStateAction != "mute" {
		t.Fatalf("delegate kind/action = %q/%q", req.Kind, req.ChatStateAction)
	}
	if req.MuteDurationMS != (8 * time.Hour).Milliseconds() {
		t.Fatalf("MuteDurationMS = %d, want %d", req.MuteDurationMS, (8 * time.Hour).Milliseconds())
	}
}

func TestDelegatedChatStateDoesNotWaitForSendMutex(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	ln, err := net.Listen("unix", sendDelegateSocketPath(storeDir))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	var sendMu sync.Mutex
	sendMu.Lock()
	defer sendMu.Unlock()
	pacer := newSendPacer(sendSpacing{})

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		handleSendDelegateConn(context.Background(), conn, nil, &sendMu, nil, pacer)
	}()

	conn, err := net.Dial("unix", sendDelegateSocketPath(storeDir))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if err := json.NewEncoder(conn).Encode(sendDelegateRequest{
		Version:         sendDelegateVersion,
		Kind:            "chat_state",
		ChatStateAction: "delete",
		To:              "123@s.whatsapp.net",
		TimeoutMS:       1000,
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var resp sendDelegateResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatalf("chat_state request blocked behind the send mutex: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, "unsupported chat state action") {
		t.Fatalf("resp = %+v", resp)
	}
}

// The caller's deadline bounds only the WAIT: once the response is written,
// the operation must keep running under its own daemon-side context until it
// completes. whatsmeow persists every app-state mutation with the operation's
// context, so cancelling it mid-apply corrupts the collection's LTHash chain
// permanently — that is how tenant 905's regular_low broke on 2026-09-08.
// This pins the wiring: op context detached from the caller deadline, alive
// after the answer, cancelled only when the work itself finishes.
func TestDelegatedChatStateOperationOutlivesCallerDeadline(t *testing.T) {
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancelWait()
	opCtx, cancelOp := chatStateOpContext(context.Background())

	release := make(chan struct{})
	opErrAfterAnswer := make(chan error, 1)

	resp := runChatStateDelegateWithDeadline(waitCtx, func() sendDelegateResponse {
		defer cancelOp()
		// Simulate a slow app-state apply: the caller deadline fires while
		// this is still running.
		<-release
		opErrAfterAnswer <- opCtx.Err()
		return sendDelegateResponse{OK: true, To: "123@s.whatsapp.net"}
	})

	if resp.OK || !strings.Contains(resp.Error, "exceeded its deadline") {
		t.Fatalf("resp = %+v, want a deadline-exceeded answer for the caller", resp)
	}

	// The answer is out; the operation continues. Its context must still be
	// live — a cancellation here is what truncates whatsmeow's apply.
	close(release)
	if err := <-opErrAfterAnswer; err != nil {
		t.Fatalf("operation context died with the caller deadline: %v", err)
	}
	select {
	case <-opCtx.Done():
		// cancelOp ran when the work finished — correct.
	case <-time.After(2 * time.Second):
		t.Fatal("operation context was never released after the work finished")
	}
}

func TestRunChatStateDelegateWithDeadlineBoundsNonCooperativeWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	blocked := make(chan struct{})
	defer close(blocked)
	started := make(chan struct{})

	start := time.Now()
	resp := runChatStateDelegateWithDeadline(ctx, func() sendDelegateResponse {
		close(started)
		<-blocked
		return sendDelegateResponse{OK: true, To: "123@s.whatsapp.net"}
	})
	elapsed := time.Since(start)

	<-started
	if resp.OK {
		t.Fatalf("resp = %+v, want a bounded timeout error", resp)
	}
	if !strings.Contains(resp.Error, "exceeded its deadline") {
		t.Fatalf("resp.Error = %q, want a deadline-exceeded message", resp.Error)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("runChatStateDelegateWithDeadline took %s, want it bounded by ctx regardless of non-cooperative work", elapsed)
	}
}
