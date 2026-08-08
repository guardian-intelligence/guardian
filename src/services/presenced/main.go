// Package main is presenced: the Wake Up Mythra presence plane. Rooms are
// 100x100 grids; each connected player is represented by a dog that moves
// autonomously under a Lua behavior script. The service demonstrates the
// production update ladder end to end:
//
//   - tier 1 (content): dog skins are content-addressed assets mounted from a
//     ConfigMap; the tick snapshot references them by name+hash and clients
//     stream them in on first sight. Shipping a new skin is a data change.
//   - tier 2 (behavior): the live behavior script and an optional shadow
//     script are mounted from a ConfigMap and hot-reloaded without a restart.
//     The shadow script runs every tick against the same inputs, its outputs
//     are compared, and divergence is exported as a metric - a shadow launch
//     of the world sim. Promotion moves the shadow file into the live slot.
//
// Protocol (JSON; binary delta encoding replaces this in the real build):
//
//	client -> server stream:   {"type":"hello","name","device","token","room","spectate"}
//	                           {"type":"join","room":"park-2"}
//	server -> client stream:   {"type":"welcome","you":{...},"token":"...","grid":[w,h]}
//	                           {"type":"presence","room":"...","players":[...]}
//	client -> server datagram: {"type":"ping","ct":...}
//	server -> client datagram: {"type":"tick","n":..,"st":..,"behavior":"..",
//	                            "dogs":[[id,x,y,skin],...]}
//	                           {"type":"pong","ct":...,"st":...,"addr":"..."}
package main

import (
	"bufio"
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
	lua "github.com/yuin/gopher-lua"
)

//go:embed static/mythra.html
var mythraHTML []byte

//go:embed behaviors/random_walk.lua
var defaultBehavior string

const (
	gridW = 100
	gridH = 100
)

var (
	mTickDur = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "presence_tick_duration_seconds",
		Help:    "Wall time of one full tick (all rooms): budget is 1/tickrate.",
		Buckets: []float64{.0005, .001, .0025, .005, .01, .02, .03, .0417, .06, .1, .25},
	})
	mSessions = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "presence_sessions", Help: "Connected sessions."}, []string{"role"})
	mRooms = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "presence_rooms_occupied", Help: "Rooms with at least one occupant."})
	mHandshakes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "presence_handshakes_total", Help: "Session handshakes."}, []string{"result"})
	mResumes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "presence_resumes_total", Help: "Token-resumed sessions."}, []string{"result"})
	mDgSent = promauto.NewCounter(prometheus.CounterOpts{
		Name: "presence_datagrams_sent_total", Help: "Datagrams sent."})
	mDgErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "presence_datagram_errors_total", Help: "SendDatagram failures."})
	mDrops = promauto.NewCounter(prometheus.CounterOpts{
		Name: "presence_fanout_dropped_total", Help: "Stream messages dropped on stalled clients."})
	mBehaviorReloads = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "presence_behavior_reloads_total", Help: "Behavior script hot-reloads."}, []string{"slot", "result"})
	mBehaviorInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "presence_behavior_script", Help: "1 for the currently loaded script hash per slot."}, []string{"slot", "hash"})
	mShadowSteps = promauto.NewCounter(prometheus.CounterOpts{
		Name: "presence_shadow_steps_total", Help: "Shadow behavior evaluations."})
	mShadowDivergence = promauto.NewCounter(prometheus.CounterOpts{
		Name: "presence_shadow_divergence_total", Help: "Shadow steps whose output differed from live."})
	mShadowErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "presence_shadow_errors_total", Help: "Shadow script eval errors."})
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

// ---------- deterministic per-(dog,tick) randomness ----------

// detRand hashes (dog id, tick, live salt) so identical scripts in live and
// shadow slots see identical rolls and diff to zero divergence.
func detRand(dogID string, tick uint64, n int) int {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], tick)
	h := sha256.Sum256(append([]byte(dogID), b[:]...))
	return int(binary.LittleEndian.Uint32(h[:4]) % uint32(n))
}

// ---------- behavior engine ----------

type behaviorSlot struct {
	mu     sync.Mutex
	name   string // "live" | "shadow"
	src    string
	hash   string
	vm     *lua.LState
	stepFn lua.LValue
	dogID  string // ambient args for the rand() host fn during a step call
	tick   uint64
}

func newBehaviorSlot(name string) *behaviorSlot { return &behaviorSlot{name: name} }

func (b *behaviorSlot) load(src string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	sum := sha256.Sum256([]byte(src))
	hash := hex.EncodeToString(sum[:4])
	if hash == b.hash {
		return nil
	}
	vm := lua.NewState(lua.Options{SkipOpenLibs: false})
	vm.SetGlobal("rand", vm.NewFunction(func(L *lua.LState) int {
		n := L.CheckInt(1)
		if n < 1 {
			n = 1
		}
		L.Push(lua.LNumber(detRand(b.dogID, b.tick, n)))
		return 1
	}))
	if err := vm.DoString(src); err != nil {
		vm.Close()
		mBehaviorReloads.WithLabelValues(b.name, "error").Inc()
		return fmt.Errorf("%s: %w", b.name, err)
	}
	stepFn := vm.GetGlobal("step")
	if stepFn.Type() != lua.LTFunction {
		vm.Close()
		mBehaviorReloads.WithLabelValues(b.name, "error").Inc()
		return fmt.Errorf("%s: script defines no step()", b.name)
	}
	if b.vm != nil {
		b.vm.Close()
	}
	mBehaviorInfo.DeletePartialMatch(prometheus.Labels{"slot": b.name})
	mBehaviorInfo.WithLabelValues(b.name, hash).Set(1)
	mBehaviorReloads.WithLabelValues(b.name, "ok").Inc()
	b.src, b.hash, b.vm, b.stepFn = src, hash, vm, stepFn
	log.Printf("behavior %s loaded: %s", b.name, hash)
	return nil
}

func (b *behaviorSlot) unload() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.vm != nil {
		b.vm.Close()
		b.vm, b.stepFn, b.src, b.hash = nil, nil, "", ""
		mBehaviorInfo.DeletePartialMatch(prometheus.Labels{"slot": b.name})
		log.Printf("behavior %s unloaded", b.name)
	}
}

func (b *behaviorSlot) loaded() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hash, b.vm != nil
}

func (b *behaviorSlot) step(tick uint64, dogID string, x, y int) (dx, dy int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.vm == nil {
		return 0, 0, nil
	}
	b.dogID, b.tick = dogID, tick
	if err := b.vm.CallByParam(lua.P{Fn: b.stepFn, NRet: 2, Protect: true},
		lua.LNumber(tick), lua.LString(dogID), lua.LNumber(x), lua.LNumber(y),
		lua.LNumber(gridW), lua.LNumber(gridH)); err != nil {
		return 0, 0, err
	}
	dyv := b.vm.Get(-1)
	dxv := b.vm.Get(-2)
	b.vm.Pop(2)
	clamp := func(v lua.LValue) int {
		n, _ := v.(lua.LNumber)
		i := int(n)
		if i > 1 {
			i = 1
		}
		if i < -1 {
			i = -1
		}
		return i
	}
	return clamp(dxv), clamp(dyv), nil
}

// watchBehaviors polls the mounted behavior dir; ConfigMap edits land on the
// mount within ~a minute of Flux applying them, with no pod restart.
func watchBehaviors(dir string, live, shadow *behaviorSlot) {
	load := func() {
		if src, err := os.ReadFile(filepath.Join(dir, "live.lua")); err == nil {
			if err := live.load(string(src)); err != nil {
				log.Printf("behavior live reload rejected (keeping current): %v", err)
			}
		}
		if src, err := os.ReadFile(filepath.Join(dir, "shadow.lua")); err == nil {
			if err := shadow.load(string(src)); err != nil {
				log.Printf("behavior shadow reload rejected: %v", err)
			}
		} else if _, ok := shadow.loaded(); ok {
			shadow.unload()
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
}

type Hub struct {
	mu      sync.Mutex
	players map[string]*Player
	rooms   map[string]map[*Player]bool
	tick    uint64
	tickHz  int
	live    *behaviorSlot
	shadow  *behaviorSlot
	assets  *assetCatalog
}

func nowMs() int64 { return time.Now().UnixMilli() }

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func newHub(tickHz int, live, shadow *behaviorSlot, assets *assetCatalog) *Hub {
	return &Hub{
		players: map[string]*Player{}, rooms: map[string]map[*Player]bool{},
		tickHz: tickHz, live: live, shadow: shadow, assets: assets,
	}
}

func (h *Hub) moveLocked(p *Player, room string) {
	if cur, ok := h.rooms[p.Room]; ok {
		delete(cur, p)
		if len(cur) == 0 {
			delete(h.rooms, p.Room)
		}
	}
	p.Room = room
	if room != "" {
		if h.rooms[room] == nil {
			h.rooms[room] = map[*Player]bool{}
		}
		h.rooms[room][p] = true
	}
	mRooms.Set(float64(len(h.rooms)))
}

func (h *Hub) broadcastRoom(room string) {
	h.mu.Lock()
	occ := h.rooms[room]
	list := make([]*Player, 0, len(occ))
	for p := range occ {
		list = append(list, p)
	}
	msg, _ := json.Marshal(map[string]any{"type": "presence", "room": room, "players": list, "st": nowMs()})
	msg = append(msg, '\n')
	for p := range occ {
		h.sendLocked(p, msg)
	}
	h.mu.Unlock()
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
// behavior for comparison when one is mounted, and snapshots each room.
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
		for _, occ := range h.rooms {
			dogs := make([][4]any, 0, len(occ))
			for p := range occ {
				if p.Role != "player" {
					continue
				}
				dx, dy, err := h.live.step(h.tick, p.ID, p.X, p.Y)
				if err != nil {
					log.Printf("live behavior error (dog %s frozen this tick): %v", p.ID, err)
					dx, dy = 0, 0
				}
				if shadowLoaded {
					sdx, sdy, serr := h.shadow.step(h.tick, p.ID, p.X, p.Y)
					mShadowSteps.Inc()
					if serr != nil {
						mShadowErrors.Inc()
					} else if sdx != dx || sdy != dy {
						mShadowDivergence.Inc()
					}
				}
				p.X = clampInt(p.X+dx, 0, gridW-1)
				p.Y = clampInt(p.Y+dy, 0, gridH-1)
				dogs = append(dogs, [4]any{p.ID, p.X, p.Y, skin})
			}
			msg, _ := json.Marshal(map[string]any{
				"type": "tick", "n": h.tick, "st": st, "behavior": liveHash, "dogs": dogs,
			})
			for p := range occ {
				if p.sess == nil {
					continue
				}
				if err := p.sess.SendDatagram(msg); err != nil {
					mDgErrors.Inc()
				} else {
					mDgSent.Inc()
				}
			}
		}
		h.mu.Unlock()
		mTickDur.Observe(time.Since(start).Seconds())
	}
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
	for _, q := range h.players {
		if q.sess != nil {
			counts[q.Role]++
		}
	}
	mSessions.WithLabelValues("player").Set(float64(counts["player"]))
	mSessions.WithLabelValues("spectator").Set(float64(counts["spectator"]))
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
			X: detRand(randHex(4), 0, gridW), Y: detRand(randHex(4), 1, gridH),
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
	p.LastSeen = nowMs()
	p.sess = sess
	p.out = make(chan []byte, 64)
	out := p.out
	h.updateSessionGauges()
	h.mu.Unlock()
	mHandshakes.WithLabelValues("ok").Inc()

	welcome, _ := json.Marshal(map[string]any{
		"type": "welcome", "you": p, "token": p.token, "resumed": resumed,
		"grid": [2]int{gridW, gridH},
	})
	stream.Write(append(welcome, '\n'))

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
	h.broadcastRoom(p.Room)

	for sc.Scan() {
		var m struct{ Type, Room string }
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Type == "join" && m.Room != "" {
			h.mu.Lock()
			old := p.Room
			h.moveLocked(p, m.Room)
			p.LastSeen = nowMs()
			h.mu.Unlock()
			h.broadcastRoom(old)
			h.broadcastRoom(m.Room)
		}
	}

	h.mu.Lock()
	if p.sess == sess {
		h.moveLocked(p, "")
		p.sess = nil
		p.out = nil
		p.LastSeen = nowMs()
	}
	room = p.Room
	h.updateSessionGauges()
	h.mu.Unlock()
	h.broadcastRoom(room)
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
		pong, _ := json.Marshal(map[string]any{"type": "pong", "ct": m.CT, "st": nowMs(), "addr": remote})
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
		Subject:      pkix.Name{CommonName: "presenced"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 24 * time.Hour),
		DNSNames:     []string{"presenced", "localhost"},
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

func main() {
	tickHz := envInt("TICK_HZ", 24)
	wtPort := envInt("WT_PORT", 4433)
	httpPort := envInt("HTTP_PORT", 9634)
	metricsPort := envInt("METRICS_PORT", 9633)
	behaviorDir := envStr("BEHAVIOR_DIR", "/etc/mythra/behavior")
	assetDir := envStr("ASSET_DIR", "/etc/mythra/assets")
	publicAddr := envStr("PUBLIC_ADDR", "") // "ip:port" advertised to clients
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

	live := newBehaviorSlot("live")
	shadow := newBehaviorSlot("shadow")
	if err := live.load(defaultBehavior); err != nil {
		log.Fatalf("embedded default behavior invalid: %v", err)
	}
	go watchBehaviors(behaviorDir, live, shadow)

	assets := newAssetCatalog(assetDir)
	hub := newHub(tickHz, live, shadow, assets)
	go hub.tickLoop()
	go hub.purgeLoop()

	wtMux := http.NewServeMux()
	wt := webtransport.Server{
		H3: &http3.Server{
			Addr: fmt.Sprintf(":%d", wtPort),
			TLSConfig: &tls.Config{
				GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
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

	// Demo page + connect info + content-addressed assets, served through the
	// normal Cloudflare-proxied ingress. The WebTransport dial goes direct to
	// PUBLIC_ADDR with the cert hash from /wt-info.
	pageMux := http.NewServeMux()
	pageMux.HandleFunc("/mythra/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(mythraHTML)
	})
	pageMux.HandleFunc("/mythra/wt-info", func(w http.ResponseWriter, r *http.Request) {
		_, hash := rc.get()
		addr := publicAddr
		if addr == "" {
			addr = "127.0.0.1:" + strconv.Itoa(wtPort)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]any{
			"addr":        addr,
			"certHashB64": base64.StdEncoding.EncodeToString(hash[:]),
		})
	})
	pageMux.HandleFunc("/mythra/assets/", func(w http.ResponseWriter, r *http.Request) {
		ref := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/mythra/assets/"), ".svg")
		body, ok := assets.get(ref)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Write(body)
	})

	obsMux := http.NewServeMux()
	obsMux.Handle("/metrics", promhttp.Handler())
	obsMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })

	log.Printf("presenced: wt=:%d http=:%d metrics=:%d tick=%dHz public=%s", wtPort, httpPort, metricsPort, tickHz, publicAddr)
	go func() { log.Fatal(wt.ListenAndServe()) }()
	go func() { log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", httpPort), pageMux)) }()
	go func() { log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", metricsPort), obsMux)) }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	log.Print("SIGTERM: closing sessions (clients resume against replacement)")
	wt.Close()
}
