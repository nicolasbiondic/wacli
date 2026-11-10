package calls

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/purpshell/meowcaller"
)

// Bridge holds the live audio endpoints of one answered call.
type Bridge struct {
	// Source is played into the call (nil means silence).
	Source meowcaller.AudioSource
	// Sink receives the peer's decoded audio (nil discards it).
	Sink meowcaller.AudioSink

	closeOnce sync.Once
	closers   []io.Closer
	rtp       *rtpSession
}

// Close releases files and sockets. Safe to call more than once.
func (b *Bridge) Close() error {
	var err error
	b.closeOnce.Do(func() {
		for _, c := range b.closers {
			if cerr := c.Close(); cerr != nil && err == nil {
				err = cerr
			}
		}
	})
	return err
}

// Stats returns RTP counters when the bridge carries RTP, for diagnostics.
func (b *Bridge) Stats() (BridgeStats, bool) {
	if b == nil || b.rtp == nil {
		return BridgeStats{}, false
	}
	return b.rtp.stats(), true
}

// BridgeStats are the RTP counters of a bridge.
type BridgeStats struct {
	PacketsIn    uint64
	PacketsOut   uint64
	PacketsLost  uint64
	FramesDroped uint64
	Remote       string
}

// BuildBridge opens the endpoints of a plan. Files are opened immediately so a
// bad path fails before the call is answered.
func BuildBridge(plan AudioPlan) (*Bridge, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	b := &Bridge{}
	if plan.In.Kind == EndpointEcho {
		loop := newLoopback(8)
		b.Source, b.Sink = loop, loop
		b.closers = append(b.closers, loop)
		return b, nil
	}
	if plan.In.Kind == EndpointRTP || plan.Out.Kind == EndpointRTP {
		sess, err := newRTPSession(plan)
		if err != nil {
			return nil, err
		}
		b.rtp = sess
		b.closers = append(b.closers, sess)
		if plan.In.Kind == EndpointRTP {
			b.Source = sess
		}
		if plan.Out.Kind == EndpointRTP {
			b.Sink = sess
		}
	}
	if plan.In.Kind == EndpointFile {
		src, err := openFileSource(plan.In.Path)
		if err != nil {
			b.Close()
			return nil, err
		}
		b.Source = src
		b.closers = append(b.closers, src)
	}
	if plan.Out.Kind == EndpointFile {
		if dir := filepath.Dir(plan.Out.Path); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				b.Close()
				return nil, fmt.Errorf("create recording dir: %w", err)
			}
		}
		rec, err := meowcaller.WAVRecorder(plan.Out.Path)
		if err != nil {
			b.Close()
			return nil, fmt.Errorf("open recording %s: %w", plan.Out.Path, err)
		}
		b.Sink = rec
		b.closers = append(b.closers, rec)
	}
	return b, nil
}

func openFileSource(path string) (meowcaller.AudioSource, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav":
		return meowcaller.WAVFile(path)
	case ".mp3":
		return meowcaller.MP3File(path)
	case ".opus", ".ogg":
		return meowcaller.OpusFile(path)
	case ".pcm", ".raw":
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		return meowcaller.PCMStream(f), nil
	}
	return nil, fmt.Errorf("unsupported audio file %s", path)
}

// loopback is the echo endpoint: frames written by the sink are read back by
// the source a little later, so the caller hears themselves. Underruns read
// silence instead of blocking the engine's send loop.
type loopback struct {
	frames chan []float32
	closed atomic.Bool
}

func newLoopback(depth int) *loopback {
	return &loopback{frames: make(chan []float32, depth)}
}

func (l *loopback) WriteFrame(frame []float32) error {
	if l.closed.Load() {
		return nil
	}
	cp := make([]float32, len(frame))
	copy(cp, frame)
	select {
	case l.frames <- cp:
	default: // full: drop the oldest to bound latency
		select {
		case <-l.frames:
		default:
		}
		select {
		case l.frames <- cp:
		default:
		}
	}
	return nil
}

func (l *loopback) ReadFrame() ([]float32, error) {
	if l.closed.Load() {
		return nil, io.EOF
	}
	select {
	case f := <-l.frames:
		return f, nil
	default:
		return make([]float32, FrameSamples), nil
	}
}

func (l *loopback) Close() error {
	l.closed.Store(true)
	return nil
}

// rtpSession is one UDP socket carrying L16 both ways: the sink packetizes
// the peer's voice towards the remote address, a reader goroutine turns
// incoming packets into frames for the source.
type rtpSession struct {
	conn    *net.UDPConn
	remote  atomic.Pointer[net.UDPAddr]
	auto    bool
	sendPT  uint8
	pack    *L16Packetizer
	packMu  sync.Mutex
	depack  *L16Depacketizer
	frames  chan []float32
	closed  atomic.Bool
	done    chan struct{}
	pktOut  atomic.Uint64
	pktIn   atomic.Uint64
	dropped atomic.Uint64
	lostMu  sync.Mutex
}

func newRTPSession(plan AudioPlan) (*rtpSession, error) {
	bindAddr := ":0"
	recvPT := uint8(0)
	if plan.In.Kind == EndpointRTP {
		bindAddr = plan.In.Addr
		recvPT = plan.In.PayloadType
	}
	laddr, err := net.ResolveUDPAddr("udp", bindAddr)
	if err != nil {
		return nil, fmt.Errorf("rtp bind %s: %w", bindAddr, err)
	}
	conn, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return nil, fmt.Errorf("rtp bind %s: %w", bindAddr, err)
	}
	s := &rtpSession{
		conn:   conn,
		depack: NewL16Depacketizer(recvPT, FrameSamples),
		frames: make(chan []float32, 16),
		done:   make(chan struct{}),
	}
	if plan.Out.Kind == EndpointRTP {
		s.sendPT = plan.Out.PayloadType
		if plan.Out.Symmetric() {
			s.auto = true
		} else {
			raddr, err := net.ResolveUDPAddr("udp", plan.Out.Addr)
			if err != nil {
				conn.Close()
				return nil, fmt.Errorf("rtp remote %s: %w", plan.Out.Addr, err)
			}
			s.remote.Store(raddr)
		}
		s.pack = NewL16Packetizer(s.sendPT, rand.Uint32(), uint16(rand.Uint32()), rand.Uint32())
	}
	go s.readLoop()
	return s, nil
}

func (s *rtpSession) readLoop() {
	defer close(s.done)
	buf := make([]byte, 2048)
	for {
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			if s.closed.Load() || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if s.auto && from != nil {
			s.remote.Store(from)
		}
		s.pktIn.Add(1)
		s.lostMu.Lock()
		frames := s.depack.Push(buf[:n])
		s.lostMu.Unlock()
		for _, f := range frames {
			select {
			case s.frames <- f:
			default:
				s.dropped.Add(1)
				select {
				case <-s.frames:
				default:
				}
				select {
				case s.frames <- f:
				default:
				}
			}
		}
	}
}

// ReadFrame feeds the call from inbound RTP; silence when nothing arrived.
func (s *rtpSession) ReadFrame() ([]float32, error) {
	if s.closed.Load() {
		return nil, io.EOF
	}
	select {
	case f := <-s.frames:
		return f, nil
	default:
		return make([]float32, FrameSamples), nil
	}
}

// WriteFrame sends the peer's voice as L16 packets to the remote address.
func (s *rtpSession) WriteFrame(frame []float32) error {
	if s.closed.Load() || s.pack == nil {
		return nil
	}
	remote := s.remote.Load()
	s.packMu.Lock()
	packets := s.pack.Push(frame)
	s.packMu.Unlock()
	if remote == nil {
		return nil // symmetric mode before the first inbound packet
	}
	for _, p := range packets {
		if _, err := s.conn.WriteToUDP(p, remote); err != nil {
			if s.closed.Load() {
				return nil
			}
			return err
		}
		s.pktOut.Add(1)
	}
	return nil
}

func (s *rtpSession) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	err := s.conn.Close()
	<-s.done
	return err
}

// LocalAddr is the bound UDP address (useful when binding to port 0).
func (s *rtpSession) LocalAddr() net.Addr { return s.conn.LocalAddr() }

func (s *rtpSession) stats() BridgeStats {
	st := BridgeStats{PacketsIn: s.pktIn.Load(), PacketsOut: s.pktOut.Load(), FramesDroped: s.dropped.Load()}
	s.lostMu.Lock()
	st.PacketsLost = s.depack.Lost
	s.lostMu.Unlock()
	if r := s.remote.Load(); r != nil {
		st.Remote = r.String()
	}
	return st
}
