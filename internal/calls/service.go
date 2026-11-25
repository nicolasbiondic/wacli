package calls

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// ErrCallNotFound is returned for an unknown or already ended call id.
var ErrCallNotFound = errors.New("call not found")

// ErrBusy is returned when answering would exceed Options.MaxConcurrent.
var ErrBusy = errors.New("too many answered calls")

// CallInfo is the public snapshot of a tracked call.
type CallInfo struct {
	CallID     string    `json:"call_id"`
	Peer       string    `json:"peer"`
	State      string    `json:"state"`
	Video      bool      `json:"video,omitempty"`
	RingingAt  time.Time `json:"ringing_at"`
	AnsweredAt time.Time `json:"answered_at,omitempty"`
	EndedAt    time.Time `json:"ended_at,omitempty"`
	EndReason  string    `json:"end_reason,omitempty"`
	Audio      string    `json:"audio,omitempty"`
	Stats      *BridgeStats
}

// Defaults for the resource caps.
const (
	DefaultMaxConcurrent = 1
	DefaultMaxTracked    = 32
	DefaultRingTimeout   = 2 * time.Minute
	endedRetention       = 5 * time.Minute
)

// Options configure a Service.
type Options struct {
	Policy Policy
	// AutoPlan is the audio used for auto-answered calls.
	AutoPlan AudioPlan
	// MaxDuration hangs up answered calls after this long (0 = no limit).
	MaxDuration time.Duration
	// MaxConcurrent caps calls answered at the same time (0 = DefaultMaxConcurrent).
	MaxConcurrent int
	// MaxTracked caps ringing+answered calls the engine engages; beyond it new
	// offers are left untouched (0 = DefaultMaxTracked).
	MaxTracked int
	// RingTimeout forgets a ringing call that never got an end event
	// (0 = DefaultRingTimeout).
	RingTimeout time.Duration
	// SharedSession is set when the service rides on the session of a running
	// sync daemon: the line is also answered by the phone and other linked
	// devices, so the service never rejects, and callers outside Policy.Allow
	// are not engaged at all (no preaccept, no relay traffic).
	SharedSession bool
	// Alternates returns other identities of a caller (the phone number of a
	// LID) so an allow list of phone numbers matches LID offers. Optional.
	Alternates func(types.JID) []types.JID
	// Logger receives meowcaller's and the service's logs.
	Logger zerolog.Logger
	// OnEvent, when set, receives lifecycle events (incoming, answered,
	// ended, …) with a small map of fields for NDJSON output.
	OnEvent func(event string, fields map[string]any)
}

// Service owns the call engine and the tracked calls of one session.
type Service struct {
	opts Options
	eng  Engine
	log  zerolog.Logger

	mu     sync.Mutex
	calls  map[string]*trackedCall
	closed bool
}

type trackedCall struct {
	call       Call
	peer       types.JID
	ringingAt  time.Time
	answeredAt time.Time
	endedAt    time.Time
	endReason  string
	plan       *AudioPlan
	bridge     *Bridge
	player     Player
	timers     []*time.Timer
}

// New installs meowcaller on wa. It MUST run before the whatsmeow client
// connects: meowcaller hooks the raw <call>/<ack> handlers at construction.
func New(wa *whatsmeow.Client, opts Options) (*Service, error) {
	if wa == nil {
		return nil, errors.New("calls: whatsmeow client is nil")
	}
	if wa.IsConnected() {
		return nil, errors.New("calls: the service must be created before the client connects")
	}
	s := newService(opts)
	mopts := []meowcaller.Option{
		meowcaller.WithLogger(opts.Logger),
		meowcaller.WithPanicHandler(s.onEnginePanic),
	}
	if opts.SharedSession {
		mopts = append(mopts, meowcaller.WithOfferFilter(s.EngageOffer))
	}
	mc := meowcaller.NewClient(wa, mopts...)
	if err := mc.RawHookErr(); err != nil {
		// Inbound calls still work through the typed events; say so loudly.
		s.log.Warn().Err(err).Msg("call engine raw hook unavailable")
	}
	s.attach(meowEngine{mc})
	return s, nil
}

// NewForEngine builds a Service on an arbitrary Engine (tests, simulations).
func NewForEngine(eng Engine, opts Options) *Service {
	s := newService(opts)
	s.attach(eng)
	return s
}

func newService(opts Options) *Service {
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = DefaultMaxConcurrent
	}
	if opts.MaxTracked <= 0 {
		opts.MaxTracked = DefaultMaxTracked
	}
	if opts.RingTimeout <= 0 {
		opts.RingTimeout = DefaultRingTimeout
	}
	return &Service{opts: opts, log: opts.Logger, calls: map[string]*trackedCall{}}
}

func (s *Service) attach(eng Engine) {
	s.eng = eng
	eng.OnIncomingCall(func(c Call) {
		s.guard("incoming call", func() { s.onIncoming(c) })
	})
}

// guard runs fn and turns a panic into a log line, so nothing the service
// does on the engine's or the control socket's goroutines can crash the
// process that also serves messages.
func (s *Service) guard(where string, fn func()) (ok bool) {
	defer func() {
		if v := recover(); v != nil {
			ok = false
			s.log.Error().Str("where", where).Interface("panic", v).Bytes("stack", debug.Stack()).Msg("calls: recovered panic")
			s.emit("call_error", map[string]any{"where": where, "error": fmt.Sprint(v)})
		}
	}()
	fn()
	return true
}

func (s *Service) onEnginePanic(p meowcaller.PanicInfo) {
	s.log.Error().Str("where", p.Where).Interface("panic", p.Value).Bytes("stack", p.Stack).Msg("call engine panic recovered; hanging up live calls")
	s.emit("call_engine_panic", map[string]any{"where": p.Where, "error": fmt.Sprint(p.Value)})
	// A media goroutine died: its call cannot carry audio any more. End every
	// live call from a fresh goroutine (never inside the panicking one).
	go s.guard("panic cleanup", func() {
		for _, info := range s.List() {
			if info.EndedAt.IsZero() {
				_ = s.Hangup(info.CallID)
			}
		}
	})
}

func (s *Service) emit(event string, fields map[string]any) {
	if s.opts.OnEvent == nil {
		return
	}
	defer func() { _ = recover() }()
	s.opts.OnEvent(event, fields)
}

// EngageOffer is the shared-session gate meowcaller consults before touching
// an offer: only callers the policy may answer, never group calls, and never
// beyond MaxTracked. Everything else rings exactly as without the engine.
func (s *Service) EngageOffer(ev *events.CallOffer) bool {
	if ev == nil {
		return false
	}
	if !ev.GroupJID.IsEmpty() {
		s.log.Info().Str("call_id", ev.CallID).Msg("group call left to the other devices")
		return false
	}
	peers := s.peersOf(ev.CallCreator, ev.CallCreatorAlt, ev.From)
	if !s.opts.Policy.Allowed(peers...) {
		s.log.Info().Str("call_id", ev.CallID).Msg("caller not in --calls-allow; left to the other devices")
		s.emit("call_ignored", map[string]any{"call_id": ev.CallID, "peer": firstNonEmpty(peers).String(), "reason": "not_allowed"})
		return false
	}
	s.mu.Lock()
	live := 0
	for _, tc := range s.calls {
		if tc.endedAt.IsZero() {
			live++
		}
	}
	closed := s.closed
	s.mu.Unlock()
	if closed || live >= s.opts.MaxTracked {
		s.emit("call_ignored", map[string]any{"call_id": ev.CallID, "peer": firstNonEmpty(peers).String(), "reason": "capacity"})
		return false
	}
	return true
}

func (s *Service) peersOf(jids ...types.JID) []types.JID {
	out := make([]types.JID, 0, len(jids)*2)
	for _, j := range jids {
		if j.IsEmpty() {
			continue
		}
		out = append(out, j)
		if s.opts.Alternates != nil {
			func() {
				defer func() { _ = recover() }()
				out = append(out, s.opts.Alternates(j)...)
			}()
		}
	}
	return out
}

func firstNonEmpty(jids []types.JID) types.JID {
	for _, j := range jids {
		if !j.IsEmpty() {
			return j
		}
	}
	return types.EmptyJID
}

func (s *Service) onIncoming(call Call) {
	id := call.ID()
	peer := call.Peer()
	tc := &trackedCall{call: call, peer: peer, ringingAt: time.Now()}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.calls[id] = tc
	s.mu.Unlock()

	call.OnEnd(func(reason string) {
		s.guard("call end", func() { s.onEnd(id, reason) })
	})
	call.OnReady(func() {
		s.emit("call_media_ready", map[string]any{"call_id": id, "peer": peer.String()})
	})
	s.addTimer(tc, s.opts.RingTimeout, func() { s.expireRinging(id, tc) })
	s.log.Info().Str("call_id", id).Str("peer", peer.String()).Bool("video", call.IsVideo()).Msg("incoming call")
	s.emit("call_incoming", map[string]any{"call_id": id, "peer": peer.String(), "video": call.IsVideo()})

	decision := s.opts.Policy.Decide(s.peersOf(peer)...)
	switch {
	case decision.Reject && s.opts.SharedSession:
		// Never decline on a shared line: the phone may still pick it up.
		s.log.Info().Str("call_id", id).Msg("reject policy ignored on a shared session")
	case decision.Reject:
		s.log.Info().Str("call_id", id).Str("reason", decision.Reason).Msg("rejecting call")
		if err := call.Reject(); err != nil {
			s.log.Warn().Err(err).Str("call_id", id).Msg("reject failed")
		}
	case decision.Answer:
		delay := s.opts.Policy.Delay
		s.log.Info().Str("call_id", id).Dur("delay", delay).Msg("auto-answering")
		s.addTimer(tc, delay, func() {
			err := s.Answer(id, s.opts.AutoPlan)
			switch {
			case err == nil, errors.Is(err, ErrCallNotFound):
			case errors.Is(err, ErrBusy):
				s.log.Info().Str("call_id", id).Msg("auto-answer skipped: another call is answered")
				s.emit("call_busy", map[string]any{"call_id": id, "peer": peer.String()})
			default:
				s.log.Warn().Err(err).Str("call_id", id).Msg("auto-answer failed")
			}
		})
	default:
		s.log.Info().Str("call_id", id).Str("reason", decision.Reason).Msg("waiting for `wacli calls answer`")
	}
}

// addTimer schedules fn under the panic guard and remembers the timer so the
// call's end stops it.
func (s *Service) addTimer(tc *trackedCall, d time.Duration, fn func()) {
	t := time.AfterFunc(d, func() { s.guard("call timer", fn) })
	s.mu.Lock()
	if !tc.endedAt.IsZero() {
		s.mu.Unlock()
		t.Stop()
		return
	}
	tc.timers = append(tc.timers, t)
	s.mu.Unlock()
}

func (s *Service) expireRinging(id string, tc *trackedCall) {
	s.mu.Lock()
	if s.calls[id] != tc || !tc.answeredAt.IsZero() || !tc.endedAt.IsZero() {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.finish(id, tc, "ring_timeout")
}

func (s *Service) answeredLive() int {
	n := 0
	for _, tc := range s.calls {
		if !tc.answeredAt.IsZero() && tc.endedAt.IsZero() {
			n++
		}
	}
	return n
}

// Answer accepts a ringing call and attaches the audio plan.
func (s *Service) Answer(callID string, plan AudioPlan) error {
	s.mu.Lock()
	tc := s.calls[callID]
	if tc == nil || !tc.endedAt.IsZero() {
		s.mu.Unlock()
		return ErrCallNotFound
	}
	if !tc.answeredAt.IsZero() {
		s.mu.Unlock()
		return fmt.Errorf("call %s already answered", callID)
	}
	if s.answeredLive() >= s.opts.MaxConcurrent {
		s.mu.Unlock()
		return ErrBusy
	}
	s.mu.Unlock()

	bridge, err := BuildBridge(plan)
	if err != nil {
		return fmt.Errorf("audio: %w", err)
	}

	s.mu.Lock()
	if tc.answeredAt.IsZero() && tc.endedAt.IsZero() && s.answeredLive() < s.opts.MaxConcurrent {
		tc.answeredAt = time.Now()
		tc.plan = &plan
		tc.bridge = bridge
	} else {
		s.mu.Unlock()
		bridge.Close()
		return fmt.Errorf("call %s is no longer ringing", callID)
	}
	s.mu.Unlock()

	var answerErr error
	ok := s.guard("answer", func() {
		// Attach audio before the accept goes out so the first decoded frames
		// are not lost.
		if bridge.Sink != nil {
			tc.call.Receive(bridge.Sink)
		}
		if bridge.Source != nil {
			player := tc.call.Play(bridge.Source)
			if player != nil {
				player.OnFinish(func() {
					s.emit("call_playback_finished", map[string]any{"call_id": callID})
				})
			}
			s.mu.Lock()
			tc.player = player
			s.mu.Unlock()
		}
		answerErr = tc.call.Answer()
	})
	if !ok && answerErr == nil {
		answerErr = errors.New("answer panicked")
	}
	if answerErr != nil {
		s.mu.Lock()
		tc.answeredAt = time.Time{}
		tc.plan, tc.bridge = nil, nil
		s.mu.Unlock()
		bridge.Close()
		return fmt.Errorf("answer: %w", answerErr)
	}
	if s.opts.MaxDuration > 0 {
		s.addTimer(tc, s.opts.MaxDuration, func() {
			s.log.Info().Str("call_id", callID).Msg("max duration reached, hanging up")
			_ = s.Hangup(callID)
		})
	}
	s.log.Info().Str("call_id", callID).Str("audio", plan.Describe()).Msg("answered")
	s.emit("call_answered", map[string]any{"call_id": callID, "peer": tc.peer.String(), "audio": plan.Describe()})
	return nil
}

// Hangup ends an answered call. A ringing call is rejected on a dedicated
// session; on a shared session it is only dropped here (the other devices keep
// ringing), since declining would be this device's decision for the line.
func (s *Service) Hangup(callID string) error {
	s.mu.Lock()
	tc := s.calls[callID]
	s.mu.Unlock()
	if tc == nil || !tc.endedAt.IsZero() {
		return ErrCallNotFound
	}
	var err error
	ok := s.guard("hangup", func() {
		switch {
		case !tc.answeredAt.IsZero():
			err = tc.call.Hangup()
		case s.opts.SharedSession:
			s.finish(callID, tc, "dropped")
		default:
			err = tc.call.Reject()
		}
	})
	if !ok {
		// The engine is in an unknown state for this call; release ours.
		s.finish(callID, tc, "hangup_failed")
		return errors.New("hangup panicked")
	}
	return err
}

func (s *Service) onEnd(callID, reason string) {
	s.mu.Lock()
	tc := s.calls[callID]
	s.mu.Unlock()
	if tc == nil {
		return
	}
	s.finish(callID, tc, reason)
}

// finish releases everything a call holds (timers, audio bridge) once, emits
// call_ended and forgets the call after a while.
func (s *Service) finish(callID string, tc *trackedCall, reason string) {
	s.mu.Lock()
	if !tc.endedAt.IsZero() {
		s.mu.Unlock()
		return
	}
	tc.endedAt = time.Now()
	tc.endReason = reason
	bridge := tc.bridge
	timers := tc.timers
	tc.timers = nil
	s.mu.Unlock()
	for _, t := range timers {
		t.Stop()
	}

	var stats *BridgeStats
	if bridge != nil {
		if st, ok := bridge.Stats(); ok {
			stats = &st
		}
		if err := bridge.Close(); err != nil {
			s.log.Warn().Err(err).Str("call_id", callID).Msg("closing audio bridge")
		}
	}
	fields := map[string]any{"call_id": callID, "peer": tc.peer.String(), "reason": reason}
	if !tc.answeredAt.IsZero() {
		fields["duration_s"] = int(tc.endedAt.Sub(tc.answeredAt).Seconds())
	}
	if stats != nil {
		fields["rtp_in"] = stats.PacketsIn
		fields["rtp_out"] = stats.PacketsOut
		fields["rtp_lost"] = stats.PacketsLost
	}
	s.log.Info().Str("call_id", callID).Str("reason", reason).Msg("call ended")
	s.emit("call_ended", fields)

	time.AfterFunc(endedRetention, func() {
		s.mu.Lock()
		if cur := s.calls[callID]; cur == tc {
			delete(s.calls, callID)
		}
		s.mu.Unlock()
	})
}

// List returns the tracked calls, newest first.
func (s *Service) List() []CallInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CallInfo, 0, len(s.calls))
	for id, tc := range s.calls {
		info := CallInfo{
			CallID:     id,
			Peer:       tc.peer.String(),
			State:      callState(tc),
			Video:      tc.call.IsVideo(),
			RingingAt:  tc.ringingAt,
			AnsweredAt: tc.answeredAt,
			EndedAt:    tc.endedAt,
			EndReason:  tc.endReason,
		}
		if tc.plan != nil {
			info.Audio = tc.plan.Describe()
		}
		if tc.bridge != nil {
			if st, ok := tc.bridge.Stats(); ok {
				info.Stats = &st
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RingingAt.After(out[j].RingingAt) })
	return out
}

func callState(tc *trackedCall) string {
	if !tc.endedAt.IsZero() {
		return "ended"
	}
	state := "unknown"
	func() {
		defer func() { _ = recover() }()
		state = phaseName(tc.call.State())
	}()
	return state
}

// Live reports how many calls are ringing or answered.
func (s *Service) Live() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, tc := range s.calls {
		if tc.endedAt.IsZero() {
			n++
		}
	}
	return n
}

// Shutdown stops taking calls, hangs up everything that is still live and
// releases what is left (bridges, timers) even if the engine never confirms.
func (s *Service) Shutdown(ctx context.Context) {
	s.mu.Lock()
	s.closed = true
	ids := make([]string, 0, len(s.calls))
	for id, tc := range s.calls {
		if tc.endedAt.IsZero() {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		_ = s.Hangup(id)
	}
	deadline := time.After(2 * time.Second)
wait:
	for s.Live() > 0 {
		select {
		case <-ctx.Done():
			break wait
		case <-deadline:
			break wait
		case <-time.After(50 * time.Millisecond):
		}
	}
	s.mu.Lock()
	var left []string
	for id, tc := range s.calls {
		if tc.endedAt.IsZero() {
			left = append(left, id)
		}
	}
	s.mu.Unlock()
	for _, id := range left {
		s.mu.Lock()
		tc := s.calls[id]
		s.mu.Unlock()
		if tc != nil {
			s.finish(id, tc, "shutdown")
		}
	}
}

func phaseName(p meowcaller.CallPhase) string {
	switch p {
	case meowcaller.CallPhaseIdle:
		return "idle"
	case meowcaller.CallPhaseCalling:
		return "calling"
	case meowcaller.CallPhaseRinging:
		return "ringing"
	case meowcaller.CallPhaseConnecting:
		return "connecting"
	case meowcaller.CallPhaseActive:
		return "active"
	case meowcaller.CallPhaseEnded:
		return "ended"
	case meowcaller.CallPhaseWaitingRoom:
		return "waiting_room"
	default:
		return fmt.Sprintf("phase(%d)", int(p))
	}
}
