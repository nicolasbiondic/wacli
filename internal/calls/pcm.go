// Package calls answers WhatsApp voice calls on top of meowcaller and bridges
// the audio to files or to an RTP peer (an Asterisk ExternalMedia channel, for
// example). Everything here is 16 kHz mono PCM: meowcaller decodes the peer's
// MLow stream to 60 ms float32 frames and expects the same back.
package calls

import (
	"encoding/binary"
	"math"
)

// SampleRate and FrameSamples mirror meowcaller's fixed audio format.
const (
	SampleRate   = 16000
	FrameSamples = 960 // 60 ms
)

// FloatToS16BE encodes float32 samples in [-1, 1] as big-endian signed 16-bit
// PCM (the RTP L16 wire format). Samples outside the range are clipped.
func FloatToS16BE(samples []float32) []byte {
	out := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.BigEndian.PutUint16(out[i*2:], uint16(floatToS16(s)))
	}
	return out
}

// S16BEToFloat decodes big-endian signed 16-bit PCM into float32 samples. A
// trailing odd byte is ignored.
func S16BEToFloat(b []byte) []float32 {
	n := len(b) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		out[i] = float32(int16(binary.BigEndian.Uint16(b[i*2:]))) / 32768
	}
	return out
}

func floatToS16(s float32) int16 {
	if s != s { // NaN
		return 0
	}
	v := float64(s) * 32768
	switch {
	case v > 32767:
		return math.MaxInt16
	case v < -32768:
		return math.MinInt16
	}
	return int16(math.Round(v))
}
