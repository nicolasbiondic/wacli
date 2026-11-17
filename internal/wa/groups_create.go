package wa

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/socket"
	"go.mau.fi/whatsmeow/types"
)

// ErrGroupCreateUnconfirmed marks a create IQ that left this device without a
// usable answer. WhatsApp may have created the group.
var ErrGroupCreateUnconfirmed = errors.New("the group create was sent but WhatsApp's answer was lost; it may still have gone through, so check the account's groups before creating it again")

// createGroupNoReplay sends the IQ whatsmeow's CreateGroup sends, with
// NoRetry set. CreateGroup goes through sendGroupIQ, which replays the saved
// frame after a reconnect; creating a group is not idempotent, so a lost
// answer must stay unknown instead of becoming a second create frame.
func createGroupNoReplay(ctx context.Context, cli *whatsmeow.Client, req whatsmeow.ReqCreateGroup) (*types.GroupInfo, error) {
	content, err := buildCreateGroupNode(ctx, cli, req)
	if err != nil {
		return nil, err
	}
	internals := cli.DangerousInternals()
	resp, err := internals.SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "w:g2",
		Type:      whatsmeow.DangerousInfoQueryType("set"),
		To:        types.GroupServerJID,
		Content:   []waBinary.Node{content},
		NoRetry:   true,
	})
	if err != nil {
		return nil, groupCreateOutcome(err)
	}
	groupNode, ok := resp.GetOptionalChildByTag("group")
	if !ok {
		return nil, fmt.Errorf("%w: %w", ErrGroupCreateUnconfirmed, &whatsmeow.ElementMissingError{Tag: "group", In: "response to create group query"})
	}
	info, err := internals.ParseGroupNode(&groupNode)
	if err != nil {
		// WhatsApp answered with a group: it exists even if a field is odd.
		return info, fmt.Errorf("%w: %w", ErrGroupCreateUnconfirmed, err)
	}
	return info, nil
}

// groupCreateOutcome keeps what is known not to have created anything (an
// explicit WhatsApp refusal, or a frame that never left this device) and
// marks every other failure as unconfirmed.
func groupCreateOutcome(err error) error {
	switch {
	case isExplicitIQRefusal(err),
		errors.Is(err, whatsmeow.ErrNotConnected),
		errors.Is(err, whatsmeow.ErrClientIsNil),
		errors.Is(err, socket.ErrSocketClosed):
		return err
	}
	return fmt.Errorf("%w: %w", ErrGroupCreateUnconfirmed, err)
}

// isExplicitIQRefusal reports an actual <iq type="error"> stanza carrying an
// <error> with a code. sendIQ also returns *IQError for any other reply to the
// request ID (an <ack>, an iq of another type), which proves nothing about
// whether the group was created.
func isExplicitIQRefusal(err error) bool {
	var iqErr *whatsmeow.IQError
	if !errors.As(err, &iqErr) || iqErr.RawNode == nil || iqErr.ErrorNode == nil || iqErr.Code == 0 {
		return false
	}
	iqType, _ := iqErr.RawNode.Attrs["type"].(string)
	return iqErr.RawNode.Tag == "iq" && iqType == "error"
}

// buildCreateGroupNode is whatsmeow's CreateGroup request body (group.go in
// both pinned SDKs), node for node. Its errors name participants by position,
// not JID, so phone numbers stay out of logs.
func buildCreateGroupNode(ctx context.Context, cli *whatsmeow.Client, req whatsmeow.ReqCreateGroup) (waBinary.Node, error) {
	participantNodes := make([]waBinary.Node, len(req.Participants), len(req.Participants)+1)
	participantNodes = append(participantNodes, waBinary.Node{
		Tag:     "member_add_mode",
		Content: string(cmp.Or(req.MemberAddMode, types.GroupMemberAddModeAllMember)),
	})
	for i, participant := range req.Participants {
		participant = participant.ToNonAD()
		var participantPN types.JID
		if participant.Server == types.HiddenUserServer {
			var err error
			participantPN, err = cli.Store.LIDs.GetPNForLID(ctx, participant)
			if err != nil {
				return waBinary.Node{}, fmt.Errorf("failed to get phone number for participant %d of %d: %v", i+1, len(req.Participants), err)
			}
		}
		participantAttrs := waBinary.Attrs{"jid": participant}
		if !participantPN.IsEmpty() {
			participantAttrs["phone_number"] = participantPN
		}
		participantNodes[i] = waBinary.Node{
			Tag:   "participant",
			Attrs: participantAttrs,
		}
		token, err := cli.DangerousInternals().EnsureTCToken(ctx, participant)
		if err != nil {
			return waBinary.Node{}, fmt.Errorf("failed to get privacy token for participant %d of %d: %v", i+1, len(req.Participants), err)
		} else if len(token) > 0 {
			participantNodes[i].Content = []waBinary.Node{{
				Tag:     "privacy",
				Content: token,
			}}
		}
	}
	if req.IsParent {
		if req.DefaultMembershipApprovalMode == "" {
			req.DefaultMembershipApprovalMode = "request_required"
		}
		participantNodes = append(participantNodes, waBinary.Node{
			Tag: "parent",
			Attrs: waBinary.Attrs{
				"default_membership_approval_mode": req.DefaultMembershipApprovalMode,
			},
		})
	} else if !req.LinkedParentJID.IsEmpty() {
		participantNodes = append(participantNodes, waBinary.Node{
			Tag:   "linked_parent",
			Attrs: waBinary.Attrs{"jid": req.LinkedParentJID},
		})
	}
	if req.IsLocked {
		participantNodes = append(participantNodes, waBinary.Node{Tag: "locked"})
	}
	if req.IsAnnounce {
		participantNodes = append(participantNodes, waBinary.Node{Tag: "announcement"})
	}
	if req.IsEphemeral {
		participantNodes = append(participantNodes, waBinary.Node{
			Tag: "ephemeral",
			Attrs: waBinary.Attrs{
				"expiration": req.DisappearingTimer,
				"trigger":    "1",
			},
		})
	} else {
		participantNodes = append(participantNodes, waBinary.Node{
			Tag:   "ephemeral",
			Attrs: waBinary.Attrs{"expiration": 0},
		})
	}
	approvalState := "off"
	if req.IsJoinApprovalRequired {
		approvalState = "on"
	}
	participantNodes = append(participantNodes, waBinary.Node{
		Tag: "membership_approval_mode",
		Content: []waBinary.Node{{
			Tag:   "group_join",
			Attrs: waBinary.Attrs{"state": approvalState},
		}},
	})
	createAttrs := waBinary.Attrs{}
	if req.Name != "" {
		createAttrs["subject"] = req.Name
	}
	return waBinary.Node{
		Tag:     "create",
		Attrs:   createAttrs,
		Content: participantNodes,
	}, nil
}
