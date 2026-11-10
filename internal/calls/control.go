package calls

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The control socket lets `wacli calls answer|hangup|active` talk to the
// process that holds the WhatsApp connection (`wacli calls serve`). One JSON
// request per connection, one JSON reply.

// ControlSocketName is the socket file inside the store directory.
const ControlSocketName = "calls.sock"

// ControlSocketPath returns the socket path for a store.
func ControlSocketPath(storeDir string) string {
	return filepath.Join(storeDir, ControlSocketName)
}

// Controller is what the control server drives (the Service, or a fake).
type Controller interface {
	Answer(callID string, plan AudioPlan) error
	Hangup(callID string) error
	List() []CallInfo
}

// ControlRequest is the wire request.
type ControlRequest struct {
	Op       string `json:"op"`
	CallID   string `json:"call_id,omitempty"`
	AudioIn  string `json:"audio_in,omitempty"`
	AudioOut string `json:"audio_out,omitempty"`
}

// ControlResponse is the wire reply.
type ControlResponse struct {
	OK    bool       `json:"ok"`
	Error string     `json:"error,omitempty"`
	Calls []CallInfo `json:"calls,omitempty"`
}

// HandleControl executes one request against a controller. It is the single
// place where the protocol is interpreted, so it is unit-tested directly.
func HandleControl(ctl Controller, req ControlRequest) ControlResponse {
	switch strings.ToLower(strings.TrimSpace(req.Op)) {
	case "answer":
		if strings.TrimSpace(req.CallID) == "" {
			return ControlResponse{Error: "call_id is required"}
		}
		plan, err := ParseAudioPlan(req.AudioIn, req.AudioOut)
		if err != nil {
			return ControlResponse{Error: err.Error()}
		}
		if err := ctl.Answer(req.CallID, plan); err != nil {
			return ControlResponse{Error: err.Error()}
		}
		return ControlResponse{OK: true, Calls: ctl.List()}
	case "hangup":
		if strings.TrimSpace(req.CallID) == "" {
			return ControlResponse{Error: "call_id is required"}
		}
		if err := ctl.Hangup(req.CallID); err != nil {
			return ControlResponse{Error: err.Error()}
		}
		return ControlResponse{OK: true, Calls: ctl.List()}
	case "list", "active":
		return ControlResponse{OK: true, Calls: ctl.List()}
	default:
		return ControlResponse{Error: fmt.Sprintf("unknown op %q", req.Op)}
	}
}

// ControlServer serves the control socket.
type ControlServer struct {
	ln   net.Listener
	path string
	ctl  Controller
}

// ListenControl binds the socket, replacing a stale file from a previous run.
func ListenControl(path string, ctl Controller) (*ControlServer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		// Only remove it if nobody is listening.
		if c, derr := net.DialTimeout("unix", path, 300*time.Millisecond); derr == nil {
			c.Close()
			return nil, fmt.Errorf("another `wacli calls serve` is already listening on %s", path)
		}
		_ = os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return &ControlServer{ln: ln, path: path, ctl: ctl}, nil
}

// Serve accepts connections until ctx is done or the listener closes.
func (s *ControlServer) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.ln.Close()
	}()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(conn)
	}
}

func (s *ControlServer) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	var req ControlRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(ControlResponse{Error: "bad request: " + err.Error()})
		return
	}
	_ = json.NewEncoder(conn).Encode(HandleControl(s.ctl, req))
}

// Close stops listening and removes the socket file.
func (s *ControlServer) Close() error {
	err := s.ln.Close()
	_ = os.Remove(s.path)
	return err
}

// Addr returns the listener address (for tests).
func (s *ControlServer) Addr() net.Addr { return s.ln.Addr() }

// SendControl performs one request against a serve process.
func SendControl(ctx context.Context, path string, req ControlRequest) (ControlResponse, error) {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return ControlResponse{}, fmt.Errorf("connect to `wacli calls serve` at %s: %w", path, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return ControlResponse{}, err
	}
	var resp ControlResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return ControlResponse{}, fmt.Errorf("read reply: %w", err)
	}
	if !resp.OK {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}
