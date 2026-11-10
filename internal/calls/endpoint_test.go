package calls

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func TestParseEndpoint(t *testing.T) {
	cases := []struct {
		spec string
		kind EndpointKind
		addr string
		pt   uint8
		path string
		err  bool
	}{
		{spec: "", kind: EndpointNone},
		{spec: "none", kind: EndpointNone},
		{spec: "silence", kind: EndpointNone},
		{spec: "echo", kind: EndpointEcho},
		{spec: "rtp://127.0.0.1:4000", kind: EndpointRTP, addr: "127.0.0.1:4000", pt: DefaultPayloadType},
		{spec: "rtp://10.0.0.5:18000?pt=96", kind: EndpointRTP, addr: "10.0.0.5:18000", pt: 96},
		{spec: "rtp://auto:0", kind: EndpointRTP, addr: "auto:0", pt: DefaultPayloadType},
		{spec: "rtp://[::1]:5000", kind: EndpointRTP, addr: "[::1]:5000", pt: DefaultPayloadType},
		{spec: "rtp://nohost", err: true},
		{spec: "rtp://1.2.3.4:99999", err: true},
		{spec: "rtp://1.2.3.4:4000?pt=200", err: true},
		{spec: "/tmp/x/hello.mp3", kind: EndpointFile, path: "/tmp/x/hello.mp3"},
		{spec: "file:/tmp/rec.wav", kind: EndpointFile, path: "/tmp/rec.wav"},
		{spec: "file:///tmp/rec.wav", kind: EndpointFile, path: "/tmp/rec.wav"},
		{spec: "voice.opus", kind: EndpointFile, path: "voice.opus"},
		{spec: "notes.txt", err: true},
	}
	for _, c := range cases {
		got, err := ParseEndpoint(c.spec)
		if c.err {
			if err == nil {
				t.Errorf("%q: expected error, got %+v", c.spec, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.spec, err)
			continue
		}
		if got.Kind != c.kind || got.Addr != c.addr || got.Path != c.path || (c.kind == EndpointRTP && got.PayloadType != c.pt) {
			t.Errorf("%q: got %+v", c.spec, got)
		}
	}
	if ep, _ := ParseEndpoint("rtp://auto:0"); !ep.Symmetric() {
		t.Error("rtp://auto:0 should be symmetric")
	}
	if ep, _ := ParseEndpoint("rtp://1.2.3.4:5"); ep.Symmetric() {
		t.Error("fixed address reported as symmetric")
	}
}

func TestParseAudioPlanValidation(t *testing.T) {
	ok := []struct{ in, out string }{
		{"silence", "none"},
		{"echo", "echo"},
		{"/a.mp3", "/b.wav"},
		{"rtp://0.0.0.0:4000", "rtp://auto:0"},
		{"rtp://0.0.0.0:4000", "rtp://10.0.0.5:18000"},
		{"/a.wav", "rtp://10.0.0.5:18000"},
	}
	for _, c := range ok {
		if _, err := ParseAudioPlan(c.in, c.out); err != nil {
			t.Errorf("in=%q out=%q: unexpected error %v", c.in, c.out, err)
		}
	}
	bad := []struct{ in, out string }{
		{"echo", "none"},
		{"silence", "echo"},
		{"rtp://auto:0", "none"},
		{"/a.mp3", "rtp://auto:0"},
		{"silence", "/rec.mp3"},
	}
	for _, c := range bad {
		if _, err := ParseAudioPlan(c.in, c.out); err == nil {
			t.Errorf("in=%q out=%q: expected error", c.in, c.out)
		}
	}
	plan, _ := ParseAudioPlan("/a.mp3", "rtp://10.0.0.5:18000?pt=96")
	if got := plan.Describe(); got != "in=file:/a.mp3 out=rtp://10.0.0.5:18000(pt=96)" {
		t.Errorf("Describe=%q", got)
	}
}

func TestPolicy(t *testing.T) {
	if _, err := ParsePolicy("maybe", nil, 0); err == nil {
		t.Fatal("bad mode accepted")
	}
	if _, err := ParsePolicy("auto", nil, -time.Second); err == nil {
		t.Fatal("negative delay accepted")
	}
	p, err := ParsePolicy("", nil, 0)
	if err != nil || p.Mode != ModeManual {
		t.Fatalf("default mode: %+v %v", p, err)
	}
	peer := types.NewJID("51999888777", types.DefaultUserServer)
	lid := types.NewJID("123456789012345", types.HiddenUserServer)

	if d := p.Decide(peer); d.Answer || d.Reject {
		t.Fatalf("manual should not act: %+v", d)
	}
	rej, _ := ParsePolicy("reject", nil, 0)
	if d := rej.Decide(peer); !d.Reject {
		t.Fatalf("reject mode: %+v", d)
	}
	auto, _ := ParsePolicy("auto", nil, time.Second)
	if d := auto.Decide(peer); !d.Answer {
		t.Fatalf("auto without allow list should answer: %+v", d)
	}
	allow, _ := ParsePolicy("auto", []string{"+51 999 888 777, 51911222333@s.whatsapp.net"}, 0)
	if len(allow.Allow) != 2 || allow.Allow[0] != "51999888777" || allow.Allow[1] != "51911222333" {
		t.Fatalf("allow list normalized to %v", allow.Allow)
	}
	if !allow.Allowed(peer) {
		t.Fatal("allowed phone rejected")
	}
	if allow.Allowed(types.NewJID("51900000000", types.DefaultUserServer)) {
		t.Fatal("unknown phone allowed")
	}
	if allow.Allowed(lid) {
		t.Fatal("unknown LID allowed")
	}
	// A LID offer with the phone identity resolved alongside still matches.
	if !allow.Allowed(lid, peer) {
		t.Fatal("LID with resolved phone rejected")
	}
	if d := allow.Decide(lid); d.Answer {
		t.Fatal("LID-only caller auto-answered against a phone allow list")
	}
}
