package main

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guardian-intelligence/guardian/src/services/mythrad/journal"
	"github.com/guardian-intelligence/guardian/src/services/postflight/controlplane/pgtest"
)

// The restore drill in miniature: an authority accepts intents, batches
// them into the journal at the tick boundary, and a reopened authority
// replays to the identical world hash — the whole truth model in one test.
func TestAuthorityJournalRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	j := journal.NewPg(pool)
	if err := j.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	mods := &modules{client: &clientModule{slot: "client"}, park: &clientModule{slot: "park"}}
	mods.client.set(defaultClientModule)
	mods.park.set(defaultParkModule)

	// openAuthority does not start the run loop: this test owns the host
	// and drives tickOnce directly.
	a, err := openAuthority(ctx, "park-test", defaultParkModule, j, mods)
	if err != nil {
		t.Fatal(err)
	}

	dog := func(id uint64) []byte {
		var p [8]byte
		binary.LittleEndian.PutUint64(p[:], id)
		return p[:]
	}
	s := &session{sub: "alice", out: make(chan []byte, 16)}
	a.stageIntent(s, 1, evJoin, dog(dogIDFor("alice")))
	a.tickOnce()
	a.stageIntent(s, 2, evCheckIn, dog(dogIDFor("alice")))
	for i := 0; i < 100; i++ {
		a.tickOnce()
	}
	if a.lastSeq < 2 {
		t.Fatalf("journal lastSeq = %d, want >= 2 (join + check_in)", a.lastSeq)
	}

	// Duplicate intent ids are dropped at the door (idempotent resend).
	before := a.lastSeq
	a.stageIntent(s, 2, evCheckIn, dog(dogIDFor("alice")))
	a.tickOnce()
	if a.lastSeq != before {
		t.Fatalf("duplicate (actor, intent_id) minted seq %d", a.lastSeq)
	}
	wantHash := a.host.Hash()
	wantTick := a.host.Tick()
	a.host.close()

	b, err := openAuthority(ctx, "park-test", defaultParkModule, j, mods)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer b.host.close()
	for b.host.Tick() < wantTick {
		b.host.Step()
	}
	if got := b.host.Hash(); got != wantHash {
		t.Fatalf("replayed park hash %016x, want %016x — journal does not reproduce the world", got, wantHash)
	}
}

// A second writer for the same park must conflict, not interleave: the
// authority closes itself instead of serving state ahead of the journal.
func TestAuthorityClosesOnAppendConflict(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	j := journal.NewPg(pool)
	if err := j.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	mods := &modules{client: &clientModule{slot: "client"}, park: &clientModule{slot: "park"}}
	a, err := openAuthority(ctx, "park-race", defaultParkModule, j, mods)
	if err != nil {
		t.Fatal(err)
	}

	// A rogue writer appends behind the authority's back. The authority's
	// next batch lands on the taken seq, conflicts, and the authority
	// closes itself rather than serve state the journal doesn't have.
	if _, err := j.Append(ctx, a.id, 0, []journal.Event{{Tick: 0, Epoch: 1, Kind: evDayReset, Actor: "rogue", Payload: []byte{0, 0, 0, 0}}}); err != nil {
		t.Fatal(err)
	}
	s := &session{sub: "bob", out: make(chan []byte, 16)}
	a.stageIntent(s, 1, evJoin, dog8(dogIDFor("bob")))
	a.tickOnce()
	if !a.isClosed() {
		t.Fatal("authority kept serving past a journal conflict (split brain)")
	}
	a.host.close()
}

func dog8(id uint64) []byte {
	var p [8]byte
	binary.LittleEndian.PutUint64(p[:], id)
	return p[:]
}

// The page-refresh contract: departures ride intent id 0 on every
// disconnect and must all land (never deduped), and a sim-rejected intent
// must not occupy the idempotency window — the corrected resend under the
// same id has to reach the sim. Both were violated by the dedup window
// spanning reconnects, which is what stranded refreshed players outside
// the park with reason 3 (absent) on every intent.
func TestRefreshRejoinAndRejectedIntentRetry(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	j := journal.NewPg(pool)
	if err := j.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	mods := &modules{client: &clientModule{slot: "client"}, park: &clientModule{slot: "park"}}
	a, err := openAuthority(ctx, "park-refresh", defaultParkModule, j, mods)
	if err != nil {
		t.Fatal(err)
	}
	defer a.host.close()

	carol := dog8(dogIDFor("carol"))
	seqAfter := func(want int64, step string) {
		t.Helper()
		if a.lastSeq != want {
			t.Fatalf("%s: journal lastSeq = %d, want %d", step, a.lastSeq, want)
		}
	}

	// First page load: join, then the connection drops and the departure
	// is staged with intent id 0.
	s1 := &session{sub: "carol", out: make(chan []byte, 16)}
	a.stageIntent(s1, 1, evJoin, carol)
	a.tickOnce()
	seqAfter(1, "first join")
	a.stageIntent(s1, 0, evLeave, carol)
	a.tickOnce()
	seqAfter(2, "first departure")

	// Refreshed page: an intent for an absent dog is rejected (reason 3),
	// then the client rejoins and retries under the SAME intent id.
	s2 := &session{sub: "carol", out: make(chan []byte, 16)}
	a.stageIntent(s2, 42, evCheckIn, carol)
	a.tickOnce()
	seqAfter(2, "check-in while absent")
	a.stageIntent(s2, 43, evJoin, carol)
	a.stageIntent(s2, 42, evCheckIn, carol)
	a.tickOnce()
	seqAfter(4, "rejoin + retried check-in")

	// Second disconnect: the departure must not be swallowed as a resend
	// of the first one.
	a.stageIntent(s2, 0, evLeave, carol)
	a.tickOnce()
	seqAfter(5, "second departure")
}
