package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

type chatStateOptions struct {
	chat string
	pick int
}

func newChatsArchiveCmd(flags *rootFlags, archive bool) *cobra.Command {
	use, short := "archive", "Archive a chat"
	if !archive {
		use, short = "unarchive", "Unarchive a chat"
	}
	opts := chatStateOptions{}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChatState(flags, opts, use, 0)
		},
	}
	addChatStateFlags(cmd, &opts)
	return cmd
}

func newChatsPinCmd(flags *rootFlags, pin bool) *cobra.Command {
	use, short := "pin", "Pin a chat"
	if !pin {
		use, short = "unpin", "Unpin a chat"
	}
	opts := chatStateOptions{}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChatState(flags, opts, use, 0)
		},
	}
	addChatStateFlags(cmd, &opts)
	return cmd
}

func newChatsMuteCmd(flags *rootFlags) *cobra.Command {
	opts := chatStateOptions{}
	var duration time.Duration
	cmd := &cobra.Command{
		Use:   "mute",
		Short: "Mute a chat",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChatState(flags, opts, "mute", duration)
		},
	}
	addChatStateFlags(cmd, &opts)
	cmd.Flags().DurationVar(&duration, "duration", 0, "mute duration (for example 8h, 24h, 168h); 0 means forever")
	return cmd
}

func newChatsUnmuteCmd(flags *rootFlags) *cobra.Command {
	opts := chatStateOptions{}
	cmd := &cobra.Command{
		Use:   "unmute",
		Short: "Unmute a chat",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChatState(flags, opts, "unmute", 0)
		},
	}
	addChatStateFlags(cmd, &opts)
	return cmd
}

func newChatsMarkReadCmd(flags *rootFlags, read bool) *cobra.Command {
	use, short := "mark-read", "Mark a chat as read"
	if !read {
		use, short = "mark-unread", "Mark a chat as unread"
	}
	opts := chatStateOptions{}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChatState(flags, opts, use, 0)
		},
	}
	addChatStateFlags(cmd, &opts)
	return cmd
}

type chatStateApp interface {
	ArchiveChat(context.Context, types.JID, bool) error
	PinChat(context.Context, types.JID, bool) error
	MuteChat(context.Context, types.JID, bool, time.Duration) error
	MarkChatRead(context.Context, types.JID, bool) error
}

type chatStateRun func(context.Context, chatStateApp, types.JID) error

func chatStateRunner(action string, muteDuration time.Duration) (chatStateRun, error) {
	switch action {
	case "archive", "unarchive":
		archive := action == "archive"
		return func(ctx context.Context, a chatStateApp, jid types.JID) error {
			return a.ArchiveChat(ctx, jid, archive)
		}, nil
	case "pin", "unpin":
		pin := action == "pin"
		return func(ctx context.Context, a chatStateApp, jid types.JID) error {
			return a.PinChat(ctx, jid, pin)
		}, nil
	case "mute":
		return func(ctx context.Context, a chatStateApp, jid types.JID) error {
			return a.MuteChat(ctx, jid, true, muteDuration)
		}, nil
	case "unmute":
		return func(ctx context.Context, a chatStateApp, jid types.JID) error {
			return a.MuteChat(ctx, jid, false, 0)
		}, nil
	case "mark-read", "mark-unread":
		read := action == "mark-read"
		return func(ctx context.Context, a chatStateApp, jid types.JID) error {
			return a.MarkChatRead(ctx, jid, read)
		}, nil
	default:
		return nil, fmt.Errorf("unsupported chat state action %q", action)
	}
}

// chatStateDelegateRequest picks the delegate protocol for a chat-state verb.
//
// `mark-read`/`mark-unread` keep upstream's dedicated `mark_read` kind so this
// fork does not diverge on a verb upstream already delegates. The remaining
// verbs (archive/pin/mute and their inverses) are not delegated upstream at
// all and use `chat_state`, which the daemon serves OUTSIDE the send
// serializer: their pre-write app-state sync can take minutes on a large
// collection and must never stall delegated sends queued behind it.
func chatStateDelegateRequest(action string, opts chatStateOptions, muteDuration time.Duration) sendDelegateRequest {
	if action == "mark-read" || action == "mark-unread" {
		read := action == "mark-read"

		return sendDelegateRequest{
			Kind: "mark_read",
			To:   opts.chat,
			Pick: opts.pick,
			Read: &read,
		}
	}

	return sendDelegateRequest{
		Kind:            "chat_state",
		ChatStateAction: action,
		To:              opts.chat,
		Pick:            opts.pick,
		MuteDurationMS:  durationMillis(muteDuration),
	}
}

func runChatState(flags *rootFlags, opts chatStateOptions, action string, muteDuration time.Duration) error {
	if strings.TrimSpace(opts.chat) == "" {
		return fmt.Errorf("--chat is required")
	}
	if err := flags.requireWritable(); err != nil {
		return err
	}
	run, err := chatStateRunner(action, muteDuration)
	if err != nil {
		return err
	}

	ctx, cancel := withTimeout(context.Background(), flags)
	defer cancel()

	a, lk, err := newApp(ctx, flags, true, false)
	if err != nil {
		resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, chatStateDelegateRequest(action, opts, muteDuration))
		if delegated {
			if delegateErr != nil {
				return delegateErr
			}
			chat := resp.To
			if chat == "" {
				chat = resp.Chat
			}
			return writeChatStateOutput(flags, action, chat)
		}
		return err
	}
	defer closeApp(a, lk)

	if err := a.EnsureAuthed(ctx); err != nil {
		return err
	}
	removePersistenceHandler, err := a.AddChatStatePersistenceHandler(ctx)
	if err != nil {
		return err
	}
	defer func() {
		// Keep the handler active until the socket is closed, then let App.Close
		// drain every persistence task before it closes the local database.
		a.WA().Disconnect()
		removePersistenceHandler()
	}()
	if err := a.Connect(ctx, false, nil); err != nil {
		return err
	}

	jid, err := resolveRecipient(a, opts.chat, recipientOptions{pick: opts.pick, asJSON: flags.asJSON})
	if err != nil {
		return err
	}
	if err := run(ctx, a, jid); err != nil {
		return err
	}
	return writeChatStateOutput(flags, action, jid.String())
}

func writeChatStateOutput(flags *rootFlags, action, chat string) error {
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, map[string]any{
			"ok":     true,
			"action": action,
			"chat":   chat,
		})
	}
	fmt.Fprintf(os.Stdout, "%s: %s\n", action, chat)
	return nil
}

func addChatStateFlags(cmd *cobra.Command, opts *chatStateOptions) {
	cmd.Flags().StringVar(&opts.chat, "chat", "", "chat name, phone number, or JID")
	cmd.Flags().IntVar(&opts.pick, "pick", 0, "choose match N when --chat is ambiguous")
}

func validateBoolFilter(name string, pos, neg bool) error {
	if pos && neg {
		return fmt.Errorf("--%s and --no-%s are mutually exclusive", name, name)
	}
	return nil
}

func boolFilter(pos, neg bool) *bool {
	if pos {
		v := true
		return &v
	}
	if neg {
		v := false
		return &v
	}
	return nil
}

func chatFlagsString(c store.Chat) string {
	var flags []string
	if c.Pinned {
		flags = append(flags, "pinned")
	}
	if c.Archived {
		flags = append(flags, "archived")
	}
	if c.Muted() {
		flags = append(flags, "muted")
	}
	if c.Unread {
		if c.UnreadCount > 0 {
			flags = append(flags, fmt.Sprintf("unread:%d", c.UnreadCount))
		} else {
			flags = append(flags, "unread")
		}
	}
	return strings.Join(flags, ",")
}

func formatMutedUntil(until int64) string {
	switch {
	case until == -1:
		return "forever"
	case until > 0:
		return time.Unix(until, 0).Local().Format(time.RFC3339)
	default:
		return ""
	}
}
