package chunkie

// The chunk's side of the multiplexed transport against a real running
// authority: sessions share one authenticated connection, each gets its
// own welcome, fan-out routes by session, and one session's close leaves
// its neighbors attached.

import (
	"bytes"
	"context"
	"net"
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
	registry := newChunks(func() []byte { b, _ := mods.sim.Get(); return b }, nil, toyVocab(), j, mods, timing{hz: 24})
	auth, err := registry.get(ctx, "park-test")
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
		if name != "park-test" {
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
			go handleTrunkConn(conn, key, "wum", resolve, 16)
		}
	}()

	pool := trunk.NewPool(key, trunk.Hooks{})
	openCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	alice, err := pool.Open(openCtx, l.Addr().String(), trunk.Open{Game: "wum", Sub: "alice", Chunk: "park-test", Role: "player", SinceSeq: -1})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := pool.Open(openCtx, l.Addr().String(), trunk.Open{Game: "wum", Sub: "bob", Chunk: "park-test", Role: "spectator", SinceSeq: -1})
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
		{trunk.Open{Game: "wum", Sub: "carol", Chunk: "park-elsewhere", Role: "player", SinceSeq: -1}, "wrong chunk"},
		{trunk.Open{Game: "elsewhere", Sub: "carol", Chunk: "park-test", Role: "player", SinceSeq: -1}, "wrong game"},
		{trunk.Open{Game: "wum", Sub: "carol", Chunk: "park-test", Role: "admin", SinceSeq: -1}, "bad open"},
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
