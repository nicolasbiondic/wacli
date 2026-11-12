package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

type earlyEchoWA struct {
	*keepUnreadWA
	echo func(context.Context, bool) (types.MessageID, error)
}

func TestKeepUnreadSendIntentCompletion(t *testing.T) {
	for _, sendErr := range []error{nil, errors.New("server rejected send"), context.DeadlineExceeded, context.Canceled} {
		name := "accepted"
		if sendErr != nil {
			name = sendErr.Error()
		}
		t.Run(name, func(t *testing.T) {
			a, err := app.New(app.Options{StoreDir: t.TempDir(), AllowUnauthed: true})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			db := a.DB()
			chat := "15550000001@s.whatsapp.net"
			if err := db.UpsertChat(chat, "dm", "Fixture", time.Now().Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			if err := db.SetChatUnreadCount(chat, 2); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var dispatchedID string
			_, err = sendWithUnreadPolicy(ctx, db, true, func(ctx context.Context) (types.MessageID, error) {
				dispatchedID = wa.MessageIDFromContext(ctx)
				return dispatchedID, sendErr
			})
			// runSendOperation cancels its attempt context after the operation
			// returns an ACK, before its caller persists the outbound row.
			cancel()
			if !errors.Is(err, sendErr) || dispatchedID == "" {
				t.Fatalf("send result: ID=%q err=%v", dispatchedID, err)
			}
			// An echo after success must still be opted out; an echo for a
			// failed/timed-out attempt must not retain the in-flight policy.
			if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat, MsgID: dispatchedID, Timestamp: time.Now(), FromMe: true, Text: "echo"}); err != nil {
				t.Fatal(err)
			}
			wantCount := 0
			if sendErr == nil {
				wantCount = 2
			}
			assertSendUnread(t, db, chat, sendErr == nil, wantCount)
		})
	}
}

func TestKeepUnreadOuterTimeoutClearsBlockedSendIntent(t *testing.T) {
	a, err := app.New(app.Options{StoreDir: t.TempDir(), AllowUnauthed: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	db := a.DB()
	chat := "15550000001@s.whatsapp.net"
	if err := db.UpsertChat(chat, "dm", "Fixture", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatUnreadCount(chat, 2); err != nil {
		t.Fatal(err)
	}
	started, finished, release := make(chan string, 1), make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := runSendAttempt(t.Context(), 50*time.Millisecond, func(ctx context.Context) (types.MessageID, error) {
			defer close(finished)
			return sendWithUnreadPolicy(ctx, db, true, func(ctx context.Context) (types.MessageID, error) {
				started <- wa.MessageIDFromContext(ctx)
				<-release // A transport that has not returned when the outer timeout fires.
				return "", ctx.Err()
			})
		})
		result <- err
	}()
	id := <-started
	defer func() { close(release); <-finished }()
	if err := <-result; err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("outer send did not time out: %v", err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat, MsgID: id, Timestamp: time.Now(), FromMe: true, Text: "after timeout"}); err != nil {
		t.Fatal(err)
	}
	assertSendUnread(t, db, chat, false, 0)
}

func (w *earlyEchoWA) SendText(ctx context.Context, _ types.JID, _ string) (types.MessageID, error) {
	return w.echo(ctx, false)
}
func (w *earlyEchoWA) SendProtoMessage(ctx context.Context, _ types.JID, _ *waProto.Message) (types.MessageID, error) {
	return w.echo(ctx, false)
}
func (w *earlyEchoWA) SendPoll(ctx context.Context, _ types.JID, _ string, _ []string, _ int, _ bool) (types.MessageID, error) {
	return w.echo(ctx, false)
}
func (w *earlyEchoWA) SendReaction(ctx context.Context, _ types.JID, _ types.JID, _ types.MessageID, _ string) (types.MessageID, error) {
	return w.echo(ctx, true)
}

type earlyEchoApp struct {
	*keepUnreadApp
	wa *earlyEchoWA
}

func (a *earlyEchoApp) WA() app.WAClient { return a.wa }

// The server/device echo crosses the store boundary before SendMessage returns
// its acknowledgement, so none of the post-send persistence has run yet.
func TestSendKeepUnreadEchoBeforePersistence(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, _, file := newSendFileFixture(t)
	for _, tc := range keepUnreadSendCases(t, file) {
		for _, delegated := range []bool{false, true} {
			for _, keep := range []bool{false, true} {
				for _, count := range []int{0, 2} {
					t.Run(fmt.Sprintf("%s/delegated=%v/keep=%v/count=%d", tc.name, delegated, keep, count), func(t *testing.T) {
						real, err := app.New(app.Options{StoreDir: t.TempDir(), AllowUnauthed: true})
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(real.Close)
						client := &keepUnreadWA{fakeSendFileWA: &fakeSendFileWA{sentID: "sent"}}
						a := &earlyEchoApp{keepUnreadApp: &keepUnreadApp{App: real, client: client}, wa: &earlyEchoWA{keepUnreadWA: client}}
						chat := "15550000001@s.whatsapp.net"
						db := a.DB()
						at := time.Now().Add(-time.Minute)
						if err := db.UpsertChat(chat, "dm", "Fixture", at); err != nil {
							t.Fatal(err)
						}
						button := store.Button{Type: "quick_reply", ID: "yes", DisplayText: "Yes", ResponseType: selectResponseButtons}
						if tc.name == "select_list" {
							button.Type, button.ResponseType = "list_row", selectResponseList
						}
						if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat, MsgID: "incoming", Timestamp: at, Text: "Please answer", Buttons: []store.Button{button}}); err != nil {
							t.Fatal(err)
						}
						if err := db.SetChatUnreadCount(chat, count); err != nil {
							t.Fatal(err)
						}
						if err := db.SetChatUnread(chat, true); err != nil {
							t.Fatal(err)
						}
						wantUnread, wantCount := keep || tc.name == "react", count
						if !wantUnread {
							wantCount = 0
						}
						a.wa.echo = func(ctx context.Context, reaction bool) (types.MessageID, error) {
							id := wa.MessageIDFromContext(ctx)
							if keep && id == "" {
								return "", fmt.Errorf("keep-unread send dispatched without a preallocated ID")
							}
							if id == "" {
								id = "sent"
							}
							echo := store.UpsertMessageParams{ChatJID: chat, MsgID: id, Timestamp: time.Now(), FromMe: true, Text: "early echo"}
							if reaction {
								echo.ReactionToID, echo.ReactionEmoji = "incoming", "👍"
							}
							if err := db.UpsertMessage(echo); err != nil {
								return "", err
							}
							state, err := db.GetChat(chat)
							if err != nil || state.Unread != wantUnread || state.UnreadCount != wantCount {
								return "", fmt.Errorf("early echo changed unread to (%v,%d), want (%v,%d): %v", state.Unread, state.UnreadCount, wantUnread, wantCount, err)
							}
							return id, nil
						}
						flags := &rootFlags{storeDir: a.StoreDir(), asJSON: true, timeout: 5 * time.Second}
						open := func(context.Context) (outboundSendApp, func(), error) { return a, func() {}, nil }
						if delegated {
							stop, err := startSendDelegateServerForStore(t.Context(), a.StoreDir(), sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
								return tc.execute(ctx, a, req)
							})
							if err != nil {
								t.Fatal(err)
							}
							t.Cleanup(stop)
							open = func(context.Context) (outboundSendApp, func(), error) { return nil, nil, lock.ErrLocked }
						}
						cmd := tc.newCmd(flags, open)
						args := append([]string{"--to", chat, "--post-send-wait", "0"}, tc.args...)
						if keep {
							args = append(args, "--keep-unread")
						}
						cmd.SetArgs(args)
						captureRootStdout(t, func() {
							if err := cmd.Execute(); err != nil {
								t.Fatal(err)
							}
						})
						assertSendUnread(t, db, chat, wantUnread, wantCount)
					})
				}
			}
		}
	}
}
