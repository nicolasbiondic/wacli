package calls

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestDaemonControlSocketAndWatch(t *testing.T) {
	dir := shortTempDir(t)
	eng := &fakeEngine{}
	policy, _ := ParsePolicy("manual", nil, 0)
	d, err := StartDaemonForEngine(context.Background(), eng, Options{Policy: policy, Logger: zerolog.Nop()}, dir)
	if err != nil {
		t.Fatal(err)
	}
	sock := ControlSocketPath(dir)
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("control socket %v perm %v", err, fi)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var lines []map[string]any
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- Watch(ctx, sock, func(b []byte) error {
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				return err
			}
			mu.Lock()
			lines = append(lines, m)
			mu.Unlock()
			return nil
		})
	}()
	waitFor(t, "watcher", func() bool { return d.Hub.Watchers() == 1 })

	eng.ring("w1", teamPN)
	req := func(r ControlRequest) ControlResponse {
		t.Helper()
		rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer rcancel()
		resp, err := SendControl(rctx, sock, r)
		if err != nil {
			t.Fatalf("%s: %v", r.Op, err)
		}
		return resp
	}
	if got := req(ControlRequest{Op: "active"}); len(got.Calls) != 1 || got.Calls[0].CallID != "w1" {
		t.Fatalf("active = %+v", got.Calls)
	}
	req(ControlRequest{Op: "answer", CallID: "w1", AudioIn: "echo", AudioOut: "echo"})
	req(ControlRequest{Op: "hangup", CallID: "w1"})

	waitFor(t, "three streamed events", func() bool { mu.Lock(); defer mu.Unlock(); return len(lines) >= 3 })
	mu.Lock()
	for i, want := range []string{"call_incoming", "call_answered", "call_ended"} {
		if lines[i]["event"] != want {
			t.Fatalf("line %d = %v, want %s", i, lines[i], want)
		}
	}
	mu.Unlock()

	d.Close()
	if err := <-watchDone; err != nil {
		t.Fatalf("watch ended with %v", err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket left behind after Close: %v", err)
	}
}

func TestParseDaemonMode(t *testing.T) {
	for in, want := range map[string]DaemonMode{"": DaemonOff, "off": DaemonOff, "Manual": DaemonManual, " auto ": DaemonAuto} {
		got, err := ParseDaemonMode(in)
		if err != nil || got != want {
			t.Fatalf("ParseDaemonMode(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseDaemonMode("reject"); err == nil {
		t.Fatal("reject is not a daemon mode: declining on a shared line would stop the phone from answering")
	}
	if (DaemonConfig{}).Enabled() || (DaemonConfig{Mode: DaemonOff}).Enabled() {
		t.Fatal("off reported enabled")
	}
}
