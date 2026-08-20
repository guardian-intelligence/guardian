package chunkie

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guardian-intelligence/guardian/src/chunkies/journal"
	"github.com/guardian-intelligence/guardian/src/chunkies/mount"
	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
	"github.com/guardian-intelligence/guardian/src/postflight/controlplane/pgtest"
)

// The suite drives the framework's own toy game — the authority host is
// game-blind, so its tests must be too. Kind numbers mirror
// sim/shared/toy/src/lib.rs by value, not by import: the host consumes
// artifacts and vocabulary, never game source.
const (
	kJoin  = 0x0100
	kMove  = 0x0101
	kLeave = 0x0102
)

func toyModule(t *testing.T) []byte {
	t.Helper()
	module, err := os.ReadFile("../sim/shared/toy.wasm")
	if err != nil {
		t.Fatalf("built toy module: %v", err)
	}
	return module
}

func toyVocab() Vocab {
	return Vocab{
		DepartKind: kLeave,
		Actions:    map[uint16]string{kJoin: "join", kMove: "move", kLeave: "leave"},
		Rejects:    map[uint32]string{1: "encoding", 2: "full", 3: "present", 4: "absent", 5: "noop", 6: "kind", 7: "backward", 8: "snapshot"},
	}
}

func move(d int32) []byte {
	return binary.LittleEndian.AppendUint32(nil, uint32(d))
}

func toyMods(module []byte) *modules {
	// The toy has no client module; any bytes serve for the hash the
	// verdict carries, and the sim slot is the module under test.
	client := mount.NewModule("client")
	client.Set(module)
	sim := mount.NewModule("sim")
	sim.Set(module)
	return &modules{client: client, sim: sim}
}

// fixedClock pins the wall clock: at wallEpoch the anchored scheduler owes
// zero ticks, so tests that drive tickOnce directly own time completely.
func fixedClock(at time.Time) timing {
	return timing{hz: 24, now: func() time.Time { return at }}
}

type publishCall struct {
	chunk    string
	tick     uint64
	firstSeq int64
	count    uint16
	run      []byte
}

// recordPublishes captures every fan-out publish; the run is copied at
// call time, so a caller reusing the buffer would be caught, not hidden.
func recordPublishes(calls *[]publishCall) publishFunc {
	return func(chunk string, tick uint64, firstSeq int64, count uint16, run []byte) {
		*calls = append(*calls, publishCall{chunk, tick, firstSeq, count, append([]byte(nil), run...)})
	}
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
	module := toyModule(t)
	mods := toyMods(module)

	// openAuthority does not start the run loop: this test owns the host
	// and drives tickOnce directly.
	var pubs []publishCall
	a, err := openAuthority(ctx, "chunk-test", module, nil, toyVocab(), j, mods, fixedClock(wallEpoch), recordPublishes(&pubs))
	if err != nil {
		t.Fatal(err)
	}

	s := &session{sub: "alice", actorID: codec.ActorFor("alice"), out: make(chan []byte, 16)}
	a.stageIntent(s, 1, kJoin, nil)
	a.tickOnce()
	a.stageIntent(s, 2, kMove, move(5))
	a.stageIntent(s, 3, kMove, move(-3))
	for i := 0; i < 100; i++ {
		a.tickOnce()
	}
	if a.lastSeq < 3 {
		t.Fatalf("journal lastSeq = %d, want >= 3 (join + two moves)", a.lastSeq)
	}

	// Duplicate intent ids are dropped at the door (idempotent resend).
	before := a.lastSeq
	a.stageIntent(s, 2, kMove, move(5))
	a.tickOnce()
	if a.lastSeq != before {
		t.Fatalf("duplicate (actor, intent_id) minted seq %d", a.lastSeq)
	}

	// The live drill's server half: an already-running authority journals
	// and fans out a rate boundary, keeps its verification history, and
	// continues serving under the new schedule.
	boundary, boundaryHash := a.host.Tick(), a.host.Hash()
	pubsBefore := len(pubs)
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
	if len(pubs) != pubsBefore+1 {
		t.Fatalf("rate boundary published %d times, want once", len(pubs)-pubsBefore)
	}
	pub := pubs[len(pubs)-1]
	recs, err := codec.ParseRecords(pub.run, int(pub.count))
	if err != nil || len(recs) != 1 || recs[0].Kind != codec.KindRateSet || pub.tick != boundary {
		t.Fatalf("rate fanout publish: %+v recs=%v err=%v", pub, recs, err)
	}
	if recs[0].Actor != 0 {
		t.Fatalf("rate_set record: actor=%d — system events are authority-minted", recs[0].Actor)
	}
	wantHash := a.host.Hash()
	wantTick := a.host.Tick()
	a.host.close()

	b, err := openAuthority(ctx, "chunk-test", module, nil, toyVocab(), j, mods,
		timing{hz: 48, now: func() time.Time { return wallEpoch }}, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer b.host.close()
	for b.host.Tick() < wantTick {
		b.host.Step()
	}
	if got := b.host.Hash(); got != wantHash {
		t.Fatalf("replayed chunk hash %016x, want %016x — journal does not reproduce the world", got, wantHash)
	}
	if b.hz != 48 {
		t.Fatalf("reopened rate = %dHz, want journaled 48Hz", b.hz)
	}
}

// A second writer for the same chunk must conflict, not interleave: the
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
	module := toyModule(t)
	a, err := openAuthority(ctx, "chunk-race", module, nil, toyVocab(), j, toyMods(module), fixedClock(wallEpoch), nil)
	if err != nil {
		t.Fatal(err)
	}

	// A rogue writer appends behind the authority's back. The authority's
	// next batch lands on the taken seq, conflicts, and the authority
	// closes itself rather than serve state the journal doesn't have.
	if _, err := j.Append(ctx, a.id, 0, []journal.Event{{Tick: 0, Epoch: 1, Kind: codec.KindDayReset, Actor: "rogue", Payload: []byte{0, 0, 0, 0}}}); err != nil {
		t.Fatal(err)
	}
	s := &session{sub: "bob", actorID: codec.ActorFor("bob"), out: make(chan []byte, 16)}
	a.stageIntent(s, 1, kJoin, nil)
	a.tickOnce()
	if !a.isClosed() {
		t.Fatal("authority kept serving past a journal conflict (split brain)")
	}
	a.host.close()
}

// The module-update lane end to end: a new sim module on the mount soaks
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
	module := toyModule(t)
	mods := toyMods(module)
	a, err := openAuthority(ctx, "chunk-epoch", module, nil, toyVocab(), j, mods, fixedClock(wallEpoch), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &session{sub: "dana", actorID: codec.ActorFor("dana"), out: make(chan []byte, 16)}
	a.stageIntent(s, 1, kJoin, nil)
	a.tickOnce()
	epochBefore := a.host.Epoch()

	// A module the runtime refuses must be pinned bad, never promoted.
	bad := append(append([]byte{}, module...), 0xFF)
	mods.sim.Set(bad)
	a.tickOnce()
	if a.moduleHash != displayHash(module) || a.cand != nil {
		t.Fatal("invalid module bytes must not open a candidate")
	}

	// Same behavior, different bytes: an appended custom section (id 0,
	// size 3, name "t", one content byte — wazero requires non-empty
	// content) is a valid wasm suffix, so the hash flips without changing
	// the sim.
	variant := append(append([]byte{}, module...), 0x00, 0x03, 0x01, 0x74, 0x00)
	mods.sim.Set(variant)
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
	a.stageIntent(s, 2, kMove, move(4))
	a.tickOnce()
	wantHash := a.host.Hash()
	wantTick := a.host.Tick()
	a.host.close()

	// Reopen the chunk as a converged deploy would: the mount serves the
	// new module, and the boundary snapshot must restore under it.
	b, err := openAuthority(ctx, "chunk-epoch", variant, nil, toyVocab(), j, mods, fixedClock(wallEpoch), nil)
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
// the chunk with an absent reject on every intent.
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
	module := toyModule(t)
	a, err := openAuthority(ctx, "chunk-refresh", module, nil, toyVocab(), j, toyMods(module), fixedClock(wallEpoch), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.host.close()

	seqAfter := func(want int64, step string) {
		t.Helper()
		if a.lastSeq != want {
			t.Fatalf("%s: journal lastSeq = %d, want %d", step, a.lastSeq, want)
		}
	}

	attach := func(s *session) {
		t.Helper()
		if res := a.handleAttach(attachReq{sess: s}); res.err != nil {
			t.Fatal(res.err)
		}
	}

	// First page load: join, then the connection drops and the departure
	// is staged with intent id 0. The first tick after any open also
	// journals the day_reset that seeds the sim's day index (seq 2).
	s1 := &session{sub: "carol", role: "player", chunk: a, actorID: codec.ActorFor("carol"), out: make(chan []byte, 16)}
	attach(s1)
	a.stageIntent(s1, 1, kJoin, nil)
	a.tickOnce()
	seqAfter(2, "first join + day_reset")
	stageDeparture(a, s1)
	a.tickOnce()
	seqAfter(3, "first departure")

	// Refreshed page: an intent for an absent actor is rejected, then the
	// client rejoins and retries under the SAME intent id.
	s2 := &session{sub: "carol", role: "player", chunk: a, actorID: codec.ActorFor("carol"), out: make(chan []byte, 16)}
	attach(s2)
	a.stageIntent(s2, 42, kMove, move(2))
	a.tickOnce()
	seqAfter(3, "move while absent")
	a.stageIntent(s2, 43, kJoin, nil)
	a.stageIntent(s2, 42, kMove, move(2))
	a.tickOnce()
	seqAfter(5, "rejoin + retried move")

	// Second disconnect: the departure must not be swallowed as a resend
	// of the first one.
	stageDeparture(a, s2)
	a.tickOnce()
	seqAfter(6, "second departure")
}

// The prod reload cascade: a page reload joins a second session while the
// transport still holds the first. The rejoin supersedes the zombie, and
// the zombie's eventual reap must not remove the dog the live session is
// standing on.
func TestStaleSessionDepartureIsFenced(t *testing.T) {
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
	module := toyModule(t)
	a, err := openAuthority(ctx, "chunk-fence", module, nil, toyVocab(), j, toyMods(module), fixedClock(wallEpoch), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.host.close()

	seqAfter := func(want int64, step string) {
		t.Helper()
		if a.lastSeq != want {
			t.Fatalf("%s: journal lastSeq = %d, want %d", step, a.lastSeq, want)
		}
	}
	attach := func(s *session) {
		t.Helper()
		if res := a.handleAttach(attachReq{sess: s}); res.err != nil {
			t.Fatal(res.err)
		}
	}

	s1 := &session{sub: "carol", role: "player", chunk: a, actorID: codec.ActorFor("carol"), out: make(chan []byte, 16)}
	attach(s1)
	a.stageIntent(s1, 1, kJoin, nil)
	a.tickOnce()
	seqAfter(2, "join + day_reset")

	// The reload's session attaches while the zombie is still undetected;
	// the zombie stays attached (the client host redials on any close, so
	// evicting it would make two live tabs supersede each other forever).
	s2 := &session{sub: "carol", role: "player", chunk: a, actorID: codec.ActorFor("carol"), out: make(chan []byte, 16)}
	attach(s2)

	// The zombie's reap stages nothing: the actor stays in the chunk.
	stageDeparture(a, s1)
	a.tickOnce()
	seqAfter(2, "fenced stale departure")

	// The live session's own close still removes the dog.
	stageDeparture(a, s2)
	a.tickOnce()
	seqAfter(3, "current departure")
}

// Publish-once fan-out at the source: each committing tick publishes
// exactly once, carrying the batch's scalars and the accepted records'
// bytes verbatim; an empty tick publishes nothing.
func TestAuthorityPublishesOncePerCommittingTick(t *testing.T) {
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
	module := toyModule(t)
	var pubs []publishCall
	a, err := openAuthority(ctx, "chunk-publish", module, nil, toyVocab(), j, toyMods(module), fixedClock(wallEpoch), recordPublishes(&pubs))
	if err != nil {
		t.Fatal(err)
	}
	defer a.host.close()

	// First tick: the staged join commits alongside the day_reset the
	// clock stages — one batch, one publish.
	s := &session{sub: "hana", actorID: codec.ActorFor("hana"), out: make(chan []byte, 16)}
	a.stageIntent(s, 1, kJoin, nil)
	tickAt := a.host.Tick()
	a.tickOnce()
	if len(pubs) != 1 {
		t.Fatalf("committing tick published %d times, want 1", len(pubs))
	}
	pub := pubs[0]
	if pub.chunk != "chunk-publish" || pub.tick != tickAt || pub.firstSeq != 1 || pub.count != 2 {
		t.Fatalf("publish = %+v, want chunk chunk-publish tick %d firstSeq 1 count 2", pub, tickAt)
	}
	recs, err := codec.ParseRecords(pub.run, int(pub.count))
	if err != nil {
		t.Fatalf("published run does not parse: %v", err)
	}
	if recs[0].Kind != kJoin || recs[0].Intent != 1 || recs[0].Actor != codec.ActorFor("hana") {
		t.Fatalf("record 0 = %+v, want the staged join", recs[0])
	}
	if recs[1].Kind != codec.KindDayReset {
		t.Fatalf("record 1 kind = %#x, want day_reset", recs[1].Kind)
	}

	// Ticks with nothing accepted publish nothing.
	a.tickOnce()
	a.tickOnce()
	if len(pubs) != 1 {
		t.Fatalf("empty ticks published (%d calls total)", len(pubs))
	}

	// A multi-intent batch rides one publish with dense scalars.
	a.stageIntent(s, 2, kMove, move(1))
	a.stageIntent(s, 3, kMove, move(2))
	lastBefore := a.lastSeq
	tickAt = a.host.Tick()
	a.tickOnce()
	if len(pubs) != 2 {
		t.Fatalf("batch published %d new times, want 1", len(pubs)-1)
	}
	pub = pubs[1]
	if pub.tick != tickAt || pub.firstSeq != lastBefore+1 || pub.count != 2 {
		t.Fatalf("batch publish = %+v, want tick %d firstSeq %d count 2", pub, tickAt, lastBefore+1)
	}
	if recs, err := codec.ParseRecords(pub.run, 2); err != nil || recs[0].Intent != 2 || recs[1].Intent != 3 {
		t.Fatalf("batch run = %+v err=%v", recs, err)
	}
}

// The anchored schedule end to end: a reopened chunk lands exactly on the
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
	module := toyModule(t)
	mods := toyMods(module)
	a, err := openAuthority(ctx, "chunk-anchored", module, nil, toyVocab(), j, mods, fixedClock(wallEpoch), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &session{sub: "erin", actorID: codec.ActorFor("erin"), out: make(chan []byte, 16)}
	a.stageIntent(s, 1, kJoin, nil)
	for i := 0; i < 5; i++ {
		a.tickOnce()
	}
	seqBefore := a.lastSeq
	a.host.close()

	// Short gap: stepped through, tick-exact, journal untouched.
	short := wallEpoch.Add(10 * time.Second)
	b, err := openAuthority(ctx, "chunk-anchored", module, nil, toyVocab(), j, mods, fixedClock(short), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := b.host.Tick(), b.targetTick(short); got != want {
		t.Fatalf("short-gap reopen at tick %d, schedule says %d", got, want)
	}
	if b.lastSeq != seqBefore {
		t.Fatalf("short-gap repayment journaled events: lastSeq %d, want %d", b.lastSeq, seqBefore)
	}
	c, err := openAuthority(ctx, "chunk-anchored", module, nil, toyVocab(), j, mods, fixedClock(short), nil)
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
	d, err := openAuthority(ctx, "chunk-anchored", module, nil, toyVocab(), j, mods, fixedClock(long), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := d.host.Tick(), d.targetTick(long); got != want {
		t.Fatalf("long-gap reopen at tick %d, schedule says %d", got, want)
	}
	// The 24h gap also crosses a UTC day boundary, but day_reset stages on
	// the first live tick, not during the dark reopen — so exactly one
	// clock_skip lands here.
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
	e, err := openAuthority(ctx, "chunk-anchored", module, nil, toyVocab(), j, mods, fixedClock(later), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.host.close()
	if got, want := e.host.Tick(), e.targetTick(later); got != want {
		t.Fatalf("post-skip reopen at tick %d, schedule says %d", got, want)
	}
	f, err := openAuthority(ctx, "chunk-anchored", module, nil, toyVocab(), j, mods, fixedClock(later), nil)
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
// piecewise (so lowering the rate later never stalls the chunk), and
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
	module := toyModule(t)
	mods := toyMods(module)
	a, err := openAuthority(ctx, "chunk-rated", module, nil, toyVocab(), j, mods, fixedClock(wallEpoch), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &session{sub: "gale", actorID: codec.ActorFor("gale"), out: make(chan []byte, 16)}
	a.stageIntent(s, 1, kJoin, nil)
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
	b, err := openAuthority(ctx, "chunk-rated", module, nil, toyVocab(), j, mods, timing{hz: 120, now: func() time.Time { return at120 }}, nil)
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
	c, err := openAuthority(ctx, "chunk-rated", module, nil, toyVocab(), j, mods, timing{hz: 24, now: func() time.Time { return at24 }}, nil)
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
	d, err := openAuthority(ctx, "chunk-rated", module, nil, toyVocab(), j, mods, timing{hz: 24, now: func() time.Time { return at24 }}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.host.close()
	if c.host.Hash() != d.host.Hash() {
		t.Fatal("two reopens at the same instant diverged (across rate boundaries)")
	}
}
