package main

import (
	"bytes"
	"context"
	"encoding/binary"
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

	"github.com/guardian-intelligence/guardian/src/services/mythrad/journal"
	"github.com/guardian-intelligence/guardian/src/services/mythrad/wire"
	"github.com/guardian-intelligence/guardian/src/services/telemetry"
)

func runChunkiesPark() {
	parkName := envStr("PARK_NAME", "park-mythra")
	parkPort := envInt("PARK_PORT", 9632)
	httpPort := envInt("HTTP_PORT", 9631)
	metricsPort := envInt("METRICS_PORT", 9637)
	maxSessions := envInt("MAX_SESSIONS", 4000)
	tickHz := envInt("TICK_HZ", 24)
	if tickHz < minTickHz || tickHz > maxTickHz {
		log.Fatalf("TICK_HZ=%d outside supported range %d..%d", tickHz, minTickHz, maxTickHz)
	}
	key, err := proxyKey(envStr("INTERNAL_KEY_FILE", ""))
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
	live := newBehaviorSlot("live")
	shadow := newBehaviorSlot("shadow")
	if err := live.load(defaultBehavior); err != nil {
		log.Fatalf("embedded behavior: %v", err)
	}
	client := &clientModule{slot: "client"}
	client.set(defaultClientModule)
	parkModule := &clientModule{slot: "park"}
	parkModule.set(defaultParkModule)
	go watchBehaviors(behaviorDir, live, shadow, client, parkModule)

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
	registry := newParks(func() []byte { b, _ := parkModule.get(); return b }, fixtureTerrain, j, mods, timing{hz: tickHz})
	authority, err := registry.get(ctx, parkName)
	if err != nil {
		log.Fatalf("open park: %v", err)
	}

	var ready atomic.Bool
	internal, err := net.Listen("tcp", fmt.Sprintf(":%d", parkPort))
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
	parkHTTP := &http.Server{Addr: fmt.Sprintf(":%d", httpPort), Handler: parkMux}
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
	log.Printf("chunkies-park: park=%s sessions=:%d http=:%d metrics=:%d", parkName, parkPort, httpPort, metricsPort)
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
	fixtureID := terrainID(fixtureTerrain)
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseUint(strings.TrimPrefix(r.URL.Path, "/terrain/"), 16, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		blob := fixtureTerrain
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
	proxy, open, err := acceptProxy(conn, key, time.Now())
	if err != nil {
		return
	}
	defer proxy.Close()
	if open.Park != park.name {
		proxy.writeMessage(proxyClose, []byte("wrong park"))
		return
	}
	if n := sessionCount.Add(1); n > int64(maxSessions) {
		sessionCount.Add(-1)
		proxy.writeMessage(proxyClose, []byte("at capacity"))
		return
	}
	defer sessionCount.Add(-1)

	done := make(chan struct{})
	s := &session{
		sub: open.Sub, role: open.Role, park: park, out: make(chan []byte, 256),
		dogID: dogIDFor(open.Sub), openedAt: time.Now(),
		closeFn: func(why string) {
			proxy.writeMessage(proxyClose, []byte(why))
			proxy.Close()
		},
	}
	attached := make(chan attachResult, 1)
	select {
	case park.attach <- attachReq{sess: s, sinceSeq: open.SinceSeq, sinceTick: open.SinceTick, done: attached}:
	case <-park.stop:
		proxy.writeMessage(proxyClose, []byte("park unavailable"))
		return
	}
	res := <-attached
	if res.err != nil {
		proxy.writeMessage(proxyClose, []byte("park unavailable"))
		return
	}
	defer park.detach(s)
	defer close(done)
	defer stageDeparture(park, s)

	go func() {
		write := func(b []byte) bool { return proxy.writeMessage(proxyStream, b) == nil }
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
		kind, payload, err := proxy.readMessage()
		if err != nil {
			return
		}
		switch kind {
		case proxyStream:
			frameKind, framePayload, err := wire.NewReader(bytes.NewReader(payload)).Next()
			if err != nil {
				continue
			}
			switch frameKind {
			case wire.KindIntent:
				in, err := wire.DecodeIntent(framePayload)
				if err != nil {
					continue
				}
				if s.role != "player" {
					s.sendReject(in.ID, rejectReadOnly)
					continue
				}
				if !intentBoundToActor(in.Kind, in.Payload, s.dogID) {
					s.sendReject(in.ID, rejectNotYours)
					continue
				}
				park.stageIntent(s, in.ID, in.Kind, in.Payload)
			case wire.KindResync:
				if _, err := wire.DecodeResync(framePayload); err != nil {
					continue
				}
				mResyncs.Inc()
				select {
				case park.attach <- attachReq{sess: s, resync: true, done: make(chan attachResult, 1)}:
				case <-park.stop:
					return
				}
			}
		case proxyDatagram:
			if verdict, ok := checkVerdict(park, payload); ok {
				if proxy.writeMessage(proxyDatagram, verdict) != nil {
					return
				}
			}
		case proxyClose:
			return
		}
	}
}

func stageDeparture(park *authority, s *session) {
	if s.role != "player" {
		return
	}
	var payload [8]byte
	binary.LittleEndian.PutUint64(payload[:], s.dogID)
	park.stageIntent(s, 0, evLeave, payload[:])
}
