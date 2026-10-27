package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

// Hold one slot through the mutation and its snapshot refresh. Different
// groups remain independent; canceled waiters share the IPC deadline budget.
type groupDelegateOrder struct {
	mu     sync.Mutex
	groups map[string]*groupDelegateSlot
}

type groupDelegateSlot struct {
	ready chan struct{}
	refs  int
}

var groupTargetPattern = regexp.MustCompile(`^[0-9]+(-[0-9]+)?(\.[0-9]+:[0-9]+|:[0-9]+)?@g\.us$`)

// ParseJID alone accepts empty users, extra @ parts and overflowing device IDs.
// Accept modern and legacy numeric group IDs, with bounded optional AD aliases.
func parseGroupTarget(raw string) (types.JID, error) {
	invalid := fmt.Errorf("invalid group JID %q: expected a non-empty numeric group @g.us", raw)
	if !groupTargetPattern.MatchString(raw) {
		return types.JID{}, invalid
	}
	user, _, _ := strings.Cut(raw, "@")
	if base, device, ok := strings.Cut(user, ":"); ok {
		if _, err := strconv.ParseUint(device, 10, 16); err != nil {
			return types.JID{}, invalid
		}
		if _, agent, ok := strings.Cut(base, "."); ok {
			if _, err := strconv.ParseUint(agent, 10, 8); err != nil {
				return types.JID{}, invalid
			}
		}
	}
	jid, err := types.ParseJID(raw)
	if err != nil || jid.User == "" || jid.Server != types.GroupServer {
		return types.JID{}, invalid
	}
	return jid.ToNonAD(), nil
}

func (o *groupDelegateOrder) acquire(ctx context.Context, group string) (func(), error) {
	jid, err := parseGroupTarget(group)
	if err != nil {
		return nil, err
	}
	group = jid.ToNonAD().String()
	o.mu.Lock()
	if o.groups == nil {
		o.groups = make(map[string]*groupDelegateSlot)
	}
	slot := o.groups[group]
	if slot == nil {
		slot = &groupDelegateSlot{ready: make(chan struct{}, 1)}
		slot.ready <- struct{}{}
		o.groups[group] = slot
	}
	slot.refs++
	o.mu.Unlock()
	drop := func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		slot.refs--
		if slot.refs == 0 {
			delete(o.groups, group)
		}
	}
	select {
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	case <-slot.ready:
	}
	release := func() { slot.ready <- struct{}{}; drop() }
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

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
