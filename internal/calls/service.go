package calls

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// ErrCallNotFound is returned for an unknown or already ended call id.
var ErrCallNotFound = errors.New("call not found")

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

// Options configure a Service.
type Options struct {
	Policy Policy
	// AutoPlan is the audio used for auto-answered calls.
	AutoPlan AudioPlan
	// MaxDuration hangs up answered calls after this long (0 = no limit).
	MaxDuration time.Duration
	// Logger receives meowcaller's and the service's logs.
	Logger zerolog.Logger
	// OnEvent, when set, receives lifecycle events (incoming, answered,
	// ended, …) with a small map of fields for NDJSON output.
	OnEvent func(event string, fields map[string]any)
}

// Service owns the meowcaller client and the tracked calls of one session.
type Service struct {
	opts Options
	mc   *meowcaller.Client
	log  zerolog.Logger

	mu    sync.Mutex
	calls map[string]*trackedCall
}

type trackedCall struct {
	call       *meowcaller.Call
	peer       types.JID
	ringingAt  time.Time
	answeredAt time.Time
	endedAt    time.Time
	endReason  string
	plan       *AudioPlan
	bridge     *Bridge
	player     *meowcaller.Player
	timer      *time.Timer
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
	s := &Service{opts: opts, log: opts.Logger, calls: map[string]*trackedCall{}}
	s.mc = meowcaller.NewClient(wa, meowcaller.WithLogger(opts.Logger))
	s.mc.OnIncomingCall(s.onIncoming)
	return s, nil
}

func (s *Service) emit(event string, fields map[string]any) {
	if s.opts.OnEvent != nil {
		s.opts.OnEvent(event, fields)
	}
}

func (s *Service) onIncoming(call *meowcaller.Call) {
	id := call.ID()
	peer := call.Peer()
	tc := &trackedCall{call: call, peer: peer, ringingAt: time.Now()}
	s.mu.Lock()
	s.calls[id] = tc
	s.mu.Unlock()

	call.OnEnd(func(reason string) { s.onEnd(id, reason) })
	call.OnReady(func() {
		s.emit("call_media_ready", map[string]any{"call_id": id, "peer": peer.String()})
	})
	s.log.Info().Str("call_id", id).Str("peer", peer.String()).Bool("video", call.IsVideo()).Msg("incoming call")
	s.emit("call_incoming", map[string]any{"call_id": id, "peer": peer.String(), "video": call.IsVideo()})

	decision := s.opts.Policy.Decide(peer)
	switch {
	case decision.Reject:
		s.log.Info().Str("call_id", id).Str("reason", decision.Reason).Msg("rejecting call")
		if err := call.Reject(); err != nil {
			s.log.Warn().Err(err).Str("call_id", id).Msg("reject failed")
		}
	case decision.Answer:
		delay := s.opts.Policy.Delay
		s.log.Info().Str("call_id", id).Dur("delay", delay).Msg("auto-answering")
		time.AfterFunc(delay, func() {
			if err := s.Answer(id, s.opts.AutoPlan); err != nil && !errors.Is(err, ErrCallNotFound) {
				s.log.Warn().Err(err).Str("call_id", id).Msg("auto-answer failed")
			}
		})
	default:
		s.log.Info().Str("call_id", id).Str("reason", decision.Reason).Msg("waiting for `wacli calls answer`")
	}
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
	s.mu.Unlock()

	bridge, err := BuildBridge(plan)
	if err != nil {
		return fmt.Errorf("audio: %w", err)
	}

	s.mu.Lock()
	if tc.answeredAt.IsZero() && tc.endedAt.IsZero() {
		tc.answeredAt = time.Now()
		tc.plan = &plan
		tc.bridge = bridge
	} else {
		s.mu.Unlock()
		bridge.Close()
		return fmt.Errorf("call %s is no longer ringing", callID)
	}
	s.mu.Unlock()

	// Attach audio before the accept goes out so the first decoded frames
	// are not lost.
	if bridge.Sink != nil {
		tc.call.Receive(bridge.Sink)
	}
	if bridge.Source != nil {
		player := tc.call.Play(bridge.Source)
		player.OnFinish(func() {
			s.emit("call_playback_finished", map[string]any{"call_id": callID})
		})
		s.mu.Lock()
		tc.player = player
		s.mu.Unlock()
	}
	if err := tc.call.Answer(); err != nil {
		s.mu.Lock()
		tc.answeredAt = time.Time{}
		tc.plan, tc.bridge = nil, nil
		s.mu.Unlock()
		bridge.Close()
		return fmt.Errorf("answer: %w", err)
	}
	if s.opts.MaxDuration > 0 {
		t := time.AfterFunc(s.opts.MaxDuration, func() {
			s.log.Info().Str("call_id", callID).Msg("max duration reached, hanging up")
			_ = s.Hangup(callID)
		})
		s.mu.Lock()
		tc.timer = t
		s.mu.Unlock()
	}
	s.log.Info().Str("call_id", callID).Str("audio", plan.Describe()).Msg("answered")
	s.emit("call_answered", map[string]any{"call_id": callID, "peer": tc.peer.String(), "audio": plan.Describe()})
	return nil
}

// Hangup ends a ringing or active call.
func (s *Service) Hangup(callID string) error {
	s.mu.Lock()
	tc := s.calls[callID]
	s.mu.Unlock()
	if tc == nil || !tc.endedAt.IsZero() {
		return ErrCallNotFound
	}
	if tc.answeredAt.IsZero() {
		return tc.call.Reject()
	}
	return tc.call.Hangup()
}

func (s *Service) onEnd(callID, reason string) {
	s.mu.Lock()
	tc := s.calls[callID]
	if tc == nil {
		s.mu.Unlock()
		return
	}
	tc.endedAt = time.Now()
	tc.endReason = reason
	bridge := tc.bridge
	if tc.timer != nil {
		tc.timer.Stop()
	}
	s.mu.Unlock()

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

	// Keep ended calls visible for a while, then forget them.
	time.AfterFunc(5*time.Minute, func() {
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
			State:      phaseName(tc.call.State()),
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

// Shutdown hangs up everything that is still live.
func (s *Service) Shutdown(ctx context.Context) {
	s.mu.Lock()
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
	for _, id := range ids {
		for {
			s.mu.Lock()
			tc := s.calls[id]
			ended := tc == nil || !tc.endedAt.IsZero()
			s.mu.Unlock()
			if ended {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-deadline:
				return
			case <-time.After(50 * time.Millisecond):
			}
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
