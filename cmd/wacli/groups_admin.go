package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	appcore "github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func newGroupsCreateCmd(flags *rootFlags) *cobra.Command {
	var name string
	var users []string
	var announceOnly bool
	var locked bool
	var joinApproval bool
	var parent bool
	var linkedParent string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a group",
		Long:  "Create a group. Delegates to the running sync process when the store is locked.",
		RunE: func(cmd *cobra.Command, args []string) error {
			create := groupCreateRequest{
				Name:         name,
				Users:        users,
				AnnounceOnly: announceOnly,
				Locked:       locked,
				JoinApproval: joinApproval,
				Community:    parent,
				LinkedParent: linkedParent,
			}
			// Reject bad input before the store opens or the request is sent.
			if _, err := parseGroupCreateRequest(create); err != nil {
				return err
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{Kind: groupCreateKind, GroupCreate: &create})
				if delegated {
					if delegateErr != nil {
						return explainGroupCreateDelegateError(delegateErr)
					}
					// A reply without the group, wrapper or not, is not a success:
					// the create was dispatched and may have happened.
					if resp.GroupCreate == nil || resp.GroupCreate.Info == nil {
						return fmt.Errorf("the running sync process did not report the created group; the group may exist, so check before creating it again, and restart `wacli sync` after upgrading")
					}
					return writeGroupCreateResult(flags, resp.GroupCreate.Info)
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

			info, err := executeGroupCreate(ctx, a.DB(), a.WA(), create)
			if err != nil {
				return err
			}
			return writeGroupCreateResult(flags, info)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "group name")
	cmd.Flags().StringSliceVar(&users, "user", nil, "initial participant phone number (+E164 and formatting ok) or JID (repeatable, 1-50, duplicates dropped)")
	cmd.Flags().BoolVar(&announceOnly, "announce-only", false, "only admins can send messages")
	cmd.Flags().BoolVar(&locked, "locked", false, "only admins can edit group info")
	cmd.Flags().BoolVar(&joinApproval, "join-approval", false, "require admin approval for new join requests")
	cmd.Flags().BoolVar(&parent, "community", false, "create a community parent group")
	cmd.Flags().StringVar(&linkedParent, "linked-parent", "", "community parent group JID for a new subgroup")
	return cmd
}

type groupCreateRequest struct {
	Name         string   `json:"name"`
	Users        []string `json:"users,omitempty"`
	AnnounceOnly bool     `json:"announce_only,omitempty"`
	Locked       bool     `json:"locked,omitempty"`
	JoinApproval bool     `json:"join_approval,omitempty"`
	Community    bool     `json:"community,omitempty"`
	LinkedParent string   `json:"linked_parent,omitempty"`
}

type groupCreateResult struct {
	Info *types.GroupInfo `json:"info"`
}

type groupCreateClient interface {
	groupJIDResolver
	CreateGroup(context.Context, wa.CreateGroupRequest) (*types.GroupInfo, error)
}

func parseGroupCreateRequest(req groupCreateRequest) (wa.CreateGroupRequest, error) {
	if strings.TrimSpace(req.Name) == "" {
		return wa.CreateGroupRequest{}, fmt.Errorf("--name is required")
	}
	if req.Community && strings.TrimSpace(req.LinkedParent) != "" {
		return wa.CreateGroupRequest{}, fmt.Errorf("--community and --linked-parent cannot be combined")
	}
	participants, err := parseGroupCreateParticipants(req.Users)
	if err != nil {
		return wa.CreateGroupRequest{}, err
	}
	var parentJID types.JID
	if strings.TrimSpace(req.LinkedParent) != "" {
		parentJID, err = parseGroupJID(req.LinkedParent)
		if err != nil {
			return wa.CreateGroupRequest{}, fmt.Errorf("parse --linked-parent: %w", err)
		}
	}
	return wa.CreateGroupRequest{
		Name:                   req.Name,
		Participants:           participants,
		IsAnnounce:             req.AnnounceOnly,
		IsLocked:               req.Locked,
		IsJoinApprovalRequired: req.JoinApproval,
		IsParent:               req.Community,
		LinkedParentJID:        parentJID,
	}, nil
}

// maxGroupCreateParticipants matches the gateway's POST /groups limit.
const maxGroupCreateParticipants = 50

// parseGroupCreateParticipants requires 1–50 distinct participants. Adding
// customers' numbers to new groups can restrict a line, so the list is never
// unbounded. The "invalid user" prefix is what callers classify as bad input;
// the text names counts, never numbers.
func parseGroupCreateParticipants(users []string) ([]types.JID, error) {
	if len(users) == 0 {
		return nil, fmt.Errorf("invalid user list: at least one --user is required")
	}
	parsed, err := parseGroupUserJIDs(users)
	if err != nil {
		return nil, err
	}
	seen := make(map[types.JID]bool, len(parsed))
	participants := parsed[:0]
	for _, jid := range parsed {
		key := jid.ToNonAD()
		if seen[key] {
			continue
		}
		seen[key] = true
		participants = append(participants, jid)
	}
	if len(participants) > maxGroupCreateParticipants {
		return nil, fmt.Errorf("invalid user list: at most %d participants per group, got %d", maxGroupCreateParticipants, len(participants))
	}
	return participants, nil
}

// executeGroupCreate makes exactly one CreateGroup call. Creating a group is
// not idempotent: an answer lost after dispatch may still have made the group,
// so neither path retries it.
func executeGroupCreate(ctx context.Context, db *store.DB, client groupCreateClient, req groupCreateRequest) (*types.GroupInfo, error) {
	create, err := parseGroupCreateRequest(req)
	if err != nil {
		return nil, err
	}
	info, err := client.CreateGroup(ctx, create)
	if err != nil {
		return nil, err
	}
	if info != nil {
		_ = persistGroupInfo(ctx, db, client, info)
	}
	return info, nil
}

// An older sync owner refuses the kind before doing anything. Keep its
// "unsupported send kind" text first, so callers that classify the error by
// prefix still see the refusal.
func explainGroupCreateDelegateError(err error) error {
	if err == nil || !strings.Contains(err.Error(), "unsupported send kind") || !strings.Contains(err.Error(), groupCreateKind) {
		return err
	}
	return fmt.Errorf("%w; the running sync process does not support delegated groups create, so nothing was created; restart `wacli sync` after upgrading, then run this again", err)
}

func writeGroupCreateResult(flags *rootFlags, info *types.GroupInfo) error {
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, info)
	}
	if info == nil {
		fmt.Fprintln(os.Stdout, "OK")
		return nil
	}
	fmt.Fprintf(os.Stdout, "JID: %s\nName: %s\n", info.JID.String(), sanitize(info.GroupName.Name))
	return nil
}

func newGroupsTopicCmd(flags *rootFlags, use string) *cobra.Command {
	var jidStr string
	var text string
	cmd := &cobra.Command{
		Use:   use,
		Short: "Set group topic/description",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(jidStr) == "" || !cmd.Flags().Changed("text") {
				return fmt.Errorf("--jid and --text are required")
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
			gjid, err := parseGroupJID(jidStr)
			if err != nil {
				return err
			}
			if err := a.WA().SetGroupTopic(ctx, gjid, text); err != nil {
				return err
			}
			if info, err := a.WA().GetGroupInfo(ctx, gjid); err == nil && info != nil {
				_ = persistGroupInfo(ctx, a.DB(), a.WA(), info)
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"jid": gjid.String(), "topic": text})
			}
			fmt.Fprintln(os.Stdout, "OK")
			return nil
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	cmd.Flags().StringVar(&text, "text", "", "new topic/description text (empty string clears it)")
	return cmd
}

func newGroupsAnnounceOnlyCmd(flags *rootFlags) *cobra.Command {
	return newGroupsToggleCmd(flags, "announce-only", "Set whether only admins can send messages", func(ctx context.Context, client appcore.WAClient, jid types.JID, enabled bool) error {
		return client.SetGroupAnnounce(ctx, jid, enabled)
	}, "announce_only")
}

func newGroupsLockedCmd(flags *rootFlags) *cobra.Command {
	return newGroupsToggleCmd(flags, "locked", "Set whether only admins can edit group info", func(ctx context.Context, client appcore.WAClient, jid types.JID, enabled bool) error {
		return client.SetGroupLocked(ctx, jid, enabled)
	}, "locked")
}

func newGroupsToggleCmd(flags *rootFlags, use, short string, apply func(context.Context, appcore.WAClient, types.JID, bool) error, jsonKey string) *cobra.Command {
	var jidStr string
	var on bool
	var off bool
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(jidStr) == "" {
				return fmt.Errorf("--jid is required")
			}
			enabled, err := parseOnOffFlags(cmd, on, off)
			if err != nil {
				return err
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
			gjid, err := parseGroupJID(jidStr)
			if err != nil {
				return err
			}
			if err := apply(ctx, a.WA(), gjid, enabled); err != nil {
				return err
			}
			if info, err := a.WA().GetGroupInfo(ctx, gjid); err == nil && info != nil {
				_ = persistGroupInfo(ctx, a.DB(), a.WA(), info)
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"jid": gjid.String(), jsonKey: enabled})
			}
			fmt.Fprintln(os.Stdout, "OK")
			return nil
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	cmd.Flags().BoolVar(&on, "on", false, "enable setting")
	cmd.Flags().BoolVar(&off, "off", false, "disable setting")
	return cmd
}

func newGroupsRequestsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "requests",
		Short: "Manage group join requests",
	}
	cmd.AddCommand(newGroupsRequestsListCmd(flags))
	cmd.AddCommand(newGroupsRequestsActionCmd(flags, "approve"))
	cmd.AddCommand(newGroupsRequestsActionCmd(flags, "reject"))
	return cmd
}

// requestListEntry is the JSON/text output shape for a single pending join request.
type requestListEntry struct {
	JID         string    `json:"JID"`
	PhoneNumber string    `json:"phone_number,omitempty"`
	RequestedAt time.Time `json:"RequestedAt"`
}

// resolveRequestEntries converts raw join-request participants to output entries,
// resolving LID JIDs to phone numbers via the provided resolver function.
func resolveRequestEntries(ctx context.Context, requests []types.GroupParticipantRequest, resolve func(context.Context, types.JID) types.JID) []requestListEntry {
	entries := make([]requestListEntry, len(requests))
	for i, req := range requests {
		pn := resolve(ctx, req.JID)
		phone := ""
		if pn.Server == types.DefaultUserServer {
			phone = "+" + pn.User
		}
		entries[i] = requestListEntry{
			JID:         req.JID.String(),
			PhoneNumber: phone,
			RequestedAt: req.RequestedAt,
		}
	}
	return entries
}

func newGroupsRequestsListCmd(flags *rootFlags) *cobra.Command {
	var jidStr string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List pending group join requests",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(jidStr) == "" {
				return fmt.Errorf("--jid is required")
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
			gjid, err := parseGroupJID(jidStr)
			if err != nil {
				return err
			}
			requests, err := a.WA().GetGroupRequestParticipants(ctx, gjid)
			if err != nil {
				return err
			}

			entries := resolveRequestEntries(ctx, requests, a.WA().ResolveLIDToPN)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, entries)
			}
			for _, e := range entries {
				fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", e.JID, e.RequestedAt.Local().Format("2006-01-02 15:04:05"), e.PhoneNumber)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	return cmd
}

func newGroupsRequestsActionCmd(flags *rootFlags, action string) *cobra.Command {
	var jidStr string
	var users []string
	cmd := &cobra.Command{
		Use:   action,
		Short: action + " group join requests",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(jidStr) == "" || len(users) == 0 {
				return fmt.Errorf("--jid and at least one --user are required")
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
			gjid, err := parseGroupJID(jidStr)
			if err != nil {
				return err
			}
			jids, err := parseGroupUserJIDs(users)
			if err != nil {
				return err
			}
			updated, err := a.WA().UpdateGroupRequestParticipants(ctx, gjid, jids, wa.GroupParticipantRequestAction(action))
			if err != nil {
				return err
			}
			if info, err := a.WA().GetGroupInfo(ctx, gjid); err == nil && info != nil {
				_ = persistGroupInfo(ctx, a.DB(), a.WA(), info)
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, updated)
			}
			fmt.Fprintln(os.Stdout, "OK")
			return nil
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	cmd.Flags().StringSliceVar(&users, "user", nil, "requesting user phone number (+E164 and formatting ok) or JID (repeatable)")
	return cmd
}

func parseOnOffFlags(cmd *cobra.Command, on, off bool) (bool, error) {
	onChanged := cmd.Flags().Changed("on")
	offChanged := cmd.Flags().Changed("off")
	if onChanged == offChanged {
		return false, fmt.Errorf("exactly one of --on or --off is required")
	}
	if onChanged {
		if !on {
			return false, fmt.Errorf("--on=false does not select a mode; use --off to disable")
		}
		return true, nil
	}
	if !off {
		return false, fmt.Errorf("--off=false does not select a mode; use --on to enable")
	}
	return false, nil
}

func parseGroupJID(raw string) (types.JID, error) {
	jid, err := types.ParseJID(strings.TrimSpace(raw))
	if err != nil {
		return types.JID{}, err
	}
	if jid.Server != types.GroupServer {
		return types.JID{}, fmt.Errorf("expected group JID ending in @g.us, got %s", jid.String())
	}
	return jid, nil
}

func parseGroupUserJIDs(users []string) ([]types.JID, error) {
	jids := make([]types.JID, 0, len(users))
	for _, user := range users {
		jid, err := wa.ParseUserOrJID(user)
		if err != nil {
			return nil, err
		}
		jids = append(jids, jid)
	}
	return jids, nil
}
