// Package main is mythrad: the Wake Up Mythra game service — WebTransport
// session gateway, dog-park world sim, and module/asset distribution. Rooms
// are 100x100 grids; each connected player is represented by a dog stepped
// by the behavior module. The page itself is served by wake-up-mythra-web;
// the apex Ingress routes only /wt-info, /behavior/, and /assets/ here.
// The service carries the production update ladder end to end:
//
//   - tier 1 (content): dog skins are content-addressed assets mounted from a
//     ConfigMap; the tick snapshot references them by name+hash and clients
//     stream them in on first sight. Shipping a new skin is a data change.
//   - tier 2 (behavior): the live behavior module and an optional shadow
//     module (Rust -> wasm, executed by wazero) are mounted from a ConfigMap
//     and hot-reloaded without a restart. The shadow module runs every tick
//     against the same inputs, its outputs are compared, and divergence is
//     exported as a metric - a shadow launch of the world sim. Promotion
//     moves the shadow file into the live slot. The client's presentation
//     module rides the same mount; its hash on every pong tells connected
//     pages to hot-swap their smoothing logic mid-session.
//
// Protocol. The hello carries "proto"; sessions without it (proto 1) get
// full-snapshot JSON tick datagrams, which stop fitting a QUIC datagram
// above ~44 dogs/room. Proto 2 splits state into stream keyframes and
// binary delta datagrams, sized so a 2000-dog park's tick is one datagram.
//
//	client -> server stream:   {"type":"hello","name","device","token","room","spectate","proto":2}
//	                           {"type":"join","room":"park-2"}
//	server -> client stream:   {"type":"welcome","you":{...},"token":"...","grid":[w,h],"seed":".."}
//	                           {"type":"presence","room","players":[first 50],"count","watching","seed"}
//	                           {"type":"keyframe","n","seq","off","st","behavior",
//	                            "dogs":[[id,x,y,skin],...],"wh"}   (proto 2; dogs order = delta roster)
//	client -> server datagram: {"type":"ping","ct":...}
//	server -> client datagram: proto 1: {"type":"tick","n","st","behavior","dogs":[...],"wh" (1/s)}
//	                           proto 2: binary delta — [0xD7, seq u32, off u16, start u16,
//	                            count u16, st u32] then 4-bit codes (dx+1)*3+(dy+1), 0xF=departed,
//	                            two per byte low-nibble-first, dogs in keyframe roster order
//	                           {"type":"pong","ct":...,"st":...,"addr":"...","cw":".."}
//
// "seed" is the room's shared randomness seed and "wh" the world-hash
// oracle: any client holding the seed recomputes the hash from its own
// snapshot via the client module and verifies it matches the sim.
package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/quic-go/quic-go/http3"
	webtransport "github.com/quic-go/webtransport-go"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

//go:embed behaviors/server.wasm
var defaultBehavior []byte

//go:embed behaviors/client.wasm
var defaultClientModule []byte

const (
	gridW = 100
	gridH = 100
)

var (
	mTickDur = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "mythra_tick_duration_seconds",
		Help:    "Wall time of one full tick (all rooms): budget is 1/tickrate.",
		Buckets: []float64{.0005, .001, .0025, .005, .01, .02, .03, .0417, .06, .1, .25},
	})
	mSessions = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mythra_sessions", Help: "Connected sessions."}, []string{"role"})
	mRooms = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mythra_rooms_occupied", Help: "Rooms with at least one occupant."})
	mHandshakes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_handshakes_total", Help: "Session handshakes."}, []string{"result"})
	mResumes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_resumes_total", Help: "Token-resumed sessions."}, []string{"result"})
	mDgSent = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_datagrams_sent_total", Help: "Datagrams sent."})
	mDgErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_datagram_errors_total", Help: "SendDatagram failures."})
	mDrops = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_fanout_dropped_total", Help: "Stream messages dropped on stalled clients."})
	mBehaviorReloads = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_behavior_reloads_total", Help: "Behavior script hot-reloads."}, []string{"slot", "result"})
	mBehaviorInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mythra_behavior_script", Help: "1 for the currently loaded script hash per slot."}, []string{"slot", "hash"})
	mShadowSteps = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_shadow_steps_total", Help: "Shadow behavior evaluations."})
	mShadowDivergence = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_shadow_divergence_total", Help: "Shadow steps whose output differed from live."})
	mShadowErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_shadow_errors_total", Help: "Shadow script eval errors."})
	mProtoSessions = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mythra_proto_sessions", Help: "Connected sessions by tick protocol."}, []string{"proto"})
	mKeyframes = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_keyframes_total", Help: "Keyframes broadcast (proto 2)."})
	mKeyframeBytes = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_keyframe_bytes_total", Help: "Keyframe bytes across all recipients."})
	mDeltaBytes = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_delta_bytes_total", Help: "Delta datagram bytes across all recipients."})
)

func envInt(k string, d int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return d
}
func envStr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// ---------- behavior engine ----------

// A behavior is a wasm module over the shared sim core (built from
// //src/services/mythrad/sim), executed by wazero. Modules import nothing
// - no WASI, no host functions - so a behavior can compute but never reach
// out, and identical modules in the live and shadow slots diff to zero
// divergence by construction. ABI: id_buf() -> ptr to a 64-byte scratch the
// host writes the dog id into; step(tick, id_len, x, y, w, h) -> packed
// (dx << 32 | dy) as two i32s.
type behaviorSlot struct {
	mu      sync.Mutex
	name    string // "live" | "shadow"
	hash    string
	rt      wazero.Runtime
	mem     api.Memory
	stepFn  api.Function
	seedFn  api.Function // optional set_seed export
	whReset api.Function // optional world-hash exports
	whAdd   api.Function
	whGet   api.Function
	idPtr   uint32
	seed    uint64 // last seed pushed via set_seed
}

func newBehaviorSlot(name string) *behaviorSlot { return &behaviorSlot{name: name} }

func (b *behaviorSlot) load(module []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	sum := sha256.Sum256(module)
	hash := hex.EncodeToString(sum[:4])
	if hash == b.hash {
		return nil
	}
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	fail := func(err error) error {
		rt.Close(ctx)
		mBehaviorReloads.WithLabelValues(b.name, "error").Inc()
		return fmt.Errorf("%s: %w", b.name, err)
	}
	mod, err := rt.Instantiate(ctx, module)
	if err != nil {
		return fail(err)
	}
	stepFn := mod.ExportedFunction("step")
	idBuf := mod.ExportedFunction("id_buf")
	if stepFn == nil || idBuf == nil || mod.Memory() == nil {
		return fail(fmt.Errorf("module must export step, id_buf, and memory"))
	}
	res, err := idBuf.Call(ctx)
	if err != nil {
		return fail(fmt.Errorf("id_buf: %w", err))
	}
	if b.rt != nil {
		b.rt.Close(ctx)
	}
	mBehaviorInfo.DeletePartialMatch(prometheus.Labels{"slot": b.name})
	mBehaviorInfo.WithLabelValues(b.name, hash).Set(1)
	mBehaviorReloads.WithLabelValues(b.name, "ok").Inc()
	b.hash, b.rt, b.mem, b.stepFn, b.idPtr = hash, rt, mod.Memory(), stepFn, uint32(res[0])
	b.seedFn = mod.ExportedFunction("set_seed")
	b.whReset = mod.ExportedFunction("wh_reset")
	b.whAdd = mod.ExportedFunction("wh_add")
	b.whGet = mod.ExportedFunction("wh_get")
	b.seed = 0
	log.Printf("behavior %s loaded: %s", b.name, hash)
	return nil
}

func (b *behaviorSlot) unload() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rt != nil {
		b.rt.Close(context.Background())
		b.rt, b.mem, b.stepFn, b.hash = nil, nil, nil, ""
		mBehaviorInfo.DeletePartialMatch(prometheus.Labels{"slot": b.name})
		log.Printf("behavior %s unloaded", b.name)
	}
}

func (b *behaviorSlot) loaded() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hash, b.rt != nil
}

func (b *behaviorSlot) step(tick, seed uint64, dogID string, x, y int) (dx, dy int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rt == nil {
		return 0, 0, nil
	}
	ctx := context.Background()
	if b.seedFn != nil && seed != b.seed {
		if _, err := b.seedFn.Call(ctx, seed); err != nil {
			return 0, 0, err
		}
		b.seed = seed
	}
	id := []byte(dogID)
	if len(id) > 64 {
		id = id[:64]
	}
	if !b.mem.Write(b.idPtr, id) {
		return 0, 0, fmt.Errorf("%s: id write out of bounds", b.name)
	}
	res, err := b.stepFn.Call(ctx, tick, uint64(len(id)),
		uint64(uint32(int32(x))), uint64(uint32(int32(y))),
		uint64(uint32(gridW)), uint64(uint32(gridH)))
	if err != nil {
		return 0, 0, err
	}
	clamp := func(v int32) int {
		return int(max(-1, min(1, v)))
	}
	return clamp(int32(res[0] >> 32)), clamp(int32(res[0])), nil
}

// worldHash computes the cross-surface oracle over one room's dogs via the
// module's wh_* exports. Returns ok=false when the loaded module predates
// the oracle ABI.
func (b *behaviorSlot) worldHash(tick, seed uint64, dogs [][4]any) (uint64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rt == nil || b.whReset == nil || b.whAdd == nil || b.whGet == nil {
		return 0, false
	}
	ctx := context.Background()
	if _, err := b.whReset.Call(ctx, tick, seed); err != nil {
		return 0, false
	}
	for _, d := range dogs {
		id := []byte(d[0].(string))
		if len(id) > 64 {
			id = id[:64]
		}
		if !b.mem.Write(b.idPtr, id) {
			return 0, false
		}
		if _, err := b.whAdd.Call(ctx, uint64(len(id)),
			uint64(uint32(int32(d[1].(int)))), uint64(uint32(int32(d[2].(int))))); err != nil {
			return 0, false
		}
	}
	res, err := b.whGet.Call(ctx)
	if err != nil {
		return 0, false
	}
	return res[0], true
}

// clientModule tracks the presentation module served to browsers: raw bytes
// plus a short content hash. The hash rides every pong, so connected pages
// hot-swap their smoothing logic mid-session the same way the server swaps
// behaviors.
type clientModule struct {
	mu    sync.Mutex
	bytes []byte
	hash  string
}

func (c *clientModule) set(module []byte) {
	sum := sha256.Sum256(module)
	hash := hex.EncodeToString(sum[:4])
	c.mu.Lock()
	changed := hash != c.hash
	c.bytes, c.hash = module, hash
	c.mu.Unlock()
	if changed {
		mBehaviorInfo.DeletePartialMatch(prometheus.Labels{"slot": "client"})
		mBehaviorInfo.WithLabelValues("client", hash).Set(1)
		log.Printf("client module loaded: %s", hash)
	}
}

func (c *clientModule) get() ([]byte, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes, c.hash
}

// watchBehaviors polls the mounted behavior dir; ConfigMap edits land on the
// mount within ~a minute of Flux applying them, with no pod restart.
func watchBehaviors(dir string, live, shadow *behaviorSlot, client *clientModule) {
	load := func() {
		if module, err := os.ReadFile(filepath.Join(dir, "live.wasm")); err == nil {
			if err := live.load(module); err != nil {
				log.Printf("behavior live reload rejected (keeping current): %v", err)
			}
		}
		if module, err := os.ReadFile(filepath.Join(dir, "shadow.wasm")); err == nil {
			if err := shadow.load(module); err != nil {
				log.Printf("behavior shadow reload rejected: %v", err)
			}
		} else if _, ok := shadow.loaded(); ok {
			shadow.unload()
		}
		if module, err := os.ReadFile(filepath.Join(dir, "client.wasm")); err == nil && len(module) > 8 {
			client.set(module)
		}
	}
	load()
	for range time.Tick(2 * time.Second) {
		load()
	}
}

// ---------- asset catalog ----------

type asset struct {
	name string
	hash string
	body []byte
}

type assetCatalog struct {
	mu     sync.Mutex
	byRef  map[string]*asset // "name.hash" -> asset
	skin   string            // current default skin ref: "cell" or "name.hash"
	dir    string
	skinOf map[string]string
}

func newAssetCatalog(dir string) *assetCatalog {
	c := &assetCatalog{byRef: map[string]*asset{}, skin: "cell", dir: dir}
	c.reload()
	go func() {
		for range time.Tick(2 * time.Second) {
			c.reload()
		}
	}()
	return c
}

func (c *assetCatalog) reload() {
	entries, _ := os.ReadDir(c.dir)
	c.mu.Lock()
	defer c.mu.Unlock()
	skin := "cell"
	for _, e := range entries {
		name := e.Name()
		if name == "skins.json" {
			if raw, err := os.ReadFile(filepath.Join(c.dir, name)); err == nil {
				var m map[string]string
				if json.Unmarshal(raw, &m) == nil && m["default"] != "" {
					skin = m["default"]
				}
			}
			continue
		}
		if !strings.HasSuffix(name, ".svg") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(c.dir, name))
		if err != nil {
			continue
		}
		sum := sha256.Sum256(body)
		h := hex.EncodeToString(sum[:4])
		base := strings.TrimSuffix(name, ".svg")
		ref := base + "." + h
		if _, ok := c.byRef[ref]; !ok {
			c.byRef[ref] = &asset{name: base, hash: h, body: body}
			log.Printf("asset loaded: %s (%d bytes)", ref, len(body))
		}
	}
	// The default skin only takes effect once the referenced asset exists,
	// so a snapshot can never reference an asset the server cannot serve.
	if skin == "cell" {
		c.skin = "cell"
	} else {
		for ref, a := range c.byRef {
			if a.name == skin {
				c.skin = ref
			}
			_ = ref
		}
	}
}

func (c *assetCatalog) defaultSkin() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.skin
}

func (c *assetCatalog) get(ref string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.byRef[ref]
	if !ok {
		return nil, false
	}
	return a.body, true
}

// ---------- world ----------

type Player struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Device   string `json:"device"`
	Room     string `json:"room"`
	Role     string `json:"role"` // "player" | "spectator"
	X        int    `json:"x"`
	Y        int    `json:"y"`
	LastSeen int64  `json:"lastSeen"`

	token string
	sess  *webtransport.Session
	out   chan []byte
	proto int
	// This tick's effective (post-clamp) movement, read by the delta
	// encoder inside the same locked tick pass that wrote it.
	dX, dY int8
}

// roomKF is a room's delta epoch: the roster order binary delta datagrams
// index into, established by the last keyframe broadcast.
type roomKF struct {
	seq    uint32
	off    uint16    // ticks since the keyframe
	roster []*Player // players (role=player) in keyframe order
	dirty  bool      // membership changed since the last presence flush
}

type Hub struct {
	mu      sync.Mutex
	players map[string]*Player
	rooms   map[string]map[*Player]bool
	seeds   map[string]uint64 // per-room shared randomness seed
	kf      map[string]*roomKF
	tick    uint64
	tickHz  int
	kfTicks int
	live    *behaviorSlot
	shadow  *behaviorSlot
	assets  *assetCatalog
	client  *clientModule
}

func nowMs() int64 { return time.Now().UnixMilli() }

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func spawnCoord(n int) int {
	var b [2]byte
	rand.Read(b[:])
	return int(binary.LittleEndian.Uint16(b[:])) % n
}

func newHub(tickHz, kfTicks int, live, shadow *behaviorSlot, assets *assetCatalog, client *clientModule) *Hub {
	return &Hub{
		players: map[string]*Player{}, rooms: map[string]map[*Player]bool{},
		seeds: map[string]uint64{}, kf: map[string]*roomKF{},
		tickHz: tickHz, kfTicks: kfTicks, live: live, shadow: shadow, assets: assets, client: client,
	}
}

func (h *Hub) moveLocked(p *Player, room string) {
	if cur, ok := h.rooms[p.Room]; ok {
		delete(cur, p)
		if len(cur) == 0 {
			delete(h.rooms, p.Room)
			delete(h.seeds, p.Room)
			delete(h.kf, p.Room)
		}
	}
	p.Room = room
	if room != "" {
		if h.rooms[room] == nil {
			h.rooms[room] = map[*Player]bool{}
			// The park's shared randomness seed: broadcast to every client
			// (welcome/presence) so any surface reproduces the server's
			// dice and world hash for this room's timeline.
			var b [8]byte
			rand.Read(b[:])
			h.seeds[room] = binary.LittleEndian.Uint64(b[:])
		}
		h.rooms[room][p] = true
	}
	mRooms.Set(float64(len(h.rooms)))
}

// presenceCap bounds the names carried per presence message. The full list
// is O(occupancy) and goes to every occupant, so an uncapped flush is
// O(n²) bytes — 2000 dogs would be a ~240MB broadcast. The page shows a
// handful of names plus counts; 50 covers it.
const presenceCap = 50

// markDirtyLocked queues a presence flush (and roster refresh) for the
// room; tickLoop flushes on keyframe cadence. Requires h.mu held.
func (h *Hub) markDirtyLocked(room string) {
	if room == "" {
		return
	}
	kf := h.kf[room]
	if kf == nil {
		kf = &roomKF{}
		h.kf[room] = kf
	}
	kf.dirty = true
}

// presenceMsgLocked builds the capped presence line. Requires h.mu held.
func (h *Hub) presenceMsgLocked(room string) []byte {
	occ := h.rooms[room]
	list := make([]*Player, 0, presenceCap)
	players, watching := 0, 0
	for p := range occ {
		if p.Role == "spectator" {
			watching++
			continue
		}
		players++
		if len(list) < presenceCap {
			list = append(list, p)
		}
	}
	msg, _ := json.Marshal(map[string]any{
		"type": "presence", "room": room, "players": list, "count": players,
		"watching": watching, "st": nowMs(),
		"seed": fmt.Sprintf("%016x", h.seeds[room]),
	})
	return append(msg, '\n')
}

func (h *Hub) sendLocked(p *Player, msg []byte) {
	if p.out == nil {
		return
	}
	select {
	case p.out <- msg:
	default:
		mDrops.Inc()
	}
}

// tickLoop advances every dog under the live behavior, runs the shadow
// behavior for comparison when one is mounted, and fans each room's state
// out: proto-1 sessions get the full-snapshot JSON datagram, proto-2
// sessions get binary movement deltas between stream keyframes.
func (h *Hub) tickLoop() {
	interval := time.Second / time.Duration(h.tickHz)
	for range time.Tick(interval) {
		start := time.Now()
		h.mu.Lock()
		h.tick++
		st := nowMs()
		liveHash, _ := h.live.loaded()
		_, shadowLoaded := h.shadow.loaded()
		skin := h.assets.defaultSkin()
		for room, occ := range h.rooms {
			seed := h.seeds[room]
			hasV1, hasV2 := false, false
			for p := range occ {
				if p.sess == nil {
					continue
				}
				if p.proto >= 2 {
					hasV2 = true
				} else {
					hasV1 = true
				}
			}
			for p := range occ {
				if p.Role != "player" {
					continue
				}
				dx, dy, err := h.live.step(h.tick, seed, p.ID, p.X, p.Y)
				if err != nil {
					log.Printf("live behavior error (dog %s frozen this tick): %v", p.ID, err)
					dx, dy = 0, 0
				}
				if shadowLoaded {
					sdx, sdy, serr := h.shadow.step(h.tick, seed, p.ID, p.X, p.Y)
					mShadowSteps.Inc()
					if serr != nil {
						mShadowErrors.Inc()
					} else if sdx != dx || sdy != dy {
						mShadowDivergence.Inc()
					}
				}
				nx := clampInt(p.X+dx, 0, gridW-1)
				ny := clampInt(p.Y+dy, 0, gridH-1)
				p.dX, p.dY = int8(nx-p.X), int8(ny-p.Y)
				p.X, p.Y = nx, ny
			}

			kf := h.kf[room]
			if kf == nil {
				kf = &roomKF{dirty: true}
				h.kf[room] = kf
			}
			isKF := h.tick%uint64(h.kfTicks) == 0 || kf.roster == nil || kf.off >= 0xFFFE

			var dogs [][4]any
			if hasV1 || (hasV2 && isKF) {
				dogs = make([][4]any, 0, len(occ))
				for p := range occ {
					if p.Role == "player" {
						dogs = append(dogs, [4]any{p.ID, p.X, p.Y, skin})
					}
				}
			}

			if hasV1 {
				snapshot := map[string]any{
					"type": "tick", "n": h.tick, "st": st, "behavior": liveHash, "dogs": dogs,
				}
				if h.tick%uint64(h.tickHz) == 0 {
					if wh, ok := h.live.worldHash(h.tick, seed, dogs); ok {
						snapshot["wh"] = fmt.Sprintf("%016x", wh)
					}
				}
				msg, _ := json.Marshal(snapshot)
				for p := range occ {
					if p.sess == nil || p.proto >= 2 {
						continue
					}
					if err := p.sess.SendDatagram(msg); err != nil {
						mDgErrors.Inc()
					} else {
						mDgSent.Inc()
					}
				}
			}

			if hasV2 {
				// Every tick emits a delta — including keyframe ticks, whose
				// delta lands just before the keyframe replaces the state.
				// That makes "delta-accumulated state == keyframe state" an
				// exact client-side invariant on a loss-free link, which the
				// page exports as a continuous decode oracle.
				if kf.roster != nil {
					kf.off++
					for _, chunk := range encodeDeltas(kf, uint32(st), room) {
						n := 0
						for p := range occ {
							if p.sess == nil || p.proto < 2 {
								continue
							}
							if err := p.sess.SendDatagram(chunk); err != nil {
								mDgErrors.Inc()
							} else {
								mDgSent.Inc()
								n++
							}
						}
						mDeltaBytes.Add(float64(len(chunk) * n))
					}
				}
				if isKF {
					kf.seq++
					kf.off = 0
					kf.roster = kf.roster[:0]
					// The keyframe's dogs array IS the delta roster: clients
					// index subsequent 4-bit codes by this order.
					ordered := make([][4]any, 0, len(dogs))
					for p := range occ {
						if p.Role == "player" {
							kf.roster = append(kf.roster, p)
							ordered = append(ordered, [4]any{p.ID, p.X, p.Y, skin})
						}
					}
					msg := h.keyframeMsgLocked(room, kf, st, liveHash, seed, ordered)
					n := 0
					for p := range occ {
						if p.sess == nil || p.proto < 2 {
							continue
						}
						h.sendLocked(p, msg)
						n++
					}
					mKeyframes.Inc()
					mKeyframeBytes.Add(float64(len(msg) * n))
				}
			}

			if kf.dirty && (isKF || !hasV2) {
				kf.dirty = false
				msg := h.presenceMsgLocked(room)
				for p := range occ {
					h.sendLocked(p, msg)
				}
			}
		}
		h.mu.Unlock()
		mTickDur.Observe(time.Since(start).Seconds())
	}
}

// keyframeMsgLocked marshals a room keyframe. dogs must be in kf.roster
// order. Every keyframe carries the world-hash oracle. Requires h.mu held.
func (h *Hub) keyframeMsgLocked(room string, kf *roomKF, st int64, behavior string, seed uint64, dogs [][4]any) []byte {
	snapshot := map[string]any{
		"type": "keyframe", "n": h.tick, "seq": kf.seq, "off": kf.off,
		"st": st, "behavior": behavior, "dogs": dogs,
	}
	if wh, ok := h.live.worldHash(h.tick, seed, dogs); ok {
		snapshot["wh"] = fmt.Sprintf("%016x", wh)
	}
	msg, _ := json.Marshal(snapshot)
	return append(msg, '\n')
}

const (
	deltaMagic     = 0xD7
	deltaHeader    = 15
	deltaChunkDogs = 2200 // (~1150B datagram budget - header) * 2 codes/byte
	deltaGone      = 0xF  // roster slot whose player left the room
)

// encodeDeltas packs one tick of roster movement into datagrams:
// [0xD7, seq u32, off u16, start u16, count u16, st u32] then 4-bit codes
// (dx+1)*3+(dy+1) low-nibble-first. Movement is bounded to ±1/axis by the
// behavior contract (asserted in sim core tests), so codes 0..8 cover it.
func encodeDeltas(kf *roomKF, st uint32, room string) [][]byte {
	var out [][]byte
	for base := 0; base < len(kf.roster); base += deltaChunkDogs {
		count := len(kf.roster) - base
		if count > deltaChunkDogs {
			count = deltaChunkDogs
		}
		buf := make([]byte, deltaHeader+(count+1)/2)
		buf[0] = deltaMagic
		binary.LittleEndian.PutUint32(buf[1:], kf.seq)
		binary.LittleEndian.PutUint16(buf[5:], kf.off)
		binary.LittleEndian.PutUint16(buf[7:], uint16(base))
		binary.LittleEndian.PutUint16(buf[9:], uint16(count))
		binary.LittleEndian.PutUint32(buf[11:], st)
		for i := 0; i < count; i++ {
			p := kf.roster[base+i]
			code := byte(deltaGone)
			if p.Room == room {
				code = byte(p.dX+1)*3 + byte(p.dY+1)
			}
			if i%2 == 0 {
				buf[deltaHeader+i/2] = code
			} else {
				buf[deltaHeader+i/2] |= code << 4
			}
		}
		out = append(out, buf)
	}
	return out
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (h *Hub) updateSessionGauges() {
	counts := map[string]int{}
	protos := map[string]int{}
	for _, q := range h.players {
		if q.sess != nil {
			counts[q.Role]++
			if q.proto >= 2 {
				protos["2"]++
			} else {
				protos["1"]++
			}
		}
	}
	mSessions.WithLabelValues("player").Set(float64(counts["player"]))
	mSessions.WithLabelValues("spectator").Set(float64(counts["spectator"]))
	mProtoSessions.WithLabelValues("1").Set(float64(protos["1"]))
	mProtoSessions.WithLabelValues("2").Set(float64(protos["2"]))
}

func (h *Hub) handleSession(sess *webtransport.Session, remote string) {
	ctx := sess.Context()
	stream, err := sess.AcceptStream(ctx)
	if err != nil {
		mHandshakes.WithLabelValues("no_stream").Inc()
		return
	}
	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	if !sc.Scan() {
		mHandshakes.WithLabelValues("no_hello").Inc()
		return
	}
	var hello struct {
		Type, Name, Device, Token, Room string
		Spectate                        bool
		Proto                           int
	}
	if json.Unmarshal(sc.Bytes(), &hello) != nil || hello.Type != "hello" {
		mHandshakes.WithLabelValues("bad_hello").Inc()
		return
	}

	h.mu.Lock()
	p, resumed := h.players[hello.Token]
	if resumed {
		mResumes.WithLabelValues("ok").Inc()
	} else {
		p = &Player{
			ID: randHex(4), token: randHex(16),
			X: spawnCoord(gridW), Y: spawnCoord(gridH),
		}
		h.players[p.token] = p
		if hello.Token != "" {
			mResumes.WithLabelValues("expired").Inc()
		}
	}
	if hello.Name != "" {
		p.Name = hello.Name
	}
	if hello.Device != "" {
		p.Device = hello.Device
	}
	p.Role = "player"
	if hello.Spectate {
		p.Role = "spectator"
	}
	room := p.Room
	if room == "" {
		room = "park-mythra"
	}
	if hello.Room != "" && !resumed {
		room = hello.Room
	}
	h.moveLocked(p, room)
	h.markDirtyLocked(p.Room)
	p.LastSeen = nowMs()
	p.sess = sess
	p.proto = hello.Proto
	p.out = make(chan []byte, 64)
	out := p.out
	roomSeed := h.seeds[p.Room]
	// Mid-window joiners get the room state now instead of waiting out the
	// keyframe cadence: a unicast keyframe with current positions in the
	// established roster order, so in-flight deltas keep applying.
	var snap []byte
	if p.proto >= 2 {
		if kf := h.kf[p.Room]; kf != nil && kf.roster != nil {
			liveHash, _ := h.live.loaded()
			skin := h.assets.defaultSkin()
			ordered := make([][4]any, 0, len(kf.roster))
			for _, q := range kf.roster {
				if q.Room == p.Room {
					ordered = append(ordered, [4]any{q.ID, q.X, q.Y, skin})
				} else {
					// Hold the departed dog's roster slot so indices line up.
					ordered = append(ordered, [4]any{q.ID, q.X, q.Y, "gone"})
				}
			}
			snap = h.keyframeMsgLocked(p.Room, kf, nowMs(), liveHash, h.seeds[p.Room], ordered)
		}
	}
	h.updateSessionGauges()
	h.mu.Unlock()
	mHandshakes.WithLabelValues("ok").Inc()

	welcome, _ := json.Marshal(map[string]any{
		"type": "welcome", "you": p, "token": p.token, "resumed": resumed,
		"grid": [2]int{gridW, gridH}, "seed": fmt.Sprintf("%016x", roomSeed),
	})
	stream.Write(append(welcome, '\n'))
	if snap != nil {
		stream.Write(snap)
	}

	go func() {
		for {
			select {
			case msg := <-out:
				stream.SetWriteDeadline(time.Now().Add(3 * time.Second))
				if _, err := stream.Write(msg); err != nil {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	go h.datagramLoop(sess, p, remote)

	for sc.Scan() {
		var m struct{ Type, Room string }
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Type == "join" && m.Room != "" {
			h.mu.Lock()
			old := p.Room
			h.moveLocked(p, m.Room)
			h.markDirtyLocked(old)
			h.markDirtyLocked(p.Room)
			p.LastSeen = nowMs()
			h.mu.Unlock()
		}
	}

	h.mu.Lock()
	if p.sess == sess {
		room = p.Room
		h.moveLocked(p, "")
		h.markDirtyLocked(room)
		p.sess = nil
		p.out = nil
		p.LastSeen = nowMs()
	}
	h.updateSessionGauges()
	h.mu.Unlock()
}

func (h *Hub) datagramLoop(sess *webtransport.Session, p *Player, remote string) {
	for {
		data, err := sess.ReceiveDatagram(sess.Context())
		if err != nil {
			return
		}
		var m struct {
			Type string  `json:"type"`
			CT   float64 `json:"ct"`
		}
		if json.Unmarshal(data, &m) != nil || m.Type != "ping" {
			continue
		}
		_, cw := h.client.get()
		pong, _ := json.Marshal(map[string]any{"type": "pong", "ct": m.CT, "st": nowMs(), "addr": remote, "cw": cw})
		if sess.SendDatagram(pong) == nil {
			mDgSent.Inc()
		}
		h.mu.Lock()
		p.LastSeen = nowMs()
		h.mu.Unlock()
	}
}

func (h *Hub) purgeLoop() {
	for range time.Tick(time.Minute) {
		h.mu.Lock()
		cut := nowMs() - time.Hour.Milliseconds()
		for tok, p := range h.players {
			if p.sess == nil && p.LastSeen < cut {
				delete(h.players, tok)
			}
		}
		h.mu.Unlock()
	}
}

// ---------- certificates ----------

// rotatingCert regenerates the self-signed ECDSA cert at half-life so the
// serverCertificateHashes contract (<=14 days validity) holds for a
// long-running pod; /wt-info always serves the current hash.
type rotatingCert struct {
	mu   sync.Mutex
	cert tls.Certificate
	hash [32]byte
	sans []net.IP
}

func newRotatingCert(sans []net.IP) *rotatingCert {
	rc := &rotatingCert{sans: sans}
	rc.rotate()
	go func() {
		for range time.Tick(5 * 24 * time.Hour) {
			rc.rotate()
		}
	}()
	return rc
}

func (rc *rotatingCert) rotate() {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "mythrad"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 24 * time.Hour),
		DNSNames:     []string{"mythrad", "localhost"},
		IPAddresses:  rc.sans,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	rc.mu.Lock()
	rc.cert = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	rc.hash = sha256.Sum256(der)
	rc.mu.Unlock()
	log.Printf("cert rotated: %s (sans %v)", hex.EncodeToString(rc.hash[:8]), rc.sans)
}

func (rc *rotatingCert) get() (tls.Certificate, [32]byte) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.cert, rc.hash
}

// fileCert serves a CA-issued cert from mounted Secret files, re-reading on
// change so cert-manager renewals land without a restart. loaded() reports
// whether a real cert is being served (vs the self-signed fallback).
type fileCert struct {
	mu       sync.Mutex
	certFile string
	keyFile  string
	cert     *tls.Certificate
	modTime  time.Time
}

func newFileCert(certFile, keyFile string) *fileCert {
	fc := &fileCert{certFile: certFile, keyFile: keyFile}
	fc.reload()
	go func() {
		for range time.Tick(30 * time.Second) {
			fc.reload()
		}
	}()
	return fc
}

func (fc *fileCert) reload() {
	st, err := os.Stat(fc.certFile)
	if err != nil {
		return
	}
	fc.mu.Lock()
	unchanged := st.ModTime().Equal(fc.modTime)
	fc.mu.Unlock()
	if unchanged {
		return
	}
	cert, err := tls.LoadX509KeyPair(fc.certFile, fc.keyFile)
	if err != nil {
		log.Printf("tls: keypair load failed (keeping previous): %v", err)
		return
	}
	fc.mu.Lock()
	fc.cert, fc.modTime = &cert, st.ModTime()
	fc.mu.Unlock()
	log.Printf("tls: loaded CA-issued certificate from %s", fc.certFile)
}

func (fc *fileCert) loaded() bool {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.cert != nil
}

func (fc *fileCert) get() *tls.Certificate {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.cert
}

func main() {
	tickHz := envInt("TICK_HZ", 24)
	wtPort := envInt("WT_PORT", 4433)
	httpPort := envInt("HTTP_PORT", 9634)
	metricsPort := envInt("METRICS_PORT", 9633)
	behaviorDir := envStr("BEHAVIOR_DIR", "/etc/mythra/behavior")
	assetDir := envStr("ASSET_DIR", "/etc/mythra/assets")
	publicAddr := envStr("PUBLIC_ADDR", "") // "host:port" advertised to clients
	allowedOrigins := envStr("ALLOWED_ORIGINS", "")

	sans := []net.IP{net.ParseIP("127.0.0.1")}
	if host, _, err := net.SplitHostPort(publicAddr); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			sans = append(sans, ip)
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				sans = append(sans, ipn.IP)
			}
		}
	}
	rc := newRotatingCert(sans)
	fc := newFileCert(envStr("TLS_CERT_FILE", ""), envStr("TLS_KEY_FILE", ""))

	live := newBehaviorSlot("live")
	shadow := newBehaviorSlot("shadow")
	if err := live.load(defaultBehavior); err != nil {
		log.Fatalf("embedded default behavior invalid: %v", err)
	}
	client := &clientModule{}
	client.set(defaultClientModule)
	go watchBehaviors(behaviorDir, live, shadow, client)

	assets := newAssetCatalog(assetDir)
	hub := newHub(tickHz, envInt("KEYFRAME_TICKS", tickHz), live, shadow, assets, client)
	go hub.tickLoop()
	go hub.purgeLoop()

	wtMux := http.NewServeMux()
	wt := webtransport.Server{
		H3: &http3.Server{
			Addr: fmt.Sprintf(":%d", wtPort),
			TLSConfig: &tls.Config{
				// CA-issued cert when the mounted Secret has one; self-signed
				// fallback (hash-pinned dial) until then.
				GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
					if c := fc.get(); c != nil {
						return c, nil
					}
					c, _ := rc.get()
					return &c, nil
				},
				NextProtos: []string{http3.NextProtoH3},
			},
			Handler:         wtMux,
			EnableDatagrams: true,
		},
		CheckOrigin: func(r *http.Request) bool {
			o := r.Header.Get("Origin")
			if o == "" || allowedOrigins == "" {
				return true
			}
			for _, a := range strings.Split(allowedOrigins, ",") {
				if o == strings.TrimSpace(a) {
					return true
				}
			}
			return false
		},
	}
	wtMux.HandleFunc("/wt", func(w http.ResponseWriter, r *http.Request) {
		sess, err := wt.Upgrade(w, r)
		if err != nil {
			mHandshakes.WithLabelValues("upgrade_failed").Inc()
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		go hub.handleSession(sess, r.RemoteAddr)
	})

	// Connect info, game modules, and content-addressed assets, served
	// through the normal Cloudflare-proxied ingress (the apex path-routes
	// these here; the page itself is the web app's job). The WebTransport
	// dial goes direct to PUBLIC_ADDR with the cert hash from /wt-info.
	pageMux := http.NewServeMux()
	serveWTInfo := func(w http.ResponseWriter, r *http.Request) {
		addr := publicAddr
		if addr == "" {
			addr = "127.0.0.1:" + strconv.Itoa(wtPort)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, cw := client.get()
		info := map[string]any{"addr": addr, "clientWasm": cw}
		// The hash rides only while the self-signed fallback serves: with a
		// CA-issued cert the dial is the standards path (WebKit-compatible).
		if !fc.loaded() {
			_, hash := rc.get()
			info["certHashB64"] = base64.StdEncoding.EncodeToString(hash[:])
		}
		json.NewEncoder(w).Encode(info)
	}
	serveClientModule := func(w http.ResponseWriter, r *http.Request) {
		module, hash := client.get()
		if len(module) == 0 {
			http.NotFound(w, r)
			return
		}
		// Pages fetch ?v=<hash> from wt-info/pong, so the bytes are immutable
		// per URL; a hash flip in a pong is the update signal.
		w.Header().Set("Content-Type", "application/wasm")
		w.Header().Set("ETag", `"`+hash+`"`)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Write(module)
	}
	serveAsset := func(prefix string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			ref := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), ".svg")
			body, ok := assets.get(ref)
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.Write(body)
		}
	}
	pageMux.HandleFunc("/wt-info", serveWTInfo)
	pageMux.HandleFunc("/assets/", serveAsset("/assets/"))
	pageMux.HandleFunc("/behavior/client.wasm", serveClientModule)

	obsMux := http.NewServeMux()
	obsMux.Handle("/metrics", promhttp.Handler())
	obsMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })

	log.Printf("mythrad: wt=:%d http=:%d metrics=:%d tick=%dHz public=%s", wtPort, httpPort, metricsPort, tickHz, publicAddr)
	go func() { log.Fatal(wt.ListenAndServe()) }()
	go func() { log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", httpPort), pageMux)) }()
	go func() { log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", metricsPort), obsMux)) }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	log.Print("SIGTERM: closing sessions (clients resume against replacement)")
	wt.Close()
}
