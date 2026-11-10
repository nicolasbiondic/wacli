package calls

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// EndpointKind says where a call's audio comes from or goes to.
type EndpointKind int

const (
	// EndpointNone discards inbound audio or sends silence outbound.
	EndpointNone EndpointKind = iota
	// EndpointEcho loops the peer's audio back to them (lab round-trip check).
	EndpointEcho
	// EndpointFile reads from or records to a local file (.wav/.mp3/.opus/.pcm).
	EndpointFile
	// EndpointRTP sends or receives L16/16000 RTP on a UDP address.
	EndpointRTP
)

func (k EndpointKind) String() string {
	switch k {
	case EndpointNone:
		return "none"
	case EndpointEcho:
		return "echo"
	case EndpointFile:
		return "file"
	case EndpointRTP:
		return "rtp"
	default:
		return fmt.Sprintf("kind(%d)", int(k))
	}
}

// Endpoint is a parsed --audio-in / --audio-out value.
type Endpoint struct {
	Kind EndpointKind
	// Path is the file path for EndpointFile.
	Path string
	// Addr is host:port for EndpointRTP. For an outbound endpoint the special
	// host "auto" means "reply to whoever sent us RTP last" (symmetric RTP).
	Addr string
	// PayloadType is the RTP payload type (EndpointRTP only).
	PayloadType uint8
	// Raw keeps the original spec for logs.
	Raw string
}

// Symmetric reports whether an RTP endpoint should learn its remote address
// from inbound packets instead of using a fixed one.
func (e Endpoint) Symmetric() bool {
	host, _, err := net.SplitHostPort(e.Addr)
	return e.Kind == EndpointRTP && err == nil && host == "auto"
}

// ParseEndpoint turns a CLI spec into an Endpoint:
//
//	""  | none | null | silence      -> EndpointNone
//	echo                             -> EndpointEcho
//	rtp://host:port[?pt=118]         -> EndpointRTP
//	rtp://auto:0                     -> EndpointRTP, symmetric (outbound only)
//	file:/path.wav | /path.wav | x.mp3 -> EndpointFile
func ParseEndpoint(spec string) (Endpoint, error) {
	raw := spec
	spec = strings.TrimSpace(spec)
	switch strings.ToLower(spec) {
	case "", "none", "null", "silence", "-":
		return Endpoint{Kind: EndpointNone, Raw: raw}, nil
	case "echo", "loopback":
		return Endpoint{Kind: EndpointEcho, Raw: raw}, nil
	}
	if strings.HasPrefix(strings.ToLower(spec), "rtp://") {
		u, err := url.Parse(spec)
		if err != nil {
			return Endpoint{}, fmt.Errorf("audio endpoint %q: %w", raw, err)
		}
		host := u.Hostname()
		port := u.Port()
		if host == "" || port == "" {
			return Endpoint{}, fmt.Errorf("audio endpoint %q: rtp:// needs host:port", raw)
		}
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return Endpoint{}, fmt.Errorf("audio endpoint %q: bad port %q", raw, port)
		}
		pt := uint8(DefaultPayloadType)
		if v := u.Query().Get("pt"); v != "" {
			n, err := strconv.ParseUint(v, 10, 7)
			if err != nil {
				return Endpoint{}, fmt.Errorf("audio endpoint %q: bad payload type %q", raw, v)
			}
			pt = uint8(n)
		}
		return Endpoint{Kind: EndpointRTP, Addr: net.JoinHostPort(host, port), PayloadType: pt, Raw: raw}, nil
	}
	path := spec
	if strings.HasPrefix(strings.ToLower(spec), "file:") {
		path = strings.TrimPrefix(spec[5:], "//")
	}
	if path == "" {
		return Endpoint{}, fmt.Errorf("audio endpoint %q: empty file path", raw)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav", ".mp3", ".opus", ".ogg", ".pcm", ".raw":
	default:
		return Endpoint{}, fmt.Errorf("audio endpoint %q: unsupported file type (use .wav, .mp3, .opus, .pcm) or rtp://host:port", raw)
	}
	return Endpoint{Kind: EndpointFile, Path: path, Raw: raw}, nil
}

// AudioPlan pairs the two directions of a call's audio.
type AudioPlan struct {
	// In is what the peer hears (played into the call).
	In Endpoint
	// Out is where the peer's voice goes.
	Out Endpoint
}

// ParseAudioPlan parses both specs and validates the combination.
func ParseAudioPlan(in, out string) (AudioPlan, error) {
	inEP, err := ParseEndpoint(in)
	if err != nil {
		return AudioPlan{}, err
	}
	outEP, err := ParseEndpoint(out)
	if err != nil {
		return AudioPlan{}, err
	}
	plan := AudioPlan{In: inEP, Out: outEP}
	if err := plan.Validate(); err != nil {
		return AudioPlan{}, err
	}
	return plan, nil
}

// Validate rejects combinations that cannot work.
func (p AudioPlan) Validate() error {
	if (p.In.Kind == EndpointEcho) != (p.Out.Kind == EndpointEcho) {
		return fmt.Errorf("echo must be used for both --audio-in and --audio-out")
	}
	if p.In.Kind == EndpointRTP && p.In.Symmetric() {
		return fmt.Errorf("--audio-in rtp://auto is not valid: the inbound side must bind a real address")
	}
	if p.Out.Kind == EndpointRTP && p.Out.Symmetric() && p.In.Kind != EndpointRTP {
		return fmt.Errorf("--audio-out rtp://auto needs an rtp:// --audio-in to learn the remote address from")
	}
	if p.Out.Kind == EndpointFile && strings.ToLower(filepath.Ext(p.Out.Path)) != ".wav" {
		return fmt.Errorf("--audio-out file must be a .wav recording")
	}
	return nil
}

// Describe is a short human summary for logs.
func (p AudioPlan) Describe() string {
	return fmt.Sprintf("in=%s out=%s", describeEndpoint(p.In), describeEndpoint(p.Out))
}

func describeEndpoint(e Endpoint) string {
	switch e.Kind {
	case EndpointFile:
		return "file:" + e.Path
	case EndpointRTP:
		return fmt.Sprintf("rtp://%s(pt=%d)", e.Addr, e.PayloadType)
	default:
		return e.Kind.String()
	}
}
