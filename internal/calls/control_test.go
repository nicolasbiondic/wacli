package calls

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeController struct {
	mu       sync.Mutex
	answered map[string]AudioPlan
	hungUp   []string
	fail     error
}

func (f *fakeController) Answer(callID string, plan AudioPlan) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	if f.answered == nil {
		f.answered = map[string]AudioPlan{}
	}
	f.answered[callID] = plan
	return nil
}

func (f *fakeController) Hangup(callID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.hungUp = append(f.hungUp, callID)
	return nil
}

func (f *fakeController) List() []CallInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []CallInfo{}
	for id, plan := range f.answered {
		out = append(out, CallInfo{CallID: id, State: "active", Audio: plan.Describe()})
	}
	return out
}

func TestHandleControl(t *testing.T) {
	ctl := &fakeController{}
	if r := HandleControl(ctl, ControlRequest{Op: "answer"}); r.OK || r.Error == "" {
		t.Fatalf("answer without call_id: %+v", r)
	}
	if r := HandleControl(ctl, ControlRequest{Op: "answer", CallID: "c1", AudioIn: "echo", AudioOut: "none"}); r.OK {
		t.Fatalf("invalid plan accepted: %+v", r)
	}
	r := HandleControl(ctl, ControlRequest{Op: "answer", CallID: "c1", AudioIn: "echo", AudioOut: "echo"})
	if !r.OK || len(r.Calls) != 1 || r.Calls[0].CallID != "c1" {
		t.Fatalf("answer: %+v", r)
	}
	if plan := ctl.answered["c1"]; plan.In.Kind != EndpointEcho {
		t.Fatalf("plan not passed through: %+v", plan)
	}
	if r := HandleControl(ctl, ControlRequest{Op: "HANGUP", CallID: "c1"}); !r.OK || len(ctl.hungUp) != 1 {
		t.Fatalf("hangup: %+v", r)
	}
	if r := HandleControl(ctl, ControlRequest{Op: "active"}); !r.OK {
		t.Fatalf("list: %+v", r)
	}
	if r := HandleControl(ctl, ControlRequest{Op: "dance"}); r.OK {
		t.Fatalf("unknown op accepted: %+v", r)
	}
	ctl.fail = ErrCallNotFound
	if r := HandleControl(ctl, ControlRequest{Op: "hangup", CallID: "zz"}); r.OK || r.Error != ErrCallNotFound.Error() {
		t.Fatalf("error not propagated: %+v", r)
	}
}

func TestControlSocketRoundTrip(t *testing.T) {
	dir := shortTempDir(t)
	path := ControlSocketPath(dir)
	ctl := &fakeController{}
	srv, err := ListenControl(path, ctl)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	// A second server on the same live socket must refuse.
	if _, err := ListenControl(path, ctl); err == nil {
		t.Fatal("second listener on a live socket did not fail")
	}

	rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
	defer rcancel()
	resp, err := SendControl(rctx, path, ControlRequest{Op: "answer", CallID: "abc", AudioIn: "silence", AudioOut: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || len(resp.Calls) != 1 || resp.Calls[0].CallID != "abc" {
		t.Fatalf("resp %+v", resp)
	}
	_, err = SendControl(rctx, path, ControlRequest{Op: "answer", CallID: "abc", AudioIn: "echo", AudioOut: "none"})
	if err == nil {
		t.Fatal("invalid plan accepted over the socket")
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket file left behind: %v", err)
	}
	if _, err := SendControl(rctx, path, ControlRequest{Op: "list"}); err == nil {
		t.Fatal("send after close succeeded")
	}
}

func TestListenControlReplacesStaleSocket(t *testing.T) {
	dir := shortTempDir(t)
	path := ControlSocketPath(dir)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	// Close the listener but keep the file, like a crashed process would.
	ln.Close()
	if _, err := os.Stat(path); err != nil {
		// Go removes the socket file on Close; recreate a stale file to simulate a crash.
		if f, err := os.Create(path); err == nil {
			f.Close()
		}
	}
	srv, err := ListenControl(path, &fakeController{})
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	srv.Close()
}

// shortTempDir avoids the 108-byte unix socket path limit on some runners.
func shortTempDir(t *testing.T) string {
	t.Helper()
	base := "/tmp"
	if _, err := os.Stat("/tmp/opencode"); err == nil {
		base = "/tmp/opencode"
	}
	dir, err := os.MkdirTemp(base, "wacli-calls-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Clean(dir)
}
