package calls

import (
	"math"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEchoBridgeLoopsFramesBack(t *testing.T) {
	plan, err := ParseAudioPlan("echo", "echo")
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildBridge(plan)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	// Empty: silence, never EOF.
	f, err := b.Source.ReadFrame()
	if err != nil || len(f) != FrameSamples {
		t.Fatalf("idle read: %v len=%d", err, len(f))
	}
	in := make([]float32, FrameSamples)
	in[10] = 0.25
	if err := b.Sink.WriteFrame(in); err != nil {
		t.Fatal(err)
	}
	in[10] = 0 // the sink must have copied the frame
	f, err = b.Source.ReadFrame()
	if err != nil || f[10] != 0.25 {
		t.Fatalf("echo frame: %v sample=%f", err, f[10])
	}
	// Overflow drops the oldest instead of blocking.
	for i := 0; i < 20; i++ {
		x := make([]float32, FrameSamples)
		x[0] = float32(i)
		_ = b.Sink.WriteFrame(x)
	}
	f, _ = b.Source.ReadFrame()
	if f[0] < 12 {
		t.Fatalf("oldest frames should have been dropped, got %f", f[0])
	}
	b.Close()
	if _, err := b.Source.ReadFrame(); err == nil {
		t.Fatal("read after close should return EOF")
	}
}

func TestRTPBridgeRoundTripOverLoopback(t *testing.T) {
	// The bridge binds a UDP port and replies to whoever sends to it
	// (rtp://auto), which is how an Asterisk ExternalMedia channel behaves.
	plan, err := ParseAudioPlan("rtp://127.0.0.1:0", "rtp://auto:0")
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildBridge(plan)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	local := b.rtp.LocalAddr().(*net.UDPAddr)

	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	// Before any inbound packet, outbound frames go nowhere but do not fail.
	if err := b.Sink.WriteFrame(make([]float32, FrameSamples)); err != nil {
		t.Fatal(err)
	}

	// Peer (Asterisk) sends 3 packets = one 60 ms frame of a 1 kHz tone.
	tone := make([]float32, FrameSamples)
	for i := range tone {
		tone[i] = float32(0.5 * math.Sin(2*math.Pi*1000*float64(i)/SampleRate))
	}
	pk := NewL16Packetizer(DefaultPayloadType, 42, 1, 1)
	for _, p := range pk.Push(tone) {
		if _, err := peer.WriteToUDP(p, local); err != nil {
			t.Fatal(err)
		}
	}
	var got []float32
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f, err := b.Source.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		if f[1] != 0 { // not silence
			got = f
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got == nil {
		t.Fatal("no inbound frame decoded from RTP")
	}
	for i := range got {
		if math.Abs(float64(got[i]-tone[i])) > 1.0/32768+1e-6 {
			t.Fatalf("sample %d: %f vs %f", i, got[i], tone[i])
		}
	}

	// Now the peer's address is known: the peer's voice frame goes out as 3 packets.
	voice := make([]float32, FrameSamples)
	for i := range voice {
		voice[i] = float32(i%64) / 64
	}
	if err := b.Sink.WriteFrame(voice); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	dp := NewL16Depacketizer(DefaultPayloadType, FrameSamples)
	buf := make([]byte, 2048)
	var frames [][]float32
	for len(frames) == 0 {
		n, _, err := peer.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("peer read: %v", err)
		}
		frames = append(frames, dp.Push(buf[:n])...)
	}
	if dp.Received != 3 {
		t.Fatalf("peer received %d packets, want 3", dp.Received)
	}
	for i := range voice {
		if math.Abs(float64(frames[0][i]-voice[i])) > 1.0/32768+1e-6 {
			t.Fatalf("outbound sample %d: %f vs %f", i, frames[0][i], voice[i])
		}
	}
	st, ok := b.Stats()
	if !ok || st.PacketsIn != 3 || st.PacketsOut != 3 || st.Remote != peer.LocalAddr().String() {
		t.Fatalf("stats %+v", st)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal("second close failed")
	}
}

func TestFileBridgeOpensRecordingAndRejectsMissingSource(t *testing.T) {
	dir := t.TempDir()
	plan, err := ParseAudioPlan(filepath.Join(dir, "missing.wav"), filepath.Join(dir, "sub", "rec.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildBridge(plan); err == nil {
		t.Fatal("missing source file accepted")
	}
	plan, _ = ParseAudioPlan("silence", filepath.Join(dir, "sub", "rec.wav"))
	b, err := BuildBridge(plan)
	if err != nil {
		t.Fatal(err)
	}
	if b.Source != nil {
		t.Fatal("silence should leave the source nil")
	}
	frame := make([]float32, FrameSamples)
	frame[0] = 0.5
	if err := b.Sink.WriteFrame(frame); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "sub", "rec.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 44+FrameSamples*2 {
		t.Fatalf("recording size %d, want %d (44-byte header + one s16 frame)", info.Size(), 44+FrameSamples*2)
	}
}
