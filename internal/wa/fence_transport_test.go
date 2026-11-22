package wa_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/sqliteutil"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/libsignal/groups"
	"go.mau.fi/libsignal/protocol"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.mau.fi/whatsmeow/util/keys"
	"google.golang.org/protobuf/proto"
)

// commitThenBlock sits inside the fence. Once armed, the first write it sees
// is committed to the real store and then held, so the decryption has
// committed but has not returned to whatsmeow, let alone reached a handler.
type commitThenBlock struct {
	armed              atomic.Bool
	committed, release chan struct{}
	once               sync.Once
}

func newCommitThenBlock() *commitThenBlock {
	return &commitThenBlock{committed: make(chan struct{}), release: make(chan struct{})}
}

func (b *commitThenBlock) hold() {
	if b.armed.Load() {
		b.once.Do(func() { close(b.committed); <-b.release })
	}
}

type blockingSessions struct {
	waStore.SessionStore
	b *commitThenBlock
}

func (s blockingSessions) PutSession(ctx context.Context, address string, session []byte) error {
	if err := s.SessionStore.PutSession(ctx, address, session); err != nil {
		return err
	}
	s.b.hold()
	return nil
}

type blockingSenderKeys struct {
	waStore.SenderKeyStore
	b *commitThenBlock
}

func (s blockingSenderKeys) PutSenderKey(ctx context.Context, group, user string, key []byte) error {
	if err := s.SenderKeyStore.PutSenderKey(ctx, group, user, key); err != nil {
		return err
	}
	s.b.hold()
	return nil
}

// The fence, deterministically (the transport exit tests in cmd/wacli catch
// the same race only by chance). Real client over Noise XX; the stand-in
// delivers one message; its decryption commits and is then held inside the
// store. The socket is closed and FenceSessionWrites is called: it must wait
// for that write, count it, and the message must then reach the handlers, so
// the command waits for it instead of closing its store under it.
func TestFenceWaitsForADecryptionThatCommitted(t *testing.T) {
	for _, kind := range []string{"1:1", "group"} {
		t.Run(kind, func(t *testing.T) { fenceWaitsForCommittedDecryption(t, kind == "group") })
	}
}

func fenceWaitsForCommittedDecryption(t *testing.T, group bool) {
	root := keys.NewKeyPair()
	previous := whatsmeow.WACertPubKey
	whatsmeow.WACertPubKey = *root.Pub
	t.Cleanup(func() { whatsmeow.WACertPubKey = previous })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	dir := t.TempDir()
	wa.PairedSessionStoreAt(t, filepath.Join(dir, "session.db"))
	a, err := app.New(app.Options{StoreDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.OpenWA(); err != nil {
		t.Fatal(err)
	}
	client := a.WA().(*wa.Client)
	cli := wa.RawClient(client)
	cli.EnableAutoReconnect = false

	b := newCommitThenBlock()
	t.Cleanup(func() {
		select {
		case <-b.release:
		default:
			close(b.release)
		}
	})
	wa.WrapFencedStores(client,
		func(s waStore.SessionStore) waStore.SessionStore { return blockingSessions{s, b} },
		func(s waStore.SenderKeyStore) waStore.SenderKeyStore { return blockingSenderKeys{s, b} })
	received := make(chan string, 4)
	handler := cli.AddEventHandler(func(evt any) {
		if msg, ok := evt.(*events.Message); ok {
			received <- msg.Info.ID
		}
	})
	t.Cleanup(func() { cli.RemoveEventHandler(handler) })

	contact := types.NewJID("5551230000", types.DefaultUserServer)
	groupJID := types.NewJID("120363000000000009", types.GroupServer)
	var node waBinary.Node
	if group {
		node = fenceGroupMessage(t, ctx, cli.Store, contact, groupJID, "fenced-group")
	} else {
		node = ownTransportSender(t, ctx, cli.Store, contact)("fenced-dm", contact, time.Unix(1700000000, 0))
	}

	static := keys.NewKeyPair()
	cert := ownTransportCertificate(t, root, static)
	peers := make(chan *ownTransportNoise, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		peer, err := ownTransportHandshake(ctx, conn, static, cert)
		if err != nil {
			t.Errorf("Noise handshake: %v", err)
			return
		}
		peers <- peer
		for {
			if _, err := peer.read(ctx); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	cli.MessengerConfig = &whatsmeow.MessengerConfig{WebsocketURL: "ws" + strings.TrimPrefix(server.URL, "http"), BaseURL: server.URL}
	cli.SetWebsocketHTTPClient(server.Client())
	if err := cli.ConnectContext(ctx); err != nil {
		t.Fatal(err)
	}
	peer := <-peers
	before := client.SessionWrites()
	b.armed.Store(true)
	if err := peer.write(ctx, node); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.committed:
	case <-ctx.Done():
		t.Fatal("the decryption never committed")
	}
	client.Disconnect()
	fenced := make(chan uint64, 1)
	go func() { fenced <- client.FenceSessionWrites() }()
	select {
	case n := <-fenced:
		t.Fatalf("FenceSessionWrites returned (%d writes) while a decryption that had committed was still in its write: the command would close its store under that message", n)
	case <-time.After(200 * time.Millisecond):
	}
	close(b.release)
	n := <-fenced
	if n != before+1 {
		t.Fatalf("FenceSessionWrites = %d, want %d: the write committed before the fence must be counted", n, before+1)
	}
	select {
	case id := <-received:
		t.Logf("fence waited for the committed write (count %d), then %s reached the handlers", n, id)
	case <-time.After(5 * time.Second):
		t.Fatal("the message whose decryption committed before the fence never reached the handlers")
	}
}

// fenceGroupMessage installs the contact's sender key for the group on the
// device, as after an earlier sender-key distribution, and returns the next
// group message (skmsg) from the contact.
func fenceGroupMessage(t *testing.T, ctx context.Context, receiver *waStore.Device, from, group types.JID, id string) waBinary.Node {
	t.Helper()
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(filepath.Join(t.TempDir(), "gpeer.db"), "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Close() })
	sender := container.NewDevice()
	sender.ID = &from
	sender.Account = receiver.Account
	if err := sender.Save(ctx); err != nil {
		t.Fatal(err)
	}
	name := protocol.NewSenderKeyName(group.String(), from.SignalAddress())
	builder := groups.NewGroupSessionBuilder(sender, waStore.SignalProtobufSerializer)
	distribution, err := builder.Create(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := groups.NewGroupSessionBuilder(receiver, waStore.SignalProtobufSerializer).Process(ctx, name, distribution); err != nil {
		t.Fatal(err)
	}
	plain, err := proto.Marshal(&waE2E.Message{Conversation: proto.String("group message held at exit")})
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := groups.NewGroupCipher(builder, name, sender).Encrypt(ctx, append(plain, 1))
	if err != nil {
		t.Fatal(err)
	}
	return waBinary.Node{Tag: "message", Attrs: waBinary.Attrs{"id": id, "from": group, "participant": from, "type": "text", "t": fmt.Sprint(time.Unix(1700000000, 0).Unix())}, Content: []waBinary.Node{
		{Tag: "enc", Attrs: waBinary.Attrs{"v": "2", "type": "skmsg"}, Content: encrypted.SignedSerialize()},
	}}
}
