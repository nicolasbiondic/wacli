package calls

import (
	"bytes"
	"math"
	"testing"
)

func TestPCMRoundTrip(t *testing.T) {
	in := []float32{0, 0.5, -0.5, 1, -1, 1.7, -2, float32(math.NaN())}
	b := FloatToS16BE(in)
	if len(b) != len(in)*2 {
		t.Fatalf("len=%d want %d", len(b), len(in)*2)
	}
	// Known values: 0 -> 0x0000, 0.5 -> 0x4000, 1 -> 0x7FFF (clipped), -1 -> 0x8000, clip 1.7 -> 0x7FFF, -2 -> 0x8000, NaN -> 0
	want := []byte{0x00, 0x00, 0x40, 0x00, 0xC0, 0x00, 0x7F, 0xFF, 0x80, 0x00, 0x7F, 0xFF, 0x80, 0x00, 0x00, 0x00}
	if !bytes.Equal(b, want) {
		t.Fatalf("encode mismatch:\n got %x\nwant %x", b, want)
	}
	out := S16BEToFloat(b)
	for i, v := range []float32{0, 0.5, -0.5, 1, -1, 1, -1, 0} {
		if math.Abs(float64(out[i]-v)) > 1.0/32768+1e-6 {
			t.Fatalf("sample %d: got %f want %f", i, out[i], v)
		}
	}
	if got := S16BEToFloat([]byte{0x7f}); len(got) != 0 {
		t.Fatalf("odd trailing byte should be ignored, got %v", got)
	}
}

func TestRTPMarshalParse(t *testing.T) {
	p := RTPPacket{Marker: true, PayloadType: 118, Sequence: 65535, Timestamp: 0xDEADBEEF, SSRC: 0x12345678, Payload: []byte{1, 2, 3, 4}}
	b := MarshalRTP(p)
	if len(b) != 16 {
		t.Fatalf("len=%d", len(b))
	}
	if b[0] != 0x80 || b[1] != 0x80|118 {
		t.Fatalf("header bytes %x", b[:2])
	}
	got, err := ParseRTP(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Marker != p.Marker || got.PayloadType != p.PayloadType || got.Sequence != p.Sequence || got.Timestamp != p.Timestamp || got.SSRC != p.SSRC || !bytes.Equal(got.Payload, p.Payload) {
		t.Fatalf("roundtrip mismatch: %+v vs %+v", got, p)
	}
}

func TestRTPParseCSRCExtensionPadding(t *testing.T) {
	// V=2, P=1, X=1, CC=1; PT 96; seq 1; ts 2; ssrc 3; one CSRC; extension
	// (profile 0xBEDE, length 1 word); payload AA BB; padding of 2 bytes.
	b := []byte{
		0xB1, 96, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3,
		0, 0, 0, 9, // CSRC
		0xBE, 0xDE, 0, 1, 0x10, 0xFF, 0, 0, // extension header + 1 word
		0xAA, 0xBB,
		0x00, 0x02, // padding
	}
	p, err := ParseRTP(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.Payload, []byte{0xAA, 0xBB}) {
		t.Fatalf("payload %x", p.Payload)
	}
	if _, err := ParseRTP(b[:10]); err == nil {
		t.Fatal("short packet accepted")
	}
	bad := append([]byte{}, b...)
	bad[0] = 0x40 // version 1
	if _, err := ParseRTP(bad); err == nil {
		t.Fatal("wrong version accepted")
	}
}

func TestL16PacketizerSplitsFramesInto20ms(t *testing.T) {
	pk := NewL16Packetizer(118, 0xABCD, 100, 1000)
	frame := make([]float32, FrameSamples) // 60 ms
	for i := range frame {
		frame[i] = float32(i%100) / 100
	}
	pkts := pk.Push(frame)
	if len(pkts) != 3 {
		t.Fatalf("got %d packets from a 60 ms frame, want 3", len(pkts))
	}
	for i, raw := range pkts {
		p, err := ParseRTP(raw)
		if err != nil {
			t.Fatal(err)
		}
		if p.Sequence != uint16(100+i) || p.Timestamp != uint32(1000+i*PacketSamples) || p.SSRC != 0xABCD || p.PayloadType != 118 {
			t.Fatalf("packet %d header %+v", i, p)
		}
		if p.Marker != (i == 0) {
			t.Fatalf("packet %d marker=%v", i, p.Marker)
		}
		if len(p.Payload) != PacketSamples*2 {
			t.Fatalf("packet %d payload %d bytes", i, len(p.Payload))
		}
	}
	// A partial frame is kept until it fills a packet.
	if got := pk.Push(frame[:100]); len(got) != 0 {
		t.Fatalf("partial push produced %d packets", len(got))
	}
	if got := pk.Push(frame[:PacketSamples-100]); len(got) != 1 {
		t.Fatalf("completing push produced %d packets", len(got))
	}
}

func TestL16DepacketizerReassemblesFramesAndCountsLoss(t *testing.T) {
	pk := NewL16Packetizer(118, 7, 0, 0)
	src := make([]float32, FrameSamples*2)
	for i := range src {
		src[i] = float32(math.Sin(float64(i) / 10))
	}
	pkts := pk.Push(src) // 6 packets
	if len(pkts) != 6 {
		t.Fatalf("%d packets", len(pkts))
	}
	dp := NewL16Depacketizer(118, FrameSamples)
	var frames [][]float32
	for i, p := range pkts {
		if i == 4 {
			continue // simulate a lost packet
		}
		frames = append(frames, dp.Push(p)...)
	}
	// 5 packets * 320 = 1600 samples -> 1 full frame + 640 pending.
	if len(frames) != 1 || dp.Pending() != 640 {
		t.Fatalf("frames=%d pending=%d", len(frames), dp.Pending())
	}
	if dp.Lost != 1 || dp.Received != 5 {
		t.Fatalf("lost=%d received=%d", dp.Lost, dp.Received)
	}
	for i := range frames[0] {
		if math.Abs(float64(frames[0][i]-src[i])) > 1.0/32768+1e-6 {
			t.Fatalf("sample %d: %f vs %f", i, frames[0][i], src[i])
		}
	}
	// Other payload types (comfort noise, DTMF) are ignored.
	other := MarshalRTP(RTPPacket{PayloadType: 13, Sequence: 99, Payload: []byte{0, 0}})
	if got := dp.Push(other); len(got) != 0 || dp.Pending() != 640 {
		t.Fatal("foreign payload type was not ignored")
	}
	if got := dp.Push([]byte{1, 2}); got != nil {
		t.Fatal("garbage produced frames")
	}
}
