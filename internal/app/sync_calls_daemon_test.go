package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/calls"
	"github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// The call engine rides on the sync daemon's session. These tests run a real
// follow-mode Sync (fake WhatsApp transport) with the engine off and on, and
// check that messages keep flowing and are all stored while a simulated call
// is offered, answered and hung up through the control socket.

const daemonTestMessages = 300

func runFollowSyncWithTraffic(t *testing.T, a *App, f *fakeWA, during func(ctx context.Context)) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := a.Sync(ctx, SyncOptions{
			Mode: SyncModeFollow,
			AfterConnect: func(ctx context.Context) error {
				go func() {
					var wg sync.WaitGroup
					wg.Add(2)
					go func() {
						defer wg.Done()
						for i := 0; i < daemonTestMessages; i++ {
							f.emit(daemonTestMessage(i))
						}
					}()
					go func() {
						defer wg.Done()
						if during != nil {
							during(ctx)
						}
					}()
					wg.Wait()
					cancel()
				}()
				return nil
			},
		})
		errCh <- err
	}()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("follow sync did not finish")
	}
	n, err := a.db.CountMessages()
	if err != nil {
		t.Fatal(err)
	}
	if n != daemonTestMessages {
		t.Fatalf("stored %d messages, want %d", n, daemonTestMessages)
	}
}

func daemonTestMessage(i int) *events.Message {
	chat := types.NewJID(fmt.Sprintf("5198700%04d", i%7), types.DefaultUserServer)
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: chat},
			ID:            types.MessageID(fmt.Sprintf("daemon-msg-%04d", i)),
			Timestamp:     time.Date(2026, 11, 24, 10, 0, 0, i*1000, time.UTC),
			PushName:      "Cliente",
		},
		Message: &waProto.Message{Conversation: proto.String(fmt.Sprintf("hola %d", i))},
	}
}

func shortStoreApp(t *testing.T) *App {
	t.Helper()
	dir, err := os.MkdirTemp("", "wcd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	a, err := New(Options{StoreDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func TestFollowSyncWithCallsOffIsUnchanged(t *testing.T) {
	a := shortStoreApp(t)
	f := newFakeWA()
	a.wa = f
	runFollowSyncWithTraffic(t, a, f, nil)
	if _, err := os.Stat(filepath.Join(a.StoreDir(), calls.ControlSocketName)); !os.IsNotExist(err) {
		t.Fatalf("calls off created a control socket: %v", err)
	}
}

func TestFollowSyncKeepsMessagesFlowingDuringACall(t *testing.T) {
	a := shortStoreApp(t)
	f := newFakeWA()
	a.wa = f

	eng := &daemonFakeEngine{}
	policy, err := calls.ParsePolicy("manual", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var evMu sync.Mutex
	var evs []string
	d, err := calls.StartDaemonForEngine(t.Context(), eng, calls.Options{
		Policy: policy,
		Logger: zerolog.Nop(),
		OnEvent: func(e string, _ map[string]any) {
			evMu.Lock()
			evs = append(evs, e)
			evMu.Unlock()
		},
	}, a.StoreDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sock := d.SocketPath()

	runFollowSyncWithTraffic(t, a, f, func(ctx context.Context) {
		for round := 0; round < 5; round++ {
			id := fmt.Sprintf("call-%d", round)
			eng.ring(id)
			ctl := func(req calls.ControlRequest) {
				rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if _, err := calls.SendControl(rctx, sock, req); err != nil {
					t.Errorf("%s %s: %v", req.Op, id, err)
				}
			}
			ctl(calls.ControlRequest{Op: "answer", CallID: id, AudioIn: "echo", AudioOut: "echo"})
			ctl(calls.ControlRequest{Op: "active"})
			ctl(calls.ControlRequest{Op: "hangup", CallID: id})
		}
	})

	if d.Service.Live() != 0 {
		t.Fatalf("%d calls still live", d.Service.Live())
	}
	evMu.Lock()
	defer evMu.Unlock()
	counts := map[string]int{}
	for _, e := range evs {
		counts[e]++
	}
	if counts["call_incoming"] != 5 || counts["call_answered"] != 5 || counts["call_ended"] != 5 {
		t.Fatalf("call events = %v", counts)
	}
}

// daemonFakeEngine is a minimal engine for the app-level test (the calls
// package has a richer one for its own tests).
type daemonFakeEngine struct {
	mu sync.Mutex
	fn func(calls.Call)
}

func (e *daemonFakeEngine) OnIncomingCall(fn func(calls.Call)) {
	e.mu.Lock()
	e.fn = fn
	e.mu.Unlock()
}

func (e *daemonFakeEngine) ring(id string) {
	e.mu.Lock()
	fn := e.fn
	e.mu.Unlock()
	fn(&daemonFakeCall{id: id, phase: meowcaller.CallPhaseRinging})
}

type daemonFakeCall struct {
	id    string
	mu    sync.Mutex
	phase meowcaller.CallPhase
	onEnd func(string)
	sink  meowcaller.AudioSink
	src   meowcaller.AudioSource
}

func (c *daemonFakeCall) ID() string { return c.id }
func (c *daemonFakeCall) Peer() types.JID {
	return types.NewJID("51991000001", types.DefaultUserServer)
}
func (c *daemonFakeCall) IsVideo() bool { return false }
func (c *daemonFakeCall) State() meowcaller.CallPhase {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.phase
}
func (c *daemonFakeCall) Answer() error {
	c.mu.Lock()
	c.phase = meowcaller.CallPhaseActive
	src, sink := c.src, c.sink
	c.mu.Unlock()
	// Push a few frames through the echo bridge, like the media loop would.
	if src != nil && sink != nil {
		frame := make([]float32, meowcaller.FrameSamples)
		for i := 0; i < 5; i++ {
			_ = sink.WriteFrame(frame)
			_, _ = src.ReadFrame()
		}
	}
	return nil
}
func (c *daemonFakeCall) Reject() error { c.end("rejected"); return nil }
func (c *daemonFakeCall) Hangup() error { c.end("hangup"); return nil }
func (c *daemonFakeCall) end(reason string) {
	c.mu.Lock()
	if c.phase == meowcaller.CallPhaseEnded {
		c.mu.Unlock()
		return
	}
	c.phase = meowcaller.CallPhaseEnded
	fn := c.onEnd
	c.mu.Unlock()
	if fn != nil {
		fn(reason)
	}
}
func (c *daemonFakeCall) Receive(s meowcaller.AudioSink) { c.mu.Lock(); c.sink = s; c.mu.Unlock() }
func (c *daemonFakeCall) Play(s meowcaller.AudioSource) calls.Player {
	c.mu.Lock()
	c.src = s
	c.mu.Unlock()
	return nopPlayer{}
}
func (c *daemonFakeCall) OnEnd(fn func(string)) { c.mu.Lock(); c.onEnd = fn; c.mu.Unlock() }
func (c *daemonFakeCall) OnReady(func())        {}

type nopPlayer struct{}

func (nopPlayer) OnFinish(func()) {}
