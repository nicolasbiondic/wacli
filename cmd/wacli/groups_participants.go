package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func newGroupsParticipantsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "participants",
		Short: "Manage group participants",
	}
	cmd.AddCommand(newGroupsParticipantsListCmd(flags))
	cmd.AddCommand(newGroupsParticipantsActionCmd(flags, "add"))
	cmd.AddCommand(newGroupsParticipantsActionCmd(flags, "remove"))
	cmd.AddCommand(newGroupsParticipantsActionCmd(flags, "promote"))
	cmd.AddCommand(newGroupsParticipantsActionCmd(flags, "demote"))
	return cmd
}

func newGroupsParticipantsListCmd(flags *rootFlags) *cobra.Command {
	var group string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List participants (local snapshot; sync --refresh-groups to update)",
		Long: `List participants from the last group snapshot saved in the local database.

This command does not connect to WhatsApp. The snapshot can be empty or stale.
Run "wacli sync --once --refresh-groups" to fetch joined groups and replace
their local participant snapshots.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(group) == "" {
				return fmt.Errorf("--jid is required")
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			participants, err := a.DB().ListGroupParticipants(group)
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, participants)
			}

			w := newTableWriter(os.Stdout)
			fmt.Fprintln(w, "USER JID\tROLE\tUPDATED")
			for _, p := range participants {
				updated := "-"
				if !p.UpdatedAt.IsZero() {
					updated = p.UpdatedAt.Local().Format("2006-01-02 15:04:05")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", p.UserJID, p.Role, updated)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&group, "jid", "", "group JID (…@g.us)")
	return cmd
}

func newGroupsParticipantsActionCmd(flags *rootFlags, action string) *cobra.Command {
	var group string
	var users []string
	cmd := &cobra.Command{
		Use:   action,
		Short: action + " participants",
		Long:  action + " participants. Delegates to the running sync process when the store is locked.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(group) == "" || len(users) == 0 {
				return fmt.Errorf("--jid and at least one --user are required")
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{Kind: groupParticipantsKind, JID: group, Users: users, GroupAction: action})
				if delegated {
					if delegateErr != nil {
						return explainGroupDelegateError(delegateErr, groupParticipantsKind, "participants "+action)
					}
					if resp.GroupParticipants == nil {
						return fmt.Errorf("the running sync process did not report participant results; restart `wacli sync` after upgrading")
					}
					return writeGroupParticipantsResult(flags, resp.GroupParticipants.Updated)
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

			updated, err := executeGroupParticipants(ctx, a.DB(), a.WA(), sendDelegateRequest{JID: group, Users: users, GroupAction: action})
			if err != nil {
				return err
			}
			return writeGroupParticipantsResult(flags, updated)
		},
	}
	cmd.Flags().StringVar(&group, "jid", "", "group JID (…@g.us)")
	cmd.Flags().StringSliceVar(&users, "user", nil, "user phone number (+E164 and formatting ok) or JID (repeatable)")
	return cmd
}

type groupParticipantsResult struct {
	Updated []types.GroupParticipant `json:"updated"`
}

type groupParticipantsClient interface {
	groupJIDResolver
	UpdateGroupParticipants(context.Context, types.JID, []types.JID, wa.GroupParticipantAction) ([]types.GroupParticipant, error)
	GetGroupInfo(context.Context, types.JID) (*types.GroupInfo, error)
}

// Both paths preserve WhatsApp's per-participant errors and refresh the snapshot
// best-effort, just as the standalone command does.
func executeGroupParticipants(ctx context.Context, db *store.DB, client groupParticipantsClient, req sendDelegateRequest) ([]types.GroupParticipant, error) {
	switch wa.GroupParticipantAction(req.GroupAction) {
	case wa.GroupParticipantAdd, wa.GroupParticipantRemove, wa.GroupParticipantPromote, wa.GroupParticipantDemote:
	default:
		return nil, fmt.Errorf("unknown participant action: %s", req.GroupAction)
	}
	if strings.TrimSpace(req.JID) == "" || len(req.Users) == 0 {
		return nil, fmt.Errorf("--jid and at least one --user are required")
	}
	gjid, err := types.ParseJID(req.JID)
	if err != nil {
		return nil, err
	}
	var jids []types.JID
	for _, user := range req.Users {
		jid, err := wa.ParseUserOrJID(user)
		if err != nil {
			return nil, err
		}
		jids = append(jids, jid)
	}
	updated, err := client.UpdateGroupParticipants(ctx, gjid, jids, wa.GroupParticipantAction(req.GroupAction))
	if err != nil {
		return nil, err
	}
	if info, err := client.GetGroupInfo(ctx, gjid); err == nil && info != nil {
		_ = persistGroupInfo(ctx, db, client, info)
	}
	return updated, nil
}

func writeGroupParticipantsResult(flags *rootFlags, updated []types.GroupParticipant) error {
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, updated)
	}
	fmt.Fprintln(os.Stdout, "OK")
	return nil
}
