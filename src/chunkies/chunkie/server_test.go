package chunkie

// The chunk's side of the multiplexed transport against a real running
// authority: sessions share one authenticated connection, each gets its
// own welcome, fan-out routes by session, and one session's close leaves
// its neighbors attached.

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guardian-intelligence/guardian/src/chunkies/journal"
	"github.com/guardian-intelligence/guardian/src/chunkies/trunk"
	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
	"github.com/guardian-intelligence/guardian/src/postflight/controlplane/pgtest"
)

// nextFrame reads session events until one carries a stream frame of the
// wanted codec kind.
func nextFrame(t *testing.T, s *trunk.Attachment, want byte) []byte {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind != trunk.KindStream {
				continue
			}
			kind, payload, err := codec.NewReader(bytes.NewReader(ev.Payload)).Next()
			if err != nil {
				t.Fatalf("bad frame from chunk: %v", err)
			}
			if kind == want {
				return payload
			}
		case <-s.Done():
			reason, _ := s.CloseReason()
			t.Fatalf("session closed (%q) while waiting for frame kind %d", reason, want)
		case <-deadline:
			t.Fatalf("no frame of kind %d", want)
		}
	}
}

func TestTrunkConnMultiplexesSessions(t *testing.T) {
	ctx := context.Background()
	pgPool, err := pgxpool.New(ctx, pgtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pgPool.Close)
	j := journal.NewPg(pgPool)
	if err := j.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	mods := toyMods(toyModule(t))
	bcast := newBroadcaster("toy", 24, 1)
	registry := newChunks(func() []byte { b, _ := mods.sim.Get(); return b }, nil, toyVocab(), j, mods, timing{hz: 24}, bcast.publish, nil)
	auth, err := registry.get(ctx, "chunk-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(auth.close)

	key := []byte("0123456789abcdef0123456789abcdef")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	resolve := func(name string) (*authority, bool) {
		if name != "chunk-test" {
			return nil, false
		}
		return auth, true
	}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go handleTrunkConn(conn, key, "toy", resolve, 16, bcast)
		}
	}()

	pool := trunk.NewPool(key, trunk.Hooks{})
	openCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	alice, err := pool.Open(openCtx, l.Addr().String(), trunk.Open{Game: "toy", Sub: "alice", Chunk: "chunk-test", Role: "player", SinceSeq: -1})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := pool.Open(openCtx, l.Addr().String(), trunk.Open{Game: "toy", Sub: "bob", Chunk: "chunk-test", Role: "spectator", SinceSeq: -1})
	if err != nil {
		t.Fatal(err)
	}

	// Both sessions attach through one connection and get their own
	// welcome.
	for _, s := range []*trunk.Attachment{alice, bob} {
		if _, err := codec.DecodeWelcome(nextFrame(t, s, codec.KindWelcome)); err != nil {
			t.Fatalf("welcome: %v", err)
		}
	}

	// A session opened for the wrong chunk or game — or with an open the
	// chunk can't accept (the version-skew shape) — is refused without disturbing the
	// connection's live sessions.
	for _, tc := range []struct {
		open   trunk.Open
		reason string
	}{
		{trunk.Open{Game: "toy", Sub: "carol", Chunk: "chunk-elsewhere", Role: "player", SinceSeq: -1}, "wrong chunk"},
		{trunk.Open{Game: "elsewhere", Sub: "carol", Chunk: "chunk-test", Role: "player", SinceSeq: -1}, "wrong game"},
		{trunk.Open{Game: "toy", Sub: "carol", Chunk: "chunk-test", Role: "admin", SinceSeq: -1}, "bad open"},
	} {
		refused, err := pool.Open(openCtx, l.Addr().String(), tc.open)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-refused.Done():
			if reason, fromChunk := refused.CloseReason(); !fromChunk || reason != tc.reason {
				t.Fatalf("close = %q fromChunk=%v, want chunk-stated %q", reason, fromChunk, tc.reason)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("open was not refused with %q", tc.reason)
		}
	}

	// An intent staged by one session fans out to every attached session,
	// each addressed by its own id. Batches may also carry system events
	// (the real-clock authority repays downtime at open), so look for the
	// record rather than assuming it travels alone.
	tickWith := func(s *trunk.Attachment, kind uint16, actor uint64) {
		t.Helper()
		for {
			_, records, err := codec.DecodeTick(nextFrame(t, s, codec.KindTick))
			if err != nil {
				t.Fatalf("tick: %v", err)
			}
			for _, r := range records {
				if r.Kind == kind && r.Actor == actor {
					return
				}
			}
		}
	}
	if err := alice.SendStream(codec.EncodeIntent(1, kJoin, codec.ActorFor("alice"), nil)); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*trunk.Attachment{alice, bob} {
		tickWith(s, kJoin, codec.ActorFor("alice"))
	}

	// Closing one session leaves its neighbor attached and served.
	bob.Close("bye")
	if err := alice.SendStream(codec.EncodeIntent(2, kMove, codec.ActorFor("alice"), move(3))); err != nil {
		t.Fatal(err)
	}
	tickWith(alice, kMove, codec.ActorFor("alice"))
	select {
	case <-alice.Done():
		reason, _ := alice.CloseReason()
		t.Fatalf("closing bob closed alice (%q)", reason)
	default:
	}
}

// The publish seam's synchronous-copy contract, proven at the wire: one
// publish reaches every attachment on the connection, and mutating the
// run buffer after publish returns cannot corrupt what was delivered.
func TestPublishCopiesRunAndFansOutPerConn(t *testing.T) {
	ctx := context.Background()
	pgPool, err := pgxpool.New(ctx, pgtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pgPool.Close)
	j := journal.NewPg(pgPool)
	if err := j.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	mods := toyMods(toyModule(t))
	bcast := newBroadcaster("toy", 24, 1)
	// A pinned clock keeps the run loop owing zero ticks, so the only
	// broadcast on the wire is the one this test publishes.
	registry := newChunks(func() []byte { b, _ := mods.sim.Get(); return b }, nil, toyVocab(), j, mods, fixedClock(wallEpoch), bcast.publish, nil)
	auth, err := registry.get(ctx, "chunk-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(auth.close)

	key := []byte("0123456789abcdef0123456789abcdef")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	resolve := func(name string) (*authority, bool) {
		if name != "chunk-test" {
			return nil, false
		}
		return auth, true
	}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go handleTrunkConn(conn, key, "toy", resolve, 16, bcast)
		}
	}()

	pool := trunk.NewPool(key, trunk.Hooks{})
	openCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	alice, err := pool.Open(openCtx, l.Addr().String(), trunk.Open{Game: "toy", Sub: "alice", Chunk: "chunk-test", Role: "player", SinceSeq: -1})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := pool.Open(openCtx, l.Addr().String(), trunk.Open{Game: "toy", Sub: "bob", Chunk: "chunk-test", Role: "spectator", SinceSeq: -1})
	if err != nil {
		t.Fatal(err)
	}
	// The catch-up snapshot anchors each attachment's splice position;
	// only then can a broadcast splice through.
	for _, s := range []*trunk.Attachment{alice, bob} {
		if _, err := codec.DecodeSnapshot(nextFrame(t, s, codec.KindSnapshot)); err != nil {
			t.Fatalf("catch-up snapshot: %v", err)
		}
	}

	want := move(9)
	run := codec.AppendEventRecord(nil, 5, kMove, codec.ActorFor("alice"), want)
	bcast.publish("chunk-test", 1, 1, 1, run)
	for i := range run {
		run[i] = 0xFF
	}
	for _, s := range []*trunk.Attachment{alice, bob} {
		tick, recs, err := codec.DecodeTick(nextFrame(t, s, codec.KindTick))
		if err != nil || tick.Tick != 1 || tick.FirstSeq != 1 || len(recs) != 1 {
			t.Fatalf("broadcast tick = %+v recs=%d err=%v", tick, len(recs), err)
		}
		if recs[0].Intent != 5 || recs[0].Kind != kMove || !bytes.Equal(recs[0].Payload, want) {
			t.Fatalf("delivered record %+v — the run mutation leaked into the broadcast", recs[0])
		}
	}
}

// recordingWriter is a healthy broadcast peer: every payload lands in
// frames without blocking.
type recordingWriter struct {
	frames chan []byte
	closed atomic.Bool
}

func (w *recordingWriter) WriteMessage(kind byte, sid uint64, payload []byte) error {
	if w.closed.Load() {
		return errors.New("closed")
	}
	w.frames <- payload
	return nil
}

func (w *recordingWriter) Close() error { w.closed.Store(true); return nil }

// blockingWriter models a peer wedged mid-write: WriteMessage parks
// until the test releases it, the way a dead TCP peer parks a write
// until its deadline.
type blockingWriter struct {
	release chan struct{}
	closed  atomic.Bool
}

func (w *blockingWriter) WriteMessage(kind byte, sid uint64, payload []byte) error {
	<-w.release
	return errors.New("closed")
}

func (w *blockingWriter) Close() error { w.closed.Store(true); return nil }

// tickBody strips one framed codec message down to the tick payload
// DecodeTick expects — the unwrap the gateway does before delivery.
func tickBody(t *testing.T, frame []byte) []byte {
	t.Helper()
	kind, payload, err := codec.NewReader(bytes.NewReader(frame)).Next()
	if err != nil || kind != codec.KindTick {
		t.Fatalf("not a tick frame: kind=%d err=%v", kind, err)
	}
	return payload
}

func TestPublishShedsWedgedConnOnly(t *testing.T) {
	b := newBroadcaster("toy", 24, 1)
	b.queueFrames = 2

	healthy := &recordingWriter{frames: make(chan []byte, 64)}
	wedged := &blockingWriter{release: make(chan struct{})}
	defer close(wedged.release)
	hc := b.register(healthy)
	wc := b.register(wedged)
	hc.subscribe("chunk-test")
	wc.subscribe("chunk-test")

	// One frame may be in flight in the wedged writer plus queueFrames
	// queued; anything past that must down the conn, not block publish.
	// Reading the healthy frame between publishes keeps the healthy queue
	// drained, so only the wedged conn can overflow.
	run := codec.AppendEventRecord(nil, 5, kMove, codec.ActorFor("alice"), move(9))
	const n = 8
	for i := 1; i <= n; i++ {
		done := make(chan struct{})
		go func() {
			b.publish("chunk-test", uint64(i), int64(i), 1, run)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("publish blocked on a wedged conn")
		}
		select {
		case payload := <-healthy.frames:
			game, chunk, frame, err := trunk.DecodeBroadcast(payload)
			if err != nil || game != "toy" || chunk != "chunk-test" {
				t.Fatalf("frame %d: game=%q chunk=%q err=%v", i, game, chunk, err)
			}
			tick, _, err := codec.DecodeTick(tickBody(t, frame))
			if err != nil || tick.Tick != uint64(i) {
				t.Fatalf("frame %d out of order: tick=%d err=%v", i, tick.Tick, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("healthy conn missing frame %d", i)
		}
	}

	b.mu.Lock()
	_, wcIn := b.conns[wc]
	_, hcIn := b.conns[hc]
	b.mu.Unlock()
	if wcIn || !wedged.closed.Load() {
		t.Fatal("wedged conn was not removed and closed")
	}
	if !hcIn || healthy.closed.Load() {
		t.Fatal("healthy conn was downed alongside the wedged one")
	}
}

func TestPublishShedsOnByteBudget(t *testing.T) {
	b := newBroadcaster("toy", 24, 1)
	b.queueBytes = 1

	w := &recordingWriter{frames: make(chan []byte, 4)}
	c := b.register(w)
	c.subscribe("chunk-test")

	run := codec.AppendEventRecord(nil, 5, kMove, codec.ActorFor("alice"), move(9))
	b.publish("chunk-test", 1, 1, 1, run)

	b.mu.Lock()
	_, in := b.conns[c]
	b.mu.Unlock()
	if in || !w.closed.Load() {
		t.Fatal("conn over byte budget was not shed")
	}
}
