package trunk

// Behavioral coverage for the multiplexed transport: how many TCP
// connections sessions cost, what failure tears down, and what stays up.

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

// fakeChunkieServer accepts authenticated connections and records every
// message; onOpen scripts the chunk's reaction to a session opening.
type fakeChunkieServer struct {
	t        *testing.T
	l        net.Listener
	accepted atomic.Int32
	onOpen   func(c *Conn, m Msg)

	mu    sync.Mutex
	msgs  []Msg
	conns []*Conn
}

func newFakeChunkieServer(t *testing.T, onOpen func(c *Conn, m Msg)) *fakeChunkieServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	f := &fakeChunkieServer{t: t, l: l, onOpen: onOpen}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			f.accepted.Add(1)
			go func(c net.Conn) {
				pc, err := Accept(c, testKey, time.Now())
				if err != nil {
					c.Close()
					return
				}
				f.mu.Lock()
				f.conns = append(f.conns, pc)
				f.mu.Unlock()
				for {
					m, err := pc.ReadMessage()
					if err != nil {
						return
					}
					if len(m.Payload) > 0 {
						m.Payload = append([]byte(nil), m.Payload...)
					}
					f.mu.Lock()
					f.msgs = append(f.msgs, m)
					f.mu.Unlock()
					if m.Kind == KindOpen && f.onOpen != nil {
						f.onOpen(pc, m)
					}
				}
			}(conn)
		}
	}()
	return f
}

func (f *fakeChunkieServer) addr() string { return f.l.Addr().String() }

func (f *fakeChunkieServer) received(kind byte) []Msg {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Msg
	for _, m := range f.msgs {
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeChunkieServer) conn(i int) *Conn {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.conns) {
		return nil
	}
	return f.conns[i]
}

func waitFor(t *testing.T, d time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fn()
}

func openTwo(t *testing.T, p *Pool, addr string) (*Attachment, *Attachment) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s1, err := p.Open(ctx, addr, Open{Game: "wum", Sub: "alice", Chunk: "park-test", Role: "player", SinceSeq: -1})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := p.Open(ctx, addr, Open{Game: "wum", Sub: "bob", Chunk: "park-test", Role: "spectator", SinceSeq: 7, SinceTick: 90})
	if err != nil {
		t.Fatal(err)
	}
	return s1, s2
}

func TestMuxSharesOneConnAcrossSessions(t *testing.T) {
	chunk := newFakeChunkieServer(t, func(c *Conn, m Msg) {
		c.WriteStream(m.SID, []byte("welcome"))
	})
	pool := NewPool(testKey, Hooks{})
	s1, s2 := openTwo(t, pool, chunk.addr())

	if !waitFor(t, 5*time.Second, func() bool { return len(chunk.received(KindOpen)) == 2 }) {
		t.Fatalf("opens = %d, want 2", len(chunk.received(KindOpen)))
	}
	// Two sessions, one TCP connection, one handshake.
	if got := chunk.accepted.Load(); got != 1 {
		t.Fatalf("sessions cost %d connections, want 1", got)
	}
	opens := chunk.received(KindOpen)
	if opens[0].SID == opens[1].SID {
		t.Fatalf("both sessions share sid %d", opens[0].SID)
	}
	o1, err := DecodeOpen(opens[0].Payload)
	if err != nil || o1.Game != "wum" || o1.Sub != "alice" || o1.Chunk != "park-test" || o1.Role != "player" || o1.SinceSeq != -1 {
		t.Fatalf("open 1 = %+v (err %v)", o1, err)
	}
	o2, err := DecodeOpen(opens[1].Payload)
	if err != nil || o2.Sub != "bob" || o2.Role != "spectator" || o2.SinceSeq != 7 || o2.SinceTick != 90 {
		t.Fatalf("open 2 = %+v (err %v)", o2, err)
	}

	// Each session's welcome landed on its own event queue.
	for i, s := range []*Attachment{s1, s2} {
		select {
		case ev := <-s.Events():
			if ev.Kind != KindStream || string(ev.Payload) != "welcome" {
				t.Fatalf("session %d event = %d %q", i+1, ev.Kind, ev.Payload)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("session %d never got its welcome", i+1)
		}
	}

	// Uplink frames route by sid.
	if err := s2.SendStream([]byte("from-bob")); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return len(chunk.received(KindStream)) == 1 }) {
		t.Fatal("uplink frame never arrived")
	}
	if m := chunk.received(KindStream)[0]; m.SID != s2.sid || string(m.Payload) != "from-bob" {
		t.Fatalf("stream = sid %d %q, want sid %d from-bob", m.SID, m.Payload, s2.sid)
	}
}

func TestMuxChunkCloseEndsOneSessionOnly(t *testing.T) {
	chunk := newFakeChunkieServer(t, nil)
	pool := NewPool(testKey, Hooks{})
	s1, s2 := openTwo(t, pool, chunk.addr())

	if !waitFor(t, 5*time.Second, func() bool { return chunk.conn(0) != nil && len(chunk.received(KindOpen)) == 2 }) {
		t.Fatal("opens never arrived")
	}
	chunk.conn(0).WriteClose(s2.sid, "wrong chunk")
	select {
	case <-s2.Done():
		if reason, fromChunk := s2.CloseReason(); !fromChunk || reason != "wrong chunk" {
			t.Fatalf("close = %q fromChunk=%v, want chunk-stated wrong chunk", reason, fromChunk)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chunk close never reached the session")
	}

	// The surviving session still works both ways.
	select {
	case <-s1.Done():
		t.Fatal("closing one session killed its neighbor")
	default:
	}
	chunk.conn(0).WriteStream(s1.sid, []byte("still-here"))
	select {
	case ev := <-s1.Events():
		if string(ev.Payload) != "still-here" {
			t.Fatalf("event = %q", ev.Payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("surviving session stopped receiving")
	}
}

func TestMuxGatewayCloseTellsChunk(t *testing.T) {
	chunk := newFakeChunkieServer(t, nil)
	pool := NewPool(testKey, Hooks{})
	s1, _ := openTwo(t, pool, chunk.addr())

	s1.Close("bye")
	if !waitFor(t, 5*time.Second, func() bool { return len(chunk.received(KindClose)) == 1 }) {
		t.Fatal("chunk never heard about the close")
	}
	if m := chunk.received(KindClose)[0]; m.SID != s1.sid || string(m.Payload) != "bye" {
		t.Fatalf("close = sid %d %q", m.SID, m.Payload)
	}
	if err := s1.SendStream([]byte("late")); err == nil {
		t.Fatal("send after close succeeded")
	}
}

func TestMuxConnLossFailsAllSessionsThenRedials(t *testing.T) {
	chunk := newFakeChunkieServer(t, nil)
	pool := NewPool(testKey, Hooks{})
	s1, s2 := openTwo(t, pool, chunk.addr())

	if !waitFor(t, 5*time.Second, func() bool { return chunk.conn(0) != nil }) {
		t.Fatal("no server conn")
	}
	chunk.conn(0).Close()
	for i, s := range []*Attachment{s1, s2} {
		select {
		case <-s.Done():
			if reason, fromChunk := s.CloseReason(); fromChunk || reason != "chunk unavailable" {
				t.Fatalf("session %d close = %q fromChunk=%v", i+1, reason, fromChunk)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("session %d survived its connection", i+1)
		}
	}

	// The next open pays one redial, not an error.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s3, err := pool.Open(ctx, chunk.addr(), Open{Game: "wum", Sub: "carol", Chunk: "park-test", Role: "player"})
	if err != nil {
		t.Fatalf("open after conn loss: %v", err)
	}
	defer s3.Close("")
	if !waitFor(t, 5*time.Second, func() bool { return chunk.accepted.Load() == 2 }) {
		t.Fatalf("redial made %d connections total, want 2", chunk.accepted.Load())
	}
}

func TestMuxPongTimeoutKillsConn(t *testing.T) {
	// A listener that accepts the handshake and then goes silent: writes
	// succeed into the OS buffer, so only the liveness probe can tell the
	// chunk is a zombie.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			if _, err := Accept(conn, testKey, time.Now()); err != nil {
				conn.Close()
			}
			// Authenticated, then silent: never reads, never pongs.
		}
	}()

	pool := NewPool(testKey, Hooks{})
	pool.PingEvery = 20 * time.Millisecond
	pool.PongWithin = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := pool.Open(ctx, l.Addr().String(), Open{Game: "wum", Sub: "alice", Chunk: "park-test", Role: "player"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
		if reason, fromChunk := s.CloseReason(); fromChunk || reason != "chunk unavailable" {
			t.Fatalf("close = %q fromChunk=%v", reason, fromChunk)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a silent chunk was never detected")
	}
}

func TestMuxBacklogClosesOnlyTheStalledSession(t *testing.T) {
	chunk := newFakeChunkieServer(t, nil)
	pool := NewPool(testKey, Hooks{})
	s1, s2 := openTwo(t, pool, chunk.addr())

	if !waitFor(t, 5*time.Second, func() bool { return chunk.conn(0) != nil && len(chunk.received(KindOpen)) == 2 }) {
		t.Fatal("opens never arrived")
	}
	// Nobody drains s1: fan-out past its queue must close it, not stall
	// the shared demux.
	for i := 0; i < 400; i++ {
		if err := chunk.conn(0).WriteStream(s1.sid, []byte("tick")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	select {
	case <-s1.Done():
		if reason, fromChunk := s1.CloseReason(); fromChunk || reason != "relay backlog" {
			t.Fatalf("close = %q fromChunk=%v", reason, fromChunk)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stalled session was never closed")
	}
	if !waitFor(t, 5*time.Second, func() bool { return len(chunk.received(KindClose)) == 1 }) {
		t.Fatal("chunk never told the stalled session closed")
	}

	chunk.conn(0).WriteStream(s2.sid, []byte("healthy"))
	select {
	case ev := <-s2.Events():
		if string(ev.Payload) != "healthy" {
			t.Fatalf("event = %q", ev.Payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("healthy session starved by its neighbor's backlog")
	}
}

func TestMuxIdleConnReaped(t *testing.T) {
	chunk := newFakeChunkieServer(t, nil)
	down := make(chan error, 1)
	pool := NewPool(testKey, Hooks{ConnDown: func(_ string, err error) { down <- err }})
	pool.PingEvery = 20 * time.Millisecond
	pool.IdleAfter = 60 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := pool.Open(ctx, chunk.addr(), Open{Game: "wum", Sub: "alice", Chunk: "park-test", Role: "player"})
	if err != nil {
		t.Fatal(err)
	}
	s.Close("bye")
	select {
	case err := <-down:
		if err == nil || err.Error() != "idle" {
			t.Fatalf("conn down = %v, want idle reap", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a sessionless connection was never reaped")
	}

	// The pool dials fresh for the next session instead of reusing the
	// reaped connection.
	s2, err := pool.Open(ctx, chunk.addr(), Open{Game: "wum", Sub: "bob", Chunk: "park-test", Role: "player"})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close("")
	if !waitFor(t, 5*time.Second, func() bool { return chunk.accepted.Load() == 2 }) {
		t.Fatalf("connections = %d, want a fresh dial after the reap", chunk.accepted.Load())
	}
}

// ---------- broadcast replication and the seq-gate splice ----------

// tickFrameFor builds a single-record tick batch at the given seq — the
// shape the chunk broadcasts and the splice gates on.
func tickFrameFor(seq int64) []byte {
	run := codec.AppendEventRecord(nil, uint64(seq), 0x0100, 7, nil)
	return codec.EncodeTick(uint64(seq), seq, 1, run)
}

func sendBroadcast(t *testing.T, c *Conn, game, chunk string, frame []byte) {
	t.Helper()
	payload, err := EncodeBroadcast(game, chunk, frame)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteMessage(KindBroadcast, 0, payload); err != nil {
		t.Fatal(err)
	}
}

// nextStream returns the next stream frame's codec kind and payload.
func nextStream(t *testing.T, a *Attachment) (byte, []byte) {
	t.Helper()
	for {
		select {
		case ev := <-a.Events():
			if ev.Kind != KindStream {
				continue
			}
			kind, p, ok := frameBody(ev.Payload)
			if !ok {
				t.Fatalf("undecodable frame %x", ev.Payload)
			}
			return kind, p
		case <-a.Done():
			reason, _ := a.CloseReason()
			t.Fatalf("attachment closed (%q) while waiting for a frame", reason)
		case <-time.After(5 * time.Second):
			t.Fatal("no frame delivered")
		}
	}
}

// welcomeServer answers every open with a welcome at the open's stated
// resume position — the no-catch-up shape, anchoring the splice there.
func welcomeServer(t *testing.T) *fakeChunkieServer {
	t.Helper()
	return newFakeChunkieServer(t, func(c *Conn, m Msg) {
		o, err := DecodeOpen(m.Payload)
		if err != nil {
			t.Errorf("open: %v", err)
			return
		}
		c.WriteStream(m.SID, codec.EncodeWelcome(codec.Welcome{Seq: o.SinceSeq, Tick: uint64(o.SinceSeq), Hz: 24, Chunk: o.Chunk}))
	})
}

func openAt(t *testing.T, p *Pool, addr, sub, chunk string, sinceSeq int64) *Attachment {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, err := p.Open(ctx, addr, Open{Game: "wum", Sub: sub, Chunk: chunk, Role: "player", SinceSeq: sinceSeq})
	if err != nil {
		t.Fatal(err)
	}
	if kind, _ := nextStream(t, a); kind != codec.KindWelcome {
		t.Fatalf("first frame kind = %d, want welcome", kind)
	}
	return a
}

// One broadcast write on the wire reaches every local attachment
// subscribed to its chunk, and only those.
func TestBroadcastReplicatesToChunkAttachments(t *testing.T) {
	chunk := welcomeServer(t)
	pool := NewPool(testKey, Hooks{})
	a1 := openAt(t, pool, chunk.addr(), "alice", "park-test", 5)
	a2 := openAt(t, pool, chunk.addr(), "bob", "park-test", 5)
	other := openAt(t, pool, chunk.addr(), "carol", "park-other", 5)
	if got := chunk.accepted.Load(); got != 1 {
		t.Fatalf("attachments cost %d connections, want 1", got)
	}

	sendBroadcast(t, chunk.conn(0), "wum", "park-test", tickFrameFor(6))
	for i, a := range []*Attachment{a1, a2} {
		kind, p := nextStream(t, a)
		if kind != codec.KindTick {
			t.Fatalf("attachment %d frame kind = %d, want tick", i+1, kind)
		}
		if tk, _, err := codec.DecodeTick(p); err != nil || tk.FirstSeq != 6 {
			t.Fatalf("attachment %d tick = %+v err=%v", i+1, tk, err)
		}
	}
	// The other chunk's attachment saw nothing: broadcasts are addressed.
	select {
	case ev := <-other.Events():
		t.Fatalf("unsubscribed attachment received %x", ev.Payload)
	default:
	}
}

// The late-attach splice: broadcasts racing ahead of the unicast catch-up
// buffer until the gap closes, then the client edge sees one dense,
// duplicate-free seq line.
func TestLateAttachSplicesGaplessStream(t *testing.T) {
	chunk := newFakeChunkieServer(t, nil)
	pool := NewPool(testKey, Hooks{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, err := pool.Open(ctx, chunk.addr(), Open{Game: "wum", Sub: "alice", Chunk: "park-test", Role: "player", SinceSeq: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool { return chunk.conn(0) != nil && len(chunk.received(KindOpen)) == 1 }) {
		t.Fatal("open never arrived")
	}
	c := chunk.conn(0)
	sid := chunk.received(KindOpen)[0].SID
	counts := broadcastCounts()

	// Live fan-out outruns the catch-up: 6 and 7 arrive before the
	// welcome, the unicast lane then repays 4 and 5, and 8 lands late. 6
	// rides the wire twice — the dedup half of the gate.
	sendBroadcast(t, c, "wum", "park-test", tickFrameFor(6))
	sendBroadcast(t, c, "wum", "park-test", tickFrameFor(7))
	c.WriteStream(sid, codec.EncodeWelcome(codec.Welcome{Seq: 5, Tick: 5, Hz: 24, Chunk: "park-test"}))
	c.WriteStream(sid, tickFrameFor(4))
	c.WriteStream(sid, tickFrameFor(5))
	sendBroadcast(t, c, "wum", "park-test", tickFrameFor(8))
	sendBroadcast(t, c, "wum", "park-test", tickFrameFor(6))
	sendBroadcast(t, c, "wum", "park-test", tickFrameFor(9))

	if kind, _ := nextStream(t, a); kind != codec.KindWelcome {
		t.Fatalf("first frame kind = %d, want welcome", kind)
	}
	for _, want := range []int64{4, 5, 6, 7, 8, 9} {
		kind, p := nextStream(t, a)
		if kind != codec.KindTick {
			t.Fatalf("frame kind = %d, want tick %d", kind, want)
		}
		tk, _, err := codec.DecodeTick(p)
		if err != nil || tk.FirstSeq != want {
			t.Fatalf("tick seq = %d (err %v), want %d — splice is not gapless/dedup-free", tk.FirstSeq, err, want)
		}
	}
	select {
	case ev := <-a.Events():
		t.Fatalf("extra frame after the spliced run: %x", ev.Payload)
	default:
	}
	// The gate's ledger: 6 and 7 took the buffered detour before
	// delivering, the second 6 deduped, 8 and 9 delivered straight through.
	for result, want := range map[string]float64{"delivered": 4, "buffered": 2, "deduped": 1, "overflow": 0} {
		if d := broadcastCounts()[result] - counts[result]; d != want {
			t.Errorf("broadcasts %s delta = %v, want %v", result, d, want)
		}
	}
}

// A mid-session resync: the unicast snapshot resets the splice position,
// stale buffered broadcasts drop, and the stream resumes densely from the
// snapshot's seq.
func TestResyncSnapshotResetsSpliceAndDropsStale(t *testing.T) {
	chunk := welcomeServer(t)
	pool := NewPool(testKey, Hooks{})
	a := openAt(t, pool, chunk.addr(), "alice", "park-test", 5)
	c := chunk.conn(0)
	sid := chunk.received(KindOpen)[0].SID
	counts := broadcastCounts()

	// 7 and 8 gap past pos 5 and buffer; the snapshot at 8 supersedes
	// them both.
	sendBroadcast(t, c, "wum", "park-test", tickFrameFor(7))
	sendBroadcast(t, c, "wum", "park-test", tickFrameFor(8))
	c.WriteStream(sid, codec.EncodeSnapshot(codec.Snapshot{Seq: 8, Tick: 8, Z: []byte{1}}))
	sendBroadcast(t, c, "wum", "park-test", tickFrameFor(9))

	if kind, _ := nextStream(t, a); kind != codec.KindSnapshot {
		t.Fatal("snapshot did not reach the client edge")
	}
	kind, p := nextStream(t, a)
	if kind != codec.KindTick {
		t.Fatalf("post-snapshot frame kind = %d, want tick", kind)
	}
	if tk, _, err := codec.DecodeTick(p); err != nil || tk.FirstSeq != 9 {
		t.Fatalf("post-snapshot tick = %+v err=%v, want seq 9 (stale 7/8 must drop)", tk, err)
	}
	// 7 and 8 buffered, then resolved as deduped when the snapshot reset
	// the position past them; only 9 delivered.
	for result, want := range map[string]float64{"delivered": 1, "buffered": 2, "deduped": 2, "overflow": 0} {
		if d := broadcastCounts()[result] - counts[result]; d != want {
			t.Errorf("broadcasts %s delta = %v, want %v", result, d, want)
		}
	}
}

// A splice buffer that overflows closes that attachment alone; its
// neighbors on the shared connection keep their streams.
func TestSpliceOverflowClosesOnlyThatAttachment(t *testing.T) {
	chunk := welcomeServer(t)
	pool := NewPool(testKey, Hooks{})
	stuck := openAt(t, pool, chunk.addr(), "alice", "park-test", 5)
	healthy := openAt(t, pool, chunk.addr(), "bob", "park-other", 5)
	c := chunk.conn(0)

	// Seq 6 never arrives, so every later frame buffers; one past the
	// frame bound must close the attachment.
	for seq := int64(7); seq < 7+spliceMaxFrames+1; seq++ {
		sendBroadcast(t, c, "wum", "park-test", tickFrameFor(seq))
	}
	select {
	case <-stuck.Done():
		if reason, fromChunk := stuck.CloseReason(); fromChunk || reason != "splice backlog" {
			t.Fatalf("close = %q fromChunk=%v, want splice backlog", reason, fromChunk)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("overflowing attachment was never closed")
	}

	sendBroadcast(t, c, "wum", "park-other", tickFrameFor(6))
	if kind, p := nextStream(t, healthy); kind != codec.KindTick {
		t.Fatalf("neighbor frame kind = %d, want tick", kind)
	} else if tk, _, err := codec.DecodeTick(p); err != nil || tk.FirstSeq != 6 {
		t.Fatalf("neighbor tick = %+v err=%v", tk, err)
	}
}

func TestMuxRejectsWrongKey(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	done := make(chan error, 1)
	go func() {
		// An accept loop, not a single accept: the pool's one-retry redial
		// may land a second connection, which must also be rejected or it
		// would sit unanswered in the backlog and the session would linger
		// to the pong timeout.
		first := true
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			_, aerr := Accept(conn, []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), time.Now())
			if first {
				done <- aerr
				first = false
			}
		}
	}()
	pool := NewPool([]byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"), Hooks{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The dial itself can succeed (the handshake is one-way); the chunk
	// must refuse to speak, so the session dies instead of attaching.
	if s, err := pool.Open(ctx, l.Addr().String(), Open{Game: "g", Sub: "a", Chunk: "p", Role: "player"}); err == nil {
		select {
		case <-s.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("session on an unauthenticated conn survived")
		}
	}
	if err := <-done; err == nil {
		t.Fatal("wrong key accepted")
	}
}
