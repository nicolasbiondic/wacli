package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

const (
	groupParticipantsKind = "group_participants"
	groupInviteLinkKind   = "group_invite_link"
)

func explainGroupDelegateError(err error, kind, action string) error {
	if err == nil || !strings.Contains(err.Error(), "unsupported send kind") || !strings.Contains(err.Error(), kind) {
		return err
	}
	return fmt.Errorf("the running sync process does not support delegated groups %s; restart `wacli sync` after upgrading, then run this again: %w", action, err)
}

func newGroupsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "groups",
		Short: "Group management",
	}
	cmd.AddCommand(newGroupsCreateCmd(flags))
	cmd.AddCommand(newGroupsListCmd(flags))
	cmd.AddCommand(newGroupsRefreshCmd(flags))
	cmd.AddCommand(newGroupsInfoCmd(flags))
	cmd.AddCommand(newGroupsRenameCmd(flags))
	cmd.AddCommand(newGroupsTopicCmd(flags, "topic"))
	cmd.AddCommand(newGroupsTopicCmd(flags, "description"))
	cmd.AddCommand(newGroupsAnnounceOnlyCmd(flags))
	cmd.AddCommand(newGroupsLockedCmd(flags))
	cmd.AddCommand(newGroupsParticipantsCmd(flags))
	cmd.AddCommand(newGroupsRequestsCmd(flags))
	cmd.AddCommand(newGroupsInviteCmd(flags))
	cmd.AddCommand(newGroupsJoinCmd(flags))
	cmd.AddCommand(newGroupsLeaveCmd(flags))
	cmd.AddCommand(newGroupsPruneCmd(flags))
	return cmd
}
