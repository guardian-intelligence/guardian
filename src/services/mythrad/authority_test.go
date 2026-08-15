package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guardian-intelligence/guardian/src/services/mythrad/journal"
	"github.com/guardian-intelligence/guardian/src/services/mythrad/wire"
	"github.com/guardian-intelligence/guardian/src/services/postflight/controlplane/pgtest"
)

// fixedClock pins the wall clock: at wallEpoch the anchored scheduler owes
// zero ticks, so tests that drive tickOnce directly own time completely.
func fixedClock(at time.Time) timing {
	return timing{hz: 24, now: func() time.Time { return at }}
}

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
	a, err := openAuthority(ctx, "park-test", defaultParkModule, fixtureTerrain, j, mods, fixedClock(wallEpoch))
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
	// A movement order makes the replay below exercise pathfinding and
	// steering through wazero, not just roster bookkeeping.
	move := append(dog(dogIDFor("alice")), 0x0A, 0x05) // node 1290 = (10, 10): open grass
	a.stageIntent(s, 3, evMoveTo, move)
	for i := 0; i < 100; i++ {
		a.tickOnce()
	}
	if a.lastSeq < 3 {
		t.Fatalf("journal lastSeq = %d, want >= 3 (join + check_in + move_to)", a.lastSeq)
	}

	// Duplicate intent ids are dropped at the door (idempotent resend).
	before := a.lastSeq
	a.stageIntent(s, 2, evCheckIn, dog(dogIDFor("alice")))
	a.tickOnce()
	if a.lastSeq != before {
		t.Fatalf("duplicate (actor, intent_id) minted seq %d", a.lastSeq)
	}

	// The live drill's server half: an already-running authority journals
	// and fans out a rate boundary, keeps its verification history, and
	// continues serving under the new schedule.
	boundary, boundaryHash := a.host.Tick(), a.host.Hash()
	a.mu.Lock()
	a.subs[s] = true
	a.mu.Unlock()
	done := make(chan error, 1)
	if err := a.stageRateChange(rateChangeReq{hz: 48, done: done}); err != nil {
		t.Fatalf("stage live rate: %v", err)
	}
	a.tickOnce()
	if err := <-done; err != nil {
		t.Fatalf("commit live rate: %v", err)
	}
	if a.hz != 48 || a.host.Rate() != 48 {
		t.Fatalf("live rate = authority %d / sim %d, want 48", a.hz, a.host.Rate())
	}
	if ok, _ := a.verdictFor(boundary, boundaryHash); ok == nil || !*ok {
		t.Fatal("live ring resize discarded the pre-boundary verification hash")
	}
	select {
	case frame := <-s.out:
		kind, payload, err := wire.NewReader(bytes.NewReader(frame)).Next()
		if err != nil || kind != wire.KindEvent {
			t.Fatalf("rate fanout frame: kind=%d err=%v", kind, err)
		}
		ev, err := wire.DecodeEvent(payload)
		if err != nil || ev.Kind != evRateSet || ev.Tick != boundary {
			t.Fatalf("rate fanout event: %+v err=%v", ev, err)
		}
	default:
		t.Fatal("connected session did not receive the live rate_set")
	}
	wantHash := a.host.Hash()
	wantTick := a.host.Tick()
	a.host.close()

	b, err := openAuthority(ctx, "park-test", defaultParkModule, fixtureTerrain, j, mods,
		timing{hz: 48, now: func() time.Time { return wallEpoch }})
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
	if b.hz != 48 {
		t.Fatalf("reopened rate = %dHz, want journaled 48Hz", b.hz)
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
	a, err := openAuthority(ctx, "park-race", defaultParkModule, fixtureTerrain, j, mods, fixedClock(wallEpoch))
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

// The module-update lane end to end: a new park module on the mount soaks
// in the dark, commits as a journaled epoch_advance with a boundary
// snapshot hashed by the NEW module, the authority keeps serving on the
// swapped host, and a reopen under the converged module restores from the
// re-anchored snapshot and replays to the identical hash.
func TestModuleEpochSwapLane(t *testing.T) {
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
	mods.park.set(defaultParkModule)
	a, err := openAuthority(ctx, "park-epoch", defaultParkModule, fixtureTerrain, j, mods, fixedClock(wallEpoch))
	if err != nil {
		t.Fatal(err)
	}
	s := &session{sub: "dana", out: make(chan []byte, 16)}
	a.stageIntent(s, 1, evJoin, dog8(dogIDFor("dana")))
	a.tickOnce()
	epochBefore := a.host.Epoch()

	// A module the runtime refuses must be pinned bad, never promoted.
	bad := append(append([]byte{}, defaultParkModule...), 0xFF)
	mods.park.set(bad)
	a.tickOnce()
	if a.moduleHash != displayHash(defaultParkModule) || a.cand != nil {
		t.Fatal("invalid module bytes must not open a candidate")
	}

	// Same behavior, different bytes: an appended custom section (id 0,
	// size 3, name "t", one content byte — wazero requires non-empty
	// content) is a valid wasm suffix, so the hash flips without changing
	// the sim.
	variant := append(append([]byte{}, defaultParkModule...), 0x00, 0x03, 0x01, 0x74, 0x00)
	mods.park.set(variant)
	for i := 0; i < soakTicks(a.hz)+5; i++ {
		a.tickOnce()
	}
	if a.moduleHash != displayHash(variant) {
		t.Fatalf("module hash = %s after soak window, want %s (swap never committed)", a.moduleHash, displayHash(variant))
	}
	if got := a.host.Epoch(); got != epochBefore+1 {
		t.Fatalf("epoch = %d after swap, want %d", got, epochBefore+1)
	}
	if a.cand != nil {
		t.Fatal("candidate host left open after commit")
	}

	// The swapped host keeps serving.
	a.stageIntent(s, 2, evCheckIn, dog8(dogIDFor("dana")))
	a.tickOnce()
	wantHash := a.host.Hash()
	wantTick := a.host.Tick()
	a.host.close()

	// Reopen the park as a converged deploy would: the mount serves the
	// new module, and the boundary snapshot must restore under it.
	b, err := openAuthority(ctx, "park-epoch", variant, fixtureTerrain, j, mods, fixedClock(wallEpoch))
	if err != nil {
		t.Fatalf("reopen after epoch swap: %v", err)
	}
	defer b.host.close()
	for b.host.Tick() < wantTick {
		b.host.Step()
	}
	if got := b.host.Hash(); got != wantHash {
		t.Fatalf("replay across the epoch boundary: hash %016x, want %016x", got, wantHash)
	}
	if got := b.host.Epoch(); got != epochBefore+1 {
		t.Fatalf("reopened epoch = %d, want %d", got, epochBefore+1)
	}
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
	a, err := openAuthority(ctx, "park-refresh", defaultParkModule, fixtureTerrain, j, mods, fixedClock(wallEpoch))
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
	// is staged with intent id 0. The first tick after any open also
	// journals the day_reset that seeds the sim's day index (seq 2).
	s1 := &session{sub: "carol", out: make(chan []byte, 16)}
	a.stageIntent(s1, 1, evJoin, carol)
	a.tickOnce()
	seqAfter(2, "first join + day_reset")
	a.stageIntent(s1, 0, evLeave, carol)
	a.tickOnce()
	seqAfter(3, "first departure")

	// Refreshed page: an intent for an absent dog is rejected (reason 3),
	// then the client rejoins and retries under the SAME intent id.
	s2 := &session{sub: "carol", out: make(chan []byte, 16)}
	a.stageIntent(s2, 42, evCheckIn, carol)
	a.tickOnce()
	seqAfter(3, "check-in while absent")
	a.stageIntent(s2, 43, evJoin, carol)
	a.stageIntent(s2, 42, evCheckIn, carol)
	a.tickOnce()
	seqAfter(5, "rejoin + retried check-in")

	// Second disconnect: the departure must not be swallowed as a resend
	// of the first one.
	a.stageIntent(s2, 0, evLeave, carol)
	a.tickOnce()
	seqAfter(6, "second departure")
}

// The anchored schedule end to end: a reopened park lands exactly on the
// tick the wall clock defines. A short gap is stepped through in the dark
// (the world keeps living, nothing journals); a long gap journals one
// clock_skip and re-floors the snapshot; both repayments are
// deterministic across independent reopens.
func TestReopenRepaysDowntime(t *testing.T) {
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
	a, err := openAuthority(ctx, "park-anchored", defaultParkModule, fixtureTerrain, j, mods, fixedClock(wallEpoch))
	if err != nil {
		t.Fatal(err)
	}
	s := &session{sub: "erin", out: make(chan []byte, 16)}
	a.stageIntent(s, 1, evJoin, dog8(dogIDFor("erin")))
	for i := 0; i < 5; i++ {
		a.tickOnce()
	}
	seqBefore := a.lastSeq
	a.host.close()

	// Short gap: stepped through, tick-exact, journal untouched.
	short := wallEpoch.Add(10 * time.Second)
	b, err := openAuthority(ctx, "park-anchored", defaultParkModule, fixtureTerrain, j, mods, fixedClock(short))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := b.host.Tick(), b.targetTick(short); got != want {
		t.Fatalf("short-gap reopen at tick %d, schedule says %d", got, want)
	}
	if b.lastSeq != seqBefore {
		t.Fatalf("short-gap repayment journaled events: lastSeq %d, want %d", b.lastSeq, seqBefore)
	}
	c, err := openAuthority(ctx, "park-anchored", defaultParkModule, fixtureTerrain, j, mods, fixedClock(short))
	if err != nil {
		t.Fatal(err)
	}
	if b.host.Hash() != c.host.Hash() {
		t.Fatal("two reopens at the same instant diverged (short gap)")
	}
	b.host.close()
	c.host.close()

	// Long gap: one clock_skip journals, the snapshot floor moves to the
	// jumped tick, and reopens past it restore deterministically.
	long := wallEpoch.Add(24 * time.Hour)
	d, err := openAuthority(ctx, "park-anchored", defaultParkModule, fixtureTerrain, j, mods, fixedClock(long))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := d.host.Tick(), d.targetTick(long); got != want {
		t.Fatalf("long-gap reopen at tick %d, schedule says %d", got, want)
	}
	if d.lastSeq != seqBefore+1 {
		t.Fatalf("long-gap repayment journaled %d events, want exactly one clock_skip", d.lastSeq-seqBefore)
	}
	snap, ok, err := j.LatestSnapshot(ctx, d.id)
	if err != nil || !ok {
		t.Fatalf("snapshot after clock_skip: ok=%v err=%v", ok, err)
	}
	if snap.Tick != d.host.Tick() {
		t.Fatalf("snapshot floor at tick %d, want the jumped tick %d", snap.Tick, d.host.Tick())
	}
	d.host.close()

	later := long.Add(10 * time.Second)
	e, err := openAuthority(ctx, "park-anchored", defaultParkModule, fixtureTerrain, j, mods, fixedClock(later))
	if err != nil {
		t.Fatal(err)
	}
	defer e.host.close()
	if got, want := e.host.Tick(), e.targetTick(later); got != want {
		t.Fatalf("post-skip reopen at tick %d, schedule says %d", got, want)
	}
	f, err := openAuthority(ctx, "park-anchored", defaultParkModule, fixtureTerrain, j, mods, fixedClock(later))
	if err != nil {
		t.Fatal(err)
	}
	defer f.host.close()
	if e.host.Hash() != f.host.Hash() {
		t.Fatal("two reopens at the same instant diverged (across a clock_skip)")
	}
}

// The rate lane end to end: the deployment's desired rate converges the
// world via one journaled rate_set in the dark, the schedule re-anchors
// piecewise (so lowering the rate later never stalls the park), and
// reopens across rate boundaries stay deterministic.
func TestReopenConvergesToDesiredRate(t *testing.T) {
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
	a, err := openAuthority(ctx, "park-rated", defaultParkModule, fixtureTerrain, j, mods, fixedClock(wallEpoch))
	if err != nil {
		t.Fatal(err)
	}
	s := &session{sub: "gale", out: make(chan []byte, 16)}
	a.stageIntent(s, 1, evJoin, dog8(dogIDFor("gale")))
	for i := 0; i < 5; i++ {
		a.tickOnce()
	}
	seqBefore := a.lastSeq
	if a.hz != 24 {
		t.Fatalf("genesis rate = %dHz, want 24", a.hz)
	}
	a.host.close()

	// Reopen wanting 120Hz: repay the 10s gap under the stored 24Hz
	// segment first, then exactly one rate_set re-anchors at that tick.
	at120 := wallEpoch.Add(10 * time.Second)
	b, err := openAuthority(ctx, "park-rated", defaultParkModule, fixtureTerrain, j, mods, timing{hz: 120, now: func() time.Time { return at120 }})
	if err != nil {
		t.Fatal(err)
	}
	if b.hz != 120 || b.host.Rate() != 120 {
		t.Fatalf("rate = %d/%d after reopen, want 120", b.hz, b.host.Rate())
	}
	if b.lastSeq != seqBefore+1 {
		t.Fatalf("rate convergence journaled %d events, want exactly one rate_set", b.lastSeq-seqBefore)
	}
	if got, want := b.host.AnchorTick(), b.host.Tick(); got != want {
		t.Fatalf("segment anchored at tick %d, want the boundary tick %d", got, want)
	}
	// The repayment ran at 24Hz granularity, so up to one old tick of
	// wall time (5 ticks at 120Hz) is still owed at the boundary — the
	// live loop's first catch-up burst repays it. Anything larger would
	// be a real discontinuity.
	if got, want := b.targetTick(at120), b.host.Tick(); got < want || got > want+5 {
		t.Fatalf("schedule discontinuity: target %d vs tick %d at the boundary", got, want)
	}
	if len(b.ring) != ringSeconds*120 {
		t.Fatalf("ring holds %d entries, want a 30s window at 120Hz (%d)", len(b.ring), ringSeconds*120)
	}
	snap, ok, err := j.LatestSnapshot(ctx, b.id)
	if err != nil || !ok || snap.Tick != b.host.Tick() {
		t.Fatalf("boundary snapshot: ok=%v err=%v tick=%d want %d", ok, err, snap.Tick, b.host.Tick())
	}
	b.host.close()

	// Lower the rate back at +20s: the 120Hz segment repays ~10s of gap
	// first (no stall — the mapping is piecewise, not global), then one
	// rate_set back to 24.
	at24 := wallEpoch.Add(20 * time.Second)
	c, err := openAuthority(ctx, "park-rated", defaultParkModule, fixtureTerrain, j, mods, timing{hz: 24, now: func() time.Time { return at24 }})
	if err != nil {
		t.Fatal(err)
	}
	defer c.host.close()
	if c.hz != 24 || c.host.Rate() != 24 {
		t.Fatalf("rate = %d/%d after lowering, want 24", c.hz, c.host.Rate())
	}
	if got, want := c.targetTick(at24), c.host.Tick(); got != want {
		t.Fatalf("lowering the rate stalled the schedule: target %d != tick %d", got, want)
	}
	// ~10s repaid at 120Hz between the two segment boundaries
	if repaid := c.host.AnchorTick() - snap.Tick; repaid < 1100 || repaid > 1300 {
		t.Fatalf("repaid %d ticks across the 120Hz segment, want ~1200", repaid)
	}
	// determinism across two rate boundaries
	d, err := openAuthority(ctx, "park-rated", defaultParkModule, fixtureTerrain, j, mods, timing{hz: 24, now: func() time.Time { return at24 }})
	if err != nil {
		t.Fatal(err)
	}
	defer d.host.close()
	if c.host.Hash() != d.host.Hash() {
		t.Fatal("two reopens at the same instant diverged (across rate boundaries)")
	}
}
