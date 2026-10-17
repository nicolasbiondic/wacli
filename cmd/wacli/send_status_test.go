package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/lock"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

func TestBuildStatusTextMessageUsesStatusStyling(t *testing.T) {
	font := int32(2)
	msg, err := buildStatusTextMessage("hello status", statusTextOptions{
		BackgroundColor: "#ff00aa",
		Font:            &font,
	})
	if err != nil {
		t.Fatalf("buildStatusTextMessage: %v", err)
	}

	ext := msg.GetExtendedTextMessage()
	if ext == nil {
		t.Fatalf("missing ExtendedTextMessage")
	}
	if ext.GetText() != "hello status" {
		t.Fatalf("text = %q", ext.GetText())
	}
	if ext.BackgroundArgb == nil || ext.GetBackgroundArgb() != 0xffff00aa {
		t.Fatalf("backgroundArgb = %#x", ext.GetBackgroundArgb())
	}
	if ext.Font == nil || ext.GetFont() != waProto.ExtendedTextMessage_FontType(font) {
		t.Fatalf("font = %v", ext.GetFont())
	}
}

func TestSendStatusCommandExposesMediaFlags(t *testing.T) {
	cmd := newSendStatusCmd(&rootFlags{})
	for _, name := range []string{"file", "mime", "message", "post-send-wait"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("missing --%s flag", name)
		}
	}
}

func TestStatusBroadcastJIDConstant(t *testing.T) {
	if types.StatusBroadcastJID.String() != "status@broadcast" {
		t.Fatalf("status broadcast JID = %q", types.StatusBroadcastJID.String())
	}
}

func TestSendStatusTextDelegatesThroughSendSocketWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
		return sendDelegateResponse{OK: true, Sent: true, To: "status@broadcast", ID: "status-id"}
	})
	defer server.stop()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "750ms",
		"send", "status", "--message", "hello status", "--background-color", "#112233", "--font", "7",
		"--post-send-wait", "25ms",
	})
	if err != nil {
		t.Fatalf("send status failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}

	req := server.nextRequest(t)
	if req.Version != sendDelegateVersion || req.Kind != "status" {
		t.Fatalf("delegate version/kind = %d/%q", req.Version, req.Kind)
	}
	if req.Message != "hello status" || req.BackgroundColor != "#112233" || req.Font == nil || *req.Font != 7 {
		t.Fatalf("delegate status options = %+v", req)
	}
	if req.TimeoutMS != 750 || req.PostSendWaitMS != 25 {
		t.Fatalf("delegate timeouts = command %dms post-send %dms", req.TimeoutMS, req.PostSendWaitMS)
	}
	if strings.Contains(stderr, "store is locked") || strings.Contains(stderr, "not authenticated") || strings.Contains(stderr, "not connected") {
		t.Fatalf("delegated command tried the direct store/client path: stderr=%q", stderr)
	}
	for _, want := range []string{`"sent":true`, `"to":"status@broadcast"`, `"id":"status-id"`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout %q missing %s", stdout, want)
		}
	}
}

func TestSendStatusMediaDelegatesCaptionAndAbsolutePathWhenStoreLocked(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
		return sendDelegateResponse{
			OK: true, Sent: true, To: "status@broadcast", ID: "media-id", File: map[string]string{"name": "promo.jpg"},
		}
	})
	defer server.stop()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "750ms",
		"send", "status", "--file", "promo.jpg", "--message", "caption", "--mime", "image/jpeg",
	})
	if err != nil {
		t.Fatalf("send status media failed: %v stdout=%q stderr=%q", err, stdout, stderr)
	}

	req := server.nextRequest(t)
	if req.Kind != "status" || req.Message != "caption" || req.MIME != "image/jpeg" {
		t.Fatalf("delegate media options = %+v", req)
	}
	// The daemon runs in another directory; a relative path would not resolve.
	if !filepath.IsAbs(req.File) || filepath.Base(req.File) != "promo.jpg" {
		t.Fatalf("delegated file = %q, want an absolute path to promo.jpg", req.File)
	}
	if !strings.Contains(stdout, `"id":"media-id"`) {
		t.Fatalf("stdout %q missing the delegated id", stdout)
	}
}

func TestSendStatusOldDaemonRefusalIsSurfacedNotRetriedLocally(t *testing.T) {
	skipPresenceDelegateSocketTestOnUnsupportedOS(t)
	storeDir := shortPresenceDelegateStoreDir(t)
	lk, err := lock.Acquire(storeDir)
	if err != nil {
		t.Fatalf("lock store: %v", err)
	}
	defer lk.Release()

	server := startPresenceDelegateTestSocket(t, storeDir, func(req sendDelegateRequest) sendDelegateResponse {
		return sendDelegateResponse{OK: false, Error: `unsupported send kind "status"`}
	})
	defer server.stop()

	stdout, stderr, err := runPresenceDelegateHelper(t, []string{
		"--store", storeDir, "--json", "--timeout", "750ms", "send", "status", "--message", "hi",
	})
	if err == nil {
		t.Fatalf("expected a failure from a daemon that predates delegated statuses, stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "unsupported send kind") {
		t.Fatalf("stderr %q does not name the daemon refusal", stderr)
	}
}

func TestExecuteDelegatedStatusRequiresContentBeforeAppUse(t *testing.T) {
	_, err := executeDelegatedSend(context.Background(), nil, sendDelegateRequest{
		Version: sendDelegateVersion, Kind: "status", Message: "  ",
	})
	if err == nil || !strings.Contains(err.Error(), "--message or --file is required") {
		t.Fatalf("empty delegated status error = %v", err)
	}
}

func TestSendDelegateRequestPreservesStatusFieldsInJSON(t *testing.T) {
	font := int32(0)
	raw, err := json.Marshal(sendDelegateRequest{Kind: "status", BackgroundColor: "#ff00aa", Font: &font})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got sendDelegateRequest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Font 0 is a real font (the system one); it must not be dropped as unset.
	if got.Font == nil || *got.Font != 0 || got.BackgroundColor != "#ff00aa" {
		t.Fatalf("round-tripped status fields = %+v", got)
	}
}
