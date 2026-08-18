// Package gametest is the conformance suite for game simulation modules.
// The suite IS the framework contract: it consumes only built artifacts
// through the sim ABI — never a game's source or vocabulary — so any game
// that passes can be served by the authority host and replicated by every
// client, and game #2 onboards by making this pass.
//
// Every property is asserted over arbitrary byte streams, which is what
// makes one suite scale to any game: determinism, snapshot completeness,
// reject purity, and system-event semantics hold for all inputs or the
// module is unservable, no matter what the bytes mean.
package gametest

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"testing"
)

// Event is one sim event: kind, acting entity, and opaque payload. Actor
// is meaningful for ABI v2 modules (the SimEvent envelope); v1 modules
// carry any actor inside the payload and the field is ignored.
type Event struct {
	Kind    uint16
	Actor   uint64
	Payload []byte
}

// System names the game's kind numbers for the framework-driven events
// (the host builds these payloads itself). Zero means the module predates
// that event and its assertions are skipped.
type System struct {
	RateSet      uint16 // {hz u32}
	ClockSkip    uint16 // {to_tick u64}, forward only
	EpochAdvance uint16 // {epoch u32, module_hash u64}
}

// Game is everything a game owes the suite: built artifacts and two
// opaque fixtures. Corpus events are valid inputs (recorded or listed);
// they widen accepted-path coverage but no property depends on them.
// Genesis may be empty only for a content-free game (ABI v2,
// content_cap 0) — the fetch dance is skipped entirely.
type Game struct {
	Park    []byte            // the game state machine module
	Modules map[string][]byte // other shipped modules: artifact gates only
	Genesis []byte            // genesis content artifact
	Corpus  []Event
	System  System
}

const (
	parkID = int64(0x67616D65)
	epoch0 = uint32(1)
)

var seeds = []uint64{1, 42, 0xC0FFEE}

func Run(t *testing.T, g Game) {
	t.Helper()
	if len(g.Park) == 0 {
		t.Fatal("gametest.Game needs Park module bytes")
	}

	t.Run("artifacts", func(t *testing.T) { runArtifacts(t, g) })
	t.Run("determinism", func(t *testing.T) { runDeterminism(t, g) })
	t.Run("snapshot_completeness", func(t *testing.T) { runSnapshotCompleteness(t, g) })
	t.Run("reject_purity", func(t *testing.T) { runRejectPurity(t, g) })
	t.Run("restore_rejects_garbage", func(t *testing.T) { runRestoreGarbage(t, g) })
	t.Run("content_identity", func(t *testing.T) { runContentIdentity(t, g) })
	t.Run("system_events", func(t *testing.T) { runSystemEvents(t, g) })
}

func runArtifacts(t *testing.T, g Game) {
	// Companion modules (session, presentation) may import host verbs, so
	// they get structural gates only: no float declarations anywhere, and
	// a declared ABI generation.
	for name, b := range g.Modules {
		if err := scanFloatDecls(b); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		exports, err := scanExports(b)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !contains(exports, "abi_version") {
			t.Errorf("%s does not export abi_version", name)
		}
	}

	// The simulation module carries the full contract: importing nothing
	// is what makes clocks, entropy, and the network unreachable.
	if err := scanFloatDecls(g.Park); err != nil {
		t.Errorf("park: %v", err)
	}
	imports, err := scanImports(g.Park)
	if err != nil {
		t.Errorf("park: %v", err)
	}
	for _, imp := range imports {
		t.Errorf("park imports %s — a simulation module must import nothing", imp)
	}

	h, err := newHost(g.Park)
	if err != nil {
		t.Fatalf("park: %v", err)
	}
	defer h.close()
	if h.abi == 0 {
		t.Errorf("park: abi_version = 0")
	}
	for _, name := range requiredExports(h.abi, g.System) {
		if _, ok := h.fns[name]; !ok {
			t.Errorf("park module does not export %s", name)
		}
	}
	if h.ioCap == 0 {
		t.Errorf("park buffers: io_cap = 0")
	}
	switch {
	case len(g.Genesis) == 0 && h.contentCap != 0:
		t.Errorf("module declares content_cap=%d but the game provides no Genesis artifact", h.contentCap)
	case len(g.Genesis) != 0 && h.contentCap == 0:
		t.Errorf("game provides a Genesis artifact but the module declares no content buffer")
	case uint32(len(g.Genesis)) > h.contentCap:
		t.Errorf("genesis artifact (%d bytes) exceeds content cap (%d)", len(g.Genesis), h.contentCap)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// open returns a park instance on the genesis artifact at the given seed.
func open(t *testing.T, g Game, seed uint64) *host {
	t.Helper()
	h, err := newHost(g.Park)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Genesis) > 0 {
		if code, err := h.setContent(g.Genesis); err != nil || code != 0 {
			h.close()
			t.Fatalf("stage(genesis): code=%d err=%v", code, err)
		}
	}
	if code, err := h.init(seed, parkID, epoch0); err != nil || code != 0 {
		h.close()
		t.Fatalf("sim_init: code=%d err=%v", code, err)
	}
	return h
}

// stream deals deterministic rounds from the corpus: each round is zero or
// more events followed by one step.
func stream(g Game, rnd *rand.Rand, rounds int) [][]Event {
	out := make([][]Event, rounds)
	for i := range out {
		for k := rnd.Intn(4); k > 0 && len(g.Corpus) > 0; k-- {
			out[i] = append(out[i], g.Corpus[rnd.Intn(len(g.Corpus))])
		}
	}
	return out
}

// drive applies the same rounds to every instance in lockstep, comparing
// apply codes, post-step hashes, and ticks. The last instance is "noisy":
// it additionally snapshots and rehashes every round, which pins the ABI
// claim that reads are inert — an instance polled constantly must derive
// the same world as one that is not.
func drive(t *testing.T, hosts []*host, rounds [][]Event) {
	t.Helper()
	noisy := hosts[len(hosts)-1]
	for i, events := range rounds {
		for j, ev := range events {
			code0, err := hosts[0].apply(ev)
			if err != nil {
				t.Fatalf("round %d event %d (kind %d): %v", i, j, ev.Kind, err)
			}
			for _, h := range hosts[1:] {
				code, err := h.apply(ev)
				if err != nil {
					t.Fatalf("round %d event %d (kind %d): %v", i, j, ev.Kind, err)
				}
				if code != code0 {
					t.Fatalf("round %d event %d (kind %d): apply codes diverge (%d vs %d)", i, j, ev.Kind, code, code0)
				}
			}
		}
		for _, h := range hosts {
			if err := h.step(); err != nil {
				t.Fatalf("round %d: %v", i, err)
			}
		}
		if _, err := noisy.snapshot(); err != nil {
			t.Fatalf("round %d: noisy snapshot: %v", i, err)
		}
		h0, err := hosts[0].hash()
		if err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		t0, err := hosts[0].tick()
		if err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		for n, h := range hosts[1:] {
			hh, err := h.hash()
			if err != nil {
				t.Fatalf("round %d: %v", i, err)
			}
			th, err := h.tick()
			if err != nil {
				t.Fatalf("round %d: %v", i, err)
			}
			if hh != h0 || th != t0 {
				t.Fatalf("round %d: instance %d diverged (hash %016x/%016x, tick %d/%d)", i, n+1, hh, h0, th, t0)
			}
		}
	}
}

// warm advances one instance through a stream without cross-comparison.
func warm(t *testing.T, h *host, g Game, seed int64, rounds int) {
	t.Helper()
	for i, events := range stream(g, rand.New(rand.NewSource(seed)), rounds) {
		for _, ev := range events {
			if _, err := h.apply(ev); err != nil {
				t.Fatalf("warm round %d (kind %d): %v", i, ev.Kind, err)
			}
		}
		if err := h.step(); err != nil {
			t.Fatalf("warm round %d: %v", i, err)
		}
	}
}

func runDeterminism(t *testing.T, g Game) {
	for _, seed := range seeds {
		a, b := open(t, g, seed), open(t, g, seed)
		defer a.close()
		defer b.close()
		drive(t, []*host{a, b}, stream(g, rand.New(rand.NewSource(int64(seed))), 96))
	}
}

func runSnapshotCompleteness(t *testing.T, g Game) {
	a := open(t, g, seeds[0])
	defer a.close()
	warm(t, a, g, 7, 40)

	snap, err := a.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	b := open(t, g, seeds[0])
	defer b.close()
	if code, err := b.restore(snap); err != nil || code != 0 {
		t.Fatalf("sim_restore(snapshot): code=%d err=%v", code, err)
	}
	ha, _ := a.hash()
	hb, err := b.hash()
	if err != nil || hb != ha {
		t.Fatalf("restored hash %016x, want %016x (err %v)", hb, ha, err)
	}
	resnap, err := b.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(resnap, snap) {
		t.Fatalf("snapshot is not canonical: restore(snap) re-snapshots to %d bytes != original %d", len(resnap), len(snap))
	}
	// The restored instance must not merely match — it must evolve
	// identically under further input.
	drive(t, []*host{a, b}, stream(g, rand.New(rand.NewSource(8)), 32))
}

func runRejectPurity(t *testing.T, g Game) {
	rnd := rand.New(rand.NewSource(11))
	h := open(t, g, seeds[0])
	defer h.close()
	warm(t, h, g, 11, 8)

	fuzz := func() Event {
		if len(g.Corpus) > 0 && rnd.Intn(2) == 0 {
			ev := g.Corpus[rnd.Intn(len(g.Corpus))]
			p := append([]byte{}, ev.Payload...)
			switch {
			case len(p) > 0 && rnd.Intn(2) == 0:
				p[rnd.Intn(len(p))] ^= byte(1 << rnd.Intn(8))
			case len(p) > 0:
				p = p[:rnd.Intn(len(p))]
			default:
				// A payload-free event mutates through its actor.
				ev.Actor = rnd.Uint64()
			}
			return Event{Kind: ev.Kind, Actor: ev.Actor, Payload: p}
		}
		p := make([]byte, rnd.Intn(41))
		rnd.Read(p)
		return Event{Kind: uint16(rnd.Intn(1024)), Actor: rnd.Uint64(), Payload: p}
	}

	accepted := 0
	for i := 0; i < 512; i++ {
		ev := fuzz()
		h0, _ := h.hash()
		t0, _ := h.tick()
		code, err := h.apply(ev)
		if err != nil {
			t.Fatalf("fuzz %d (kind %d, %d bytes): %v", i, ev.Kind, len(ev.Payload), err)
		}
		if code == 0 {
			accepted++
			continue
		}
		h1, _ := h.hash()
		t1, _ := h.tick()
		if h1 != h0 || t1 != t0 {
			t.Fatalf("fuzz %d (kind %d): rejected with code %d but mutated state (hash %016x -> %016x, tick %d -> %d)",
				i, ev.Kind, code, h0, h1, t0, t1)
		}
	}
	t.Logf("fuzz: %d/512 mutations accepted", accepted)
}

func runRestoreGarbage(t *testing.T, g Game) {
	base := open(t, g, seeds[0])
	defer base.close()
	warm(t, base, g, 13, 8)
	snap, err := base.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 64)
	rand.New(rand.NewSource(17)).Read(random)

	for name, garbage := range map[string][]byte{
		"random":    random,
		"empty":     {},
		"truncated": snap[:len(snap)/2],
	} {
		h := open(t, g, seeds[0])
		h0, _ := h.hash()
		code, err := h.restore(garbage)
		if err != nil {
			h.close()
			t.Fatalf("restore(%s): %v", name, err)
		}
		if code == 0 {
			h.close()
			t.Fatalf("restore(%s) accepted %d bytes that are not a snapshot", name, len(garbage))
		}
		h1, herr := h.hash()
		if herr != nil || h1 != h0 {
			h.close()
			t.Fatalf("restore(%s) rejected but damaged state: hash %016x -> %016x (err %v)", name, h0, h1, herr)
		}
		if err := h.step(); err != nil {
			h.close()
			t.Fatalf("instance unusable after rejected restore(%s): %v", name, err)
		}
		h.close()
	}
}

func runContentIdentity(t *testing.T, g Game) {
	if len(g.Genesis) == 0 {
		t.Skip("content-free game")
	}
	h := open(t, g, seeds[0])
	defer h.close()
	if id, err := h.contentID(); err != nil || id != contentID(g.Genesis) {
		t.Fatalf("content id = %016x, want the artifact's content id %016x (err %v)", id, contentID(g.Genesis), err)
	}
	fresh, err := newHost(g.Park)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.close()
	garbage := make([]byte, 64)
	rand.New(rand.NewSource(19)).Read(garbage)
	if code, err := fresh.setContent(garbage); err != nil || code == 0 {
		t.Fatalf("content stage accepted 64 garbage bytes (code=%d err=%v)", code, err)
	}
}

func runSystemEvents(t *testing.T, g Game) {
	le32 := func(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }
	le64 := func(v uint64) []byte { b := make([]byte, 8); binary.LittleEndian.PutUint64(b, v); return b }

	if kind := g.System.RateSet; kind != 0 {
		t.Run("rate_set", func(t *testing.T) {
			a, b := open(t, g, seeds[0]), open(t, g, seeds[0])
			defer a.close()
			defer b.close()
			drive(t, []*host{a, b}, stream(g, rand.New(rand.NewSource(23)), 8))
			at, _ := a.tick()
			ev := []Event{{Kind: kind, Payload: le32(48)}}
			drive(t, []*host{a, b}, [][]Event{ev})
			if rate, err := a.rate(); err != nil || rate != 48 {
				t.Fatalf("sim_rate = %d after rate_set 48 (err %v)", rate, err)
			}
			if anchor, err := a.anchorTick(); err != nil || anchor != at {
				t.Fatalf("sim_anchor_tick = %d after rate_set at tick %d (err %v)", anchor, at, err)
			}
			snap, err := a.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			c := open(t, g, seeds[0])
			defer c.close()
			if code, err := c.restore(snap); err != nil || code != 0 {
				t.Fatalf("restore across rate boundary: code=%d err=%v", code, err)
			}
			if rate, err := c.rate(); err != nil || rate != 48 {
				t.Fatalf("restored sim_rate = %d, want 48 (err %v)", rate, err)
			}
		})
	}

	if kind := g.System.ClockSkip; kind != 0 {
		t.Run("clock_skip", func(t *testing.T) {
			a, b := open(t, g, seeds[0]), open(t, g, seeds[0])
			defer a.close()
			defer b.close()
			drive(t, []*host{a, b}, stream(g, rand.New(rand.NewSource(29)), 8))
			at, _ := a.tick()
			drive(t, []*host{a, b}, [][]Event{{{Kind: kind, Payload: le64(at + 500)}}})
			// drive stepped once after the event, so the skip lands +1.
			if now, err := a.tick(); err != nil || now != at+501 {
				t.Fatalf("sim_tick = %d after clock_skip to %d and one step (err %v)", now, at+500, err)
			}
			if code, err := a.apply(Event{Kind: kind, Payload: le64(at)}); err != nil || code == 0 {
				t.Fatalf("backward clock_skip accepted (code=%d err=%v) — skips are forward only", code, err)
			}
		})
	}

	if kind := g.System.EpochAdvance; kind != 0 {
		t.Run("epoch_advance", func(t *testing.T) {
			a, b := open(t, g, seeds[0]), open(t, g, seeds[0])
			defer a.close()
			defer b.close()
			drive(t, []*host{a, b}, stream(g, rand.New(rand.NewSource(31)), 8))
			e, _ := a.epoch()
			payload := append(le32(e+1), le64(0xD06F00D)...)
			drive(t, []*host{a, b}, [][]Event{{{Kind: kind, Payload: payload}}})
			if now, err := a.epoch(); err != nil || now != e+1 {
				t.Fatalf("sim_epoch = %d after epoch_advance to %d (err %v)", now, e+1, err)
			}
		})
	}
}

// contentID is the framework's content-address derivation for artifacts —
// mix64(fnv1a(blob)) — which the module must mirror in sim_terrain_id.
func contentID(blob []byte) uint64 {
	h := uint64(0xCBF29CE484222325)
	for _, b := range blob {
		h ^= uint64(b)
		h *= 0x00000100000001B3
	}
	z := h
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}
