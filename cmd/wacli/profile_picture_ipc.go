package main

import (
	"context"
	"errors"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

const profilePictureInfoKind = "profile_picture_info"

type profilePictureInfo struct {
	URL        string `json:"url"`
	ID         string `json:"id"`
	Type       string `json:"type"`
	DirectPath string `json:"directPath"`
}

type profilePictureClient interface {
	LinkedJID() string
	GetProfilePictureInfo(context.Context, types.JID, bool, string) (*types.ProfilePictureInfo, error)
}

// Only the follow owner's live connection is used. There is intentionally no
// standalone CLI command or connection fallback for this read operation.
func executeProfilePictureInfo(ctx context.Context, client profilePictureClient, req sendDelegateRequest) sendDelegateResponse {
	failure := func(code, message string) sendDelegateResponse {
		return sendDelegateResponse{ErrorCode: code, Error: message}
	}
	raw := req.JID
	if raw == "" {
		raw = client.LinkedJID()
		if raw == "" {
			return failure("unavailable", "account is not linked")
		}
	}
	jid, err := types.ParseJID(raw)
	if err != nil || jid.User == "" || strings.TrimSpace(raw) != raw ||
		(jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer && jid.Server != types.GroupServer) {
		return failure("invalid_jid", "jid must identify a user or group")
	}
	info, err := client.GetProfilePictureInfo(ctx, jid.ToNonAD(), req.Preview, "")
	switch {
	case errors.Is(err, whatsmeow.ErrProfilePictureNotSet):
		return failure("not_set", "profile picture is not set")
	case errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized):
		return failure("hidden", "profile picture is private")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return failure("timeout", "profile picture lookup timed out")
	case err != nil:
		return failure("lookup_failed", "profile picture lookup failed")
	case info == nil || info.URL == "":
		return failure("lookup_failed", "profile picture response has no URL")
	}
	return sendDelegateResponse{OK: true, ProfilePicture: &profilePictureInfo{
		URL: info.URL, ID: info.ID, Type: info.Type, DirectPath: info.DirectPath,
	}}
}
