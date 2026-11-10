package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/calls"
	"github.com/openclaw/wacli/internal/out"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow"
)

// Voice calls (experimental). `calls serve` holds its own WhatsApp connection
// for a store and answers calls through meowcaller; `calls answer`, `calls
// hangup` and `calls active` talk to it over a control socket in the store
// directory. Never point this at a production line: answering calls with an
// unofficial client is a ban risk, so it is meant for a lab number in its own
// store (see docs/calls-answer-lab.md).

func newCallsServeCmd(flags *rootFlags) *cobra.Command {
	var answerMode string
	var allow []string
	var answerDelay time.Duration
	var audioIn string
	var audioOut string
	var maxDuration time.Duration
	var logLevel string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Connect and answer voice calls (experimental; lab numbers only)",
		Long: `Connects the store's session and waits for voice calls.

In manual mode (default) every ring is reported and someone must run
` + "`wacli calls answer --call-id … --audio-in … --audio-out …`" + `. In auto mode calls
from --allow (or anyone) are answered after --answer-delay with the audio
given here. This command keeps its own connection: do not run it next to
` + "`wacli sync`" + ` on the same store.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			policy, err := calls.ParsePolicy(answerMode, allow, answerDelay)
			if err != nil {
				return err
			}
			plan, err := calls.ParseAudioPlan(audioIn, audioOut)
			if err != nil {
				return err
			}
			level, err := zerolog.ParseLevel(strings.ToLower(strings.TrimSpace(logLevel)))
			if err != nil {
				return fmt.Errorf("--log-level: %w", err)
			}

			events := out.NewEventWriter(os.Stderr, flags.events)
			ctx, stop := signalContextWithEvents(events)
			defer stop()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)
			if err := a.OpenWA(); err != nil {
				return err
			}
			raw, ok := a.WA().(interface{ Whatsmeow() *whatsmeow.Client })
			if !ok || raw.Whatsmeow() == nil {
				return fmt.Errorf("session store is not open")
			}

			var logOut io.Writer = os.Stderr
			if flags.events {
				logOut = io.Discard
			}
			logger := zerolog.New(zerolog.ConsoleWriter{Out: logOut, TimeFormat: time.TimeOnly}).Level(level).With().Timestamp().Logger()

			svc, err := calls.New(raw.Whatsmeow(), calls.Options{
				Policy:      policy,
				AutoPlan:    plan,
				MaxDuration: maxDuration,
				Logger:      logger,
				OnEvent: func(event string, fields map[string]any) {
					if events.Enabled() {
						_ = events.Emit(event, fields)
						return
					}
					fmt.Fprintf(os.Stderr, "%s %s %s\n", time.Now().Format(time.TimeOnly), event, formatEventFields(fields))
				},
			})
			if err != nil {
				return err
			}

			sockPath := calls.ControlSocketPath(a.StoreDir())
			ctl, err := calls.ListenControl(sockPath, svc)
			if err != nil {
				return err
			}
			defer ctl.Close()
			go func() { _ = ctl.Serve(ctx) }()

			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Connected. Waiting for calls (mode=%s, audio %s). Control socket: %s\n", policy.Mode, plan.Describe(), sockPath)

			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			svc.Shutdown(shutdownCtx)
			return nil
		},
	}
	cmd.Flags().StringVar(&answerMode, "answer", "manual", "manual (report only), auto (answer allowed callers) or reject")
	cmd.Flags().StringSliceVar(&allow, "allow", nil, "callers that may be auto-answered (phone numbers or JIDs; default: anyone)")
	cmd.Flags().DurationVar(&answerDelay, "answer-delay", 2*time.Second, "wait before auto-answering")
	cmd.Flags().StringVar(&audioIn, "audio-in", "silence", "what the caller hears on auto-answer: file (.wav/.mp3/.opus/.pcm), rtp://host:port, echo or silence")
	cmd.Flags().StringVar(&audioOut, "audio-out", "none", "where the caller's voice goes on auto-answer: file.wav, rtp://host:port, rtp://auto:0 (reply to the RTP sender), echo or none")
	cmd.Flags().DurationVar(&maxDuration, "max-duration", 10*time.Minute, "hang up answered calls after this long (0 = never)")
	cmd.Flags().StringVar(&logLevel, "log-level", "info", "meowcaller log level (trace, debug, info, warn, error)")
	return cmd
}

func newCallsAnswerCmd(flags *rootFlags) *cobra.Command {
	var callID string
	var audioIn string
	var audioOut string
	cmd := &cobra.Command{
		Use:   "answer",
		Short: "Answer a ringing call in a running `wacli calls serve`",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			if _, err := calls.ParseAudioPlan(audioIn, audioOut); err != nil {
				return err
			}
			return runCallsControl(flags, calls.ControlRequest{Op: "answer", CallID: callID, AudioIn: audioIn, AudioOut: audioOut})
		},
	}
	cmd.Flags().StringVar(&callID, "call-id", "", "call id reported by `calls serve` (required)")
	cmd.Flags().StringVar(&audioIn, "audio-in", "silence", "what the caller hears: file (.wav/.mp3/.opus/.pcm), rtp://host:port, echo or silence")
	cmd.Flags().StringVar(&audioOut, "audio-out", "none", "where the caller's voice goes: file.wav, rtp://host:port, rtp://auto:0, echo or none")
	_ = cmd.MarkFlagRequired("call-id")
	return cmd
}

func newCallsHangupCmd(flags *rootFlags) *cobra.Command {
	var callID string
	cmd := &cobra.Command{
		Use:   "hangup",
		Short: "Hang up (or reject) a call in a running `wacli calls serve`",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			return runCallsControl(flags, calls.ControlRequest{Op: "hangup", CallID: callID})
		},
	}
	cmd.Flags().StringVar(&callID, "call-id", "", "call id (required)")
	_ = cmd.MarkFlagRequired("call-id")
	return cmd
}

func newCallsActiveCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "active",
		Short: "List ringing, active and recently ended calls in a running `wacli calls serve`",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCallsControl(flags, calls.ControlRequest{Op: "list"})
		},
	}
}

func runCallsControl(flags *rootFlags, req calls.ControlRequest) error {
	storeDir, err := resolveStoreDir(flags)
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(context.Background(), flags)
	defer cancel()
	resp, err := calls.SendControl(ctx, calls.ControlSocketPath(storeDir), req)
	if err != nil {
		return err
	}
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, map[string]any{"calls": resp.Calls})
	}
	return writeLiveCalls(os.Stdout, resp.Calls, fullTableOutput(flags.fullOutput))
}

func writeLiveCalls(dst io.Writer, list []calls.CallInfo, fullOutput bool) error {
	w := newTableWriter(dst)
	fmt.Fprintln(w, "CALL ID\tPEER\tSTATE\tRINGING\tANSWERED\tENDED\tAUDIO")
	for _, c := range list {
		ended := ""
		if !c.EndedAt.IsZero() {
			ended = c.EndedAt.Local().Format("15:04:05")
			if c.EndReason != "" {
				ended += " (" + c.EndReason + ")"
			}
		}
		answered := ""
		if !c.AnsweredAt.IsZero() {
			answered = c.AnsweredAt.Local().Format("15:04:05")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			tableCell(c.CallID, 36, fullOutput),
			tableCell(c.Peer, 28, fullOutput),
			c.State,
			c.RingingAt.Local().Format("15:04:05"),
			answered,
			tableCell(ended, 28, fullOutput),
			tableCell(c.Audio, 40, fullOutput),
		)
	}
	return w.Flush()
}

func formatEventFields(fields map[string]any) string {
	parts := make([]string, 0, len(fields))
	for _, k := range []string{"call_id", "peer", "audio", "reason", "duration_s", "rtp_in", "rtp_out", "rtp_lost", "video"} {
		if v, ok := fields[k]; ok {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return strings.Join(parts, " ")
}
