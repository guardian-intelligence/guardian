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
	"github.com/guardian-intelligence/guardian/src/services/telemetry"
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
			go handleParkProxy(conn, key, authority, maxSessions)
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

func handleParkProxy(conn net.Conn, key []byte, park *authority, maxSessions int) {
	proxy, open, err := parkproxy.Accept(conn, key, time.Now())
	if err != nil {
		return
	}
	defer proxy.Close()
	if open.Park != park.name {
		proxy.WriteMessage(parkproxy.KindClose, []byte("wrong park"))
		return
	}
	if n := sessionCount.Add(1); n > int64(maxSessions) {
		sessionCount.Add(-1)
		proxy.WriteMessage(parkproxy.KindClose, []byte("at capacity"))
		return
	}
	defer sessionCount.Add(-1)

	done := make(chan struct{})
	s := &session{
		sub: open.Sub, role: open.Role, park: park, out: make(chan []byte, 256),
		dogID: wum.DogIDFor(open.Sub), openedAt: time.Now(),
		closeFn: func(why string) {
			proxy.WriteMessage(parkproxy.KindClose, []byte(why))
			proxy.Close()
		},
	}
	attached := make(chan attachResult, 1)
	select {
	case park.attach <- attachReq{sess: s, sinceSeq: open.SinceSeq, sinceTick: open.SinceTick, done: attached}:
	case <-park.stop:
		proxy.WriteMessage(parkproxy.KindClose, []byte("park unavailable"))
		return
	}
	res := <-attached
	if res.err != nil {
		proxy.WriteMessage(parkproxy.KindClose, []byte("park unavailable"))
		return
	}
	defer park.detach(s)
	defer close(done)
	defer stageDeparture(park, s)

	go func() {
		write := func(b []byte) bool { return proxy.WriteMessage(parkproxy.KindStream, b) == nil }
		if !write(res.welcome) {
			proxy.Close()
			return
		}
		for _, frame := range park.catchupFrames(open.SinceSeq, open.SinceTick, res) {
			if !write(frame) {
				proxy.Close()
				return
			}
		}
		for {
			select {
			case frame := <-s.out:
				if !write(frame) {
					proxy.Close()
					return
				}
			case <-done:
				return
			}
		}
	}()

	for {
		kind, payload, err := proxy.ReadMessage()
		if err != nil {
			return
		}
		switch kind {
		case parkproxy.KindStream:
			frameKind, framePayload, err := codec.NewReader(bytes.NewReader(payload)).Next()
			if err != nil {
				continue
			}
			switch frameKind {
			case codec.KindIntent:
				rec, err := codec.DecodeIntent(framePayload)
				if err != nil {
					continue
				}
				if s.role != "player" {
					s.sendReject(rec.Intent, wum.RejectReadOnly)
					continue
				}
				// Game-blind binding: the envelope's actor must be the
				// authenticated session's, for every kind.
				if rec.Actor != s.dogID {
					s.sendReject(rec.Intent, wum.RejectNotYours)
					continue
				}
				park.stageIntent(s, rec.Intent, rec.Kind, rec.Payload)
			case codec.KindResync:
				if _, err := codec.DecodeResync(framePayload); err != nil {
					continue
				}
				mResyncs.Inc()
				select {
				case park.attach <- attachReq{sess: s, resync: true, done: make(chan attachResult, 1)}:
				case <-park.stop:
					return
				}
			}
		case parkproxy.KindDatagram:
			if verdict, ok := checkVerdict(park, payload); ok {
				if proxy.WriteMessage(parkproxy.KindDatagram, verdict) != nil {
					return
				}
			}
		case parkproxy.KindClose:
			return
		}
	}
}

func stageDeparture(park *authority, s *session) {
	if s.role != "player" {
		return
	}
	park.stageIntent(s, 0, wum.EvLeave, nil)
}
