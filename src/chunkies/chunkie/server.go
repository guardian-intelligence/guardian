package chunkie

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/guardian-intelligence/guardian/src/chunkies/journal"
	"github.com/guardian-intelligence/guardian/src/chunkies/mount"
	"github.com/guardian-intelligence/guardian/src/chunkies/trunk"
	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
	"github.com/guardian-intelligence/guardian/src/shared/go/telemetry"
)

// Run is the chunkies-chunkie process: one configured chunk authority behind
// the authenticated gateway transport.
func Run() {
	chunkName := envStr("CHUNK_NAME", "")
	if chunkName == "" {
		log.Fatal("CHUNK_NAME not set")
	}
	game := envStr("GAME", "")
	if game == "" {
		log.Fatal("GAME not set")
	}
	internalHost := envStr("INTERNAL_HOST", "127.0.0.1")
	trunkPort := envInt("TRUNK_PORT", 9632)
	httpPort := envInt("HTTP_PORT", 9631)
	metricsPort := envInt("METRICS_PORT", 9637)
	maxSessions := envInt("MAX_SESSIONS", 4000)
	tickHz := envInt("TICK_HZ", 24)
	if tickHz < minTickHz || tickHz > maxTickHz {
		log.Fatalf("TICK_HZ=%d outside supported range %d..%d", tickHz, minTickHz, maxTickHz)
	}
	key, err := trunk.ReadKey(envStr("INTERNAL_KEY_FILE", ""))
	if err != nil {
		log.Fatalf("internal key: %v", err)
	}
	// The game arrives as content: modules through the behavior mount, and
	// the genesis artifact plus vocabulary manifest as boot-read files.
	// The manifest is required and fails loud like every other identity
	// env; a game that truly needs no vocabulary says so explicitly.
	manifestFile := envStr("GAME_MANIFEST_FILE", "")
	if manifestFile == "" {
		log.Fatal("GAME_MANIFEST_FILE not set (GAME_MANIFEST_FILE=none declares a bare vocabulary)")
	}
	if manifestFile == "none" {
		manifestFile = ""
	}
	vocab, err := loadVocab(manifestFile)
	if err != nil {
		log.Fatalf("game manifest: %v", err)
	}
	var genesis []byte
	if path := os.Getenv("GENESIS_FILE"); path != "" {
		genesis, err = os.ReadFile(path)
		if err != nil {
			log.Fatalf("genesis artifact: %v", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	traceShutdown, err := telemetry.Init(ctx, "chunkies-chunkie", os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	if err != nil {
		log.Fatalf("tracing: %v", err)
	}
	defer traceShutdown(context.Background())

	behaviorDir := envStr("BEHAVIOR_DIR", "/etc/chunkies/behavior")
	client := mount.NewModule("client")
	simModule := mount.NewModule("sim")
	// The game arrives entirely as mounted content, and the mount exists
	// before the process does (a ConfigMap volume mounts before the
	// container starts): one synchronous scan, then crash loudly if the
	// pod was declared without its game.
	mount.Load(behaviorDir, acceptModule, client, simModule)
	if err := mount.Require(client, simModule); err != nil {
		log.Fatalf("%v (BEHAVIOR_DIR=%s)", err, behaviorDir)
	}
	go mount.Watch(behaviorDir, acceptModule, client, simModule)

	dsn, err := databaseURL()
	if err != nil {
		log.Fatalf("journal database: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("journal pool: %v", err)
	}
	defer pool.Close()
	j := journal.NewPg(pool)
	for {
		migrateCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = j.Migrate(migrateCtx)
		cancel()
		if err == nil {
			break
		}
		log.Printf("journal unavailable: %v", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}

	mods := &modules{client: client, sim: simModule}
	// One chunk per chunkie today; the queue depth scales with the
	// served set when that changes.
	bcast := newBroadcaster(game, tickHz, 1)
	registry := newChunks(func() []byte { b, _ := simModule.Get(); return b }, genesis, vocab, j, mods, timing{hz: tickHz}, bcast.publish)
	auth, err := registry.get(ctx, chunkName)
	if err != nil {
		log.Fatalf("open chunk: %v", err)
	}

	var ready atomic.Bool
	internalAddr := net.JoinHostPort(internalHost, strconv.Itoa(trunkPort))
	internal, err := net.Listen("tcp", internalAddr)
	if err != nil {
		log.Fatalf("chunk listen: %v", err)
	}
	defer internal.Close()
	resolve := func(name string) (*authority, bool) {
		if name != chunkName {
			return nil, false
		}
		return auth, true
	}
	go func() {
		for {
			conn, err := internal.Accept()
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("chunk accept: %v", err)
				}
				return
			}
			go handleTrunkConn(conn, key, game, resolve, maxSessions, bcast)
		}
	}()

	chunkieMux := http.NewServeMux()
	chunkieMux.HandleFunc("/terrain/", terrainHandler(j, &ready, genesis))
	if os.Getenv("CHUNKIES_DEV_LIVE_TICK_RATE") == "true" {
		chunkieMux.HandleFunc("/dev/tick-rate", devTickRateHandler(registry, map[string]bool{chunkName: true}))
	}
	chunkieHTTPAddr := net.JoinHostPort(internalHost, strconv.Itoa(httpPort))
	chunkieHTTP := &http.Server{Addr: chunkieHTTPAddr, Handler: chunkieMux}
	go func() {
		if err := chunkieHTTP.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("chunk http: %v", err)
			stop()
		}
	}()

	obsMux := http.NewServeMux()
	obsMux.Handle("/metrics", promhttp.Handler())
	obsMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	obsMux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() || auth.isClosed() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	obs := &http.Server{Addr: fmt.Sprintf(":%d", metricsPort), Handler: obsMux}
	go func() {
		if err := obs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("chunk metrics: %v", err)
			stop()
		}
	}()

	ready.Store(true)
	log.Printf("chunkies-chunkie: chunk=%s sessions=%s http=%s metrics=:%d", chunkName, internalAddr, chunkieHTTPAddr, metricsPort)
	<-ctx.Done()
	ready.Store(false)
	internal.Close()
	auth.close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	chunkieHTTP.Shutdown(shutdownCtx)
	obs.Shutdown(shutdownCtx)
}

func terrainHandler(j journal.Journal, ready *atomic.Bool, genesis []byte) http.HandlerFunc {
	genesisID := terrainID(genesis)
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseUint(strings.TrimPrefix(r.URL.Path, "/terrain/"), 16, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// The genesis artifact serves from memory; every other content id
		// is a journal read.
		blob := genesis
		if len(genesis) == 0 || id != genesisID {
			if !ready.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			var found bool
			blob, found, err = j.TerrainBlob(ctx, id)
			if err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if !found {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Write(blob)
	}
}

// broadcaster is the publish-once half of tick fan-out: each committed
// tick is encoded once and handed to every gateway connection with a
// live subscriber for the chunk; the gateway replicates to its local
// attachments. A conn subscribes a chunk while it has sessions open on
// it, so an idle conn costs no tick writes.
type broadcaster struct {
	game string
	// queueFrames/queueBytes bound each conn's broadcast queue; fields
	// so tests can shrink them.
	queueFrames int
	queueBytes  int64

	mu    sync.Mutex
	conns map[*broadcastConn]struct{}
}

// Queue bounds. Depth is time-based — broadcastQueueSecs of frames at
// the boot tick rate, one frame per subscribed chunk per tick — pinned
// between two ceilings: comfortably above scheduler/GC hiccup scale,
// and under the trunk's 10s write deadline, so a peer that stops
// draining always surfaces as a deterministic queue-full shed rather
// than a raced write error. The byte bound is the gateway splice
// buffer's sibling: payloads are shared across conns, so it caps the
// pathological large-frame case, not the steady state.
const (
	broadcastQueueSecs     = 5
	broadcastQueueMinDepth = 64
	broadcastQueueBytes    = 8 << 20
)

// broadcastWriter is what the broadcaster needs from a trunk conn — a
// seam so tests can wedge the write side deterministically.
type broadcastWriter interface {
	WriteMessage(kind byte, sid uint64, payload []byte) error
	Close() error
}

type broadcastConn struct {
	pc broadcastWriter

	mu   sync.Mutex
	subs map[string]int

	// queue feeds the conn's writer goroutine and queued counts its
	// bytes; stop ends the writer. once makes teardown idempotent
	// between the shed paths and unregister.
	queue  chan []byte
	queued atomic.Int64
	stop   chan struct{}
	once   sync.Once
}

func newBroadcaster(game string, hz, chunks int) *broadcaster {
	return &broadcaster{
		game:        game,
		queueFrames: max(broadcastQueueSecs*hz*chunks, broadcastQueueMinDepth),
		queueBytes:  broadcastQueueBytes,
		conns:       map[*broadcastConn]struct{}{},
	}
}

func (b *broadcaster) register(pc broadcastWriter) *broadcastConn {
	c := &broadcastConn{pc: pc, subs: map[string]int{}, queue: make(chan []byte, b.queueFrames), stop: make(chan struct{})}
	b.mu.Lock()
	b.conns[c] = struct{}{}
	b.mu.Unlock()
	go b.write(c)
	return c
}

// remove tears a conn down: out of the publish registry synchronously,
// writer stopped, conn closed — its sessions redial through the
// gateway. shed marks the one countable cause, a full queue; read-side
// teardown (unregister) and write errors stay uncounted, since both are
// dominated by ordinary conn death and a genuinely wedged peer fills
// the queue well before the write deadline expires.
func (b *broadcaster) remove(c *broadcastConn, shed bool) {
	b.mu.Lock()
	delete(b.conns, c)
	b.mu.Unlock()
	c.once.Do(func() {
		if shed {
			mBroadcastSheds.Inc()
		}
		c.pc.Close()
		close(c.stop)
	})
}

func (b *broadcaster) unregister(c *broadcastConn) { b.remove(c, false) }

// write is the conn's broadcast writer: the only goroutine putting tick
// fan-out on this wire, so per-conn broadcast order is the queue's FIFO
// order. A wedged peer blocks here — never the tick loop — until a
// queue-full shed or a write-deadline error downs the conn.
func (b *broadcaster) write(c *broadcastConn) {
	// On exit, release whatever the queue still pins.
	defer func() {
		for {
			select {
			case p := <-c.queue:
				c.queued.Add(-int64(len(p)))
			default:
				return
			}
		}
	}()
	for {
		select {
		case payload := <-c.queue:
			c.queued.Add(-int64(len(payload)))
			if c.pc.WriteMessage(trunk.KindBroadcast, 0, payload) != nil {
				b.remove(c, false)
				return
			}
		case <-c.stop:
			return
		}
	}
}

func (c *broadcastConn) subscribe(chunk string) {
	c.mu.Lock()
	c.subs[chunk]++
	c.mu.Unlock()
}

func (c *broadcastConn) unsubscribe(chunk string) {
	c.mu.Lock()
	if c.subs[chunk]--; c.subs[chunk] <= 0 {
		delete(c.subs, chunk)
	}
	c.mu.Unlock()
}

func (c *broadcastConn) subscribed(chunk string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subs[chunk] > 0
}

// publish is the authority's tick fan-out (publishFunc). The payload is
// encoded once, synchronously — the caller reuses the run buffer next
// tick — and delivery is detached: enqueue per conn and return. The
// tick loop never touches the network, so a peer that stops draining
// costs its own connection (queue overflow downs it; sessions redial
// through the gateway) instead of holding the tick — a stall the WAL's
// lag watchdog could never see, since nothing is appended while a tick
// waits on a write.
func (b *broadcaster) publish(chunk string, tick uint64, firstSeq int64, count uint16, run []byte) {
	payload, err := trunk.EncodeBroadcast(b.game, chunk, codec.EncodeTick(tick, firstSeq, count, run))
	if err != nil {
		// A name too long for the wire also has no subscribers (Open caps
		// the same strings), so there is nothing to write.
		return
	}
	// The snapshot is load-bearing: remove takes b.mu, so shedding from
	// inside the range would deadlock.
	b.mu.Lock()
	targets := make([]*broadcastConn, 0, len(b.conns))
	for c := range b.conns {
		if c.subscribed(chunk) {
			targets = append(targets, c)
		}
	}
	b.mu.Unlock()
	n := int64(len(payload))
	for _, c := range targets {
		if c.queued.Add(n) > b.queueBytes {
			c.queued.Add(-n)
			b.remove(c, true)
			continue
		}
		select {
		case c.queue <- payload:
		default:
			c.queued.Add(-n)
			b.remove(c, true)
		}
	}
}

// connSession is one multiplexed session's chunk-side state. The demux
// loop routes its frames here; run's two goroutines (writer, intent
// drain) mirror the pair the per-session transport used to spawn.
type connSession struct {
	pc      *trunk.Conn
	sid     uint64
	s       *session
	open    trunk.Open
	inbound chan []byte
	done    chan struct{}
	reap    func()
	once    sync.Once
	// resyncShed remembers a resync request shed under inbound pressure;
	// the drain replays it once it resumes.
	resyncShed atomic.Bool
}

// terminate ends this session only; the shared connection lives on for
// the others. It is reached from the tick loop (a fan-out backlog kick
// runs under the authority's lock), so it must never block on a network
// write.
func (cs *connSession) terminate(why string, notify bool) {
	cs.once.Do(func() {
		cs.reap()
		if notify {
			go func() {
				if cs.pc.WriteClose(cs.sid, why) != nil {
					// A failed write may have left a partial frame; the
					// framing cannot resync, so the connection is done.
					cs.pc.Close()
				}
			}()
		}
		close(cs.done)
	})
}

func handleTrunkConn(conn net.Conn, key []byte, game string, resolve func(chunk string) (*authority, bool), maxSessions int, b *broadcaster) {
	pc, err := trunk.Accept(conn, key, time.Now())
	if err != nil {
		return
	}
	defer pc.Close()
	bc := b.register(pc)
	defer b.unregister(bc)
	// The demux loop is the only writer of the map's entries; terminate
	// (reachable from the authority's goroutines) deletes through reap, so
	// access is locked.
	var mu sync.Mutex
	sessions := map[uint64]*connSession{}
	lookup := func(sid uint64) *connSession {
		mu.Lock()
		defer mu.Unlock()
		return sessions[sid]
	}
	defer func() {
		mu.Lock()
		all := make([]*connSession, 0, len(sessions))
		for _, cs := range sessions {
			all = append(all, cs)
		}
		mu.Unlock()
		for _, cs := range all {
			cs.terminate("chunk unavailable", false)
		}
	}()

	for {
		msg, err := pc.ReadMessage()
		if err != nil {
			return
		}
		switch msg.Kind {
		case trunk.KindOpen:
			if lookup(msg.SID) != nil {
				// The gateway is the only authenticated peer and never
				// reuses an id; reuse means the connection state itself is
				// not to be trusted any further.
				return
			}
			open, err := trunk.DecodeOpen(msg.Payload)
			if err != nil {
				// A malformed open is that session's failure (a version-skew
				// window during a rolling deploy), not grounds to drop every
				// live session on the pair.
				if pc.WriteClose(msg.SID, "bad open") != nil {
					return
				}
				continue
			}
			if open.Game != game {
				if pc.WriteClose(msg.SID, "wrong game") != nil {
					return
				}
				continue
			}
			chunk, ok := resolve(open.Chunk)
			if !ok {
				if pc.WriteClose(msg.SID, "wrong chunk") != nil {
					return
				}
				continue
			}
			if n := sessionCount.Add(1); n > int64(maxSessions) {
				sessionCount.Add(-1)
				if pc.WriteClose(msg.SID, "at capacity") != nil {
					return
				}
				continue
			}
			cs := &connSession{
				pc: pc, sid: msg.SID, open: open,
				inbound: make(chan []byte, 256), done: make(chan struct{}),
			}
			// Subscribed before the session attaches, so no committed tick
			// can fall between the authority registering the session and
			// this conn hearing broadcasts.
			bc.subscribe(open.Chunk)
			cs.reap = func() {
				mu.Lock()
				delete(sessions, cs.sid)
				mu.Unlock()
				bc.unsubscribe(cs.open.Chunk)
			}
			cs.s = &session{
				sub: open.Sub, role: open.Role, chunk: chunk, out: make(chan []byte, 256),
				dogID: codec.ActorFor(open.Sub), openedAt: time.Now(),
				closeFn: func(why string) { cs.terminate(why, true) },
			}
			mu.Lock()
			sessions[msg.SID] = cs
			mu.Unlock()
			go cs.run()
		case trunk.KindStream:
			cs := lookup(msg.SID)
			if cs == nil {
				continue
			}
			select {
			case cs.inbound <- msg.Payload:
			default:
				// A full buffer means the drain is stalled — usually behind
				// the authority (an attach or journal wait), not the client.
				// Shed the frame instead of the player: intents are
				// idempotent by (actor, intent_id), so nothing is double
				// applied and the disconnect is saved. A resync request
				// must survive the shed, though — its payload is unused, so
				// a flag replays it when the drain resumes.
				if kind, _, err := codec.NewReader(bytes.NewReader(msg.Payload)).Next(); err == nil && kind == codec.KindResync {
					cs.resyncShed.Store(true)
				} else {
					mInboundDropped.Inc()
				}
			}
		case trunk.KindDatagram:
			cs := lookup(msg.SID)
			if cs == nil {
				continue
			}
			if verdict, ok := checkVerdict(cs.s.chunk, msg.Payload); ok {
				if pc.WriteDatagram(msg.SID, verdict) != nil {
					return
				}
			}
		case trunk.KindClose:
			if cs := lookup(msg.SID); cs != nil {
				cs.terminate("", false)
			}
		}
	}
}

func (cs *connSession) run() {
	defer sessionCount.Add(-1)
	chunk := cs.s.chunk
	attached := make(chan attachResult, 1)
	select {
	case chunk.attach <- attachReq{sess: cs.s, sinceSeq: cs.open.SinceSeq, sinceTick: cs.open.SinceTick, done: attached}:
	case <-chunk.stop:
		cs.terminate("chunk unavailable", true)
		return
	case <-cs.done:
		return
	}
	res := <-attached
	if res.err != nil {
		cs.terminate("chunk unavailable", true)
		return
	}
	defer chunk.detach(cs.s)
	defer stageDeparture(chunk, cs.s)

	go func() {
		// A failed write may have left a partial frame on the shared
		// connection, which the framing cannot resync from: down the whole
		// connection, not just this session.
		write := func(b []byte) bool {
			if err := cs.pc.WriteStream(cs.sid, b); err != nil {
				cs.pc.Close()
				cs.terminate("chunk unavailable", false)
				return false
			}
			return true
		}
		if !write(res.welcome) {
			return
		}
		for _, frame := range chunk.catchupFrames(cs.open.SinceSeq, cs.open.SinceTick, res) {
			if !write(frame) {
				return
			}
		}
		for {
			select {
			case frame := <-cs.s.out:
				if !write(frame) {
					return
				}
			case <-cs.done:
				return
			}
		}
	}()

	for {
		select {
		case b := <-cs.inbound:
			cs.handleFrame(b)
			// The flag is only ever set while inbound is full, so there is
			// always a drained frame to hang this replay on.
			if cs.resyncShed.Swap(false) {
				cs.requestResync()
			}
		case <-cs.done:
			return
		}
	}
}

func (cs *connSession) requestResync() {
	mResyncs.Inc()
	select {
	case cs.s.chunk.attach <- attachReq{sess: cs.s, resync: true, done: make(chan attachResult, 1)}:
	case <-cs.s.chunk.stop:
		cs.terminate("chunk unavailable", true)
	}
}

func (cs *connSession) handleFrame(payload []byte) {
	s, chunk := cs.s, cs.s.chunk
	frameKind, framePayload, err := codec.NewReader(bytes.NewReader(payload)).Next()
	if err != nil {
		return
	}
	switch frameKind {
	case codec.KindIntent:
		rec, err := codec.DecodeIntent(framePayload)
		if err != nil {
			return
		}
		if s.role != "player" {
			s.sendReject(rec.Intent, codec.RejectReadOnly)
			return
		}
		// Game-blind binding: the envelope's actor must be the
		// authenticated session's, for every kind.
		if rec.Actor != s.dogID {
			s.sendReject(rec.Intent, codec.RejectNotYours)
			return
		}
		chunk.stageIntent(s, rec.Intent, rec.Kind, rec.Payload)
	case codec.KindResync:
		if _, err := codec.DecodeResync(framePayload); err != nil {
			return
		}
		cs.requestResync()
	}
}

// stageDeparture stages the game's departure event, if it declares one.
// Only the actor's current session may depart: a superseded session (its
// player rejoined before the transport reaped it) stages nothing, so a
// stale close can never remove the entity a live session stands on.
func stageDeparture(chunk *authority, s *session) {
	if s.role != "player" || chunk.vocab.DepartKind == 0 {
		return
	}
	chunk.mu.Lock()
	current := chunk.players[s.dogID] == s
	if current {
		delete(chunk.players, s.dogID)
	}
	chunk.mu.Unlock()
	if !current {
		return
	}
	chunk.stageIntent(s, 0, chunk.vocab.DepartKind, nil)
}
