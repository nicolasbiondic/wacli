package calls

import (
	"sync"
	"sync/atomic"

	"github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow/types"
)

// fakeEngine simulates the call stack: ring() is an inbound offer that got
// through the offer filter; the fake calls end when hung up, like meowcaller.
type fakeEngine struct {
	mu sync.Mutex
	fn func(Call)
}

func (e *fakeEngine) OnIncomingCall(fn func(Call)) {
	e.mu.Lock()
	e.fn = fn
	e.mu.Unlock()
}

func (e *fakeEngine) ring(id string, peer types.JID) *fakeCall {
	c := &fakeCall{id: id, peer: peer, phase: meowcaller.CallPhaseRinging}
	e.mu.Lock()
	fn := e.fn
	e.mu.Unlock()
	fn(c)
	return c
}

type fakeCall struct {
	id   string
	peer types.JID

	mu        sync.Mutex
	phase     meowcaller.CallPhase
	onEnd     func(string)
	sink      meowcaller.AudioSink
	source    meowcaller.AudioSource
	answers   atomic.Int32
	rejects   atomic.Int32
	hangups   atomic.Int32
	panicOn   string // "answer" or "hangup"
	stuck     bool   // hangup never confirms the end
	answerErr error
}

func (c *fakeCall) ID() string      { return c.id }
func (c *fakeCall) Peer() types.JID { return c.peer }
func (c *fakeCall) IsVideo() bool   { return false }
func (c *fakeCall) State() meowcaller.CallPhase {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.phase
}

func (c *fakeCall) Answer() error {
	if c.panicOn == "answer" {
		panic("fake engine answer bug")
	}
	c.answers.Add(1)
	if c.answerErr != nil {
		return c.answerErr
	}
	c.mu.Lock()
	c.phase = meowcaller.CallPhaseActive
	c.mu.Unlock()
	return nil
}

func (c *fakeCall) Reject() error {
	c.rejects.Add(1)
	c.end("rejected")
	return nil
}

func (c *fakeCall) Hangup() error {
	if c.panicOn == "hangup" {
		panic("fake engine hangup bug")
	}
	c.hangups.Add(1)
	if !c.stuck {
		c.end("hangup")
	}
	return nil
}

// end is the engine's terminate path: it closes the sink and fires OnEnd.
func (c *fakeCall) end(reason string) {
	c.mu.Lock()
	if c.phase == meowcaller.CallPhaseEnded {
		c.mu.Unlock()
		return
	}
	c.phase = meowcaller.CallPhaseEnded
	fn, sink := c.onEnd, c.sink
	c.mu.Unlock()
	if sink != nil {
		_ = sink.Close()
	}
	if fn != nil {
		fn(reason)
	}
}

func (c *fakeCall) Receive(s meowcaller.AudioSink) { c.mu.Lock(); c.sink = s; c.mu.Unlock() }
func (c *fakeCall) Play(s meowcaller.AudioSource) Player {
	c.mu.Lock()
	c.source = s
	c.mu.Unlock()
	return fakePlayer{}
}
func (c *fakeCall) OnEnd(fn func(string)) { c.mu.Lock(); c.onEnd = fn; c.mu.Unlock() }
func (c *fakeCall) OnReady(func())        {}

type fakePlayer struct{}

func (fakePlayer) OnFinish(func()) {}

// eventLog records service events for assertions.
type eventLog struct {
	mu  sync.Mutex
	got []recordedEvent
	ch  chan recordedEvent
}

type recordedEvent struct {
	name   string
	fields map[string]any
}

func newEventLog() *eventLog { return &eventLog{ch: make(chan recordedEvent, 256)} }

func (l *eventLog) on(name string, fields map[string]any) {
	l.mu.Lock()
	l.got = append(l.got, recordedEvent{name, fields})
	l.mu.Unlock()
	select {
	case l.ch <- recordedEvent{name, fields}:
	default:
	}
}

func (l *eventLog) names() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.got))
	for i, e := range l.got {
		out[i] = e.name
	}
	return out
}

func (l *eventLog) find(name string) (map[string]any, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.got {
		if e.name == name {
			return e.fields, true
		}
	}
	return nil, false
}
