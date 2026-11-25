package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	appPkg "github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/calls"
	"github.com/openclaw/wacli/internal/out"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// Voice calls on the daemon's own session (experimental). With --calls off
// (the default) none of this runs and `wacli sync` behaves exactly as before.
// With manual or auto, the call engine is installed on the same whatsmeow
// client before Sync connects it, so no extra linked device is used; `wacli
// calls answer|hangup|active|watch` reach it through <store>/calls.sock.

type syncCallsFlags struct {
	mode          string
	allow         []string
	answerDelay   time.Duration
	audioIn       string
	audioOut      string
	maxDuration   time.Duration
	maxConcurrent int
	ringTimeout   time.Duration
	logLevel      string
}

func (f *syncCallsFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.mode, "calls", string(calls.DaemonOff), "answer voice calls on this session (experimental): off, manual (wait for \"wacli calls answer\") or auto")
	cmd.Flags().StringSliceVar(&f.allow, "calls-allow", nil, "only engage calls from these numbers or JIDs; every other call rings on the other devices untouched (default: everyone)")
	cmd.Flags().DurationVar(&f.answerDelay, "calls-answer-delay", 2*time.Second, "wait before auto-answering (--calls auto)")
	cmd.Flags().StringVar(&f.audioIn, "calls-audio-in", "silence", "what the caller hears on auto-answer: file, rtp://host:port, echo or silence")
	cmd.Flags().StringVar(&f.audioOut, "calls-audio-out", "none", "where the caller's voice goes on auto-answer: file.wav, rtp://host:port, rtp://auto:0, echo or none")
	cmd.Flags().DurationVar(&f.maxDuration, "calls-max-duration", 10*time.Minute, "hang up answered calls after this long (0 = never)")
	cmd.Flags().IntVar(&f.maxConcurrent, "calls-max-concurrent", calls.DefaultMaxConcurrent, "calls answered at the same time; more stay ringing on the other devices")
	cmd.Flags().DurationVar(&f.ringTimeout, "calls-ring-timeout", calls.DefaultRingTimeout, "forget a ringing call that never ends")
	cmd.Flags().StringVar(&f.logLevel, "calls-log-level", "info", "call engine log level (trace, debug, info, warn, error)")
}

// config validates the flags. Every --calls-* flag needs --calls on, so a
// typo cannot silently leave a tenant half-configured.
func (f *syncCallsFlags) config(cmd *cobra.Command, notFollow bool) (calls.DaemonConfig, error) {
	mode, err := calls.ParseDaemonMode(f.mode)
	if err != nil {
		return calls.DaemonConfig{}, err
	}
	cfg := calls.DaemonConfig{
		Mode:          mode,
		Allow:         f.allow,
		AnswerDelay:   f.answerDelay,
		AudioIn:       f.audioIn,
		AudioOut:      f.audioOut,
		MaxDuration:   f.maxDuration,
		MaxConcurrent: f.maxConcurrent,
		RingTimeout:   f.ringTimeout,
		LogLevel:      f.logLevel,
	}
	if !cfg.Enabled() {
		for _, name := range []string{"calls-allow", "calls-answer-delay", "calls-audio-in", "calls-audio-out", "calls-max-duration", "calls-max-concurrent", "calls-ring-timeout", "calls-log-level"} {
			if cmd.Flags().Changed(name) {
				return calls.DaemonConfig{}, fmt.Errorf("--%s requires --calls manual or auto", name)
			}
		}
		return cfg, nil
	}
	if notFollow {
		return calls.DaemonConfig{}, fmt.Errorf("--calls needs follow mode (calls ring while the daemon runs)")
	}
	if cfg.AnswerDelay < 0 || cfg.MaxDuration < 0 || cfg.RingTimeout < 0 {
		return calls.DaemonConfig{}, fmt.Errorf("--calls-* durations must not be negative")
	}
	if cfg.MaxConcurrent < 1 {
		return calls.DaemonConfig{}, fmt.Errorf("--calls-max-concurrent must be at least 1")
	}
	if _, err := zerolog.ParseLevel(strings.ToLower(strings.TrimSpace(cfg.LogLevel))); err != nil {
		return calls.DaemonConfig{}, fmt.Errorf("--calls-log-level: %w", err)
	}
	if _, err := calls.ParseAudioPlan(cfg.AudioIn, cfg.AudioOut); err != nil {
		return calls.DaemonConfig{}, err
	}
	return cfg, nil
}

type rawWhatsmeow interface{ Whatsmeow() *whatsmeow.Client }

func startSyncCalls(ctx context.Context, a *appPkg.App, flags *rootFlags, cfg calls.DaemonConfig) (*calls.Daemon, error) {
	if err := a.OpenWA(); err != nil {
		return nil, err
	}
	waCli := a.WA()
	raw, ok := waCli.(rawWhatsmeow)
	if !ok || raw.Whatsmeow() == nil {
		return nil, fmt.Errorf("--calls: session store is not open")
	}
	level, _ := zerolog.ParseLevel(strings.ToLower(strings.TrimSpace(cfg.LogLevel)))
	var logOut io.Writer = os.Stderr
	if flags.events {
		logOut = io.Discard
	}
	logger := zerolog.New(zerolog.ConsoleWriter{Out: logOut, TimeFormat: time.TimeOnly, NoColor: true}).
		Level(level).With().Timestamp().Str("component", "calls").Logger()
	events := out.NewEventWriter(os.Stderr, flags.events)
	d, err := calls.StartDaemon(ctx, cfg, calls.DaemonDeps{
		Client:   raw.Whatsmeow(),
		StoreDir: a.StoreDir(),
		Alternates: func(j types.JID) []types.JID {
			if j.Server != types.HiddenUserServer {
				return nil
			}
			pn := waCli.ResolveLIDToPN(ctx, j)
			if pn.IsEmpty() || pn == j {
				return nil
			}
			return []types.JID{pn}
		},
		Emit: func(event string, fields map[string]any) {
			if events.Enabled() {
				_ = events.Emit(event, fields)
				return
			}
			fmt.Fprintf(os.Stderr, "calls: %s %s\n", event, formatEventFields(fields))
		},
		Logger: logger,
	})
	if err != nil {
		return nil, fmt.Errorf("--calls: %w", err)
	}
	allow := "everyone"
	if len(cfg.Allow) > 0 {
		allow = fmt.Sprintf("%d allowed caller(s)", len(cfg.Allow))
	}
	fmt.Fprintf(os.Stderr, "calls: engine on this session (mode=%s, %s, max %d answered); control socket %s\n", cfg.Mode, allow, cfg.MaxConcurrent, d.SocketPath())
	return d, nil
}
