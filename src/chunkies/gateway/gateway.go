package gateway

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	webtransport "github.com/quic-go/webtransport-go"

	"github.com/guardian-intelligence/guardian/src/chunkies/mount"
	"github.com/guardian-intelligence/guardian/src/chunkies/parkproxy"
	"github.com/guardian-intelligence/guardian/src/chunkies/codec"
	"github.com/guardian-intelligence/guardian/src/services/telemetry"
)

type chunkiesGateway struct {
	admission *gameHandlers
	directory *chunkDirectory
	game      string
	key       []byte
}

// Run is the chunkies-gateway process: public WebTransport, admission,
// and park routing.
func Run() {
	wtPort := envInt("WT_PORT", 4433)
	httpPort := envInt("HTTP_PORT", 9634)
	metricsPort := envInt("METRICS_PORT", 9633)
	publicAddr := envStr("PUBLIC_ADDR", "")
	allowedOrigins := envStr("ALLOWED_ORIGINS", "")
	maxSessions := envInt("MAX_SESSIONS", 4000)
	game := envStr("GAME", "wum")
	directoryFile := envStr("CHUNK_DIRECTORY_FILE", "/etc/chunkies/directory/chunks.conf")
	directory := newChunkDirectory()
	if err := loadChunkDirectory(directoryFile, directory); err != nil {
		// Fail-open on boot would mean a gateway that can never route;
		// fail-closed but alive means the watcher picks the file up the
		// moment the mount appears.
		log.Printf("chunk directory %s: %v (starting empty)", directoryFile, err)
	}
	go watchChunkDirectory(directoryFile, directory)
	key, err := parkproxy.ReadKey(envStr("INTERNAL_KEY_FILE", ""))
	if err != nil {
		log.Fatalf("internal key: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	traceShutdown, err := telemetry.Init(ctx, "chunkies-gateway", os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	if err != nil {
		log.Fatalf("tracing: %v", err)
	}
	defer traceShutdown(context.Background())

	behaviorDir := envStr("BEHAVIOR_DIR", "/etc/mythra/behavior")
	client := mount.NewModule("client", mount.DefaultClient)
	parkModule := mount.NewModule("park", mount.DefaultPark)
	// nil acceptance: the gateway only distributes module bytes, and the
	// browser's boot gate is the consumer-side decision there.
	go mount.Watch(behaviorDir, nil, client, parkModule)
	assets := newAssetCatalog(envStr("ASSET_DIR", "/etc/mythra/assets"))

	issuer := envStr("OIDC_ISSUER", "https://auth.wakeupmythra.com/realms/wakeupmythra.com")
	gate := newOIDCGate(issuer, envStr("OIDC_JWKS_URL", ""), envStr("OIDC_CLIENT_IDS", "wake-up-mythra"), os.Getenv("REQUIRE_EMAIL_VERIFIED") == "true")
	tickets, err := newTicketMint(os.Getenv("TICKET_KEY_FILE"))
	if err != nil {
		log.Fatalf("ticket key: %v", err)
	}
	admission := &gameHandlers{
		tickets: tickets, maxSessions: maxSessions, directory: directory, game: game,
		anonMints: newAnonLimiter(),
	}
	gateway := &chunkiesGateway{admission: admission, directory: directory, game: game, key: key}

	sans := []net.IP{net.ParseIP("127.0.0.1")}
	if host, _, err := net.SplitHostPort(publicAddr); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			sans = append(sans, ip)
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			if ipn, ok := addr.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
				sans = append(sans, ipn.IP)
			}
		}
	}
	rc := newRotatingCert(sans)
	fc := newFileCert(envStr("TLS_CERT_FILE", ""), envStr("TLS_KEY_FILE", ""))

	wtMux := http.NewServeMux()
	wt := webtransport.Server{
		H3: &http3.Server{
			Addr: fmt.Sprintf(":%d", wtPort),
			TLSConfig: &tls.Config{
				GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
					if cert := fc.get(); cert != nil {
						return cert, nil
					}
					cert, _ := rc.get()
					return &cert, nil
				},
				NextProtos: []string{http3.NextProtoH3},
			},
			QUICConfig: &quic.Config{
				// The protocol uses exactly one client-opened bidi stream
				// (plus the WT session's own); uni streams are h3 plumbing
				// (control + QPACK). 4 of each is protocol minimum plus
				// slack, not idle attack surface.
				EnableDatagrams: true, MaxIncomingStreams: 4, MaxIncomingUniStreams: 4,
				InitialStreamReceiveWindow: 16 * 1024, MaxStreamReceiveWindow: 256 * 1024,
				InitialConnectionReceiveWindow: 32 * 1024, MaxConnectionReceiveWindow: 512 * 1024,
				MaxIdleTimeout: 60 * time.Second,
			},
			Handler: wtMux, EnableDatagrams: true,
		},
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" || allowedOrigins == "" {
				return true
			}
			for _, allowed := range strings.Split(allowedOrigins, ",") {
				if origin == strings.TrimSpace(allowed) {
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
			return
		}
		go gateway.handleSession(sess)
	})

	certHash := func() (string, bool) {
		if fc.loaded() {
			return "", false
		}
		_, hash := rc.get()
		return base64.StdEncoding.EncodeToString(hash[:]), true
	}
	pageMux := http.NewServeMux()
	pageMux.HandleFunc("/session", admission.handleSessionMint(gate, publicAddr, certHash))
	pageMux.HandleFunc("/wt-info", func(w http.ResponseWriter, _ *http.Request) {
		addr := publicAddr
		if addr == "" {
			addr = "127.0.0.1:" + strconv.Itoa(wtPort)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, cw := client.Get()
		_, pw := parkModule.Get()
		info := map[string]any{"addr": addr, "clientWasm": cw, "parkWasm": pw}
		if hash, selfSigned := certHash(); selfSigned {
			info["certHashB64"] = hash
		}
		json.NewEncoder(w).Encode(info)
	})
	serveModule := func(module *mount.Module) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			body, hash := module.Get()
			if len(body) == 0 {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/wasm")
			w.Header().Set("ETag", `"`+hash+`"`)
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.Write(body)
		}
	}
	pageMux.HandleFunc("/behavior/client.wasm", serveModule(client))
	pageMux.HandleFunc("/behavior/park.wasm", serveModule(parkModule))
	pageMux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		ref := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/assets/"), ".svg")
		body, ok := assets.get(ref)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Write(body)
	})
	parkHTTP, err := url.Parse(envStr("PARK_HTTP_URL", "http://127.0.0.1:9631"))
	if err != nil {
		log.Fatalf("PARK_HTTP_URL: %v", err)
	}
	parkProxy := httputil.NewSingleHostReverseProxy(parkHTTP)
	pageMux.Handle("/terrain/", parkProxy)
	if os.Getenv("WUM_DEV_LIVE_TICK_RATE") == "true" {
		pageMux.Handle("/dev/tick-rate", parkProxy)
	}

	page := &http.Server{Addr: fmt.Sprintf(":%d", httpPort), Handler: telemetry.Middleware(pageMux, telemetry.WithTraceparentOnly("/wt-info"))}
	obsMux := http.NewServeMux()
	obsMux.Handle("/metrics", promhttp.Handler())
	obsMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	obsMux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	obs := &http.Server{Addr: fmt.Sprintf(":%d", metricsPort), Handler: obsMux}

	go func() {
		if err := wt.ListenAndServe(); err != nil && ctx.Err() == nil {
			log.Printf("gateway webtransport: %v", err)
			stop()
		}
	}()
	go func() {
		if err := page.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("gateway http: %v", err)
			stop()
		}
	}()
	go func() {
		if err := obs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("gateway metrics: %v", err)
			stop()
		}
	}()

	log.Printf("chunkies-gateway: wt=:%d http=:%d metrics=:%d public=%s", wtPort, httpPort, metricsPort, publicAddr)
	<-ctx.Done()
	wt.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	page.Shutdown(shutdownCtx)
	obs.Shutdown(shutdownCtx)
}

// Doorman-level reject reasons live above the sim's code space.
const (
	rejectReadOnly = 100
	rejectNotYours = 101
)

func (g *chunkiesGateway) handleSession(sess *webtransport.Session) {
	if n := sessionCount.Add(1); n > int64(g.admission.maxSessions) {
		sessionCount.Add(-1)
		mHandshakes.WithLabelValues("capacity").Inc()
		sess.CloseWithError(4029, "at capacity")
		return
	}
	defer sessionCount.Add(-1)
	ctx := sess.Context()
	stream, err := sess.AcceptStream(ctx)
	if err != nil {
		mHandshakes.WithLabelValues("no_stream").Inc()
		return
	}
	stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	frames := codec.NewReader(stream)
	kind, payload, err := frames.Next()
	if err != nil || kind != codec.KindHello {
		mHandshakes.WithLabelValues("bad_hello").Inc()
		sess.CloseWithError(4400, "bad hello")
		return
	}
	hello, err := codec.DecodeHello(payload)
	if err != nil || hello.Proto != codec.Proto {
		mHandshakes.WithLabelValues("bad_hello").Inc()
		sess.CloseWithError(4400, "bad hello")
		return
	}
	ticket, err := g.admission.tickets.check(hello.Ticket)
	if err != nil {
		mHandshakes.WithLabelValues("bad_ticket").Inc()
		sess.CloseWithError(4401, "bad ticket")
		return
	}
	backend, ok := g.directory.lookup(g.game, ticket.Park)
	if !ok {
		mHandshakes.WithLabelValues("park_unavailable").Inc()
		sess.CloseWithError(4503, "park unavailable")
		return
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	proxy, err := parkproxy.Dial(dialCtx, backend, g.key, parkproxy.Open{
		Sub: ticket.Sub, Park: ticket.Park, Role: ticket.Role,
		Remote: sess.RemoteAddr().String(), SinceSeq: hello.SinceSeq, SinceTick: hello.SinceTick,
	})
	cancel()
	if err != nil {
		mHandshakes.WithLabelValues("park_unavailable").Inc()
		sess.CloseWithError(4503, "park unavailable")
		return
	}
	defer proxy.Close()
	defer sess.CloseWithError(4000, "bye")
	stream.SetReadDeadline(time.Time{})
	mHandshakes.WithLabelValues("ok").Inc()
	mSessions.WithLabelValues(ticket.Role).Inc()
	defer mSessions.WithLabelValues(ticket.Role).Dec()

	var streamMu sync.Mutex
	writeStream := func(frame []byte) error {
		streamMu.Lock()
		defer streamMu.Unlock()
		stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, err := stream.Write(frame)
		return err
	}
	go func() {
		for {
			kind, payload, err := proxy.ReadMessage()
			if err != nil {
				sess.CloseWithError(4503, "park unavailable")
				return
			}
			switch kind {
			case parkproxy.KindStream:
				if writeStream(payload) != nil {
					proxy.Close()
					return
				}
			case parkproxy.KindDatagram:
				if sess.SendDatagram(payload) != nil {
					mDgErrors.Inc()
				} else {
					mDgSent.Inc()
				}
			case parkproxy.KindClose:
				sess.CloseWithError(4000, string(payload))
				return
			}
		}
	}()
	go func() {
		for {
			data, err := sess.ReceiveDatagram(ctx)
			if err != nil {
				return
			}
			// The only datagram a client may send is a fixed-size hash
			// check; everything else is dropped here so junk never
			// reaches a park.
			if len(data) != codec.CheckLen || data[0] != codec.DgCheck {
				mDgRejected.Inc()
				continue
			}
			if proxy.WriteMessage(parkproxy.KindDatagram, data) != nil {
				return
			}
		}
	}()

	// Uplink shaper: a session earns 20 frames/s with a burst of 40. An
	// empty bucket pauses the read loop until it refills — QUIC flow
	// control conveys the pause to a bursting client — instead of closing
	// the session. Closes are reserved for protocol violations (a framing
	// error from the reader).
	tokens := 40.0
	lastRefill := time.Now()
	for {
		kind, payload, err := frames.Next()
		if err != nil {
			proxy.WriteMessage(parkproxy.KindClose, nil)
			return
		}
		now := time.Now()
		tokens = min(40, tokens+20*now.Sub(lastRefill).Seconds())
		lastRefill = now
		if tokens < 1 {
			time.Sleep(time.Duration((1 - tokens) / 20 * float64(time.Second)))
			tokens = 1
			lastRefill = time.Now()
		}
		tokens--
		switch kind {
		case codec.KindIntent:
			rec, err := codec.DecodeIntent(payload)
			if err != nil {
				continue
			}
			if ticket.Role != "player" {
				writeStream(codec.EncodeReject(codec.Reject{Intent: rec.Intent, Reason: rejectReadOnly}))
				continue
			}
			// Game-blind binding: the envelope's actor must be the
			// authenticated subject's, for every kind — then the client's
			// bytes travel untouched.
			if rec.Actor != codec.ActorFor(ticket.Sub) {
				writeStream(codec.EncodeReject(codec.Reject{Intent: rec.Intent, Reason: rejectNotYours}))
				continue
			}
			if proxy.WriteMessage(parkproxy.KindStream, frames.Raw()) != nil {
				return
			}
		case codec.KindResync:
			if _, err := codec.DecodeResync(payload); err == nil {
				if proxy.WriteMessage(parkproxy.KindStream, frames.Raw()) != nil {
					return
				}
			}
		default:
			// Unknown client kinds are forward-compat room, not errors —
			// but silence would hide a skewed client. Count and drop.
			mUnknownFrames.Inc()
		}
	}
}
