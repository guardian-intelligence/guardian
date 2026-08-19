package gateway

// Behavioral coverage for the gateway relay loop: what a connected client
// can get through to a chunk, and what the gateway absorbs. The chunk side
// is a fake speaking the real proxy protocol, so every assertion is about
// bytes that actually crossed the internal boundary.

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	webtransport "github.com/quic-go/webtransport-go"

	"github.com/guardian-intelligence/guardian/src/chunkies/trunk"
	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

// fakeChunkie accepts authenticated multiplexed connections and records
// every message the gateway forwards (pings are answered inside
// ReadMessage and never recorded).
type fakeChunkie struct {
	addr string
	mu   sync.Mutex
	msgs []trunk.Msg
}

func newFakeChunkie(t *testing.T, key []byte) *fakeChunkie {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	p := &fakeChunkie{addr: l.Addr().String()}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				proxy, err := trunk.Accept(c, key, time.Now())
				if err != nil {
					c.Close()
					return
				}
				for {
					m, err := proxy.ReadMessage()
					if err != nil {
						return
					}
					if len(m.Payload) > 0 {
						m.Payload = append([]byte(nil), m.Payload...)
					}
					p.mu.Lock()
					p.msgs = append(p.msgs, m)
					p.mu.Unlock()
				}
			}(conn)
		}
	}()
	return p
}

func (p *fakeChunkie) received(kind byte) [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out [][]byte
	for _, m := range p.msgs {
		if m.Kind == kind {
			out = append(out, m.Payload)
		}
	}
	return out
}

// waitFor polls until fn returns true or the deadline passes.
func waitFor(t *testing.T, d time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fn()
}

type relayHarness struct {
	backend *fakeChunkie
	tickets *ticketMint
	dir     *chunkDirectory
	chunk   string
	dial    func(t *testing.T) *webtransport.Session
}

func newRelayHarness(t *testing.T) *relayHarness {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	backend := newFakeChunkie(t, key)
	tickets, err := newTicketMint("")
	if err != nil {
		t.Fatal(err)
	}
	dir := newChunkDirectory()
	dir.replace(map[string]string{"wum/park-test": backend.addr})
	gw := &chunkiesGateway{
		admission: &gameHandlers{tickets: tickets, maxSessions: 16, directory: dir, game: "wum"},
		game:      "wum",
		trunks:    trunk.NewTrunks(key, dir, trunk.Hooks{}),
	}

	rc := newRotatingCert([]net.IP{net.ParseIP("127.0.0.1")})
	cert, _ := rc.get()
	mux := http.NewServeMux()
	wt := &webtransport.Server{
		H3: &http3.Server{
			TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{cert},
				NextProtos:   []string{http3.NextProtoH3},
			},
			QUICConfig: &quic.Config{
				EnableDatagrams: true, EnableStreamResetPartialDelivery: true, MaxIncomingStreams: 4, MaxIncomingUniStreams: 4,
				MaxIdleTimeout: 30 * time.Second,
			},
			Handler: mux, EnableDatagrams: true,
		},
	}
	mux.HandleFunc("/wt", func(w http.ResponseWriter, r *http.Request) {
		sess, err := wt.Upgrade(w, r)
		if err != nil {
			return
		}
		go gw.handleSession(sess)
	})
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go wt.Serve(udp)
	t.Cleanup(func() { wt.Close(); udp.Close() })

	dial := func(t *testing.T) *webtransport.Session {
		t.Helper()
		d := webtransport.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{http3.NextProtoH3}},
			QUICConfig:      &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true, MaxIdleTimeout: 30 * time.Second},
		}
		t.Cleanup(func() { d.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t.Cleanup(cancel)
		_, sess, err := d.Dial(ctx, "https://"+udp.LocalAddr().String()+"/wt", nil)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { sess.CloseWithError(0, "") })
		return sess
	}
	return &relayHarness{backend: backend, tickets: tickets, dir: dir, chunk: "park-test", dial: dial}
}

// hello opens the bidi stream and completes admission for the given role.
func (h *relayHarness) hello(t *testing.T, sess *webtransport.Session, sub, role string) *webtransport.Stream {
	t.Helper()
	stream, err := sess.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw := h.tickets.mint(ticket{Sub: sub, Chunk: h.chunk, Role: role, Exp: time.Now().Add(time.Minute).Unix()})
	frame := codec.EncodeHello(codec.Hello{Proto: codec.Proto, SinceSeq: -1, Ticket: raw})
	if _, err := stream.Write(frame); err != nil {
		t.Fatal(err)
	}
	return stream
}

func moveIntent(sub string, id uint64, node uint16) []byte {
	return codec.EncodeIntent(id, 4, codec.ActorFor(sub), binary.LittleEndian.AppendUint16(nil, node))
}

func TestRelaySplicesIntentBytesVerbatim(t *testing.T) {
	h := newRelayHarness(t)
	sess := h.dial(t)
	stream := h.hello(t, sess, "splice-sub", "player")

	frame := moveIntent("splice-sub", 7, 42)
	if _, err := stream.Write(frame); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return len(h.backend.received(trunk.KindStream)) == 1 }) {
		t.Fatalf("intent never reached the chunk")
	}
	got := h.backend.received(trunk.KindStream)[0]
	if string(got) != string(frame) {
		t.Fatalf("chunk received %x, want the client's frame %x", got, frame)
	}
}

func TestRelayPacesBurstsInsteadOfClosing(t *testing.T) {
	h := newRelayHarness(t)
	sess := h.dial(t)
	stream := h.hello(t, sess, "burst-sub", "player")

	const n = 60 // burst capacity is 40 at 20/s: the tail must be paced, not killed
	start := time.Now()
	for i := range n {
		if _, err := stream.Write(moveIntent("burst-sub", uint64(i+1), 1)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if !waitFor(t, 15*time.Second, func() bool { return len(h.backend.received(trunk.KindStream)) == n }) {
		t.Fatalf("chunk got %d of %d intents; the old limiter would have closed the session", len(h.backend.received(trunk.KindStream)), n)
	}
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
		t.Fatalf("burst of %d relayed in %v: the shaper did not pace", n, elapsed)
	}
	// The session must still be usable after the burst.
	if _, err := stream.Write(moveIntent("burst-sub", n+1, 2)); err != nil {
		t.Fatalf("session unusable after burst: %v", err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return len(h.backend.received(trunk.KindStream)) == n+1 }) {
		t.Fatal("post-burst intent never arrived")
	}
}

func TestRelayDropsJunkDatagrams(t *testing.T) {
	h := newRelayHarness(t)
	sess := h.dial(t)
	h.hello(t, sess, "dg-sub", "player")

	junkShort := make([]byte, codec.CheckLen-1)
	junkKind := make([]byte, codec.CheckLen)
	junkKind[0] = 0x7f
	good := codec.EncodeCheck(codec.Check{Tick: 9, WH: 1, CTMS: 5})
	for _, dg := range [][]byte{junkShort, junkKind, good} {
		if err := sess.SendDatagram(dg); err != nil {
			t.Fatal(err)
		}
	}
	if !waitFor(t, 5*time.Second, func() bool { return len(h.backend.received(trunk.KindDatagram)) >= 1 }) {
		t.Fatal("well-formed check never reached the chunk")
	}
	// Datagrams are unordered; wait a beat, then require the junk stayed out.
	time.Sleep(200 * time.Millisecond)
	for _, got := range h.backend.received(trunk.KindDatagram) {
		if string(got) != string(good) {
			t.Fatalf("junk datagram %x crossed the gateway", got)
		}
	}
}

func TestRelayDropsUnknownKindsAndLivesOn(t *testing.T) {
	h := newRelayHarness(t)
	sess := h.dial(t)
	stream := h.hello(t, sess, "kind-sub", "player")

	if _, err := stream.Write(codec.EncodeFrame(0x33, []byte("mystery"))); err != nil {
		t.Fatal(err)
	}
	intent := moveIntent("kind-sub", 11, 3)
	if _, err := stream.Write(intent); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return len(h.backend.received(trunk.KindStream)) == 1 }) {
		t.Fatal("intent after unknown frame never arrived — unknown kind killed the session")
	}
	if got := h.backend.received(trunk.KindStream)[0]; string(got) != string(intent) {
		t.Fatalf("chunk received %x, want %x", got, intent)
	}
}

func TestRelayRejectsSpectatorIntents(t *testing.T) {
	h := newRelayHarness(t)
	sess := h.dial(t)
	stream := h.hello(t, sess, "watcher", "spectator")

	if _, err := stream.Write(moveIntent("watcher", 21, 9)); err != nil {
		t.Fatal(err)
	}
	frames := codec.NewReader(stream)
	kind, payload, err := frames.Next()
	if err != nil {
		t.Fatal(err)
	}
	if kind != codec.KindReject {
		t.Fatalf("kind = %d, want reject", kind)
	}
	rej, err := codec.DecodeReject(payload)
	if err != nil || rej.Intent != 21 || rej.Reason != codec.RejectReadOnly {
		t.Fatalf("reject = %+v (err %v), want intent 21 reason read_only", rej, err)
	}
	if got := h.backend.received(trunk.KindStream); len(got) != 0 {
		t.Fatalf("spectator intent reached the chunk: %x", got)
	}
}
func TestRelayRoutesChunksAddedAtRuntime(t *testing.T) {
	h := newRelayHarness(t)

	// Before the directory knows the chunk, the hello is refused.
	sess := h.dial(t)
	stream, err := sess.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw := h.tickets.mint(ticket{Sub: "late-sub", Chunk: "park-late", Role: "player", Exp: time.Now().Add(time.Minute).Unix()})
	if _, err := stream.Write(codec.EncodeHello(codec.Hello{Proto: codec.Proto, SinceSeq: -1, Ticket: raw})); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool {
		_, err := stream.Read(make([]byte, 1))
		return err != nil
	}) {
		t.Fatal("hello for an unknown chunk was not refused")
	}

	// The same file-load path the watcher runs makes it routable with no
	// process restart.
	path := filepath.Join(t.TempDir(), "chunks.conf")
	if err := os.WriteFile(path, []byte("wum park-late "+h.backend.addr+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadChunkDirectory(path, h.dir); err != nil {
		t.Fatal(err)
	}

	h.chunk = "park-late"
	sess2 := h.dial(t)
	stream2 := h.hello(t, sess2, "late-sub", "player")
	if _, err := stream2.Write(moveIntent("late-sub", 5, 1)); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return len(h.backend.received(trunk.KindStream)) == 1 }) {
		t.Fatal("intent to the runtime-added chunk never arrived")
	}
}
