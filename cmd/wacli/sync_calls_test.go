package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appPkg "github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/calls"
	"github.com/spf13/cobra"
	waBinary "go.mau.fi/whatsmeow/binary"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func parseSyncCallsFlags(t *testing.T, args ...string) (calls.DaemonConfig, error, bool) {
	t.Helper()
	var f syncCallsFlags
	cmd := &cobra.Command{Use: "sync"}
	var once bool
	cmd.Flags().BoolVar(&once, "once", false, "")
	f.register(cmd)
	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	cfg, err := f.config(cmd, once)
	return cfg, err, once
}

func TestSyncCallsFlagsDefaultOff(t *testing.T) {
	cfg, err, _ := parseSyncCallsFlags(t)
	if err != nil || cfg.Enabled() {
		t.Fatalf("default = %+v, %v; want off", cfg, err)
	}
	for _, args := range [][]string{
		{"--calls-allow", "+51991000001"},
		{"--calls-audio-in", "echo"},
		{"--calls", "off", "--calls-max-concurrent", "2"},
	} {
		if _, err, _ := parseSyncCallsFlags(t, args...); err == nil || !strings.Contains(err.Error(), "requires --calls") {
			t.Fatalf("%v accepted without --calls on: %v", args, err)
		}
	}
	if _, err, _ := parseSyncCallsFlags(t, "--calls", "manual", "--once"); err == nil {
		t.Fatal("--calls accepted outside follow mode")
	}
	if _, err, _ := parseSyncCallsFlags(t, "--calls", "reject"); err == nil {
		t.Fatal("--calls reject accepted on a shared session")
	}
	if _, err, _ := parseSyncCallsFlags(t, "--calls", "auto", "--calls-audio-in", "nope.xyz"); err == nil {
		t.Fatal("bad auto audio accepted")
	}
	cfg, err, _ = parseSyncCallsFlags(t, "--calls", "auto", "--calls-allow", "+51 991 000 001,51991000002", "--calls-audio-in", "echo", "--calls-audio-out", "echo")
	if err != nil || cfg.Mode != calls.DaemonAuto || len(cfg.Allow) != 2 {
		t.Fatalf("auto config = %+v, %v", cfg, err)
	}
}

// TestSyncCallsOnRealWhatsmeowClient installs the engine on the session's real
// (unconnected) whatsmeow client, the way `sync --follow --calls manual` does,
// and dispatches call and message events through whatsmeow's own dispatcher:
// handlers registered after the engine (sync's) still see every event, a
// caller outside --calls-allow is not engaged, and Close removes the socket.
func TestSyncCallsOnRealWhatsmeowClient(t *testing.T) {
	dir, err := os.MkdirTemp("", "wsc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	a, err := appPkg.New(appPkg.Options{StoreDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenWA(); err != nil {
		t.Fatal(err)
	}
	wm := a.WA().(rawWhatsmeow).Whatsmeow()

	cfg := calls.DaemonConfig{Mode: calls.DaemonManual, Allow: []string{"51991000001"}, AudioIn: "silence", AudioOut: "none", MaxConcurrent: 1, LogLevel: "warn"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := startSyncCalls(ctx, a, &rootFlags{events: true}, cfg)
	if err != nil {
		t.Fatalf("startSyncCalls: %v", err)
	}
	sock := filepath.Join(dir, calls.ControlSocketName)
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("control socket: %v", err)
	}

	var offers, messages atomic.Int32
	wm.AddEventHandler(func(evt any) {
		switch evt.(type) {
		case *events.CallOffer:
			offers.Add(1)
		case *events.Message:
			messages.Add(1)
		}
	})
	stranger := types.NewJID("51987000002", types.DefaultUserServer)
	team := types.NewJID("51991000001", types.DefaultUserServer)
	for i, from := range []types.JID{stranger, team} {
		failed := wm.DangerousInternals().DispatchEvent(&events.CallOffer{
			BasicCallMeta: types.BasicCallMeta{From: from, CallCreator: from, CallID: "offer-" + string(rune('a'+i)), Timestamp: time.Now()},
			Data:          &waBinary.Node{Tag: "offer"},
		})
		if failed {
			t.Fatal("a handler failed while dispatching a call offer")
		}
	}
	wm.DangerousInternals().DispatchEvent(&events.Message{
		Info:    types.MessageInfo{MessageSource: types.MessageSource{Chat: stranger, Sender: stranger}, ID: "m1", Timestamp: time.Now()},
		Message: &waProto.Message{Conversation: proto.String("hola")},
	})
	if offers.Load() != 2 || messages.Load() != 1 {
		t.Fatalf("later handler saw %d offers and %d messages, want 2 and 1", offers.Load(), messages.Load())
	}
	// The stranger is never tracked; the team offer has no call key in this
	// test, so the engine drops it before ringing.
	if n := d.Service.Live(); n != 0 {
		t.Fatalf("%d calls tracked from offers without keys", n)
	}
	rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer rcancel()
	if _, err := calls.SendControl(rctx, sock, calls.ControlRequest{Op: "active"}); err != nil {
		t.Fatalf("active over the daemon socket: %v", err)
	}
	d.Close()
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket left behind: %v", err)
	}
}
