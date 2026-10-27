package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func newGroupsInviteCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "invite",
		Short: "Manage group invite links",
	}
	cmd.AddCommand(newGroupsInviteLinkCmd(flags))
	return cmd
}

func newGroupsInviteLinkCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Get or revoke invite links",
	}
	cmd.AddCommand(newGroupsInviteLinkGetCmd(flags))
	cmd.AddCommand(newGroupsInviteLinkRevokeCmd(flags))
	return cmd
}

func newGroupsInviteLinkGetCmd(flags *rootFlags) *cobra.Command {
	var jidStr string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get invite link",
		Long:  "Get invite link. Delegates to the running sync process when the store is locked.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(jidStr) == "" {
				return fmt.Errorf("--jid is required")
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}
			gjid, err := parseGroupTarget(jidStr)
			if err != nil {
				return err
			}
			jidStr = gjid.String()
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{Kind: groupInviteLinkKind, JID: jidStr, GroupAction: "get"})
				if delegated {
					if delegateErr != nil {
						return explainGroupDelegateError(delegateErr, groupInviteLinkKind, "invite link get")
					}
					return writeGroupInviteLinkResult(flags, resp.GroupInviteLink)
				}
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(ctx); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			result, err := executeGroupInviteLink(ctx, a.WA(), sendDelegateRequest{JID: jidStr, GroupAction: "get"})
			if err != nil {
				return err
			}
			return writeGroupInviteLinkResult(flags, result)
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	return cmd
}

func newGroupsInviteLinkRevokeCmd(flags *rootFlags) *cobra.Command {
	var jidStr string
	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke/reset invite link",
		Long:  "Revoke/reset invite link. Delegates to the running sync process when the store is locked.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(jidStr) == "" {
				return fmt.Errorf("--jid is required")
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}
			gjid, err := parseGroupTarget(jidStr)
			if err != nil {
				return err
			}
			jidStr = gjid.String()
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{Kind: groupInviteLinkKind, JID: jidStr, GroupAction: "revoke"})
				if delegated {
					if delegateErr != nil {
						return explainGroupDelegateError(delegateErr, groupInviteLinkKind, "invite link revoke")
					}
					return writeGroupInviteLinkResult(flags, resp.GroupInviteLink)
				}
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(ctx); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			result, err := executeGroupInviteLink(ctx, a.WA(), sendDelegateRequest{JID: jidStr, GroupAction: "revoke"})
			if err != nil {
				return err
			}
			return writeGroupInviteLinkResult(flags, result)
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	return cmd
}

type groupInviteLinkResult struct {
	JID     string `json:"jid"`
	Link    string `json:"link"`
	Revoked bool   `json:"revoked,omitempty"`
}

type groupInviteLinkClient interface {
	GetGroupInviteLink(context.Context, types.JID, bool) (string, error)
}

func executeGroupInviteLink(ctx context.Context, client groupInviteLinkClient, req sendDelegateRequest) (*groupInviteLinkResult, error) {
	if req.GroupAction != "get" && req.GroupAction != "revoke" {
		return nil, fmt.Errorf("unsupported group invite link action %q", req.GroupAction)
	}
	if strings.TrimSpace(req.JID) == "" {
		return nil, fmt.Errorf("--jid is required")
	}
	gjid, err := parseGroupTarget(req.JID)
	if err != nil {
		return nil, err
	}
	reset := req.GroupAction == "revoke"
	link, err := client.GetGroupInviteLink(ctx, gjid, reset)
	if err != nil {
		return nil, err
	}
	return &groupInviteLinkResult{JID: gjid.String(), Link: link, Revoked: reset}, nil
}

func writeGroupInviteLinkResult(flags *rootFlags, result *groupInviteLinkResult) error {
	if result == nil {
		return fmt.Errorf("the running sync process did not report invite link results; restart `wacli sync` after upgrading")
	}
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, result)
	}
	fmt.Fprintln(os.Stdout, result.Link)
	return nil
}

func newGroupsJoinCmd(flags *rootFlags) *cobra.Command {
	var code string
	cmd := &cobra.Command{
		Use:   "join",
		Short: "Join group by invite code",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(code) == "" {
				return fmt.Errorf("--code is required")
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(ctx); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			jid, err := a.WA().JoinGroupWithLink(ctx, code)
			if err != nil {
				return err
			}
			if info, err := a.WA().GetGroupInfo(ctx, jid); err == nil && info != nil {
				_ = persistGroupInfo(ctx, a.DB(), a.WA(), info)
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"jid": jid.String(), "joined": true})
			}
			fmt.Fprintf(os.Stdout, "Joined: %s\n", jid.String())
			return nil
		},
	}
	cmd.Flags().StringVar(&code, "code", "", "invite code (from link)")
	return cmd
}
