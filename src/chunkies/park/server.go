package park

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
	"github.com/guardian-intelligence/guardian/src/chunkies/parkproxy"
	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
	"github.com/guardian-intelligence/guardian/src/shared/go/telemetry"
)

// Run is the chunkies-park process: one configured park authority behind
// the authenticated gateway transport.
func Run() {
	parkName := envStr("PARK_NAME", "")
	if parkName == "" {
		log.Fatal("PARK_NAME not set")
	}
	internalHost := envStr("INTERNAL_HOST", "127.0.0.1")
	parkPort := envInt("PARK_PORT", 9632)
	httpPort := envInt("HTTP_PORT", 9631)
	metricsPort := envInt("METRICS_PORT", 9637)
	maxSessions := envInt("MAX_SESSIONS", 4000)
	tickHz := envInt("TICK_HZ", 24)
	if tickHz < minTickHz || tickHz > maxTickHz {
		log.Fatalf("TICK_HZ=%d outside supported range %d..%d", tickHz, minTickHz, maxTickHz)
	}
	key, err := parkproxy.ReadKey(envStr("INTERNAL_KEY_FILE", ""))
	if err != nil {
		log.Fatalf("internal key: %v", err)
	}
	// The game arrives as content: modules through the behavior mount, and
	// the genesis artifact plus vocabulary manifest as boot-read files.
	vocab, err := loadVocab(os.Getenv("GAME_MANIFEST_FILE"))
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

	traceShutdown, err := telemetry.Init(ctx, "chunkies-park", os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	if err != nil {
		log.Fatalf("tracing: %v", err)
	}
	defer traceShutdown(context.Background())

	behaviorDir := envStr("BEHAVIOR_DIR", "/etc/chunkies/behavior")
	client := mount.NewModule("client", mount.DefaultClient)
	parkModule := mount.NewModule("park", mount.DefaultPark)
	go mount.Watch(behaviorDir, acceptModule, client, parkModule)

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

	mods := &modules{client: client, park: parkModule}
	registry := newParks(func() []byte { b, _ := parkModule.Get(); return b }, genesis, vocab, j, mods, timing{hz: tickHz})
	authority, err := registry.get(ctx, parkName)
	if err != nil {
		log.Fatalf("open park: %v", err)
	}

	var ready atomic.Bool
	internalAddr := net.JoinHostPort(internalHost, strconv.Itoa(parkPort))
	internal, err := net.Listen("tcp", internalAddr)
	if err != nil {
		log.Fatalf("park listen: %v", err)
	}
	defer internal.Close()
	go func() {
		for {
			conn, err := internal.Accept()
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("park accept: %v", err)
				}
				return
			}
			go handleParkConn(conn, key, authority, maxSessions)
		}
	}()

	parkMux := http.NewServeMux()
	parkMux.HandleFunc("/terrain/", terrainHandler(j, &ready, genesis))
	if os.Getenv("CHUNKIES_DEV_LIVE_TICK_RATE") == "true" {
		parkMux.HandleFunc("/dev/tick-rate", devTickRateHandler(registry, map[string]bool{parkName: true}))
	}
	parkHTTPAddr := net.JoinHostPort(internalHost, strconv.Itoa(httpPort))
	parkHTTP := &http.Server{Addr: parkHTTPAddr, Handler: parkMux}
	go func() {
		if err := parkHTTP.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("park http: %v", err)
			stop()
		}
	}()

	obsMux := http.NewServeMux()
	obsMux.Handle("/metrics", promhttp.Handler())
	obsMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	obsMux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() || authority.isClosed() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	obs := &http.Server{Addr: fmt.Sprintf(":%d", metricsPort), Handler: obsMux}
	go func() {
		if err := obs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("park metrics: %v", err)
			stop()
		}
	}()

	ready.Store(true)
	log.Printf("chunkies-park: park=%s sessions=%s http=%s metrics=:%d", parkName, internalAddr, parkHTTPAddr, metricsPort)
	<-ctx.Done()
	ready.Store(false)
	internal.Close()
	authority.close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	parkHTTP.Shutdown(shutdownCtx)
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

// connSession is one multiplexed session's park-side state. The demux
// loop routes its frames here; run's two goroutines (writer, intent
// drain) mirror the pair the per-session transport used to spawn.
type connSession struct {
	pc      *parkproxy.Conn
	sid     uint64
	s       *session
	open    parkproxy.Open
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

func handleParkConn(conn net.Conn, key []byte, park *authority, maxSessions int) {
	pc, err := parkproxy.Accept(conn, key, time.Now())
	if err != nil {
		return
	}
	defer pc.Close()
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
			cs.terminate("park unavailable", false)
		}
	}()

	for {
		msg, err := pc.ReadMessage()
		if err != nil {
			return
		}
		switch msg.Kind {
		case parkproxy.KindOpen:
			if lookup(msg.SID) != nil {
				// The gateway is the only authenticated peer and never
				// reuses an id; reuse means the connection state itself is
				// not to be trusted any further.
				return
			}
			open, err := parkproxy.DecodeOpen(msg.Payload)
			if err != nil {
				// A malformed open is that session's failure (a version-skew
				// window during a rolling deploy), not grounds to drop every
				// live session on the pair.
				if pc.WriteClose(msg.SID, "bad open") != nil {
					return
				}
				continue
			}
			if open.Park != park.name {
				if pc.WriteClose(msg.SID, "wrong park") != nil {
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
			cs.reap = func() {
				mu.Lock()
				delete(sessions, cs.sid)
				mu.Unlock()
			}
			cs.s = &session{
				sub: open.Sub, role: open.Role, park: park, out: make(chan []byte, 256),
				dogID: codec.ActorFor(open.Sub), openedAt: time.Now(),
				closeFn: func(why string) { cs.terminate(why, true) },
			}
			mu.Lock()
			sessions[msg.SID] = cs
			mu.Unlock()
			go cs.run()
		case parkproxy.KindStream:
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
		case parkproxy.KindDatagram:
			if lookup(msg.SID) == nil {
				continue
			}
			if verdict, ok := checkVerdict(park, msg.Payload); ok {
				if pc.WriteDatagram(msg.SID, verdict) != nil {
					return
				}
			}
		case parkproxy.KindClose:
			if cs := lookup(msg.SID); cs != nil {
				cs.terminate("", false)
			}
		}
	}
}

func (cs *connSession) run() {
	defer sessionCount.Add(-1)
	park := cs.s.park
	attached := make(chan attachResult, 1)
	select {
	case park.attach <- attachReq{sess: cs.s, sinceSeq: cs.open.SinceSeq, sinceTick: cs.open.SinceTick, done: attached}:
	case <-park.stop:
		cs.terminate("park unavailable", true)
		return
	case <-cs.done:
		return
	}
	res := <-attached
	if res.err != nil {
		cs.terminate("park unavailable", true)
		return
	}
	defer park.detach(cs.s)
	defer stageDeparture(park, cs.s)

	go func() {
		// A failed write may have left a partial frame on the shared
		// connection, which the framing cannot resync from: down the whole
		// connection, not just this session.
		write := func(b []byte) bool {
			if err := cs.pc.WriteStream(cs.sid, b); err != nil {
				cs.pc.Close()
				cs.terminate("park unavailable", false)
				return false
			}
			return true
		}
		if !write(res.welcome) {
			return
		}
		for _, frame := range park.catchupFrames(cs.open.SinceSeq, cs.open.SinceTick, res) {
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
	case cs.s.park.attach <- attachReq{sess: cs.s, resync: true, done: make(chan attachResult, 1)}:
	case <-cs.s.park.stop:
		cs.terminate("park unavailable", true)
	}
}

func (cs *connSession) handleFrame(payload []byte) {
	s, park := cs.s, cs.s.park
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
		park.stageIntent(s, rec.Intent, rec.Kind, rec.Payload)
	case codec.KindResync:
		if _, err := codec.DecodeResync(framePayload); err != nil {
			return
		}
		cs.requestResync()
	}
}

// stageDeparture stages the game's departure event, if it declares one.
func stageDeparture(park *authority, s *session) {
	if s.role != "player" || park.vocab.DepartKind == 0 {
		return
	}
	park.stageIntent(s, 0, park.vocab.DepartKind, nil)
}
