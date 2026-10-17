package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	stdpng "image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

// statusRecorderWA is a fake WhatsApp client that records what the daemon-side
// status executor puts on the wire. Any other call panics through the embedded
// nil interface, which catches accidental coupling.
type statusRecorderWA struct {
	app.WAClient
	sentTo   []types.JID
	sentMsgs []*waProto.Message
	uploads  int
	sendErr  error
	sentID   types.MessageID
}

func (f *statusRecorderWA) Upload(_ context.Context, data []byte, _ whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	f.uploads++
	hash := sha256.Sum256(data)
	return whatsmeow.UploadResponse{FileSHA256: hash[:], FileLength: uint64(len(data)), DirectPath: "/v/t62/x"}, nil
}

func (f *statusRecorderWA) SendProtoMessage(_ context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error) {
	if f.sendErr != nil {
		return "", f.sendErr
	}
	f.sentTo = append(f.sentTo, to)
	f.sentMsgs = append(f.sentMsgs, msg)
	return f.sentID, nil
}

type statusExecutorApp struct {
	wa       *statusRecorderWA
	db       *store.DB
	storeDir string
}

func (a *statusExecutorApp) WA() app.WAClient { return a.wa }
func (a *statusExecutorApp) DB() *store.DB    { return a.db }
func (a *statusExecutorApp) StoreDir() string { return a.storeDir }
func (a *statusExecutorApp) Connect(context.Context, bool, func(string)) error {
	return errors.New("the executor must not reconnect on the happy path")
}

func newStatusExecutorApp(t *testing.T) *statusExecutorApp {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "wacli.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &statusExecutorApp{wa: &statusRecorderWA{sentID: "STATUS-ID"}, db: db, storeDir: dir}
}

func TestExecuteDelegatedStatusPostsStyledTextAndStoresIt(t *testing.T) {
	a := newStatusExecutorApp(t)
	font := int32(7)
	resp, err := executeDelegatedStatus(context.Background(), a, sendDelegateRequest{
		Version: sendDelegateVersion, Kind: "status", Message: "hello status",
		BackgroundColor: "#128C7E", Font: &font,
	})
	if err != nil {
		t.Fatalf("executeDelegatedStatus: %v", err)
	}
	if !resp.OK || !resp.Sent || resp.ID != "STATUS-ID" || resp.To != "status@broadcast" || resp.Error != "" || resp.StoreWarning != "" {
		t.Fatalf("response = %+v", resp)
	}
	if len(a.wa.sentTo) != 1 || a.wa.sentTo[0] != types.StatusBroadcastJID {
		t.Fatalf("destinations = %v, want exactly status@broadcast", a.wa.sentTo)
	}
	ext := a.wa.sentMsgs[0].GetExtendedTextMessage()
	if ext == nil || ext.GetText() != "hello status" || ext.GetBackgroundArgb() != 0xff128C7E ||
		ext.GetFont() != waProto.ExtendedTextMessage_FontType(font) {
		t.Fatalf("wire message = %+v", a.wa.sentMsgs[0])
	}
	row, err := a.db.GetStatusMessage("STATUS-ID")
	if err != nil {
		t.Fatalf("status_messages row: %v", err)
	}
	if !row.FromMe || row.Text != "hello status" || row.BackgroundColor != "#128C7E" || row.Font != 7 {
		t.Fatalf("stored status = %+v", row)
	}
}

func TestExecuteDelegatedStatusPostsMediaWithCaptionAndStoresIt(t *testing.T) {
	a := newStatusExecutorApp(t)
	img := filepath.Join(t.TempDir(), "promo.png")
	var png bytes.Buffer
	if err := stdpng.Encode(&png, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatalf("encode image: %v", err)
	}
	if err := os.WriteFile(img, png.Bytes(), 0o600); err != nil {
		t.Fatalf("write image: %v", err)
	}
	resp, err := executeDelegatedStatus(context.Background(), a, sendDelegateRequest{
		Version: sendDelegateVersion, Kind: "status", File: img, Message: "the caption", MIME: "image/png",
	})
	if err != nil {
		t.Fatalf("executeDelegatedStatus: %v", err)
	}
	if !resp.OK || !resp.Sent || resp.ID != "STATUS-ID" || resp.To != "status@broadcast" || resp.StoreWarning != "" {
		t.Fatalf("response = %+v", resp)
	}
	if resp.File["media"] != "image" || resp.File["mime_type"] != "image/png" || resp.File["name"] != "promo.png" {
		t.Fatalf("response file meta = %v", resp.File)
	}
	if a.wa.uploads != 1 || len(a.wa.sentTo) != 1 || a.wa.sentTo[0] != types.StatusBroadcastJID {
		t.Fatalf("uploads = %d, destinations = %v", a.wa.uploads, a.wa.sentTo)
	}
	sent := a.wa.sentMsgs[0].GetImageMessage()
	if sent == nil || sent.GetCaption() != "the caption" || sent.GetMimetype() != "image/png" {
		t.Fatalf("wire message = %+v", a.wa.sentMsgs[0])
	}
	row, err := a.db.GetStatusMessage("STATUS-ID")
	if err != nil {
		t.Fatalf("status_messages row: %v", err)
	}
	if !row.FromMe || row.MediaType != "image" || row.MediaCaption != "the caption" || row.Filename != "promo.png" || row.MimeType != "image/png" {
		t.Fatalf("stored status = %+v", row)
	}
}

func TestExecuteDelegatedStatusStoreFailureIsAWarningNotAFailure(t *testing.T) {
	a := newStatusExecutorApp(t)
	// The send already succeeded when the store is touched: break it first.
	if err := a.db.Close(); err != nil {
		t.Fatalf("close the store: %v", err)
	}
	resp, err := executeDelegatedStatus(context.Background(), a, sendDelegateRequest{
		Version: sendDelegateVersion, Kind: "status", Message: "delivered anyway",
	})
	if err != nil {
		t.Fatalf("a delivered status must not fail: %v", err)
	}
	if !resp.OK || resp.ID != "STATUS-ID" || resp.StoreWarning == "" {
		t.Fatalf("response = %+v, want delivered with a store warning", resp)
	}
}

func TestExecuteDelegatedStatusSurfacesSendErrorsAndStoresNothing(t *testing.T) {
	a := newStatusExecutorApp(t)
	a.wa.sendErr = errors.New("server rejected the status")
	_, err := executeDelegatedStatus(context.Background(), a, sendDelegateRequest{
		Version: sendDelegateVersion, Kind: "status", Message: "nope",
	})
	if err == nil || !strings.Contains(err.Error(), "server rejected the status") {
		t.Fatalf("error = %v, want the send error", err)
	}
	if _, err := a.db.GetStatusMessage("STATUS-ID"); err == nil {
		t.Fatal("a failed send left a status_messages row behind")
	}
}
