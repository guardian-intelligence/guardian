package main

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/rand"
	"encoding/binary"
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
	rt    wazero.Runtime
	mem   api.Memory
	ioPtr uint32
	ioCap uint32

	fInit, fRestore, fSnapshot, fStep, fApply, fHash, fTick, fEpoch api.Function
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
	ioBuf, ioCap := get("io_buf"), get("io_cap")
	if h.mem == nil || h.fInit == nil || h.fRestore == nil || h.fSnapshot == nil ||
		h.fStep == nil || h.fApply == nil || h.fHash == nil || h.fTick == nil ||
		h.fEpoch == nil || ioBuf == nil || ioCap == nil {
		rt.Close(ctx)
		return nil, errors.New("park module missing sim ABI exports")
	}
	p, err := ioBuf.Call(ctx)
	if err != nil {
		rt.Close(ctx)
		return nil, err
	}
	c, err := ioCap.Call(ctx)
	if err != nil {
		rt.Close(ctx)
		return nil, err
	}
	h.ioPtr, h.ioCap = uint32(p[0]), uint32(c[0])
	return h, nil
}

func (h *parkHost) close() { h.rt.Close(context.Background()) }

func (h *parkHost) call(f api.Function, args ...uint64) uint64 {
	res, err := f.Call(context.Background(), args...)
	if err != nil {
		// The module validates its inputs and never traps on data; a trap
		// is a host bug and this park instance is unusable.
		panic(fmt.Sprintf("park module trapped: %v", err))
	}
	if len(res) == 0 {
		return 0
	}
	return res[0]
}

func (h *parkHost) Init(seed uint64, parkID int64, epoch uint32) {
	h.call(h.fInit, seed, uint64(parkID), uint64(epoch))
}

func (h *parkHost) Restore(state []byte) error {
	if uint32(len(state)) > h.ioCap || !h.mem.Write(h.ioPtr, state) {
		return errors.New("snapshot does not fit module io buffer")
	}
	if code := h.call(h.fRestore, uint64(len(state))); code != 0 {
		return fmt.Errorf("sim_restore rejected snapshot: code %d", code)
	}
	return nil
}

func (h *parkHost) Snapshot() []byte {
	n := uint32(h.call(h.fSnapshot))
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
func (h *parkHost) Epoch() uint32 { return uint32(h.call(h.fEpoch)) }

// Apply runs one encoded event (kind u16 LE + payload) through sim_apply.
// The module must not mutate on a nonzero return (pinned by its tests).
func (h *parkHost) Apply(event []byte) uint32 {
	if uint32(len(event)) > h.ioCap || !h.mem.Write(h.ioPtr, event) {
		return 1 // the sim's ERR_ENCODING
	}
	return uint32(h.call(h.fApply, uint64(len(event))))
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

func parkIDFor(name string) int64 {
	f := fnv.New64a()
	f.Write([]byte(name))
	return int64(f.Sum64())
}

// openAuthority restores the park from its journal (genesis-snapshotting a
// brand new one) and starts its loop. A park whose snapshot does not replay
// to its recorded world hash is refused, never served wrong.
func openAuthority(ctx context.Context, name string, module []byte, j journal.Journal, mods *modules) (*authority, error) {
	host, err := newParkHost(module)
	if err != nil {
		return nil, err
	}
	a := &authority{
		name: name, id: parkIDFor(name), host: host, j: j, mods: mods,
		subs:       map[*session]bool{},
		seen:       map[string]struct{}{},
		attach:     make(chan attachReq),
		stop:       make(chan struct{}),
		lastSnapAt: time.Now(),
		utcDay:     uint32(time.Now().Unix() / 86400),
	}
	snap, ok, err := j.LatestSnapshot(ctx, a.id)
	if err != nil {
		host.close()
		return nil, err
	}
	if ok {
		if err := host.Restore(snap.State); err != nil {
			host.close()
			return nil, fmt.Errorf("park %s: %w", name, err)
		}
		if got := host.Hash(); got != snap.WH {
			host.close()
			return nil, fmt.Errorf("park %s: snapshot hash mismatch (stored %016x, got %016x) — refusing to serve", name, snap.WH, got)
		}
		a.lastSeq = snap.Seq
		if err := j.Read(ctx, a.id, snap.Seq+1, func(ev journal.Event) error {
			for host.Tick() < ev.Tick {
				host.Step()
			}
			if code := host.Apply(encodeEvent(ev.Kind, ev.Payload)); code != 0 {
				return fmt.Errorf("replay: event seq %d rejected with code %d", ev.Seq, code)
			}
			a.lastSeq = ev.Seq
			return nil
		}); err != nil {
			host.close()
			return nil, fmt.Errorf("park %s: %w", name, err)
		}
	} else {
		var b [8]byte
		rand.Read(b[:])
		host.Init(binary.LittleEndian.Uint64(b[:]), a.id, 1)
		// The genesis snapshot makes the seed durable before anything is
		// served: events alone cannot recreate a park.
		if err := j.PutSnapshot(ctx, a.id, journal.Snapshot{
			Seq: 0, Tick: 0, Epoch: 1, WH: host.Hash(), State: host.Snapshot(),
		}); err != nil {
			host.close()
			return nil, err
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
	key := fmt.Sprintf("%s/%d", s.sub, intentID)
	a.mu.Lock()
	if _, dup := a.seen[key]; dup {
		a.mu.Unlock()
		return
	}
	a.seen[key] = struct{}{}
	a.seenFifo = append(a.seenFifo, key)
	if len(a.seenFifo) > dedupWindow {
		delete(a.seen, a.seenFifo[0])
		a.seenFifo = a.seenFifo[1:]
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

	a.mu.Lock()
	staged := a.staged
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
			}
			mIntentsRejected.Inc()
			continue
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
		firstSeq, err := a.j.Append(ctx, a.id, a.lastSeq, accepted)
		cancel()
		mAppendDur.Observe(time.Since(start).Seconds())
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
	t := a.host.Tick()
	wh := a.host.Hash()
	a.mu.Lock()
	a.ring[t%hashRingTicks] = ringEntry{tick: t, wh: wh}
	a.ringHead = t
	a.mu.Unlock()

	if a.eventsSinceSnap >= snapshotEvery || (a.eventsSinceSnap > 0 && time.Since(a.lastSnapAt) > snapshotMaxAge) {
		a.eventsSinceSnap = 0
		a.lastSnapAt = time.Now()
		snap := journal.Snapshot{
			Seq: a.lastSeq, Tick: t, Epoch: a.host.Epoch(), WH: wh, State: a.host.Snapshot(),
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
	welcome, _ := json.Marshal(map[string]any{
		"type": "welcome", "park": a.name, "role": s.role, "epoch": a.host.Epoch(),
		"seq": a.lastSeq, "tick": a.host.Tick(), "grid": [2]int{100, 100},
	})
	res := attachResult{
		welcome: append(welcome, '\n'),
		seq:     a.lastSeq,
		tick:    a.host.Tick(),
		epoch:   a.host.Epoch(),
		wh:      a.host.Hash(),
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
	snapLine := snapshotLineFrom(res.seq, res.tick, res.epoch, res.wh, res.state)
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
	line := snapshotLineFrom(a.lastSeq, t, a.host.Epoch(), a.host.Hash(), a.host.Snapshot())
	a.mu.Lock()
	a.snapCache = snapCacheEntry{tick: t, line: line}
	a.mu.Unlock()
	return line
}

func snapshotLineFrom(seq int64, tick uint64, epoch uint32, wh uint64, state []byte) []byte {
	var z bytes.Buffer
	w, _ := flate.NewWriter(&z, flate.BestCompression)
	w.Write(state)
	w.Close()
	msg, _ := json.Marshal(map[string]any{
		"type": "snapshot", "seq": seq, "tick": tick,
		"epoch": epoch, "wh": fmt.Sprintf("%016x", wh),
		"z": z.Bytes(), // json encodes []byte as base64
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
	mu     sync.Mutex
	byName map[string]*authority
	module func() []byte
	j      journal.Journal
	mods   *modules
}

func newParks(module func() []byte, j journal.Journal, mods *modules) *parks {
	return &parks{byName: map[string]*authority{}, module: module, j: j, mods: mods}
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
	a, err := openAuthority(ctx, name, p.module(), p.j, p.mods)
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
