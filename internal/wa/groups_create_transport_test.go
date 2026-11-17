package wa

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openclaw/wacli/internal/sqliteutil"
	"go.mau.fi/libsignal/ecc"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waCert"
	"go.mau.fi/whatsmeow/proto/waWa6"
	"go.mau.fi/whatsmeow/socket"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// These tests run the real whatsmeow client (websocket, Noise transport,
// sendIQ, disconnect handling and auto-reconnect) against a local fake
// WhatsApp server that records every frame it decrypts. Nothing leaves
// loopback: the client's websocket dialer is pointed at the test listener and
// the SDK's pinned server certificate root is swapped for a test key.

type createReply int

const (
	createDropConnection createReply = iota // accept the frame, never answer, close the socket
	createAnswerGroup
	createAnswerRefusal
	createAnswerAck // an <ack> with the create's ID: neither a result nor an IQ error
)

type recordedCreate struct {
	conn  int
	id    string
	frame []byte // the decrypted frame, exactly as the client sent it
	node  *waBinary.Node
}

type fakeWAServer struct {
	t      *testing.T
	static *keys.KeyPair
	cert   []byte
	reply  func(n int) createReply

	mu      sync.Mutex
	conns   int
	creates []recordedCreate
	srv     *httptest.Server
}

var (
	testOwnJID = types.JID{User: "15550000000", Device: 7, Server: types.DefaultUserServer}
	testOwnLID = types.JID{User: "999000", Device: 7, Server: types.HiddenUserServer}
)

func newFakeWAServer(t *testing.T, reply func(n int) createReply) *fakeWAServer {
	t.Helper()
	root, intermediate, static := keys.NewKeyPair(), keys.NewKeyPair(), keys.NewKeyPair()
	now := time.Now()
	sign := func(signer *keys.KeyPair, details *waCert.CertChain_NoiseCertificate_Details) *waCert.CertChain_NoiseCertificate {
		raw, err := proto.Marshal(details)
		if err != nil {
			t.Fatal(err)
		}
		sig := ecc.CalculateSignature(ecc.NewDjbECPrivateKey(*signer.Priv), raw)
		return &waCert.CertChain_NoiseCertificate{Details: raw, Signature: sig[:]}
	}
	validity := func(serial, issuer uint32, key []byte) *waCert.CertChain_NoiseCertificate_Details {
		return &waCert.CertChain_NoiseCertificate_Details{
			Serial: proto.Uint32(serial), IssuerSerial: proto.Uint32(issuer), Key: key,
			NotBefore: proto.Uint64(uint64(now.Add(-time.Hour).Unix())), NotAfter: proto.Uint64(uint64(now.Add(time.Hour).Unix())),
		}
	}
	cert, err := proto.Marshal(&waCert.CertChain{
		Intermediate: sign(root, validity(1, whatsmeow.WACertIssuerSerial, intermediate.Pub[:])),
		Leaf:         sign(intermediate, validity(2, 1, static.Pub[:])),
	})
	if err != nil {
		t.Fatal(err)
	}
	previousRoot := whatsmeow.WACertPubKey
	whatsmeow.WACertPubKey = *root.Pub
	t.Cleanup(func() { whatsmeow.WACertPubKey = previousRoot })

	s := &fakeWAServer{t: t, static: static, cert: cert, reply: reply}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// httpClient dials the fake server whatever host the SDK asks for.
func (s *fakeWAServer) httpClient() *http.Client {
	addr := s.srv.Listener.Addr().String()
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // loopback test server
	}}
}

func (s *fakeWAServer) snapshot() (int, []recordedCreate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns, append([]recordedCreate(nil), s.creates...)
}

func (s *fakeWAServer) serve(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	s.mu.Lock()
	s.conns++
	connID := s.conns
	s.mu.Unlock()
	ctx := r.Context()
	frames := &frameReader{conn: conn, header: true}

	// Noise_XX responder, mirroring whatsmeow's handshake.go.
	hello, err := frames.next(ctx)
	if err != nil {
		return
	}
	var clientHello waWa6.HandshakeMessage
	if proto.Unmarshal(hello, &clientHello) != nil || len(clientHello.GetClientHello().GetEphemeral()) != 32 {
		return
	}
	clientEphemeral := [32]byte(clientHello.GetClientHello().GetEphemeral())
	ns := newServerNoise(frames.connHeader)
	ns.authenticate(clientEphemeral[:])
	ephemeral := keys.NewKeyPair()
	ns.authenticate(ephemeral.Pub[:])
	ns.mix(*ephemeral.Priv, clientEphemeral)
	staticCiphertext := ns.encrypt(s.static.Pub[:])
	ns.mix(*s.static.Priv, clientEphemeral)
	certCiphertext := ns.encrypt(s.cert)
	serverHello, _ := proto.Marshal(&waWa6.HandshakeMessage{ServerHello: &waWa6.HandshakeMessage_ServerHello{
		Ephemeral: ephemeral.Pub[:], Static: staticCiphertext, Payload: certCiphertext,
	}})
	if writeFrame(ctx, conn, serverHello) != nil {
		return
	}
	finish, err := frames.next(ctx)
	if err != nil {
		return
	}
	var clientFinish waWa6.HandshakeMessage
	if proto.Unmarshal(finish, &clientFinish) != nil {
		return
	}
	clientStatic, err := ns.decrypt(clientFinish.GetClientFinish().GetStatic())
	if err != nil || len(clientStatic) != 32 {
		s.t.Errorf("fake server: client static: %v", err)
		return
	}
	ns.mix(*ephemeral.Priv, [32]byte(clientStatic))
	if _, err := ns.decrypt(clientFinish.GetClientFinish().GetPayload()); err != nil {
		s.t.Errorf("fake server: client payload: %v", err)
		return
	}
	transport := ns.split()

	send := func(node waBinary.Node) error {
		raw, err := waBinary.Marshal(node)
		if err != nil {
			return err
		}
		return writeFrame(ctx, conn, transport.seal(raw))
	}
	if send(waBinary.Node{Tag: "success", Attrs: waBinary.Attrs{"t": strconv.FormatInt(time.Now().Unix(), 10), "lid": testOwnLID}}) != nil {
		return
	}
	for {
		ciphertext, err := frames.next(ctx)
		if err != nil {
			return
		}
		plaintext, err := transport.open(ciphertext)
		if err != nil {
			s.t.Errorf("fake server: decrypt frame: %v", err)
			return
		}
		unpacked, err := waBinary.Unpack(plaintext)
		if err != nil {
			continue
		}
		node, err := waBinary.Unmarshal(unpacked)
		if err != nil || node.Tag != "iq" {
			continue
		}
		id, _ := node.Attrs["id"].(string)
		if _, isCreate := node.GetOptionalChildByTag("create"); isCreate && node.Attrs["xmlns"] == "w:g2" {
			s.mu.Lock()
			s.creates = append(s.creates, recordedCreate{conn: connID, id: id, frame: bytes.Clone(plaintext), node: node})
			n := len(s.creates)
			s.mu.Unlock()
			switch s.reply(n) {
			case createDropConnection:
				// The create was accepted; its answer is lost with the socket.
				return
			case createAnswerGroup:
				_ = send(createdGroupAnswer(id))
			case createAnswerAck:
				_ = send(waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{"id": id, "from": types.GroupServerJID}})
			case createAnswerRefusal:
				_ = send(waBinary.Node{Tag: "iq", Attrs: waBinary.Attrs{"id": id, "type": "error", "from": types.GroupServerJID}, Content: []waBinary.Node{
					{Tag: "error", Attrs: waBinary.Attrs{"code": "406", "text": "not-acceptable"}},
				}})
			}
			continue
		}
		_ = send(waBinary.Node{Tag: "iq", Attrs: waBinary.Attrs{"id": id, "type": "result", "from": types.ServerJID}})
	}
}

func createdGroupAnswer(id string) waBinary.Node {
	return waBinary.Node{Tag: "iq", Attrs: waBinary.Attrs{"id": id, "type": "result", "from": types.GroupServerJID}, Content: []waBinary.Node{{
		Tag: "group",
		Attrs: waBinary.Attrs{
			"id": "120363000000000001", "subject": "Nuevo grupo", "s_t": "1700000000", "creation": "1700000000",
			"creator": testOwnLID.ToNonAD(), "addressing_mode": "lid",
		},
		Content: []waBinary.Node{
			{Tag: "participant", Attrs: waBinary.Attrs{"jid": testOwnLID.ToNonAD(), "phone_number": testOwnJID.ToNonAD(), "type": "superadmin"}},
			{Tag: "participant", Attrs: waBinary.Attrs{"jid": types.NewJID("15551112222", types.DefaultUserServer)}},
			{Tag: "locked"},
		},
	}}}
}

// frameReader splits websocket messages into WhatsApp frames: a 3-byte length
// prefix, plus the 4-byte connection header before the very first frame.
type frameReader struct {
	conn       *websocket.Conn
	header     bool
	connHeader []byte
	buf        []byte
}

func (f *frameReader) next(ctx context.Context) ([]byte, error) {
	for {
		if f.header && len(f.buf) >= len(socket.WAConnHeader) {
			f.connHeader = bytes.Clone(f.buf[:len(socket.WAConnHeader)])
			f.buf = f.buf[len(socket.WAConnHeader):]
			f.header = false
		}
		if !f.header && len(f.buf) >= socket.FrameLengthSize {
			length := int(f.buf[0])<<16 | int(f.buf[1])<<8 | int(f.buf[2])
			if len(f.buf) >= socket.FrameLengthSize+length {
				frame := bytes.Clone(f.buf[socket.FrameLengthSize : socket.FrameLengthSize+length])
				f.buf = f.buf[socket.FrameLengthSize+length:]
				return frame, nil
			}
		}
		_, data, err := f.conn.Read(ctx)
		if err != nil {
			return nil, err
		}
		f.buf = append(f.buf, data...)
	}
}

func writeFrame(ctx context.Context, conn *websocket.Conn, data []byte) error {
	frame := make([]byte, socket.FrameLengthSize+len(data))
	frame[0], frame[1], frame[2] = byte(len(data)>>16), byte(len(data)>>8), byte(len(data))
	copy(frame[socket.FrameLengthSize:], data)
	return conn.Write(ctx, websocket.MessageBinary, frame)
}

// serverNoise is the responder half of whatsmeow's socket.NoiseHandshake,
// built from the standard library (the SDK keeps its final key split private).
type serverNoise struct {
	hash, salt []byte
	key        cipher.AEAD
	counter    uint32
}

func newServerNoise(header []byte) *serverNoise {
	ns := &serverNoise{hash: []byte(socket.NoiseStartPattern)}
	ns.salt = ns.hash
	ns.key = mustGCM(ns.hash)
	ns.authenticate(header)
	return ns
}

func mustGCM(key []byte) cipher.AEAD {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return aead
}

func noiseIV(counter uint32) []byte {
	iv := make([]byte, 12)
	binary.BigEndian.PutUint32(iv[8:], counter)
	return iv
}

func (ns *serverNoise) authenticate(data []byte) {
	sum := sha256.Sum256(append(bytes.Clone(ns.hash), data...))
	ns.hash = sum[:]
}

func (ns *serverNoise) expand(secret []byte) (first, second []byte) {
	out, err := hkdf.Key(sha256.New, secret, ns.salt, "", 64)
	if err != nil {
		panic(err)
	}
	return out[:32], out[32:]
}

func (ns *serverNoise) mix(priv, pub [32]byte) {
	private, err := ecdh.X25519().NewPrivateKey(priv[:])
	if err != nil {
		panic(err)
	}
	public, err := ecdh.X25519().NewPublicKey(pub[:])
	if err != nil {
		panic(err)
	}
	secret, err := private.ECDH(public)
	if err != nil {
		panic(err)
	}
	salt, key := ns.expand(secret)
	ns.counter, ns.salt, ns.key = 0, salt, mustGCM(key)
}

func (ns *serverNoise) encrypt(plaintext []byte) []byte {
	ciphertext := ns.key.Seal(nil, noiseIV(ns.counter), plaintext, ns.hash)
	ns.counter++
	ns.authenticate(ciphertext)
	return ciphertext
}

func (ns *serverNoise) decrypt(ciphertext []byte) ([]byte, error) {
	plaintext, err := ns.key.Open(nil, noiseIV(ns.counter), ciphertext, ns.hash)
	ns.counter++
	if err == nil {
		ns.authenticate(ciphertext)
	}
	return plaintext, err
}

type serverTransport struct {
	read, write           cipher.AEAD
	readCount, writeCount uint32
}

// split returns the server's transport keys: the client writes with the first
// derived key and reads with the second.
func (ns *serverNoise) split() *serverTransport {
	clientWrite, clientRead := ns.expand(nil)
	return &serverTransport{read: mustGCM(clientWrite), write: mustGCM(clientRead)}
}

func (st *serverTransport) open(ciphertext []byte) ([]byte, error) {
	plaintext, err := st.read.Open(nil, noiseIV(st.readCount), ciphertext, nil)
	st.readCount++
	return plaintext, err
}

func (st *serverTransport) seal(plaintext []byte) []byte {
	ciphertext := st.write.Seal(nil, noiseIV(st.writeCount), plaintext, nil)
	st.writeCount++
	return ciphertext
}

var (
	testPNParticipant    = types.NewJID("15551112222", types.DefaultUserServer)
	testLIDParticipant   = types.NewJID("333000", types.HiddenUserServer)
	testLIDParticipantPN = types.NewJID("15553334444", types.DefaultUserServer)
)

// connectToFakeServer logs a real whatsmeow client in to the fake server, with
// a device store holding a LID mapping and a privacy token, so the create
// request exercises every participant branch.
func connectToFakeServer(t *testing.T, server *fakeWAServer) *whatsmeow.Client {
	t.Helper()
	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(filepath.Join(t.TempDir(), "session.db"), "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Close() })
	device := container.NewDevice()
	device.ID = &testOwnJID
	device.LID = testOwnLID
	device.Account = testAccount()
	if err := device.Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := device.LIDs.PutLIDMapping(ctx, testLIDParticipant, testLIDParticipantPN); err != nil {
		t.Fatal(err)
	}
	if err := device.PrivacyTokens.PutPrivacyTokens(ctx, store.PrivacyToken{User: testLIDParticipant, Token: []byte("tc-token"), Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	cli := whatsmeow.NewClient(device, waLog.Noop)
	cli.SetWebsocketHTTPClient(server.httpClient())
	cli.EnableAutoReconnect = true
	if err := cli.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cli.Disconnect)
	if !cli.WaitForConnection(5 * time.Second) {
		t.Fatal("client did not log in to the fake server")
	}
	return cli
}

func testCreateRequest() CreateGroupRequest {
	return CreateGroupRequest{
		Name:                   "Nuevo grupo",
		Participants:           []types.JID{testPNParticipant, testLIDParticipant},
		IsAnnounce:             true,
		IsLocked:               true,
		IsJoinApprovalRequired: true,
		LinkedParentJID:        types.NewJID("120363000000000999", types.GroupServer),
	}
}

func sdkCreateRequest(req CreateGroupRequest) whatsmeow.ReqCreateGroup {
	return whatsmeow.ReqCreateGroup{
		Name:                        req.Name,
		Participants:                req.Participants,
		GroupAnnounce:               types.GroupAnnounce{IsAnnounce: req.IsAnnounce},
		GroupLocked:                 types.GroupLocked{IsLocked: req.IsLocked},
		GroupMembershipApprovalMode: types.GroupMembershipApprovalMode{IsJoinApprovalRequired: req.IsJoinApprovalRequired},
		GroupParent:                 types.GroupParent{IsParent: req.IsParent},
		GroupLinkedParent:           types.GroupLinkedParent{LinkedParentJID: req.LinkedParentJID},
	}
}

func waitForConns(t *testing.T, server *fakeWAServer, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conns, _ := server.snapshot(); conns >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	conns, _ := server.snapshot()
	t.Fatalf("client opened %d connections, want %d", conns, want)
}

// The control: the SDK's own CreateGroup replays the saved create frame on the
// new socket after a reconnect. This is the behavior wacli must not inherit,
// and it proves the harness can see a replay.
func TestSDKCreateGroupReplaysCreateFrameAfterReconnect(t *testing.T) {
	if raceEnabled {
		// The SDK's replay path waits on its expectedDisconnect event from
		// retryFrame while autoReconnect does too, and exsync.Event.GetChan
		// writes under a read lock. That upstream race is in the very path
		// wacli now avoids; the regression below still runs under -race.
		t.Skip("SDK replay path trips an upstream go.mau.fi/util/exsync race")
	}
	server := newFakeWAServer(t, func(n int) createReply {
		if n == 1 {
			return createDropConnection
		}
		return createAnswerGroup
	})
	cli := connectToFakeServer(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := cli.CreateGroup(ctx, sdkCreateRequest(testCreateRequest())); err != nil {
		t.Fatalf("SDK create after replay: %v", err)
	}
	conns, creates := server.snapshot()
	if len(creates) != 2 || conns < 2 || creates[0].conn == creates[1].conn {
		t.Fatalf("expected the SDK to replay the create on a new socket: conns=%d creates=%d", conns, len(creates))
	}
	if creates[0].id != creates[1].id || !bytes.Equal(creates[0].frame, creates[1].frame) {
		t.Fatalf("replayed frame differs: ids %s/%s", creates[0].id, creates[1].id)
	}
}

// One accepted create frame, a lost answer and a reconnect: wacli's create
// reports an unconfirmed outcome and no second create frame reaches the wire.
func TestCreateGroupLostAnswerAndReconnectSendsOneFrame(t *testing.T) {
	server := newFakeWAServer(t, func(int) createReply { return createDropConnection })
	cli := connectToFakeServer(t, server)
	client := &Client{client: cli}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	info, err := client.CreateGroup(ctx, testCreateRequest())
	if info != nil || !errors.Is(err, ErrGroupCreateUnconfirmed) || !strings.Contains(err.Error(), "may still have gone through") {
		t.Fatalf("lost answer must be unconfirmed, got info=%v err=%v", info, err)
	}
	var disconnected *whatsmeow.DisconnectedError
	if !errors.As(err, &disconnected) {
		t.Fatalf("cause lost: %v", err)
	}
	// The client reconnects and logs in again; give a replay every chance.
	// Poll instead of WaitForConnection: two concurrent waiters on the SDK's
	// expectedDisconnect event (autoReconnect is one) trip the race detector
	// inside go.mau.fi/util/exsync.
	waitForConns(t, server, 2)
	deadline := time.Now().Add(5 * time.Second)
	for !cli.IsLoggedIn() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !cli.IsLoggedIn() {
		t.Fatal("client did not log back in")
	}
	time.Sleep(time.Second)
	if _, creates := server.snapshot(); len(creates) != 1 {
		t.Fatalf("create frames on the wire = %d, want exactly 1", len(creates))
	}
}

// wacli's create IQ is the SDK's, attribute for attribute, and its answer is
// parsed the same way.
func TestCreateGroupSendsTheSDKRequestAndParsesItsAnswer(t *testing.T) {
	for _, community := range []bool{false, true} {
		t.Run(fmt.Sprintf("community=%t", community), func(t *testing.T) {
			req := testCreateRequest()
			if community {
				req.IsParent, req.LinkedParentJID = true, types.JID{}
			}
			server := newFakeWAServer(t, func(int) createReply { return createAnswerGroup })
			cli := connectToFakeServer(t, server)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			want, err := cli.CreateGroup(ctx, sdkCreateRequest(req))
			if err != nil {
				t.Fatal(err)
			}
			got, err := (&Client{client: cli}).CreateGroup(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("parsed group differs:\n got %+v\nwant %+v", got, want)
			}
			_, creates := server.snapshot()
			if len(creates) != 2 {
				t.Fatalf("create frames = %d", len(creates))
			}
			sdk, ours := creates[0].node, creates[1].node
			delete(sdk.Attrs, "id")
			delete(ours.Attrs, "id")
			if !reflect.DeepEqual(ours, sdk) {
				t.Fatalf("create IQ differs:\n got %s\nwant %s", ours, sdk)
			}
			// The LID participant carries its phone number and privacy token.
			var lidParticipant *waBinary.Node
			create := ours.GetChildByTag("create")
			for _, child := range create.GetChildren() {
				if child.Tag == "participant" && child.Attrs["jid"] == testLIDParticipant {
					lidParticipant = &child
				}
			}
			if lidParticipant == nil || lidParticipant.Attrs["phone_number"] != testLIDParticipantPN {
				t.Fatalf("LID phone-number branch not exercised: %s", ours)
			}
			if token, _ := lidParticipant.GetChildByTag("privacy").Content.([]byte); string(token) != "tc-token" {
				t.Fatalf("privacy token branch not exercised: %s", ours)
			}
		})
	}
}

// An explicit WhatsApp refusal is known not to have created anything.
func TestCreateGroupRefusalStaysARefusal(t *testing.T) {
	server := newFakeWAServer(t, func(int) createReply { return createAnswerRefusal })
	cli := connectToFakeServer(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := (&Client{client: cli}).CreateGroup(ctx, testCreateRequest())
	var iqErr *whatsmeow.IQError
	if !errors.As(err, &iqErr) || iqErr.Code != 406 || errors.Is(err, ErrGroupCreateUnconfirmed) {
		t.Fatalf("refusal = %v", err)
	}
	if _, creates := server.snapshot(); len(creates) != 1 {
		t.Fatalf("create frames = %d", len(creates))
	}
}

// A reply to the create's ID that is not an IQ error stanza is accepted by
// the SDK's response waiter and surfaces as *IQError, but it does not say the
// group was not created.
func TestCreateGroupUnexpectedReplyIsUnconfirmed(t *testing.T) {
	server := newFakeWAServer(t, func(int) createReply { return createAnswerAck })
	cli := connectToFakeServer(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	info, err := (&Client{client: cli}).CreateGroup(ctx, testCreateRequest())
	var iqErr *whatsmeow.IQError
	if info != nil || !errors.Is(err, ErrGroupCreateUnconfirmed) || !strings.Contains(err.Error(), "may still have gone through") {
		t.Fatalf("unexpected reply must be unconfirmed, got info=%v err=%v", info, err)
	}
	if !errors.As(err, &iqErr) || iqErr.Code != 0 || iqErr.RawNode == nil || iqErr.RawNode.Tag != "ack" {
		t.Fatalf("cause lost: %#v", err)
	}
	if _, creates := server.snapshot(); len(creates) != 1 {
		t.Fatalf("create frames = %d", len(creates))
	}
}

// iqErrorFor builds the error the SDK's sendIQ returns for a reply node.
func iqErrorFor(reply waBinary.Node) *whatsmeow.IQError {
	iqErr := &whatsmeow.IQError{RawNode: &reply}
	if errNode, ok := reply.GetOptionalChildByTag("error"); ok {
		iqErr.ErrorNode = &errNode
		ag := errNode.AttrGetter()
		iqErr.Code = ag.OptionalInt("code")
		iqErr.Text = ag.OptionalString("text")
	}
	return iqErr
}

func TestGroupCreateOutcomeClassification(t *testing.T) {
	errorIQ := func(attrs waBinary.Attrs) waBinary.Node {
		return waBinary.Node{Tag: "iq", Attrs: waBinary.Attrs{"id": "1", "type": "error"}, Content: []waBinary.Node{{Tag: "error", Attrs: attrs}}}
	}
	for _, tc := range []struct {
		err       error
		uncertain bool
	}{
		{iqErrorFor(errorIQ(waBinary.Attrs{"code": "406", "text": "not-acceptable"})), false},
		{fmt.Errorf("wrapped: %w", iqErrorFor(errorIQ(waBinary.Attrs{"code": "403", "text": "forbidden"}))), false},
		{iqErrorFor(errorIQ(waBinary.Attrs{"text": "no code"})), true},
		{iqErrorFor(waBinary.Node{Tag: "iq", Attrs: waBinary.Attrs{"id": "1", "type": "error"}}), true},
		{iqErrorFor(waBinary.Node{Tag: "ack", Attrs: waBinary.Attrs{"id": "1"}}), true},
		{iqErrorFor(waBinary.Node{Tag: "iq", Attrs: waBinary.Attrs{"id": "1", "type": "get"}}), true},
		{&whatsmeow.IQError{Code: 406, Text: "no stanza"}, true},
		{whatsmeow.ErrNotConnected, false},
		{whatsmeow.ErrClientIsNil, false},
		{fmt.Errorf("send: %w", socket.ErrSocketClosed), false},
		{&whatsmeow.DisconnectedError{Action: "info query"}, true},
		{whatsmeow.ErrIQTimedOut, true},
		{context.DeadlineExceeded, true},
		{context.Canceled, true},
		{errors.New("failed to write websocket frame"), true},
	} {
		got := groupCreateOutcome(tc.err)
		if errors.Is(got, ErrGroupCreateUnconfirmed) != tc.uncertain || !errors.Is(got, tc.err) {
			t.Fatalf("%v -> %v, want uncertain=%t", tc.err, got, tc.uncertain)
		}
	}
}

// Build errors name the participant by position: a phone number or LID in an
// error ends up in the owner's log and in the caller's JSON.
func TestBuildCreateGroupNodeErrorsNamePositionsNotJIDs(t *testing.T) {
	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(filepath.Join(t.TempDir(), "session.db"), "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	device := container.NewDevice()
	device.ID = &testOwnJID
	device.Account = testAccount()
	if err := device.Save(ctx); err != nil {
		t.Fatal(err)
	}
	cli := whatsmeow.NewClient(device, waLog.Noop)
	// Every lookup below now fails inside the device store.
	if err := container.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		participants []types.JID
		want         string
	}{
		{[]types.JID{testPNParticipant, testLIDParticipant}, "failed to get privacy token for participant 1 of 2"},
		{[]types.JID{testLIDParticipant}, "failed to get phone number for participant 1 of 1"},
	} {
		_, err := buildCreateGroupNode(ctx, cli, whatsmeow.ReqCreateGroup{Name: "x", Participants: tc.participants})
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Fatalf("error = %v, want prefix %q", err, tc.want)
		}
		for _, jid := range tc.participants {
			if strings.Contains(err.Error(), jid.User) {
				t.Fatalf("error names a participant: %v", err)
			}
		}
	}
}
