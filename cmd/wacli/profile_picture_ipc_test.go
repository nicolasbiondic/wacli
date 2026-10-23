package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

type pictureFake struct {
	jid     types.JID
	preview bool
	calls   int
	err     error
}

func (f *pictureFake) LinkedJID() string { return "123:4@s.whatsapp.net" }
func (f *pictureFake) GetProfilePictureInfo(_ context.Context, jid types.JID, preview bool, _ string) (*types.ProfilePictureInfo, error) {
	f.jid, f.preview = jid, preview
	f.calls++
	return &types.ProfilePictureInfo{URL: "https://pps.whatsapp.net/photo", ID: "p1", Type: "image", DirectPath: "/photo"}, f.err
}

func TestProfilePictureInfo(t *testing.T) {
	for _, tc := range []struct {
		jid, code string
		err       error
	}{
		{"", "", nil}, {"123@lid", "", nil}, {"123-456@g.us", "", nil},
		{"status@broadcast", "invalid_jid", nil}, {"123@newsletter", "invalid_jid", nil},
		{"123", "invalid_jid", nil}, {"@s.whatsapp.net", "invalid_jid", nil},
		{"123@s.whatsapp.net ", "invalid_jid", nil},
		{"", "not_set", fmt.Errorf("wrapped: %w", whatsmeow.ErrProfilePictureNotSet)},
		{"", "hidden", fmt.Errorf("wrapped: %w", whatsmeow.ErrProfilePictureUnauthorized)},
		{"", "timeout", context.DeadlineExceeded}, {"", "lookup_failed", fmt.Errorf("network")},
	} {
		t.Run(tc.jid+tc.code, func(t *testing.T) {
			f := &pictureFake{err: tc.err}
			resp := executeProfilePictureInfo(context.Background(), f, sendDelegateRequest{JID: tc.jid, Preview: true})
			if resp.ErrorCode != tc.code || resp.OK != (tc.code == "") {
				t.Fatalf("%+v", resp)
			}
			if tc.code == "invalid_jid" && f.calls != 0 {
				t.Fatal("queried invalid JID")
			}
			if resp.OK && (resp.ProfilePicture.ID != "p1" || !f.preview || f.jid.Device != 0) {
				t.Fatalf("%+v / %+v", resp, f)
			}
		})
	}
}

func TestProfilePictureBypassesSendQueue(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	// Empty slot blocks every send, but not a picture lookup.
	go handleSendDelegateConn(context.Background(), server, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		return executeProfilePictureInfo(ctx, &pictureFake{}, req), nil
	}, make(chan struct{}, 1), newSendPacer(sendSpacing{}))
	if err := json.NewEncoder(client).Encode(sendDelegateRequest{Version: 1, Kind: profilePictureInfoKind, TimeoutMS: 500}); err != nil {
		t.Fatal(err)
	}
	var resp sendDelegateResponse
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.ProfilePicture == nil {
		t.Fatalf("%+v", resp)
	}
}
