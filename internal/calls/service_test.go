package calls

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

var (
	teamPN  = types.NewJID("51991000001", types.DefaultUserServer)
	teamLID = types.NewJID("208000000001", types.HiddenUserServer)
	client  = types.NewJID("51987000002", types.DefaultUserServer)
)

func echoPlan(t *testing.T) AudioPlan {
	t.Helper()
	p, err := ParseAudioPlan("echo", "echo")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func sharedService(t *testing.T, mode Mode, allow []string, mut func(*Options)) (*Service, *fakeEngine, *eventLog) {
	t.Helper()
	policy, err := ParsePolicy(string(mode), allow, 0)
	if err != nil {
		t.Fatal(err)
	}
	log := newEventLog()
	opts := Options{
		Policy:        policy,
		AutoPlan:      echoPlan(t),
		SharedSession: true,
		Logger:        zerolog.Nop(),
		OnEvent:       log.on,
		Alternates: func(j types.JID) []types.JID {
			if j == teamLID {
				return []types.JID{teamPN}
			}
			return nil
		},
	}
	if mut != nil {
		mut(&opts)
	}
	eng := &fakeEngine{}
	s := NewForEngine(eng, opts)
	t.Cleanup(func() { s.Shutdown(context.Background()) })
	return s, eng, log
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSharedManualAnswerHangupReleasesEverything(t *testing.T) {
	s, eng, log := sharedService(t, ModeManual, nil, nil)
	call := eng.ring("c1", teamPN)
	if got := s.List(); len(got) != 1 || got[0].State != "ringing" {
		t.Fatalf("list after ring = %+v", got)
	}
	if err := s.Answer("c1", echoPlan(t)); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	s.mu.Lock()
	tc := s.calls["c1"]
	bridge := tc.bridge
	timers := len(tc.timers)
	s.mu.Unlock()
	if call.answers.Load() != 1 || call.sink == nil || call.source == nil {
		t.Fatalf("audio not attached before accept: answers=%d sink=%v source=%v", call.answers.Load(), call.sink, call.source)
	}
	if timers == 0 {
		t.Fatal("no ring/max-duration timers tracked")
	}
	if err := s.Hangup("c1"); err != nil {
		t.Fatalf("Hangup: %v", err)
	}
	ended, ok := log.find("call_ended")
	if !ok || ended["reason"] != "hangup" {
		t.Fatalf("call_ended = %v (%v), events %v", ended, ok, log.names())
	}
	if _, ok := ended["duration_s"]; !ok {
		t.Fatal("call_ended without duration")
	}
	if !bridge.Source.(*loopback).closed.Load() {
		t.Fatal("audio bridge left open after the call ended")
	}
	s.mu.Lock()
	left := len(tc.timers)
	s.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d timers left after end", left)
	}
	want := []string{"call_incoming", "call_answered", "call_ended"}
	if got := log.names(); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if err := s.Hangup("c1"); !errors.Is(err, ErrCallNotFound) {
		t.Fatalf("second hangup = %v, want ErrCallNotFound", err)
	}
}

func TestSharedSessionNeverRejects(t *testing.T) {
	s, eng, log := sharedService(t, ModeReject, nil, nil)
	call := eng.ring("c1", teamPN)
	if call.rejects.Load() != 0 {
		t.Fatal("shared session rejected a call: the phone could no longer pick it up")
	}
	if err := s.Hangup("c1"); err != nil {
		t.Fatalf("Hangup ringing: %v", err)
	}
	if call.rejects.Load() != 0 {
		t.Fatal("hanging up a ringing call on a shared session sent a reject")
	}
	if f, _ := log.find("call_ended"); f["reason"] != "dropped" {
		t.Fatalf("call_ended = %v, want reason dropped", f)
	}
}

func TestDedicatedSessionHangupOfRingingCallRejects(t *testing.T) {
	s, eng, _ := sharedService(t, ModeManual, nil, func(o *Options) { o.SharedSession = false })
	call := eng.ring("c1", teamPN)
	if err := s.Hangup("c1"); err != nil {
		t.Fatal(err)
	}
	if call.rejects.Load() != 1 {
		t.Fatal("calls serve no longer rejects a ringing call on hangup")
	}
}

func TestEngageOfferFiltersByAllowListGroupsAndCapacity(t *testing.T) {
	s, eng, log := sharedService(t, ModeManual, []string{"+51 991 000 001"}, func(o *Options) { o.MaxTracked = 1 })
	offer := func(id string, creator types.JID, group bool) *events.CallOffer {
		ev := &events.CallOffer{BasicCallMeta: types.BasicCallMeta{From: creator, CallCreator: creator, CallID: id}, Data: &waBinary.Node{Tag: "offer"}}
		if group {
			ev.GroupJID = types.NewJID("12036300000", types.GroupServer)
		}
		return ev
	}
	if s.EngageOffer(offer("x1", client, false)) {
		t.Fatal("engaged a caller outside --calls-allow")
	}
	if f, _ := log.find("call_ignored"); f["reason"] != "not_allowed" {
		t.Fatalf("call_ignored = %v", f)
	}
	if !s.EngageOffer(offer("x2", teamLID, false)) {
		t.Fatal("LID caller whose phone number is allowed was not engaged")
	}
	if s.EngageOffer(offer("x3", teamPN, true)) {
		t.Fatal("engaged a group call")
	}
	if s.EngageOffer(nil) {
		t.Fatal("engaged a nil offer")
	}
	eng.ring("x2", teamLID)
	if s.EngageOffer(offer("x4", teamPN, false)) {
		t.Fatal("engaged beyond MaxTracked")
	}
}

func TestMaxConcurrentAndAutoBusy(t *testing.T) {
	s, eng, log := sharedService(t, ModeAuto, nil, nil)
	first := eng.ring("a1", teamPN)
	waitFor(t, "first auto-answer", func() bool { return first.answers.Load() == 1 })
	second := eng.ring("a2", teamPN)
	waitFor(t, "call_busy", func() bool { _, ok := log.find("call_busy"); return ok })
	if second.answers.Load() != 0 {
		t.Fatal("answered a second call beyond --calls-max-concurrent")
	}
	if err := s.Answer("a2", echoPlan(t)); !errors.Is(err, ErrBusy) {
		t.Fatalf("manual answer of second = %v, want ErrBusy", err)
	}
	if err := s.Hangup("a1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Answer("a2", echoPlan(t)); err != nil {
		t.Fatalf("answer after the first ended: %v", err)
	}
}

func TestEnginePanicsAreContained(t *testing.T) {
	s, eng, log := sharedService(t, ModeManual, nil, nil)
	call := eng.ring("p1", teamPN)
	call.panicOn = "answer"
	err := s.Answer("p1", echoPlan(t))
	if err == nil {
		t.Fatal("Answer did not report the engine panic")
	}
	s.mu.Lock()
	tc := s.calls["p1"]
	s.mu.Unlock()
	if !tc.answeredAt.IsZero() || tc.bridge != nil {
		t.Fatal("failed answer left the call marked answered or holding audio")
	}
	if _, ok := log.find("call_error"); !ok {
		t.Fatalf("no call_error event: %v", log.names())
	}
	call.panicOn = ""
	if err := s.Answer("p1", echoPlan(t)); err != nil {
		t.Fatalf("answer after a recovered panic: %v", err)
	}
	call.panicOn = "hangup"
	if err := s.Hangup("p1"); err == nil {
		t.Fatal("Hangup did not report the engine panic")
	}
	if s.Live() != 0 {
		t.Fatal("a call whose hangup panicked is still live")
	}

	// A panic in one of the engine's own goroutines hangs up live calls.
	c2 := eng.ring("p2", teamPN)
	if err := s.Answer("p2", echoPlan(t)); err != nil {
		t.Fatal(err)
	}
	s.onEnginePanic(meowcaller.PanicInfo{Where: "audio send loop", Value: "boom"})
	waitFor(t, "panic cleanup", func() bool { return c2.hangups.Load() == 1 && s.Live() == 0 })
	if _, ok := log.find("call_engine_panic"); !ok {
		t.Fatal("no call_engine_panic event")
	}
	// Callbacks into a panicking OnEvent never escape either.
	s.opts.OnEvent = func(string, map[string]any) { panic("listener bug") }
	eng.ring("p3", teamPN)
}

func TestRingTimeoutForgetsStuckCalls(t *testing.T) {
	s, eng, log := sharedService(t, ModeManual, nil, func(o *Options) { o.RingTimeout = 30 * time.Millisecond })
	eng.ring("r1", teamPN)
	waitFor(t, "ring timeout", func() bool { return s.Live() == 0 })
	if f, _ := log.find("call_ended"); f["reason"] != "ring_timeout" {
		t.Fatalf("call_ended = %v", f)
	}
}

func TestShutdownReleasesCallsTheEngineNeverEnds(t *testing.T) {
	s, eng, log := sharedService(t, ModeManual, nil, nil)
	call := eng.ring("s1", teamPN)
	call.stuck = true
	if err := s.Answer("s1", echoPlan(t)); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	bridge := s.calls["s1"].bridge
	s.mu.Unlock()
	start := time.Now()
	s.Shutdown(context.Background())
	if time.Since(start) > 4*time.Second {
		t.Fatal("shutdown hung")
	}
	if s.Live() != 0 || !bridge.Source.(*loopback).closed.Load() {
		t.Fatal("shutdown left a live call or an open bridge")
	}
	if f, _ := log.find("call_ended"); f["reason"] != "shutdown" {
		t.Fatalf("call_ended = %v", f)
	}
	eng.ring("s2", teamPN)
	if s.Live() != 0 {
		t.Fatal("tracked a call after shutdown")
	}
}

func TestEventHubDropsInsteadOfBlocking(t *testing.T) {
	h := NewEventHub(1, 2)
	ch, cancel, err := h.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Subscribe(); !errors.Is(err, ErrTooManyWatchers) {
		t.Fatalf("second subscriber = %v, want cap", err)
	}
	for i := 0; i < 5; i++ {
		h.Publish("call_incoming", map[string]any{"i": i})
	}
	<-ch
	<-ch
	h.Publish("call_ended", nil)
	line := string(<-ch)
	if want := `"dropped":3`; !strings.Contains(line, want) {
		t.Fatalf("line %s lacks %s", line, want)
	}
	cancel()
	cancel()
	h.Close()
	if _, ok := <-ch; ok {
		t.Fatal("channel open after cancel")
	}
}
