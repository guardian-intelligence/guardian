package main

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/guardian-intelligence/guardian/src/services/mythrad/journal"
)

// hashRingTicks bounds how stale a client check (and a rejoin's fast-
// forward) may be: ~30s at 24Hz. Older checks answer "unknown", which the
// client counts as a strike; rejoins further behind get a snapshot (the
// compute bound from docs/netcode.md).
const (
	tickHz         = 24
	hashRingTicks  = 30 * tickHz
	snapshotEvery  = 512 // events between durable snapshots
	snapshotMaxAge = 15 * time.Minute
	dedupWindow    = 4096 // remembered (actor, intent_id) pairs per park
)

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
	h.fInit, h.fRestore, h.fSnapshot = get("sim_init"), get("sim_restore"), get("sim_snapshot")
	h.fStep, h.fApply, h.fHash = get("sim_step"), get("sim_apply"), get("sim_hash")
	h.fTick, h.fEpoch = get("sim_tick"), get("sim_epoch")
	h.fSetTerrain, h.fTerrainID = get("sim_set_terrain"), get("sim_terrain_id")
	ioBuf, ioCap := get("io_buf"), get("io_cap")
	terrainBuf, terrainCap := get("terrain_buf"), get("terrain_cap")
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
	sess     *session
	actor    string
	intentID uint64
	kind     uint16
	payload  []byte
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
// (catchupLines), so the tick loop never blocks on a client's history.
type attachResult struct {
	welcome []byte
	seq     int64
	tick    uint64
	epoch   uint32
	wh      uint64
	terrain string
	state   []byte
	err     error
}

type snapCacheEntry struct {
	tick uint64
	line []byte
}

type ringEntry struct {
	tick uint64
	wh   uint64
}

// modules exposes the current distribution hashes riding every verdict:
// the client presentation module and the park module itself.
type modules struct {
	client *clientModule
	park   *clientModule
}

// authority is one park's sim authority: journal writer, validator, and
// fan-out hub (docs/netcode.md). run() is the single owner of the host.
type authority struct {
	name string
	id   int64
	host *parkHost
	j    journal.Journal
	mods *modules

	// The active terrain artifact, cached for welcome/snapshot lines so
	// clients know which blob to fetch before restoring, and as raw bytes
	// for candidate modules under soak. Written only by the boot path and
	// the tick goroutine's replay.
	terrainHex  string
	terrainBlob []byte

	// The module-update lane (docs/netcode.md, module epochs): when the
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
	ring     [hashRingTicks]ringEntry
	ringHead uint64
	lastSeq  int64
	closed   bool
	seen     map[string]struct{}
	seenFifo []string

	attach chan attachReq
	stop   chan struct{}

	snapCache       snapCacheEntry
	eventsSinceSnap int
	lastSnapAt      time.Time
	utcDay          uint32
}

const soakTicks = 120 // ~5s of dark lockstep before an epoch swap commits

func displayHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:4])
}

// swapPrelude advances the module-update lane at the top of a tick. When a
// candidate's soak has just completed it returns the epoch_advance intent
// that must lead this tick's batch — the journaled boundary between the
// old module's ticks and the new one's.
func (a *authority) swapPrelude() []stagedIntent {
	bytes, hash := a.mods.park.get()
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
		a.cand, a.candHash, a.soakLeft = cand, hash, soakTicks
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
		log.Printf("park %s: module %s soaking for %d ticks (live %s)", a.name, hash, soakTicks, a.moduleHash)
		return nil
	}
	if a.soakLeft > 0 {
		a.soakLeft--
		return nil
	}
	var p [12]byte
	binary.LittleEndian.PutUint32(p[:4], a.host.Epoch()+1)
	binary.LittleEndian.PutUint64(p[4:], a.candSum)
	return []stagedIntent{{actor: "system", kind: evEpochAdvance, payload: p[:]}}
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
	a.ring[t%hashRingTicks] = ringEntry{tick: t, wh: wh}
	a.snapCache = snapCacheEntry{}
	a.mu.Unlock()
	a.eventsSinceSnap = 0
	a.lastSnapAt = time.Now()
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
// welcome/snapshot lines (and the bytes for soak candidates).
func (a *authority) setTerrain(blob []byte) error {
	if _, _, _, err := terrainHeaderFields(blob); err != nil {
		return err
	}
	if err := a.host.SetTerrain(blob); err != nil {
		return err
	}
	a.terrainHex = fmt.Sprintf("%016x", terrainID(blob))
	a.terrainBlob = blob
	return nil
}

// openAuthority restores the park from its journal (genesis-snapshotting a
// brand new one on the genesis terrain) and starts its loop. A park whose
// snapshot does not replay to its recorded world hash is refused, never
// served wrong.
func openAuthority(ctx context.Context, name string, module []byte, genesisTerrain []byte, j journal.Journal, mods *modules) (*authority, error) {
	host, err := newParkHost(module)
	if err != nil {
		return nil, err
	}
	a := &authority{
		name: name, id: parkIDFor(name), host: host, j: j, mods: mods,
		moduleHash: displayHash(module),
		subs:       map[*session]bool{},
		seen:       map[string]struct{}{},
		attach:     make(chan attachReq),
		stop:       make(chan struct{}),
		lastSnapAt: time.Now(),
		utcDay:     uint32(time.Now().Unix() / 86400),
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
			if ev.Kind == evTerrainSet && len(ev.Payload) == 12 {
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
			if code := host.Apply(encodeEvent(ev.Kind, ev.Payload)); code != 0 {
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
	// The caller starts run(); tests drive tickOnce directly instead.
	return a, nil
}

func encodeEvent(kind uint16, payload []byte) []byte {
	out := make([]byte, 2+len(payload))
	binary.LittleEndian.PutUint16(out, kind)
	copy(out[2:], payload)
	return out
}

// stageIntent queues a session intent for the next tick boundary. Intents
// are idempotent by (actor, intent_id): a resend after rejoin is dropped
// here, and the original event (already fanned out) is the acknowledgment.
func (a *authority) stageIntent(s *session, intentID uint64, kind uint16, payload []byte) {
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
	a.staged = append(a.staged, stagedIntent{sess: s, actor: s.sub, intentID: intentID, kind: kind, payload: payload})
	a.mu.Unlock()
}

func (a *authority) stageSystem(kind uint16, payload []byte) {
	a.mu.Lock()
	a.staged = append(a.staged, stagedIntent{actor: "system", kind: kind, payload: payload})
	a.mu.Unlock()
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
	e := a.ring[tick%hashRingTicks]
	if e.tick != tick {
		return nil, a.ringHead
	}
	v := e.wh == wh
	return &v, a.ringHead
}

// run is the authority loop: the single goroutine touching the parkHost.
func (a *authority) run() {
	ticker := time.NewTicker(time.Second / tickHz)
	defer ticker.Stop()
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
		case <-ticker.C:
			a.tickOnce()
		}
	}
}

func (a *authority) tickOnce() {
	start := time.Now()
	// Wall clock enters the sim only as a journal event: the day index
	// becomes day_reset here and is never read inside the module.
	if day := uint32(time.Now().Unix() / 86400); day != a.utcDay {
		a.utcDay = day
		var p [4]byte
		binary.LittleEndian.PutUint32(p[:], day)
		a.stageSystem(evDayReset, p[:])
	}

	prelude := a.swapPrelude()
	committing := len(prelude) > 0

	a.mu.Lock()
	staged := append(prelude, a.staged...)
	a.staged = nil
	a.mu.Unlock()

	var accepted []journal.Event
	tick := a.host.Tick()
	epoch := a.host.Epoch()
	for _, in := range staged {
		code := a.host.Apply(encodeEvent(in.kind, in.payload))
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
				a.name, in.actor, in.kind, in.intentID, rejectReasonName(code), code)
			mIntentsRejected.WithLabelValues(rejectReasonName(code)).Inc()
			continue
		}
		// Feed the accepted event to the soaking candidate: a module that
		// cannot replay the live journal must never be promoted.
		if a.cand != nil {
			ccode, cerr := a.cand.ApplyE(encodeEvent(in.kind, in.payload))
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
			log.Printf("park %s: journal append failed (%v) — closing for a journal-clean reopen", a.name, err)
			a.close()
			return
		}
		mEventsAppended.Add(float64(len(accepted)))
		a.mu.Lock()
		for i := range accepted {
			accepted[i].Seq = firstSeq + int64(i)
			line := eventLine(&accepted[i])
			for s := range a.subs {
				s.send(line)
			}
		}
		a.lastSeq = accepted[len(accepted)-1].Seq
		a.mu.Unlock()
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
	a.ring[t%hashRingTicks] = ringEntry{tick: t, wh: wh}
	a.ringHead = t
	a.mu.Unlock()

	if committing && a.cand != nil {
		a.promote(t)
	}

	if a.eventsSinceSnap >= snapshotEvery || (a.eventsSinceSnap > 0 && time.Since(a.lastSnapAt) > snapshotMaxAge) {
		a.eventsSinceSnap = 0
		a.lastSnapAt = time.Now()
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
// position (docs/netcode.md: one machine, three entrances) and captures
// the raw snapshot state; everything expensive happens later in
// catchupLines on the session goroutine. A mid-session resync stays
// in-stream — the snapshot must land between the events around it, so it
// is queued here — but its deflate pass is cached per tick, bounding a
// resync storm to one compression per tick.
func (a *authority) handleAttach(req attachReq) attachResult {
	s := req.sess
	if req.resync {
		s.send(a.snapshotLine())
		mCatchup.WithLabelValues("resync").Inc()
		return attachResult{}
	}
	// The terrain hex is the only terrain fact on the wire: dimensions and
	// schema live in the content-addressed blob every consumer fetches.
	welcome, _ := json.Marshal(map[string]any{
		"type": "welcome", "park": a.name, "role": s.role, "epoch": a.host.Epoch(),
		"seq": a.lastSeq, "tick": a.host.Tick(), "terrain": a.terrainHex,
	})
	res := attachResult{
		welcome: append(welcome, '\n'),
		seq:     a.lastSeq,
		tick:    a.host.Tick(),
		epoch:   a.host.Epoch(),
		wh:      a.host.Hash(),
		terrain: a.terrainHex,
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

// catchupLines builds the catch-up material on the caller's goroutine.
// Divergence resyncs and fresh joins get the snapshot; rejoins get
// min-cost with the ring-depth compute bound. Live events queued after the
// attach position follow on the session channel, so the journal read stops
// at the attach seq — anything newer is already on its way.
func (a *authority) catchupLines(sinceSeq int64, sinceTick uint64, res attachResult) [][]byte {
	snapLine := snapshotLineFrom(res.seq, res.tick, res.epoch, res.wh, res.terrain, res.state)
	if sinceSeq > 0 && sinceSeq <= res.seq && sinceTick+hashRingTicks >= res.tick {
		var events [][]byte
		eventBytes := 0
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := a.j.Read(ctx, a.id, sinceSeq+1, func(ev journal.Event) error {
			if ev.Seq > res.seq {
				return errPastAttach
			}
			line := eventLine(&ev)
			events = append(events, line)
			eventBytes += len(line)
			return nil
		})
		cancel()
		if errors.Is(err, errPastAttach) {
			err = nil
		}
		if err == nil && eventBytes < len(snapLine) {
			mCatchup.WithLabelValues("events").Inc()
			return events
		}
	}
	mCatchup.WithLabelValues("snapshot").Inc()
	return [][]byte{snapLine}
}

// snapshotLine serves the resync path from the tick goroutine, cached per
// tick so concurrent resyncs share one deflate pass.
func (a *authority) snapshotLine() []byte {
	t := a.host.Tick()
	a.mu.Lock()
	cached := a.snapCache
	a.mu.Unlock()
	if cached.tick == t && cached.line != nil {
		return cached.line
	}
	line := snapshotLineFrom(a.lastSeq, t, a.host.Epoch(), a.host.Hash(), a.terrainHex, a.host.Snapshot())
	a.mu.Lock()
	a.snapCache = snapCacheEntry{tick: t, line: line}
	a.mu.Unlock()
	return line
}

func snapshotLineFrom(seq int64, tick uint64, epoch uint32, wh uint64, terrain string, state []byte) []byte {
	var z bytes.Buffer
	w, _ := flate.NewWriter(&z, flate.BestCompression)
	w.Write(state)
	w.Close()
	msg, _ := json.Marshal(map[string]any{
		"type": "snapshot", "seq": seq, "tick": tick,
		"epoch": epoch, "wh": fmt.Sprintf("%016x", wh),
		"terrain": terrain,
		"z":       z.Bytes(), // json encodes []byte as base64
	})
	return append(msg, '\n')
}

func eventLine(ev *journal.Event) []byte {
	msg, _ := json.Marshal(map[string]any{
		"type": "event", "seq": ev.Seq, "tick": ev.Tick, "kind": ev.Kind,
		"actor": ev.Actor, "intent": ev.IntentID, "p": ev.Payload,
	})
	return append(msg, '\n')
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
}

func newParks(module func() []byte, genesis []byte, j journal.Journal, mods *modules) *parks {
	return &parks{byName: map[string]*authority{}, module: module, genesis: genesis, j: j, mods: mods}
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
	a, err := openAuthority(ctx, name, p.module(), p.genesis, p.j, p.mods)
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
