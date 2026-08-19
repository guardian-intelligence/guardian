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
	"github.com/guardian-intelligence/guardian/src/games/wake-up-mythra/services/wum"
	"github.com/guardian-intelligence/guardian/src/shared/go/telemetry"
)

// Run is the chunkies-park process: one configured park authority behind
// the authenticated gateway transport.
func Run() {
	parkName := envStr("PARK_NAME", "park-mythra")
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	traceShutdown, err := telemetry.Init(ctx, "chunkies-park", os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	if err != nil {
		log.Fatalf("tracing: %v", err)
	}
	defer traceShutdown(context.Background())

	behaviorDir := envStr("BEHAVIOR_DIR", "/etc/mythra/behavior")
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
	registry := newParks(func() []byte { b, _ := parkModule.Get(); return b }, wum.FixtureTerrain, j, mods, timing{hz: tickHz})
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
	parkMux.HandleFunc("/terrain/", terrainHandler(j, &ready))
	if os.Getenv("WUM_DEV_LIVE_TICK_RATE") == "true" {
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

func terrainHandler(j journal.Journal, ready *atomic.Bool) http.HandlerFunc {
	fixtureID := terrainID(wum.FixtureTerrain)
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseUint(strings.TrimPrefix(r.URL.Path, "/terrain/"), 16, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		blob := wum.FixtureTerrain
		if id != fixtureID {
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
	once    sync.Once
}

// terminate ends this session only; the shared connection lives on for
// the others, so it must never be closed from here.
func (cs *connSession) terminate(why string, notify bool) {
	cs.once.Do(func() {
		if notify {
			cs.pc.WriteClose(cs.sid, why)
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
	// sessions is touched only by this demux loop; termination elsewhere
	// closes a session's done channel and the loop prunes it on the next
	// frame for that id.
	sessions := map[uint64]*connSession{}
	defer func() {
		for _, cs := range sessions {
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
			open, err := parkproxy.DecodeOpen(msg.Payload)
			if err != nil || sessions[msg.SID] != nil {
				// The gateway is the only authenticated peer; a malformed
				// open or a reused id means the connection state is not to
				// be trusted any further.
				return
			}
			if open.Park != park.name {
				pc.WriteClose(msg.SID, "wrong park")
				continue
			}
			if n := sessionCount.Add(1); n > int64(maxSessions) {
				sessionCount.Add(-1)
				pc.WriteClose(msg.SID, "at capacity")
				continue
			}
			cs := &connSession{
				pc: pc, sid: msg.SID, open: open,
				inbound: make(chan []byte, 64), done: make(chan struct{}),
			}
			cs.s = &session{
				sub: open.Sub, role: open.Role, park: park, out: make(chan []byte, 256),
				dogID: wum.DogIDFor(open.Sub), openedAt: time.Now(),
				closeFn: func(why string) { cs.terminate(why, true) },
			}
			sessions[msg.SID] = cs
			go cs.run()
		case parkproxy.KindStream:
			cs := sessions[msg.SID]
			if cs == nil {
				continue
			}
			select {
			case <-cs.done:
				delete(sessions, msg.SID)
				continue
			default:
			}
			select {
			case cs.inbound <- msg.Payload:
			default:
				// The gateway shapes intents to 20/s; a full buffer means
				// this session's drain is wedged, not that the client is
				// fast.
				cs.terminate("intent backlog", true)
				delete(sessions, msg.SID)
			}
		case parkproxy.KindDatagram:
			if sessions[msg.SID] == nil {
				continue
			}
			if verdict, ok := checkVerdict(park, msg.Payload); ok {
				if pc.WriteDatagram(msg.SID, verdict) != nil {
					return
				}
			}
		case parkproxy.KindClose:
			if cs := sessions[msg.SID]; cs != nil {
				cs.terminate("", false)
				delete(sessions, msg.SID)
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
		write := func(b []byte) bool { return cs.pc.WriteStream(cs.sid, b) == nil }
		if !write(res.welcome) {
			cs.terminate("park unavailable", false)
			return
		}
		for _, frame := range park.catchupFrames(cs.open.SinceSeq, cs.open.SinceTick, res) {
			if !write(frame) {
				cs.terminate("park unavailable", false)
				return
			}
		}
		for {
			select {
			case frame := <-cs.s.out:
				if !write(frame) {
					cs.terminate("park unavailable", false)
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
		case <-cs.done:
			return
		}
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
			s.sendReject(rec.Intent, wum.RejectReadOnly)
			return
		}
		// Game-blind binding: the envelope's actor must be the
		// authenticated session's, for every kind.
		if rec.Actor != s.dogID {
			s.sendReject(rec.Intent, wum.RejectNotYours)
			return
		}
		park.stageIntent(s, rec.Intent, rec.Kind, rec.Payload)
	case codec.KindResync:
		if _, err := codec.DecodeResync(framePayload); err != nil {
			return
		}
		mResyncs.Inc()
		select {
		case park.attach <- attachReq{sess: s, resync: true, done: make(chan attachResult, 1)}:
		case <-park.stop:
			cs.terminate("park unavailable", true)
		}
	}
}

func stageDeparture(park *authority, s *session) {
	if s.role != "player" {
		return
	}
	park.stageIntent(s, 0, wum.EvLeave, nil)
}
