package calls

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// RTP framing for linear PCM (L16, RFC 3551 §4.5.11) at 16 kHz mono: the
// format Asterisk calls slin16 and offers through ExternalMedia / chan_rtp.
// The packetizer turns meowcaller's 60 ms frames into 20 ms packets (320
// samples, 640 bytes), which is what Asterisk expects by default; the
// depacketizer does the reverse.

const (
	rtpHeaderLen = 12
	rtpVersion   = 2
	// PacketSamples is the number of samples per RTP packet (20 ms at 16 kHz).
	PacketSamples = 320
	// DefaultPayloadType is the dynamic payload type used for L16/16000 when
	// none is configured. Asterisk ExternalMedia with format=slin16 uses a
	// dynamic type as well; both ends only need to agree.
	DefaultPayloadType = 118
)

var errRTPShort = errors.New("rtp: packet shorter than header")

// RTPPacket is a parsed RTP packet (header fields plus payload).
type RTPPacket struct {
	Marker      bool
	PayloadType uint8
	Sequence    uint16
	Timestamp   uint32
	SSRC        uint32
	Payload     []byte
}

// MarshalRTP serializes a packet without CSRCs or extensions.
func MarshalRTP(p RTPPacket) []byte {
	buf := make([]byte, rtpHeaderLen+len(p.Payload))
	buf[0] = rtpVersion << 6
	buf[1] = p.PayloadType & 0x7f
	if p.Marker {
		buf[1] |= 0x80
	}
	binary.BigEndian.PutUint16(buf[2:], p.Sequence)
	binary.BigEndian.PutUint32(buf[4:], p.Timestamp)
	binary.BigEndian.PutUint32(buf[8:], p.SSRC)
	copy(buf[rtpHeaderLen:], p.Payload)
	return buf
}

// ParseRTP decodes an RTP packet, skipping CSRCs, header extensions and
// padding so the payload is exactly the media bytes.
func ParseRTP(b []byte) (RTPPacket, error) {
	if len(b) < rtpHeaderLen {
		return RTPPacket{}, errRTPShort
	}
	if v := b[0] >> 6; v != rtpVersion {
		return RTPPacket{}, fmt.Errorf("rtp: unsupported version %d", v)
	}
	padding := b[0]&0x20 != 0
	extension := b[0]&0x10 != 0
	csrcCount := int(b[0] & 0x0f)
	p := RTPPacket{
		Marker:      b[1]&0x80 != 0,
		PayloadType: b[1] & 0x7f,
		Sequence:    binary.BigEndian.Uint16(b[2:]),
		Timestamp:   binary.BigEndian.Uint32(b[4:]),
		SSRC:        binary.BigEndian.Uint32(b[8:]),
	}
	offset := rtpHeaderLen + 4*csrcCount
	if len(b) < offset {
		return RTPPacket{}, errors.New("rtp: truncated CSRC list")
	}
	if extension {
		if len(b) < offset+4 {
			return RTPPacket{}, errors.New("rtp: truncated header extension")
		}
		extLen := int(binary.BigEndian.Uint16(b[offset+2:])) * 4
		offset += 4 + extLen
		if len(b) < offset {
			return RTPPacket{}, errors.New("rtp: truncated header extension body")
		}
	}
	end := len(b)
	if padding {
		if end == offset {
			return RTPPacket{}, errors.New("rtp: padding flag on empty payload")
		}
		pad := int(b[end-1])
		if pad == 0 || pad > end-offset {
			return RTPPacket{}, errors.New("rtp: invalid padding length")
		}
		end -= pad
	}
	p.Payload = b[offset:end]
	return p, nil
}

// L16Packetizer splits 16 kHz mono float32 frames into L16 RTP packets.
type L16Packetizer struct {
	PayloadType uint8
	SSRC        uint32
	seq         uint16
	timestamp   uint32
	started     bool
	pending     []float32
}

// NewL16Packetizer returns a packetizer with the given SSRC and initial
// sequence/timestamp values (callers normally pick them at random).
func NewL16Packetizer(payloadType uint8, ssrc uint32, seq uint16, timestamp uint32) *L16Packetizer {
	return &L16Packetizer{PayloadType: payloadType, SSRC: ssrc, seq: seq, timestamp: timestamp}
}

// Push appends samples and returns every complete packet they produce. The
// first packet of the stream carries the marker bit. Leftover samples that do
// not fill a packet are kept for the next call.
func (p *L16Packetizer) Push(samples []float32) [][]byte {
	p.pending = append(p.pending, samples...)
	var out [][]byte
	for len(p.pending) >= PacketSamples {
		chunk := p.pending[:PacketSamples]
		p.pending = p.pending[PacketSamples:]
		out = append(out, MarshalRTP(RTPPacket{
			Marker:      !p.started,
			PayloadType: p.PayloadType,
			Sequence:    p.seq,
			Timestamp:   p.timestamp,
			SSRC:        p.SSRC,
			Payload:     FloatToS16BE(chunk),
		}))
		p.started = true
		p.seq++
		p.timestamp += PacketSamples
	}
	return out
}

// L16Depacketizer reassembles incoming L16 RTP packets into fixed-size
// frames. It tolerates any packet size and ignores payload types other than
// the configured one (0 accepts any), so comfort-noise or DTMF events on the
// same port do not corrupt the audio.
type L16Depacketizer struct {
	PayloadType uint8
	FrameSize   int
	buf         []float32
	lastSeq     uint16
	haveSeq     bool
	Lost        uint64
	Received    uint64
}

// NewL16Depacketizer returns a depacketizer producing frames of frameSize
// samples (FrameSamples for meowcaller).
func NewL16Depacketizer(payloadType uint8, frameSize int) *L16Depacketizer {
	if frameSize <= 0 {
		frameSize = FrameSamples
	}
	return &L16Depacketizer{PayloadType: payloadType, FrameSize: frameSize}
}

// Push parses one datagram and appends its samples. It returns every complete
// frame now available. Malformed packets are dropped silently.
func (d *L16Depacketizer) Push(datagram []byte) [][]float32 {
	pkt, err := ParseRTP(datagram)
	if err != nil {
		return nil
	}
	if d.PayloadType != 0 && pkt.PayloadType != d.PayloadType {
		return nil
	}
	if d.haveSeq {
		if gap := int(int16(pkt.Sequence - d.lastSeq)); gap > 1 {
			d.Lost += uint64(gap - 1)
		}
	}
	d.lastSeq = pkt.Sequence
	d.haveSeq = true
	d.Received++
	d.buf = append(d.buf, S16BEToFloat(pkt.Payload)...)
	var out [][]float32
	for len(d.buf) >= d.FrameSize {
		frame := make([]float32, d.FrameSize)
		copy(frame, d.buf[:d.FrameSize])
		d.buf = d.buf[d.FrameSize:]
		out = append(out, frame)
	}
	return out
}

// Pending reports how many samples are buffered short of a full frame.
func (d *L16Depacketizer) Pending() int { return len(d.buf) }
