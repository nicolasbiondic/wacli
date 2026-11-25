package calls

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// DaemonMode is the --calls value of `wacli sync --follow`.
type DaemonMode string

const (
	// DaemonOff (the default) leaves the daemon exactly as without calls.
	DaemonOff DaemonMode = "off"
	// DaemonManual engages allowed callers and waits for `wacli calls answer`.
	DaemonManual DaemonMode = "manual"
	// DaemonAuto answers allowed callers after the answer delay.
	DaemonAuto DaemonMode = "auto"
)

// DaemonConfig is what `wacli sync --follow --calls …` asks for.
type DaemonConfig struct {
	Mode          DaemonMode
	Allow         []string
	AnswerDelay   time.Duration
	AudioIn       string
	AudioOut      string
	MaxDuration   time.Duration
	MaxConcurrent int
	RingTimeout   time.Duration
	LogLevel      string
}

// Enabled reports whether the daemon should load the call engine at all.
func (c DaemonConfig) Enabled() bool { return c.Mode != "" && c.Mode != DaemonOff }

// ParseDaemonMode validates --calls.
func ParseDaemonMode(v string) (DaemonMode, error) {
	switch m := DaemonMode(strings.ToLower(strings.TrimSpace(v))); m {
	case "", DaemonOff:
		return DaemonOff, nil
	case DaemonManual, DaemonAuto:
		return m, nil
	default:
		return "", fmt.Errorf("--calls must be off, manual or auto (got %q)", v)
	}
}

// Daemon is the call engine riding on a sync daemon's session.
type Daemon struct {
	Service *Service
	Hub     *EventHub
	ctl     *ControlServer
	cancel  context.CancelFunc
	done    chan struct{}
}

// DaemonDeps are the pieces of the running daemon the engine needs.
type DaemonDeps struct {
	// Client is the session's whatsmeow client, before it connects.
	Client *whatsmeow.Client
	// StoreDir is where the control socket goes.
	StoreDir string
	// Alternates resolves other identities of a caller (LID → phone number).
	Alternates func(types.JID) []types.JID
	// Emit receives every call event (stderr NDJSON or a log line).
	Emit func(event string, fields map[string]any)
	// LogOutput receives the engine's logs.
	Logger zerolog.Logger
}

// StartDaemon validates cfg, installs the engine on the session and opens the
// control socket. Call it before the client connects; Close it when sync ends.
func StartDaemon(ctx context.Context, cfg DaemonConfig, deps DaemonDeps) (*Daemon, error) {
	if !cfg.Enabled() {
		return nil, errors.New("calls: daemon mode is off")
	}
	policyMode := string(ModeManual)
	if cfg.Mode == DaemonAuto {
		policyMode = string(ModeAuto)
	}
	policy, err := ParsePolicy(policyMode, cfg.Allow, cfg.AnswerDelay)
	if err != nil {
		return nil, err
	}
	plan, err := ParseAudioPlan(cfg.AudioIn, cfg.AudioOut)
	if err != nil {
		return nil, err
	}
	if cfg.Mode == DaemonAuto {
		if err := plan.Validate(); err != nil {
			return nil, err
		}
	}
	if cfg.MaxConcurrent < 0 {
		return nil, errors.New("--calls-max-concurrent must not be negative")
	}
	hub := NewEventHub(8, 64)
	emit := func(event string, fields map[string]any) {
		hub.Publish(event, fields)
		if deps.Emit != nil {
			deps.Emit(event, fields)
		}
	}
	svc, err := New(deps.Client, Options{
		Policy:        policy,
		AutoPlan:      plan,
		MaxDuration:   cfg.MaxDuration,
		MaxConcurrent: cfg.MaxConcurrent,
		RingTimeout:   cfg.RingTimeout,
		SharedSession: true,
		Alternates:    deps.Alternates,
		Logger:        deps.Logger,
		OnEvent:       emit,
	})
	if err != nil {
		hub.Close()
		return nil, err
	}
	return startDaemonControl(ctx, svc, hub, deps.StoreDir)
}

// StartDaemonForEngine is StartDaemon over an arbitrary engine (tests).
func StartDaemonForEngine(ctx context.Context, eng Engine, opts Options, storeDir string) (*Daemon, error) {
	hub := NewEventHub(8, 64)
	inner := opts.OnEvent
	opts.OnEvent = func(event string, fields map[string]any) {
		hub.Publish(event, fields)
		if inner != nil {
			inner(event, fields)
		}
	}
	opts.SharedSession = true
	return startDaemonControl(ctx, NewForEngine(eng, opts), hub, storeDir)
}

func startDaemonControl(ctx context.Context, svc *Service, hub *EventHub, storeDir string) (*Daemon, error) {
	ctl, err := ListenControl(ControlSocketPath(storeDir), svc)
	if err != nil {
		hub.Close()
		svc.Shutdown(context.Background())
		return nil, err
	}
	ctl.WithEvents(hub)
	cctx, cancel := context.WithCancel(ctx)
	d := &Daemon{Service: svc, Hub: hub, ctl: ctl, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		_ = ctl.Serve(cctx)
	}()
	return d, nil
}

// SocketPath is the control socket of the daemon.
func (d *Daemon) SocketPath() string { return d.ctl.path }

// Close hangs up live calls, ends watchers and removes the socket.
func (d *Daemon) Close() {
	if d == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d.Service.Shutdown(ctx)
	d.Hub.Close()
	d.cancel()
	_ = d.ctl.Close()
	<-d.done
}
