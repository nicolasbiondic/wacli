package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func newMessagesForwardCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var id string
	var to string
	var pick int
	postSendWait := postSendRetryReceiptWait

	cmd := &cobra.Command{
		Use:   "forward",
		Short: "Forward a stored message",
		Long:  "Forward a stored message. Delegates to the running sync process when the store is locked.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(chat) == "" || strings.TrimSpace(id) == "" || strings.TrimSpace(to) == "" {
				return fmt.Errorf("--chat, --id, and --to are required")
			}
			if pick != 0 {
				return errForwardPick
			}
			// Refuse names before the store opens or anything is dispatched.
			if _, err := parseForwardRecipient(to); err != nil {
				return err
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{
					Kind:           forwardKind,
					Chat:           chat,
					ID:             id,
					To:             to,
					PostSendWaitMS: durationMillis(postSendWait),
				})
				if delegated {
					if delegateErr != nil {
						return delegateErr
					}
					warnSendStoreFailureMsg(os.Stderr, resp.ID, resp.StoreWarning)
					return writeForwardResult(flags, resp.To, resp.ID, resp.Source, resp.StoreWarning)
				}
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(ctx); err != nil {
				return err
			}
			plan, err := prepareForward(ctx, a, chat, id, to)
			if err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			sent, err := sendForward(ctx, a, plan)
			if err != nil {
				return err
			}
			warnSendStoreFailure(os.Stderr, sent.id, sent.storeErr)

			waitForPostSendRetryReceipts(ctx, postSendWait)

			return writeForwardResult(flags, sent.to.String(), sent.id, plan.source.MsgID, sent.storeWarning())
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "source chat JID or phone number")
	cmd.Flags().StringVar(&id, "id", "", "source message ID to forward")
	cmd.Flags().StringVar(&to, "to", "", "recipient JID or exact phone number (names are not looked up)")
	// Kept so older invocations fail with a clear message instead of an unknown flag.
	cmd.Flags().IntVar(&pick, "pick", 0, "not supported: forward takes a JID or phone number")
	_ = cmd.Flags().MarkHidden("pick")
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after forward so retry receipts can be handled (0 disables)")
	return cmd
}

const forwardKind = "forward"

// forwardApp is what the standalone command and the sync owner share to
// forward a stored message.
type forwardApp interface {
	messageChatApp
	WA() app.WAClient
	Connect(context.Context, bool, func(string)) error
}

type forwardPlan struct {
	source    store.Message
	mediaInfo *store.MediaDownloadInfo
	to        types.JID
}

type forwardOutcome struct {
	to       types.JID
	id       string
	storeErr error
}

func (o forwardOutcome) storeWarning() string {
	if o.storeErr == nil {
		return ""
	}
	return o.storeErr.Error()
}

var (
	errForwardRecipient = errors.New("--to must be a JID (containing @) or a phone number; forward does not look up names")
	errForwardPick      = errors.New("--pick does not apply: forward takes a JID or phone number, not a name")
)

// parseForwardRecipient accepts only an explicit JID or an exact phone number.
// Forward never resolves names: a fuzzy match could copy a stored message to
// the wrong person. The error never echoes the input or any candidate.
func parseForwardRecipient(to string) (types.JID, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return types.JID{}, fmt.Errorf("--to is required")
	}
	jid, err := wa.ParseUserOrJID(to)
	if err != nil {
		return types.JID{}, errForwardRecipient
	}
	return jid, nil
}

// prepareForward reads and validates the source message and recipient from
// the local store. It never touches the network.
func prepareForward(ctx context.Context, a messageChatApp, chat, id, to string) (forwardPlan, error) {
	toJID, err := parseForwardRecipient(to)
	if err != nil {
		return forwardPlan{}, err
	}
	source, _, err := loadMessageMutationTarget(ctx, a, chat, id)
	if err != nil {
		return forwardPlan{}, err
	}
	if err := validateMessageCanForward(source); err != nil {
		return forwardPlan{}, err
	}
	var mediaInfo *store.MediaDownloadInfo
	if strings.TrimSpace(source.MediaType) != "" {
		info, err := a.DB().GetMediaDownloadInfo(source.ChatJID, source.MsgID)
		if err != nil {
			return forwardPlan{}, err
		}
		mediaInfo = &info
	}
	if mediaInfo != nil && toJID.Server == types.NewsletterServer {
		return forwardPlan{}, fmt.Errorf("media forwarding to channels is not supported")
	}
	return forwardPlan{source: source, mediaInfo: mediaInfo, to: toJID}, nil
}

// sendForward sends a prepared forward on a connected client and records the
// outgoing copy. A local history failure is returned as storeErr, not as an
// error: the message was already delivered.
func sendForward(ctx context.Context, a forwardApp, plan forwardPlan) (forwardOutcome, error) {
	// The sync owner's stderr is a log: name the recipient's role, not its JID.
	toJID := warmupRecipientAs(ctx, a.WA(), plan.to, "the forward recipient", os.Stderr)
	if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
		return forwardOutcome{}, err
	}
	payload, err := buildForwardedMessage(plan.source, plan.mediaInfo)
	if err != nil {
		return forwardOutcome{}, err
	}
	sentID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
		return a.WA().SendProtoMessage(ctx, toJID, payload.Message)
	})
	if err != nil {
		var timeout *sendAttemptTimeoutError
		if errors.As(err, &timeout) {
			// The attempt may have reached WhatsApp before its deadline.
			return forwardOutcome{}, fmt.Errorf("forward may still have gone through, so check the recipient's chat before retrying: %w", err)
		}
		return forwardOutcome{}, err
	}

	now := time.Now().UTC()
	chatName := a.WA().ResolveChatName(ctx, toJID, "")
	var storeErr error
	if err := a.DB().UpsertChat(toJID.String(), chatKindFromJID(toJID), chatName, now); err != nil {
		storeErr = fmt.Errorf("chat update: %w", err)
	}
	if err := a.DB().UpsertMessage(store.UpsertMessageParams{
		ChatJID:         toJID.String(),
		ChatName:        chatName,
		MsgID:           string(sentID),
		SenderName:      "me",
		Timestamp:       now,
		FromMe:          true,
		Text:            payload.Text,
		DisplayText:     payload.Text,
		MediaType:       payload.MediaType,
		MediaCaption:    payload.MediaCaption,
		Filename:        payload.Filename,
		MimeType:        payload.MimeType,
		DirectPath:      payload.DirectPath,
		MediaKey:        payload.MediaKey,
		FileSHA256:      payload.FileSHA256,
		FileEncSHA256:   payload.FileEncSHA256,
		FileLength:      payload.FileLength,
		IsForwarded:     true,
		ForwardingScore: payload.ForwardingScore,
	}); err != nil {
		storeErr = errors.Join(storeErr, fmt.Errorf("message update: %w", err))
	}
	return forwardOutcome{to: toJID, id: string(sentID), storeErr: storeErr}, nil
}

func writeForwardResult(flags *rootFlags, to, id, source, storeWarning string) error {
	if flags.asJSON {
		body := map[string]any{
			"forwarded": true,
			"to":        to,
			"id":        id,
			"source":    source,
		}
		if storeWarning != "" {
			body["store_warning"] = storeWarning
		}
		return out.WriteJSON(os.Stdout, body)
	}
	fmt.Fprintf(os.Stdout, "Forwarded message %s to %s (id %s)\n", source, to, id)
	return nil
}

func validateMessageCanForward(msg store.Message) error {
	if msg.Revoked {
		return fmt.Errorf("message %s is deleted", msg.MsgID)
	}
	if msg.DeletedForMe {
		return fmt.Errorf("message %s was deleted for me", msg.MsgID)
	}
	if strings.TrimSpace(msg.ReactionToID) != "" {
		return fmt.Errorf("reaction messages cannot be forwarded")
	}
	mediaType := strings.ToLower(strings.TrimSpace(msg.MediaType))
	if mediaType != "" && !isStoredMediaType(mediaType) {
		return fmt.Errorf("%s messages cannot be forwarded", mediaType)
	}
	if mediaType == "" && strings.TrimSpace(messageForwardText(msg)) == "" {
		return fmt.Errorf("only text messages can be forwarded")
	}
	return nil
}

func messageForwardText(msg store.Message) string {
	if strings.TrimSpace(msg.Text) != "" {
		return msg.Text
	}
	if strings.TrimSpace(msg.DisplayText) != "" {
		return msg.DisplayText
	}
	return ""
}

type forwardedMessagePayload struct {
	Message         *waProto.Message
	Text            string
	MediaType       string
	MediaCaption    string
	Filename        string
	MimeType        string
	DirectPath      string
	MediaKey        []byte
	FileSHA256      []byte
	FileEncSHA256   []byte
	FileLength      uint64
	ForwardingScore uint32
}

func buildForwardedMessage(msg store.Message, mediaInfo *store.MediaDownloadInfo) (forwardedMessagePayload, error) {
	forwardingScore := msg.ForwardingScore + 1
	mediaType := strings.ToLower(strings.TrimSpace(msg.MediaType))
	if mediaType == "" {
		text := messageForwardText(msg)
		return forwardedMessagePayload{
			Message:         buildForwardedTextMessage(text, forwardingScore),
			Text:            text,
			ForwardingScore: forwardingScore,
		}, nil
	}
	if mediaInfo == nil {
		return forwardedMessagePayload{}, fmt.Errorf("message has no media metadata")
	}
	if err := validateStoredMediaInfo(*mediaInfo); err != nil {
		return forwardedMessagePayload{}, err
	}

	payload := forwardedMessagePayload{
		Text:            msg.MediaCaption,
		MediaType:       mediaType,
		MediaCaption:    msg.MediaCaption,
		Filename:        mediaInfo.Filename,
		MimeType:        mediaInfo.MimeType,
		DirectPath:      mediaInfo.DirectPath,
		MediaKey:        append([]byte(nil), mediaInfo.MediaKey...),
		FileSHA256:      append([]byte(nil), mediaInfo.FileSHA256...),
		FileEncSHA256:   append([]byte(nil), mediaInfo.FileEncSHA256...),
		FileLength:      mediaInfo.FileLength,
		ForwardingScore: forwardingScore,
	}
	ctx := forwardedContextInfo(forwardingScore)
	message, err := buildStoredMediaMessage(mediaType, msg.MediaCaption, *mediaInfo, ctx)
	if err != nil {
		return forwardedMessagePayload{}, err
	}
	payload.Message = message
	if mediaType == "document" && strings.TrimSpace(payload.Filename) == "" {
		payload.Filename = "document"
	}
	return payload, nil
}

func buildForwardedTextMessage(text string, forwardingScore uint32) *waProto.Message {
	return &waProto.Message{
		ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text:        proto.String(text),
			ContextInfo: forwardedContextInfo(forwardingScore),
		},
	}
}

func forwardedContextInfo(forwardingScore uint32) *waProto.ContextInfo {
	if forwardingScore == 0 {
		forwardingScore = 1
	}
	return &waProto.ContextInfo{
		IsForwarded:     proto.Bool(true),
		ForwardingScore: proto.Uint32(forwardingScore),
	}
}
