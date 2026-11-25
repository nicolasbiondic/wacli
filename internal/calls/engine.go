package calls

import (
	"github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow/types"
)

// Engine is the call stack the Service drives. In production it is meowcaller
// on the session's whatsmeow client; tests use a fake to simulate offers.
type Engine interface {
	OnIncomingCall(func(Call))
}

// Call is one live call of an Engine.
type Call interface {
	ID() string
	Peer() types.JID
	IsVideo() bool
	State() meowcaller.CallPhase
	Answer() error
	Reject() error
	Hangup() error
	Receive(meowcaller.AudioSink)
	Play(meowcaller.AudioSource) Player
	OnEnd(func(reason string))
	OnReady(func())
}

// Player is the playback handle of an answered call.
type Player interface {
	OnFinish(func())
}

type meowEngine struct{ c *meowcaller.Client }

func (m meowEngine) OnIncomingCall(fn func(Call)) {
	m.c.OnIncomingCall(func(c *meowcaller.Call) { fn(meowCall{c}) })
}

type meowCall struct{ *meowcaller.Call }

func (m meowCall) Play(src meowcaller.AudioSource) Player { return m.Call.Play(src) }
