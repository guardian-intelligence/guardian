package park

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/guardian-intelligence/guardian/src/chunkies/journal"
	"github.com/guardian-intelligence/guardian/src/chunkies/mount"
	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
	"github.com/guardian-intelligence/guardian/src/games/wake-up-mythra/services/wum"
)

const (
	snapshotEvery  = 512 // events between durable snapshots
	snapshotMaxAge = 15 * time.Minute
	dedupWindow    = 4096 // remembered (actor, intent_id) pairs per park

	// ringSeconds bounds how stale a client check (and a rejoin's fast-
	// forward) may be. Older checks answer "unknown", which the client
	// counts as a strike; rejoins further behind get a snapshot (the
	// compute bound from src/chunkies/README.md).
	ringSeconds = 30

	// catchupBurst caps ticks repaid per scheduler wakeup, so attach
	// requests stay serviced while a hitch is being worked off.
	catchupBurst = 8

	// darkStepWindow bounds the downtime the reopen path repays by
	// stepping tick-by-tick (the world keeps living through a short
	// restart). Longer gaps journal a clock_skip instead, keeping replay
	// cost bounded and making the jump an event every replica sees.
	darkStepWindow = 60 * time.Second

	// Kept in lockstep with sim/shared/park's rate_set validation. Below
	// 12Hz a boosted dog would cross more than half a cell in one axis
	// sub-move, violating nav's collision boundary.
	minTickHz = 12
	maxTickHz = 1000
)

// wallEpoch is the instant of tick 0 for every park: tick N is defined as
// wallEpoch + N*tickDur + the park's phase offset. The mapping is a
// definition, not a measurement — the scheduler chases it, so hitches and
// downtime are always repaid and a tick number doubles as a timestamp.
var wallEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// timing carries the desired tick rate (the actual rate is world state:
// the sim's journaled rate segment, which the dark phase converges toward
// hz via rate_set) and the wall clock, injectable so tests and simulation
// harnesses own time.
type timing struct {
	hz  int
	now func() time.Time
}

// parkHost wraps one wazero instance of the park module (the game state
// machine). Not goroutine-safe: owned by its authority's loop.
type parkHost struct {
	rt         wazero.Runtime
	mem        api.Memory
	ioPtr      uint32
	ioCap      uint32
	terrainPtr uint32
	terrainCap uint32

	fInit, fRestore, fSnapshot, fStep, fApply, fHash, fTick, fEpoch,
	fSetTerrain, fTerrainID api.Function

	// Optional (absent on modules from before rates existed, which run
	// the genesis segment): the sim's rate and mapping anchor.
	fRate, fAnchorTick, fAnchorNs api.Function
}

// hostABIEra is the sim ABI generation this host speaks. Any other era is
// refused at instantiation — the guard that makes a behavior ConfigMap
// converging ahead of the process image a held update instead of a
// misinterpreted event stream.
const hostABIEra = 2

// acceptModule is the mount's acceptance gate: park-slot bytes must
// instantiate under this host and declare its ABI era. The client slot
// passes through — this process only distributes those bytes, and the
// browser's own boot gate decides there.
func acceptModule(slot string, module []byte) error {
	if slot != "park" {
		return nil
	}
	h, err := newParkHost(module)
	if err != nil {
		return err
	}
	h.close()
	return nil
}

func newParkHost(module []byte) (*parkHost, error) {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	mod, err := rt.Instantiate(ctx, module)
	if err != nil {
		rt.Close(ctx)
		return nil, err
	}
	h := &parkHost{rt: rt, mem: mod.Memory()}
	get := func(name string) api.Function { return mod.ExportedFunction(name) }
	fABI := get("abi_version")
	if fABI == nil {
		rt.Close(ctx)
		return nil, errors.New("park module does not declare abi_version")
	}
	if vs, err := fABI.Call(ctx); err != nil || len(vs) == 0 {
		rt.Close(ctx)
		return nil, fmt.Errorf("park module abi_version: %v", err)
	} else if era := uint32(vs[0]); era != hostABIEra {
		rt.Close(ctx)
		return nil, fmt.Errorf("park module declares ABI era %d, this host speaks %d", era, hostABIEra)
	}
	h.fInit, h.fRestore, h.fSnapshot = get("sim_init"), get("sim_restore"), get("sim_snapshot")
	h.fStep, h.fApply, h.fHash = get("sim_step"), get("sim_apply"), get("sim_hash")
	h.fTick, h.fEpoch = get("sim_tick"), get("sim_epoch")
	h.fSetTerrain, h.fTerrainID = get("sim_content_stage"), get("sim_content_id")
	h.fRate, h.fAnchorTick, h.fAnchorNs = get("sim_rate"), get("sim_anchor_tick"), get("sim_anchor_ns")
	ioBuf, ioCap := get("io_buf"), get("io_cap")
	terrainBuf, terrainCap := get("content_buf"), get("content_cap")
	if h.mem == nil || h.fInit == nil || h.fRestore == nil || h.fSnapshot == nil ||
		h.fStep == nil || h.fApply == nil || h.fHash == nil || h.fTick == nil ||
		h.fEpoch == nil || h.fSetTerrain == nil || h.fTerrainID == nil ||
		ioBuf == nil || ioCap == nil || terrainBuf == nil || terrainCap == nil {
		rt.Close(ctx)
		return nil, errors.New("park module missing sim ABI exports")
	}
	for _, f := range []struct {
		dst *uint32
		fn  api.Function
	}{{&h.ioPtr, ioBuf}, {&h.ioCap, ioCap}, {&h.terrainPtr, terrainBuf}, {&h.terrainCap, terrainCap}} {
		v, err := f.fn.Call(ctx)
		if err != nil {
			rt.Close(ctx)
			return nil, err
		}
		*f.dst = uint32(v[0])
	}
	return h, nil
}

func (h *parkHost) close() { h.rt.Close(context.Background()) }

// callE is the non-panicking call used for candidate modules under soak:
// a candidate that traps is a rejected deploy, not a host bug.
func (h *parkHost) callE(f api.Function, args ...uint64) (uint64, error) {
	res, err := f.Call(context.Background(), args...)
	if err != nil {
		return 0, err
	}
	if len(res) == 0 {
		return 0, nil
	}
	return res[0], nil
}

func (h *parkHost) call(f api.Function, args ...uint64) uint64 {
	res, err := h.callE(f, args...)
	if err != nil {
		// The live module validates its inputs and never traps on data; a
		// trap is a host bug and this park instance is unusable.
		panic(fmt.Sprintf("park module trapped: %v", err))
	}
	return res
}

// call32 is call for i32-returning exports. wazero's raw result slots are
// unspecified above the result width — the arm64 backend leaves argument
// remnants in the high bits — so every i32 result MUST come through here,
// never through a bare `call(...) != 0`.
func (h *parkHost) call32(f api.Function, args ...uint64) uint32 {
	return uint32(h.call(f, args...))
}

func (h *parkHost) Init(seed uint64, parkID int64, epoch uint32) error {
	if code := h.call32(h.fInit, seed, uint64(parkID), uint64(epoch)); code != 0 {
		return fmt.Errorf("sim_init rejected: code %d", code)
	}
	return nil
}

// SetTerrain loads a terrain artifact into the module. Required before
// Init or Restore and around every terrain_set event during replay — the
// sim cross-checks the blob's identity at each of those boundaries.
func (h *parkHost) SetTerrain(blob []byte) error {
	if uint32(len(blob)) > h.terrainCap || !h.mem.Write(h.terrainPtr, blob) {
		return errors.New("terrain blob does not fit module terrain buffer")
	}
	if code := h.call32(h.fSetTerrain, uint64(len(blob))); code != 0 {
		return fmt.Errorf("sim_set_terrain rejected blob: code %d", code)
	}
	return nil
}

func (h *parkHost) TerrainID() uint64 { return h.call(h.fTerrainID) }

func (h *parkHost) Restore(state []byte) error {
	if uint32(len(state)) > h.ioCap || !h.mem.Write(h.ioPtr, state) {
		return errors.New("snapshot does not fit module io buffer")
	}
	if code := h.call32(h.fRestore, uint64(len(state))); code != 0 {
		return fmt.Errorf("sim_restore rejected snapshot: code %d", code)
	}
	return nil
}

func (h *parkHost) Snapshot() []byte {
	n := h.call32(h.fSnapshot)
	b, ok := h.mem.Read(h.ioPtr, n)
	if !ok {
		panic("park module snapshot read out of bounds")
	}
	out := make([]byte, n)
	copy(out, b)
	return out
}

func (h *parkHost) Step()         { h.call(h.fStep) }
func (h *parkHost) Hash() uint64  { return h.call(h.fHash) }
func (h *parkHost) Tick() uint64  { return h.call(h.fTick) }
func (h *parkHost) Epoch() uint32 { return h.call32(h.fEpoch) }

// Rate and the mapping anchor come from state; a pre-rate module runs the
// genesis segment (24Hz anchored at tick 0 = wallEpoch).
func (h *parkHost) Rate() int {
	if h.fRate == nil {
		return 24
	}
	return int(h.call32(h.fRate))
}

func (h *parkHost) AnchorTick() uint64 {
	if h.fAnchorTick == nil {
		return 0
	}
	return h.call(h.fAnchorTick)
}

func (h *parkHost) AnchorNs() uint64 {
	if h.fAnchorNs == nil {
		return 0
	}
	return h.call(h.fAnchorNs)
}

// Candidate-safe variants: errors instead of panics, used only while a
// new module soaks in the dark before an epoch swap.
func (h *parkHost) SetTerrainE(blob []byte) error {
	if uint32(len(blob)) > h.terrainCap || !h.mem.Write(h.terrainPtr, blob) {
		return errors.New("terrain blob does not fit candidate terrain buffer")
	}
	code, err := h.callE(h.fSetTerrain, uint64(len(blob)))
	if err != nil {
		return err
	}
	if uint32(code) != 0 {
		return fmt.Errorf("candidate rejected terrain: code %d", uint32(code))
	}
	return nil
}

func (h *parkHost) RestoreE(state []byte) error {
	if uint32(len(state)) > h.ioCap || !h.mem.Write(h.ioPtr, state) {
		return errors.New("snapshot does not fit candidate io buffer")
	}
	code, err := h.callE(h.fRestore, uint64(len(state)))
	if err != nil {
		return err
	}
	if uint32(code) != 0 {
		return fmt.Errorf("candidate rejected snapshot: code %d", uint32(code))
	}
	return nil
}

func (h *parkHost) ApplyE(event []byte) (uint32, error) {
	if uint32(len(event)) > h.ioCap || !h.mem.Write(h.ioPtr, event) {
		return 1, nil
	}
	code, err := h.callE(h.fApply, uint64(len(event)))
	return uint32(code), err
}

func (h *parkHost) StepE() error {
	_, err := h.callE(h.fStep)
	return err
}

func (h *parkHost) HashE() (uint64, error) { return h.callE(h.fHash) }

// Apply runs one encoded event (kind u16 LE + payload) through sim_apply.
// The module must not mutate on a nonzero return (pinned by its tests).
func (h *parkHost) Apply(event []byte) uint32 {
	if uint32(len(event)) > h.ioCap || !h.mem.Write(h.ioPtr, event) {
		return 1 // the sim's ERR_ENCODING
	}
	return h.call32(h.fApply, uint64(len(event)))
}

// stagedIntent is a session (or system) intent waiting for the tick
// boundary. sess is nil for system events.
type stagedIntent struct {
	sess       *session
	actor      string
	actorID    uint64
	intentID   uint64
	kind       uint16
	payload    []byte
	receivedAt time.Time
	dequeuedAt time.Time
	rateHz     int
	done       chan error
}

type rateChangeReq struct {
	hz   int
	done chan error
}

type attachReq struct {
	sess      *session
	sinceSeq  int64
	sinceTick uint64
	resync    bool
	done      chan attachResult
}

// attachResult carries the raw catch-up material out of the tick
// goroutine: the attach position and an uncompressed snapshot capture. The
// journal read and the deflate pass happen on the session's own goroutine
// (catchupFrames), so the tick loop never blocks on a client's history.
type attachResult struct {
	welcome []byte
	seq     int64
	tick    uint64
	epoch   uint32
	wh      uint64
	terrain uint64
	state   []byte
	err     error
}

type snapCacheEntry struct {
	tick  uint64
	frame []byte
}

type ringEntry struct {
	tick uint64
	wh   uint64
}

// modules exposes the current distribution hashes riding every verdict:
// the client presentation module and the park module itself.
type modules struct {
	client *mount.Module
	park   *mount.Module
}

// authority is one park's sim authority: journal writer, validator, and
// fan-out hub (src/chunkies/README.md). run() is the single owner of the host.
type authority struct {
	name string
	id   int64
	host *parkHost
	j    journal.Journal
	mods *modules

	// The tick schedule, derived from the sim's journaled rate segment
	// (refreshSchedule): tick anchorTick falls at anchor, later ticks
	// advance at tickDur. anchor degrades to a process-local stand-in
	// when the module cannot journal a clock_skip yet. phase staggers
	// co-located parks' tick work across the tick interval, derived from
	// the immutable park id so player-votable metadata can never move a
	// park's clock.
	tm         timing
	hz         int
	tickDur    time.Duration
	phase      time.Duration
	anchor     time.Time
	anchorTick uint64

	// The active terrain artifact, cached for welcome/snapshot frames so
	// clients know which blob to fetch before restoring, and as raw bytes
	// for candidate modules under soak. Written only by the boot path and
	// the tick goroutine's replay.
	terrain     uint64
	terrainBlob []byte

	// The module-update lane (src/chunkies/README.md, module epochs): when the
	// behavior mount serves a park module whose hash differs from the
	// running one, a candidate instance soaks in the dark — fed the same
	// events, stepped in lockstep, fanning out nothing — and on a clean
	// soak the swap commits as an epoch_advance journal event plus a
	// synchronous boundary snapshot hashed by the new module. All fields
	// are owned by the tick goroutine.
	moduleHash string // display hash of the running module
	cand       *parkHost
	candHash   string
	candSum    uint64 // first 8 bytes (LE) of the candidate's sha256
	soakLeft   int
	badModule  string // last hash that failed soak; retried only on change

	mu       sync.Mutex
	staged   []stagedIntent
	subs     map[*session]bool
	ring     []ringEntry // ringSeconds*hz entries
	ringHead uint64
	lastSeq  int64
	closed   bool
	seen     map[string]struct{}
	seenFifo []string

	attach chan attachReq
	rate   chan rateChangeReq
	stop   chan struct{}

	snapCache       snapCacheEntry
	eventsSinceSnap int
	lastSnapAt      time.Time
	utcDay          uint32
}

const soakDuration = 5 * time.Second

func soakTicks(hz int) int {
	return int(soakDuration * time.Duration(hz) / time.Second)
}

func displayHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:4])
}

// swapPrelude advances the module-update lane at the top of a tick. When a
// candidate's soak has just completed it returns the epoch_advance intent
// that must lead this tick's batch — the journaled boundary between the
// old module's ticks and the new one's.
func (a *authority) swapPrelude() []stagedIntent {
	bytes, hash := a.mods.park.Get()
	if a.cand != nil && hash != a.candHash {
		a.failSoak("", errors.New("superseded by a newer module"))
	}
	if a.cand == nil {
		if len(bytes) == 0 || hash == a.moduleHash || hash == a.badModule {
			return nil
		}
		cand, err := newParkHost(bytes)
		if err != nil {
			a.badModule = hash
			log.Printf("park %s: module %s rejected: %v", a.name, hash, err)
			mEpochSwaps.WithLabelValues("soak_abort").Inc()
			return nil
		}
		a.cand, a.candHash, a.soakLeft = cand, hash, soakTicks(a.hz)
		sum := sha256.Sum256(bytes)
		a.candSum = binary.LittleEndian.Uint64(sum[:8])
		if err := cand.SetTerrainE(a.terrainBlob); err != nil {
			a.failSoak(hash, err)
			return nil
		}
		if err := cand.RestoreE(a.host.Snapshot()); err != nil {
			a.failSoak(hash, err)
			return nil
		}
		log.Printf("park %s: module %s soaking for %s / %d ticks at %dHz (live %s)",
			a.name, hash, soakDuration, a.soakLeft, a.hz, a.moduleHash)
		return nil
	}
	if a.soakLeft > 0 {
		a.soakLeft--
		return nil
	}
	var p [12]byte
	binary.LittleEndian.PutUint32(p[:4], a.host.Epoch()+1)
	binary.LittleEndian.PutUint64(p[4:], a.candSum)
	return []stagedIntent{{actor: "system", kind: wum.EvEpochAdvance, payload: p[:]}}
}

// failSoak rejects the candidate. A non-empty hash pins it as bad so the
// lane only retries when the mount serves different bytes.
func (a *authority) failSoak(hash string, err error) {
	if a.cand != nil {
		a.cand.close()
		a.cand = nil
	}
	if hash != "" {
		a.badModule = hash
	}
	log.Printf("park %s: module soak aborted (%s): %v", a.name, a.candHash, err)
	mEpochSwaps.WithLabelValues("soak_abort").Inc()
}

// promote commits the swap right after the boundary tick: the candidate
// re-restores the authoritative boundary state (its soak state was a
// validation instrument, not a lineage), the boundary snapshot goes
// durable under the NEW module's hash — the anchor journal replay will
// restore from — and only then does the candidate become the host.
func (a *authority) promote(t uint64) {
	state := a.host.Snapshot()
	if err := a.cand.RestoreE(state); err != nil {
		a.failSoak(a.candHash, fmt.Errorf("boundary restore: %w", err))
		return
	}
	wh, err := a.cand.HashE()
	if err != nil {
		a.failSoak(a.candHash, fmt.Errorf("boundary hash: %w", err))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = a.j.PutSnapshot(ctx, a.id, journal.Snapshot{
		Seq: a.lastSeq, Tick: t, Epoch: a.host.Epoch(), WH: wh,
		TerrainID: a.host.TerrainID(), State: state,
	})
	cancel()
	if err != nil {
		// The epoch advanced in state and journal but the module stays — a
		// loud no-op. The lane retries when the mount serves new bytes.
		a.failSoak(a.candHash, fmt.Errorf("boundary snapshot: %w", err))
		return
	}
	mSnapshots.Inc()
	old := a.host
	a.host, a.cand = a.cand, nil
	old.close()
	prev := a.moduleHash
	a.moduleHash = a.candHash
	a.mu.Lock()
	// The ring entry for the boundary tick and any cached snapshot line
	// were computed by the old module; re-anchor both.
	a.ring[t%uint64(len(a.ring))] = ringEntry{tick: t, wh: wh}
	a.snapCache = snapCacheEntry{}
	a.mu.Unlock()
	a.eventsSinceSnap = 0
	a.lastSnapAt = a.tm.now()
	log.Printf("park %s: epoch %d — module %s live (was %s), wh %016x", a.name, a.host.Epoch(), a.moduleHash, prev, wh)
	mEpochSwaps.WithLabelValues("committed").Inc()
}

func parkIDFor(name string) int64 {
	f := fnv.New64a()
	// The generation tag versions every park's journal lineage at once: a
	// park id under a new tag has an empty journal, so worlds born under
	// an incompatible sim never replay through the current one.
	f.Write([]byte("g2/" + name))
	return int64(f.Sum64())
}

// setTerrain loads a blob into the host and caches its identity for the
// welcome/snapshot frames (and the bytes for soak candidates).
func (a *authority) setTerrain(blob []byte) error {
	if _, _, _, err := terrainHeaderFields(blob); err != nil {
		return err
	}
	if err := a.host.SetTerrain(blob); err != nil {
		return err
	}
	a.terrain = terrainID(blob)
	a.terrainBlob = blob
	return nil
}

// openAuthority restores the park from its journal (genesis-snapshotting a
// brand new one on the genesis terrain), repays the gap between the
// replayed tick and the wall-clock schedule while the doors are still
// closed, and hands back an authority ready for run(). A park whose
// snapshot does not replay to its recorded world hash is refused, never
// served wrong.
func openAuthority(ctx context.Context, name string, module []byte, genesisTerrain []byte, j journal.Journal, mods *modules, tm timing) (*authority, error) {
	if tm.hz == 0 {
		tm.hz = 24
	}
	if tm.hz < minTickHz || tm.hz > maxTickHz {
		return nil, fmt.Errorf("tick rate %dHz outside supported range %d..%d", tm.hz, minTickHz, maxTickHz)
	}
	if tm.now == nil {
		tm.now = time.Now
	}
	host, err := newParkHost(module)
	if err != nil {
		return nil, err
	}
	a := &authority{
		name: name, id: parkIDFor(name), host: host, j: j, mods: mods,
		tm:         tm,
		moduleHash: displayHash(module),
		subs:       map[*session]bool{},
		seen:       map[string]struct{}{},
		attach:     make(chan attachReq),
		rate:       make(chan rateChangeReq),
		stop:       make(chan struct{}),
		lastSnapAt: tm.now(),
		// utcDay 0: the first tick after every open stages a day_reset, so
		// a park that slept across midnight wakes with the right day.
	}
	fail := func(err error) (*authority, error) {
		host.close()
		return nil, fmt.Errorf("park %s: %w", name, err)
	}
	snap, ok, err := j.LatestSnapshot(ctx, a.id)
	if err != nil {
		return fail(err)
	}
	if ok {
		blob, found, err := j.TerrainBlob(ctx, snap.TerrainID)
		if err != nil {
			return fail(err)
		}
		if !found {
			return fail(fmt.Errorf("terrain %016x missing — snapshot cannot restore", snap.TerrainID))
		}
		if err := a.setTerrain(blob); err != nil {
			return fail(err)
		}
		if err := host.Restore(snap.State); err != nil {
			return fail(err)
		}
		if got := host.Hash(); got != snap.WH {
			return fail(fmt.Errorf("snapshot hash mismatch (stored %016x, got %016x) — refusing to serve", snap.WH, got))
		}
		a.lastSeq = snap.Seq
		// Buffer the tail before replaying: the tail is bounded by the
		// snapshot cadence, and terrain_set replay must fetch blobs — a
		// query inside Read's row streaming would hold two pool
		// connections at once and can deadlock a small pool.
		var tail []journal.Event
		if err := j.Read(ctx, a.id, snap.Seq+1, func(ev journal.Event) error {
			tail = append(tail, ev)
			return nil
		}); err != nil {
			return fail(err)
		}
		for _, ev := range tail {
			// Step to the event's tick under the terrain that was live for
			// those ticks; only then may a terrain_set swap the blob — the
			// same choreography the live run and every client follow.
			for host.Tick() < ev.Tick {
				host.Step()
			}
			if ev.Kind == wum.EvTerrainSet && len(ev.Payload) == 12 {
				tid := binary.LittleEndian.Uint64(ev.Payload[4:12])
				blob, found, err := j.TerrainBlob(ctx, tid)
				if err != nil {
					return fail(err)
				}
				if !found {
					return fail(fmt.Errorf("terrain %016x missing at seq %d", tid, ev.Seq))
				}
				if err := a.setTerrain(blob); err != nil {
					return fail(err)
				}
			}
			actor, payload := wum.EventActor(ev.Kind, ev.Actor, ev.Payload)
			if code := host.Apply(simEvent(ev.Kind, actor, payload)); code != 0 {
				return fail(fmt.Errorf("replay: event seq %d rejected with code %d", ev.Seq, code))
			}
			a.lastSeq = ev.Seq
		}
	} else {
		// Genesis: the terrain artifact becomes durable first, then the
		// snapshot that references it — events alone cannot recreate a
		// park, and a snapshot must never point at an unstored blob.
		schema, _, _, err := terrainHeaderFields(genesisTerrain)
		if err != nil {
			return fail(err)
		}
		tid := terrainID(genesisTerrain)
		if err := j.PutTerrain(ctx, tid, schema, genesisTerrain); err != nil {
			return fail(err)
		}
		if err := a.setTerrain(genesisTerrain); err != nil {
			return fail(err)
		}
		var b [8]byte
		rand.Read(b[:])
		if err := host.Init(binary.LittleEndian.Uint64(b[:]), a.id, 1); err != nil {
			return fail(err)
		}
		if err := j.PutSnapshot(ctx, a.id, journal.Snapshot{
			Seq: 0, Tick: 0, Epoch: 1, WH: host.Hash(), TerrainID: tid, State: host.Snapshot(),
		}); err != nil {
			return fail(err)
		}
	}
	// The schedule is world state: derive it from the restored rate
	// segment, then repay the gap between the stored tick and the wall
	// clock before the doors open.
	a.refreshSchedule(true)
	if target := a.targetTick(tm.now()); target > host.Tick() {
		if time.Duration(target-host.Tick())*a.tickDur <= darkStepWindow {
			// The world lives through a short restart: step it, filling
			// the ring so rejoining clients keep their fast-forward lane.
			for host.Tick() < target {
				host.Step()
				t := host.Tick()
				a.ring[t%uint64(len(a.ring))] = ringEntry{tick: t, wh: host.Hash()}
			}
			a.ringHead = host.Tick()
		} else if err := a.clockSkip(ctx, target); err != nil {
			return fail(err)
		}
	}
	// Still dark: converge the world's rate toward the deployment's
	// desired rate, so a rate change is a reopen and no live session
	// ever straddles two rates.
	if tm.hz != a.hz {
		if err := a.rateChange(ctx, tm.hz); err != nil {
			return fail(err)
		}
	}
	// The caller starts run(); tests drive tickOnce directly instead.
	return a, nil
}

// rateChange journals the desired tick rate as a rate_set at the current
// tick. The sim re-anchors the piecewise mapping; the boundary snapshot
// re-floors replay under the new segment.
func (a *authority) rateChange(ctx context.Context, hz int) error {
	tick, epoch := a.host.Tick(), a.host.Epoch()
	var p [4]byte
	binary.LittleEndian.PutUint32(p[:], uint32(hz))
	if code := a.host.Apply(simEvent(wum.EvRateSet, 0, p[:])); code != 0 {
		// A module from before rates existed (mount skew during a
		// deploy): hold the world's rate; the desired rate lands on the
		// first reopen under a rate-capable module.
		log.Printf("park %s: module rejected rate_set %dHz (code %d) — holding %dHz", a.name, hz, code, a.hz)
		return nil
	}
	ev := journal.Event{Tick: tick, Epoch: epoch, Kind: wum.EvRateSet, Actor: "system", Payload: p[:]}
	firstSeq, err := a.j.Append(ctx, a.id, a.lastSeq, []journal.Event{ev})
	if err != nil {
		// State is ahead of the journal: never serve it.
		return fmt.Errorf("rate_set append: %w", err)
	}
	a.lastSeq = firstSeq
	mEventsAppended.Inc()
	mRateChanges.Inc()
	a.refreshSchedule(true)
	t := a.host.Tick()
	a.ring[t%uint64(len(a.ring))] = ringEntry{tick: t, wh: a.host.Hash()}
	a.ringHead = t
	if err := a.j.PutSnapshot(ctx, a.id, journal.Snapshot{
		Seq: a.lastSeq, Tick: t, Epoch: epoch, WH: a.host.Hash(),
		TerrainID: a.host.TerrainID(), State: a.host.Snapshot(),
	}); err != nil {
		log.Printf("park %s: snapshot after rate_set failed: %v", a.name, err)
	} else {
		mSnapshots.Inc()
		a.lastSnapAt = a.tm.now()
	}
	log.Printf("park %s: rate_set %dHz at tick %d", a.name, hz, t)
	return nil
}

// clockSkip journals the repayment of a long gap: one event that jumps sim
// time to target without stepping through it. What a skip means for the
// world is the module's decision — the host only moves bytes.
func (a *authority) clockSkip(ctx context.Context, target uint64) error {
	tick, epoch := a.host.Tick(), a.host.Epoch()
	var p [8]byte
	binary.LittleEndian.PutUint64(p[:], target)
	if code := a.host.Apply(simEvent(wum.EvClockSkip, 0, p[:])); code != 0 {
		// A module from before clock_skip existed (mount skew during a
		// deploy): anchor to this process so the schedule holds drift-free
		// for the instance's life; the real repayment lands on the first
		// reopen under a skip-capable module.
		a.anchorTick = tick
		a.anchor = a.tm.now().Add(-a.phase)
		log.Printf("park %s: module rejected clock_skip (code %d) — process-anchored until the module lane converges", a.name, code)
		return nil
	}
	ev := journal.Event{Tick: tick, Epoch: epoch, Kind: wum.EvClockSkip, Actor: "system", Payload: p[:]}
	firstSeq, err := a.j.Append(ctx, a.id, a.lastSeq, []journal.Event{ev})
	if err != nil {
		// State is ahead of the journal: never serve it.
		return fmt.Errorf("clock_skip append: %w", err)
	}
	a.lastSeq = firstSeq
	mEventsAppended.Inc()
	mClockSkips.Inc()
	t := a.host.Tick()
	a.ring[t%uint64(len(a.ring))] = ringEntry{tick: t, wh: a.host.Hash()}
	a.ringHead = t
	// A fresh floor so future replays restore past the gap instead of
	// re-crossing it. Failure is not fatal: replay re-applies the skip.
	if err := a.j.PutSnapshot(ctx, a.id, journal.Snapshot{
		Seq: a.lastSeq, Tick: t, Epoch: epoch, WH: a.host.Hash(),
		TerrainID: a.host.TerrainID(), State: a.host.Snapshot(),
	}); err != nil {
		log.Printf("park %s: snapshot after clock_skip failed: %v", a.name, err)
	} else {
		mSnapshots.Inc()
		a.lastSnapAt = a.tm.now()
	}
	log.Printf("park %s: clock_skip %d -> %d (%s of downtime repaid)",
		a.name, tick, t, time.Duration(t-tick)*a.tickDur)
	return nil
}

// simEvent encodes an event for sim_apply: the module's own ABI (kind u16
// then payload), which is not the wire's event frame.
// simEvent is the SimEvent encoding the module's apply receives:
// kind u16 | actor u64 | payload — the trailing bytes of the wire
// EventRecord, so a staged intent's bytes are what the sim validates.
func simEvent(kind uint16, actor uint64, payload []byte) []byte {
	out := make([]byte, 10+len(payload))
	binary.LittleEndian.PutUint16(out, kind)
	binary.LittleEndian.PutUint64(out[2:], actor)
	copy(out[10:], payload)
	return out
}

func elapsed(from, to time.Time) time.Duration {
	if from.IsZero() || to.Before(from) {
		return 0
	}
	return to.Sub(from)
}

// recordIntent closes one server-observed action lifecycle. The span's
// duration is receipt -> durable fan-out/reject; tick_queue_ms isolates the
// only component a higher tick rate is expected to shrink. Intent/actor ids
// never become attributes: kind, result, park and rate are bounded analysis
// dimensions, while per-player identifiers would be both unnecessary and
// high-cardinality.
func (a *authority) recordIntent(in stagedIntent, result string, reject uint32, finished time.Time) {
	if in.sess == nil || in.intentID == 0 || in.receivedAt.IsZero() {
		return
	}
	kind := wum.ActionName(in.kind)
	rateHz := in.rateHz
	if rateHz == 0 {
		rateHz = a.hz
	}
	queue := elapsed(in.receivedAt, in.dequeuedAt)
	total := elapsed(in.receivedAt, finished)
	mIntentQueueDur.WithLabelValues(kind).Observe(queue.Seconds())
	mIntentAuthorityDur.WithLabelValues(kind, result).Observe(total.Seconds())

	attrs := []attribute.KeyValue{
		attribute.String("wum.kind", kind),
		attribute.String("wum.result", result),
		attribute.String("wum.park", a.name),
		attribute.Int("wum.rate_hz", rateHz),
		attribute.Float64("wum.tick_queue_ms", float64(queue.Microseconds())/1000),
		attribute.Float64("wum.authority_ms", float64(total.Microseconds())/1000),
	}
	if reject != 0 {
		attrs = append(attrs, attribute.String("wum.reject_reason", wum.RejectReasonName(reject)))
	}
	_, span := otel.Tracer("guardian/chunkies/authority").Start(
		context.Background(),
		"mythra.intent",
		trace.WithTimestamp(in.receivedAt),
		trace.WithAttributes(attrs...),
	)
	span.End(trace.WithTimestamp(finished))
}

// stageIntent queues a session intent for the next tick boundary. Intents
// are idempotent by (actor, intent_id): a resend after rejoin is dropped
// here, and the original event (already fanned out) is the acknowledgment.
func (a *authority) stageIntent(s *session, intentID uint64, kind uint16, payload []byte) {
	receivedAt := a.tm.now()
	if payload == nil {
		// v5 payloads are often empty (the actor rides the envelope); the
		// journal column is NOT NULL and a nil slice is not an encoding.
		payload = []byte{}
	}
	a.mu.Lock()
	// Intent id 0 marks connection-lifecycle intents (the departure staged
	// on disconnect): every session of a sub uses the same id there, so
	// idempotency bookkeeping would swallow every departure after the first.
	if intentID != 0 {
		key := fmt.Sprintf("%s/%d", s.sub, intentID)
		if _, dup := a.seen[key]; dup {
			a.mu.Unlock()
			mIntentsDeduped.Inc()
			return
		}
		a.seen[key] = struct{}{}
		a.seenFifo = append(a.seenFifo, key)
		if len(a.seenFifo) > dedupWindow {
			delete(a.seen, a.seenFifo[0])
			a.seenFifo = a.seenFifo[1:]
		}
	}
	a.staged = append(a.staged, stagedIntent{
		sess: s, actor: s.sub, actorID: s.dogID, intentID: intentID, kind: kind,
		payload: payload, receivedAt: receivedAt,
	})
	a.mu.Unlock()
}

func (a *authority) stageSystem(kind uint16, payload []byte) {
	a.mu.Lock()
	a.staged = append(a.staged, stagedIntent{actor: "system", kind: kind, payload: payload})
	a.mu.Unlock()
}

// requestRate journals a live rate boundary on the authority goroutine and
// returns only after durable fan-out. It is wired only by the local-dev HTTP
// control; production rate policy remains Git/deployment state.
func (a *authority) requestRate(ctx context.Context, hz int) error {
	done := make(chan error, 1)
	select {
	case a.rate <- rateChangeReq{hz: hz, done: done}:
	case <-ctx.Done():
		return ctx.Err()
	case <-a.stop:
		return errors.New("authority closed")
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-a.stop:
		return errors.New("authority closed")
	}
}

func (a *authority) stageRateChange(req rateChangeReq) error {
	if req.hz < minTickHz || req.hz > maxTickHz {
		return fmt.Errorf("tick rate %dHz outside supported range %d..%d", req.hz, minTickHz, maxTickHz)
	}
	if req.hz == a.hz {
		// The development drill restores its durable starting rate before
		// every run. Treat an already-converged request as successful so the
		// control is repeatable on a fresh authority as well as a used one.
		req.done <- nil
		return nil
	}
	var payload [4]byte
	binary.LittleEndian.PutUint32(payload[:], uint32(req.hz))
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, in := range a.staged {
		if in.kind == wum.EvRateSet {
			return errors.New("another tick-rate change is pending")
		}
	}
	a.staged = append(a.staged, stagedIntent{
		actor: "system", kind: wum.EvRateSet, payload: payload[:], done: req.done,
	})
	return nil
}

func (a *authority) detach(s *session) {
	a.mu.Lock()
	delete(a.subs, s)
	a.mu.Unlock()
}

// verdictFor answers a client hash check from the ring. ok=nil means the
// tick has left the ring (unknown — a client strike).
func (a *authority) verdictFor(tick, wh uint64) (ok *bool, now uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.ring[tick%uint64(len(a.ring))]
	if e.tick != tick {
		return nil, a.ringHead
	}
	v := e.wh == wh
	return &v, a.ringHead
}

// refreshSchedule derives the tick schedule from the sim's rate segment —
// state, not config, so every pod generation (and rollback) computes the
// identical mapping. Live changes preserve and rehash the existing
// verification ring while resizing it for the new wall-time depth. They
// also preserve the park's sub-tick phase so the boundary itself does not
// move; dark boot/convergence may derive the phase for its resulting rate.
func (a *authority) refreshSchedule(rephase bool) {
	a.hz = a.host.Rate()
	a.tickDur = time.Second / time.Duration(a.hz)
	if rephase {
		a.phase = time.Duration(uint64(a.id) % uint64(a.tickDur))
	}
	a.anchorTick = a.host.AnchorTick()
	a.anchor = wallEpoch.Add(time.Duration(a.host.AnchorNs()))
	if need := ringSeconds * a.hz; len(a.ring) != need {
		a.mu.Lock()
		next := make([]ringEntry, need)
		for _, e := range a.ring {
			if e.tick != 0 || e.wh != 0 {
				at := e.tick % uint64(need)
				if (next[at].tick == 0 && next[at].wh == 0) || e.tick > next[at].tick {
					next[at] = e
				}
			}
		}
		a.ring = next
		a.mu.Unlock()
	}
}

// tickInstant is the wall-clock moment tick t is scheduled to run. Divide
// cumulative ticks, rather than multiplying a truncated tick duration, so
// rates that do not divide one second never accumulate scheduler drift.
func (a *authority) tickInstant(t uint64) time.Time {
	ticks := t - a.anchorTick
	seconds := ticks / uint64(a.hz)
	remainder := ticks % uint64(a.hz)
	offset := time.Duration(seconds)*time.Second +
		time.Duration(remainder*uint64(time.Second)/uint64(a.hz))
	return a.anchor.Add(a.phase + offset)
}

// targetTick is the tick whose instant most recently passed: the tick the
// park should be on right now.
func (a *authority) targetTick(now time.Time) uint64 {
	d := now.Sub(a.anchor) - a.phase
	if d < 0 {
		return a.anchorTick
	}
	seconds := uint64(d / time.Second)
	remainder := uint64(d % time.Second)
	return a.anchorTick + seconds*uint64(a.hz) +
		remainder*uint64(a.hz)/uint64(time.Second)
}

// run is the authority loop: the single goroutine touching the parkHost.
// Ticks are scheduled against the wall clock, never free-counted: each
// wakeup runs every tick whose instant has passed (bounded per wakeup),
// then sleeps until the next tick's instant. A short hitch repays itself;
// the sim never runs ahead of its schedule — if the wall clock steps
// backward, the park idles until reality catches up.
func (a *authority) run() {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-a.stop:
			if a.cand != nil {
				a.cand.close()
			}
			a.host.close()
			return
		case req := <-a.attach:
			req.done <- a.handleAttach(req)
		case req := <-a.rate:
			if err := a.stageRateChange(req); err != nil {
				req.done <- err
			}
		case <-timer.C:
			now := a.tm.now()
			target := a.targetTick(now)
			if cur := a.host.Tick(); target > cur+uint64(len(a.ring)) {
				// A live gap past the hash ring (process pause, clock step
				// forward): every client is beyond fast-forward range
				// anyway, so repay the gap where gaps are repaid — the
				// dark reopen path.
				log.Printf("park %s: %d ticks behind schedule — closing to repay the gap dark", a.name, target-cur)
				a.close()
				continue
			}
			for n := 0; n < catchupBurst && a.host.Tick() < target; n++ {
				a.tickOnce()
			}
			cur := a.host.Tick()
			mTickLag.WithLabelValues(a.name).Set(now.Sub(a.tickInstant(cur)).Seconds())
			wait := a.tickInstant(cur + 1).Sub(a.tm.now())
			if wait < 0 {
				wait = 0
			}
			timer.Reset(wait)
		}
	}
}

func (a *authority) tickOnce() {
	start := time.Now()
	// Wall clock enters the sim only as a journal event: the day index
	// becomes day_reset here and is never read inside the module.
	if day := uint32(a.tm.now().Unix() / 86400); day != a.utcDay {
		a.utcDay = day
		var p [4]byte
		binary.LittleEndian.PutUint32(p[:], day)
		a.stageSystem(wum.EvDayReset, p[:])
	}

	prelude := a.swapPrelude()
	committing := len(prelude) > 0

	a.mu.Lock()
	staged := append(prelude, a.staged...)
	a.staged = nil
	a.mu.Unlock()
	dequeuedAt := a.tm.now()
	for i := range staged {
		if !staged[i].receivedAt.IsZero() {
			staged[i].dequeuedAt = dequeuedAt
			staged[i].rateHz = a.hz
		}
	}

	var accepted []journal.Event
	var acceptedIntents []stagedIntent
	tick := a.host.Tick()
	epoch := a.host.Epoch()
	for _, in := range staged {
		code := a.host.Apply(simEvent(in.kind, in.actorID, in.payload))
		if code != 0 {
			if in.sess != nil {
				in.sess.sendReject(in.intentID, code)
				// A rejected intent produced no journal event, so it must
				// not occupy the idempotency window: a corrected resend
				// under the same id has to reach the sim.
				if in.intentID != 0 {
					a.mu.Lock()
					delete(a.seen, fmt.Sprintf("%s/%d", in.actor, in.intentID))
					a.mu.Unlock()
				}
			}
			log.Printf("park %s: intent rejected: actor=%s kind=%d intent=%d reason=%s(%d)",
				a.name, in.actor, in.kind, in.intentID, wum.RejectReasonName(code), code)
			mIntentsRejected.WithLabelValues(wum.RejectReasonName(code)).Inc()
			a.recordIntent(in, "rejected", code, a.tm.now())
			if in.done != nil {
				in.done <- fmt.Errorf("rate_set rejected: %s (%d)", wum.RejectReasonName(code), code)
			}
			continue
		}
		// Feed the accepted event to the soaking candidate: a module that
		// cannot replay the live journal must never be promoted.
		if a.cand != nil {
			ccode, cerr := a.cand.ApplyE(simEvent(in.kind, in.actorID, in.payload))
			if cerr != nil {
				a.failSoak(a.candHash, fmt.Errorf("apply trap: %w", cerr))
			} else if ccode != 0 {
				a.failSoak(a.candHash, fmt.Errorf("rejected kind %d (code %d) the live module accepted", in.kind, ccode))
			}
		}
		accepted = append(accepted, journal.Event{
			Tick: tick, Epoch: epoch, Kind: in.kind,
			Actor: in.actor, IntentID: in.intentID, Payload: in.payload,
		})
		acceptedIntents = append(acceptedIntents, in)
	}

	// Durable-before-visible: the batch commits before any session sees an
	// event. On journal failure the authority's state is ahead of the
	// truth, so the only safe move is to stop serving; sessions redial and
	// the park reopens from the journal (roll forward, never serve
	// divergence).
	if len(accepted) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		commitStart := time.Now()
		firstSeq, err := a.j.Append(ctx, a.id, a.lastSeq, accepted)
		mAppendDur.Observe(time.Since(commitStart).Seconds())
		cancel()
		if err != nil {
			mAppendErrors.Inc()
			finished := a.tm.now()
			for _, in := range acceptedIntents {
				a.recordIntent(in, "append_error", 0, finished)
				if in.done != nil {
					in.done <- fmt.Errorf("rate_set append: %w", err)
				}
			}
			log.Printf("park %s: journal append failed (%v) — closing for a journal-clean reopen", a.name, err)
			a.close()
			return
		}
		mEventsAppended.Add(float64(len(accepted)))
		// One batch per tick: the run is the accepted intents' record
		// bytes, seqs dense from the journal's first — the same bytes a
		// client sent are the bytes every client receives.
		var run []byte
		for i := range accepted {
			accepted[i].Seq = firstSeq + int64(i)
			run = codec.AppendEventRecord(run, accepted[i].IntentID, accepted[i].Kind,
				acceptedIntents[i].actorID, accepted[i].Payload)
		}
		f := codec.EncodeTick(tick, firstSeq, uint16(len(accepted)), run)
		a.mu.Lock()
		for s := range a.subs {
			s.send(f)
		}
		a.lastSeq = accepted[len(accepted)-1].Seq
		a.mu.Unlock()
		for _, in := range acceptedIntents {
			if in.kind != wum.EvRateSet || len(in.payload) != 4 {
				continue
			}
			oldHz := a.hz
			a.refreshSchedule(false)
			a.mu.Lock()
			a.snapCache = snapCacheEntry{}
			a.mu.Unlock()
			mRateChanges.Inc()
			log.Printf("park %s: live rate_set %dHz -> %dHz at tick %d",
				a.name, oldHz, a.hz, tick)
		}
		finished := a.tm.now()
		for _, in := range acceptedIntents {
			a.recordIntent(in, "accepted", 0, finished)
			if in.done != nil {
				in.done <- nil
			}
		}
		a.eventsSinceSnap += len(accepted)
	}

	a.host.Step()
	if a.cand != nil {
		if err := a.cand.StepE(); err != nil {
			a.failSoak(a.candHash, fmt.Errorf("step trap: %w", err))
		}
	}
	t := a.host.Tick()
	wh := a.host.Hash()
	a.mu.Lock()
	a.ring[t%uint64(len(a.ring))] = ringEntry{tick: t, wh: wh}
	a.ringHead = t
	a.mu.Unlock()

	if committing && a.cand != nil {
		a.promote(t)
	}

	if a.eventsSinceSnap >= snapshotEvery || (a.eventsSinceSnap > 0 && a.tm.now().Sub(a.lastSnapAt) > snapshotMaxAge) {
		a.eventsSinceSnap = 0
		a.lastSnapAt = a.tm.now()
		snap := journal.Snapshot{
			Seq: a.lastSeq, Tick: t, Epoch: a.host.Epoch(), WH: wh,
			TerrainID: a.host.TerrainID(), State: a.host.Snapshot(),
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := a.j.PutSnapshot(ctx, a.id, snap); err != nil {
				log.Printf("park %s: snapshot write failed: %v", a.name, err)
			} else {
				mSnapshots.Inc()
			}
		}()
	}
	mTickDur.Observe(time.Since(start).Seconds())
}

// handleAttach registers the subscription atomically with the live stream
// position (src/chunkies/README.md: one machine, three entrances) and captures
// the raw snapshot state; everything expensive happens later in
// catchupLines on the session goroutine. A mid-session resync stays
// in-stream — the snapshot must land between the events around it, so it
// is queued here — but its deflate pass is cached per tick, bounding a
// resync storm to one compression per tick.
func (a *authority) handleAttach(req attachReq) attachResult {
	s := req.sess
	if req.resync {
		s.send(a.cachedSnapshotFrame())
		mCatchup.WithLabelValues("resync").Inc()
		return attachResult{}
	}
	// The terrain id is the only terrain fact on the wire: dimensions and
	// schema live in the content-addressed blob every consumer fetches.
	res := attachResult{
		// Lineage and generation are the netcode rewrite's stream
		// identity; the Postgres-journal era has exactly one history per
		// park, so both ride as zero until the fencing lane mints them.
		welcome: codec.EncodeWelcome(codec.Welcome{
			Lineage: 0, Generation: 0, Sub: 0,
			Epoch: a.host.Epoch(), Seq: a.lastSeq, Tick: a.host.Tick(),
			Hz: uint32(a.hz), Role: codec.RoleCode(s.role), Content: a.terrain,
			Chunk: a.name,
		}),
		seq:     a.lastSeq,
		tick:    a.host.Tick(),
		epoch:   a.host.Epoch(),
		wh:      a.host.Hash(),
		terrain: a.terrain,
		state:   a.host.Snapshot(),
	}

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return attachResult{err: errors.New("authority closed")}
	}
	a.subs[s] = true
	a.mu.Unlock()
	return res
}

var errPastAttach = errors.New("past attach position")

// catchupFrames builds the catch-up material on the caller's goroutine.
// Divergence resyncs and fresh joins get the snapshot; rejoins get
// min-cost with the ring-depth compute bound. Live events queued after the
// attach position follow on the session channel, so the journal read stops
// at the attach seq — anything newer is already on its way.
func (a *authority) catchupFrames(sinceSeq int64, sinceTick uint64, res attachResult) [][]byte {
	snap := snapshotFrameFor(res.seq, res.tick, res.epoch, res.wh, res.terrain, res.state)
	if sinceSeq > 0 && sinceSeq <= res.seq && sinceTick+uint64(len(a.ring)) >= res.tick {
		var events [][]byte
		eventBytes := 0
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := a.j.Read(ctx, a.id, sinceSeq+1, func(ev journal.Event) error {
			if ev.Seq > res.seq {
				return errPastAttach
			}
			f := eventFrameFor(&ev)
			events = append(events, f)
			eventBytes += len(f)
			return nil
		})
		cancel()
		if errors.Is(err, errPastAttach) {
			err = nil
		}
		if err == nil && eventBytes < len(snap) {
			mCatchup.WithLabelValues("events").Inc()
			return events
		}
	}
	mCatchup.WithLabelValues("snapshot").Inc()
	return [][]byte{snap}
}

// cachedSnapshotFrame serves the resync path from the tick goroutine,
// cached per tick so concurrent resyncs share one deflate pass.
func (a *authority) cachedSnapshotFrame() []byte {
	t := a.host.Tick()
	a.mu.Lock()
	cached := a.snapCache
	a.mu.Unlock()
	if cached.tick == t && cached.frame != nil {
		return cached.frame
	}
	f := snapshotFrameFor(a.lastSeq, t, a.host.Epoch(), a.host.Hash(), a.terrain, a.host.Snapshot())
	a.mu.Lock()
	a.snapCache = snapCacheEntry{tick: t, frame: f}
	a.mu.Unlock()
	return f
}

func snapshotFrameFor(seq int64, tick uint64, epoch uint32, wh, terrain uint64, state []byte) []byte {
	var z bytes.Buffer
	w, _ := flate.NewWriter(&z, flate.BestCompression)
	w.Write(state)
	w.Close()
	return codec.EncodeSnapshot(codec.Snapshot{
		Lineage: 0, Seq: seq, Tick: tick, Epoch: epoch, WH: wh,
		Content: terrain, Z: z.Bytes(),
	})
}

// eventFrameFor rebuilds one journal row as a single-record tick batch
// for catch-up, resolving the payload era on the way out.
func eventFrameFor(ev *journal.Event) []byte {
	actor, payload := wum.EventActor(ev.Kind, ev.Actor, ev.Payload)
	run := codec.AppendEventRecord(nil, ev.IntentID, ev.Kind, actor, payload)
	return codec.EncodeTick(ev.Tick, ev.Seq, 1, run)
}

func (a *authority) close() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	subs := make([]*session, 0, len(a.subs))
	for s := range a.subs {
		subs = append(subs, s)
	}
	a.subs = map[*session]bool{}
	a.mu.Unlock()
	for _, s := range subs {
		s.closeSession("park reopening")
	}
	close(a.stop)
}

func (a *authority) isClosed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closed
}

// parks is the registry of open authorities on this hub.
type parks struct {
	mu      sync.Mutex
	byName  map[string]*authority
	module  func() []byte
	genesis []byte // terrain artifact every brand-new park is born with
	j       journal.Journal
	mods    *modules
	tm      timing
}

func newParks(module func() []byte, genesis []byte, j journal.Journal, mods *modules, tm timing) *parks {
	return &parks{byName: map[string]*authority{}, module: module, genesis: genesis, j: j, mods: mods, tm: tm}
}

func (p *parks) get(ctx context.Context, name string) (*authority, error) {
	p.mu.Lock()
	if a, ok := p.byName[name]; ok && !a.isClosed() {
		p.mu.Unlock()
		return a, nil
	}
	p.mu.Unlock()
	// Open outside the registry lock (journal replay takes a moment); the
	// rare double-open race resolves to the first registered instance.
	a, err := openAuthority(ctx, name, p.module(), p.genesis, p.j, p.mods, p.tm)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if cur, ok := p.byName[name]; ok && !cur.isClosed() {
		a.close()
		a.host.close() // its run loop never started
		return cur, nil
	}
	p.byName[name] = a
	go a.run()
	mParks.Set(float64(len(p.byName)))
	return a, nil
}

// current returns an already-running authority. The local rate drill uses
// this instead of get: its subject is an existing connected session, so a
// control request must not quietly create a new park and call that success.
func (p *parks) current(name string) (*authority, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.byName[name]
	return a, ok && !a.isClosed()
}
