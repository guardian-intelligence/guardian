package park

// The park's side of the multiplexed transport against a real running
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
	"github.com/guardian-intelligence/guardian/src/chunkies/mount"
	"github.com/guardian-intelligence/guardian/src/chunkies/parkproxy"
	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
	"github.com/guardian-intelligence/guardian/src/games/wake-up-mythra/services/wum"
	"github.com/guardian-intelligence/guardian/src/postflight/controlplane/pgtest"
)

// nextFrame reads session events until one carries a stream frame of the
// wanted codec kind.
func nextFrame(t *testing.T, s *parkproxy.Session, want byte) []byte {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind != parkproxy.KindStream {
				continue
			}
			kind, payload, err := codec.NewReader(bytes.NewReader(ev.Payload)).Next()
			if err != nil {
				t.Fatalf("bad frame from park: %v", err)
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

func TestParkConnMultiplexesSessions(t *testing.T) {
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
	mods := &modules{client: mount.NewModule("client", mount.DefaultClient), park: mount.NewModule("park", mount.DefaultPark)}
	registry := newParks(func() []byte { b, _ := mods.park.Get(); return b }, wum.FixtureTerrain, j, mods, timing{hz: 24})
	authority, err := registry.get(ctx, "park-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(authority.close)

	key := []byte("0123456789abcdef0123456789abcdef")
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
			go handleParkConn(conn, key, authority, 16)
		}
	}()

	pool := parkproxy.NewPool(key, parkproxy.Hooks{})
	openCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	alice, err := pool.Open(openCtx, l.Addr().String(), parkproxy.Open{Sub: "alice", Park: "park-test", Role: "player", SinceSeq: -1})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := pool.Open(openCtx, l.Addr().String(), parkproxy.Open{Sub: "bob", Park: "park-test", Role: "spectator", SinceSeq: -1})
	if err != nil {
		t.Fatal(err)
	}

	// Both sessions attach through one connection and get their own
	// welcome.
	for _, s := range []*parkproxy.Session{alice, bob} {
		if _, err := codec.DecodeWelcome(nextFrame(t, s, codec.KindWelcome)); err != nil {
			t.Fatalf("welcome: %v", err)
		}
	}

	// A session opened for the wrong park is refused without disturbing
	// the connection's live sessions.
	wrong, err := pool.Open(openCtx, l.Addr().String(), parkproxy.Open{Sub: "carol", Park: "park-elsewhere", Role: "player", SinceSeq: -1})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-wrong.Done():
		if reason, fromPark := wrong.CloseReason(); !fromPark || reason != "wrong park" {
			t.Fatalf("close = %q fromPark=%v, want park-stated wrong park", reason, fromPark)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wrong-park open was not refused")
	}

	// An intent staged by one session fans out to every attached session,
	// each addressed by its own id. Batches may also carry system events
	// (the real-clock authority repays downtime at open), so look for the
	// record rather than assuming it travels alone.
	tickWith := func(s *parkproxy.Session, kind uint16, actor uint64) {
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
	if err := alice.SendStream(codec.EncodeIntent(1, wum.EvJoin, wum.DogIDFor("alice"), nil)); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*parkproxy.Session{alice, bob} {
		tickWith(s, wum.EvJoin, wum.DogIDFor("alice"))
	}

	// Closing one session leaves its neighbor attached and served.
	bob.Close("bye")
	if err := alice.SendStream(codec.EncodeIntent(2, wum.EvCheckIn, wum.DogIDFor("alice"), nil)); err != nil {
		t.Fatal(err)
	}
	tickWith(alice, wum.EvCheckIn, wum.DogIDFor("alice"))
	select {
	case <-alice.Done():
		reason, _ := alice.CloseReason()
		t.Fatalf("closing bob closed alice (%q)", reason)
	default:
	}
}
