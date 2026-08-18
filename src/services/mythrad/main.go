package main

import (
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
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	webtransport "github.com/quic-go/webtransport-go"

	"github.com/guardian-intelligence/guardian/src/services/mythrad/journal"
	"github.com/guardian-intelligence/guardian/src/services/telemetry"
)

//go:embed behaviors/client.wasm
var defaultClientModule []byte

//go:embed behaviors/park.wasm
var defaultParkModule []byte

// The fixture terrain: the world every brand-new park is born with until
// procedural generation exists. Committed bytes (diff-tested against the
// generator) so park identity can only change through an explicit refresh.
//
//go:embed terrain/fixture_park.bin
var fixtureTerrain []byte

var (
	mTickDur = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "mythra_tick_duration_seconds",
		Help:    "Wall time of one authority tick incl. validation, batch append, and fan-out.",
		Buckets: []float64{.0005, .001, .0025, .005, .01, .02, .03, .0417, .06, .1, .25},
	})
	mTickLag = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mythra_tick_lag_seconds",
		Help: "How far the park runs behind its wall-clock tick schedule. Steady state sits inside one tick; sustained growth means the sim cannot keep up; strongly negative means the wall clock stepped backward.",
	}, []string{"park"})
	mClockSkips = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_clock_skips_total", Help: "clock_skip events journaled to repay authority downtime."})
	mRateChanges = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_rate_changes_total", Help: "rate_set events journaled to converge a park to the deployment's tick rate."})
	mAppendDur = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "mythra_journal_append_seconds",
		Help:    "Tick-batched journal append commit time (the Append call alone).",
		Buckets: []float64{.0005, .001, .0025, .005, .01, .02, .03, .0417, .06, .1, .25, .5},
	})
	mIntentQueueDur = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mythra_intent_tick_queue_seconds",
		Help:    "Player intent time from server receipt until the authority begins its next tick, by bounded action kind.",
		Buckets: []float64{.0005, .001, .0025, .005, .0075, .01, .015, .02, .03, .0417, .06, .1, .25},
	}, []string{"kind"})
	mIntentAuthorityDur = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mythra_intent_authority_seconds",
		Help:    "Player intent time from server receipt through validation and durable append to fan-out (or rejection), by bounded action kind and result.",
		Buckets: []float64{.0005, .001, .0025, .005, .0075, .01, .015, .02, .03, .0417, .06, .1, .25, .5},
	}, []string{"kind", "result"})
	mSessions = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mythra_sessions", Help: "Connected sessions."}, []string{"role"})
	mParks = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "mythra_parks_open", Help: "Open park authorities."})
	mHandshakes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_handshakes_total", Help: "Session handshakes."}, []string{"result"})
	mMints = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_session_mints_total", Help: "POST /session ticket mints."}, []string{"result"})
	mEventsAppended = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_journal_events_total", Help: "Events appended to the journal."})
	mAppendErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_journal_append_errors_total", Help: "Failed journal appends (authority closes on each)."})
	mSnapshots = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_snapshots_total", Help: "Durable snapshots written."})
	mIntentsRejected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_intents_rejected_total", Help: "Intents rejected by validation or authorization, by reason."}, []string{"reason"})
	mIntentsDeduped = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_intents_deduped_total", Help: "Intents dropped by the (actor, intent_id) idempotency window."})
	mChecks = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_checks_total", Help: "Client hash checks answered."}, []string{"result"})
	mResyncs = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_resyncs_total", Help: "Client-requested divergence resyncs."})
	mCatchup = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_catchup_total", Help: "Catch-up material served, by kind."}, []string{"kind"})
	mDgSent = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_datagrams_sent_total", Help: "Datagrams sent."})
	mDgErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_datagram_errors_total", Help: "SendDatagram failures."})
	mDrops = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mythra_fanout_dropped_total", Help: "Sessions closed for stream backlog."})
	mBehaviorInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mythra_behavior_script", Help: "1 for the currently loaded module hash per slot."}, []string{"slot", "hash"})
	mEpochSwaps = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_epoch_swaps_total", Help: "Park module epoch-swap lane outcomes."}, []string{"result"})
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

// ---------- module distribution ----------

// clientModule tracks a distributed module's bytes plus content hash:
// pages fetch by hash, so bytes are immutable per URL and a hash flip on a
// verdict is the update signal.
type clientModule struct {
	mu    sync.Mutex
	slot  string
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
		mBehaviorInfo.DeletePartialMatch(prometheus.Labels{"slot": c.slot})
		mBehaviorInfo.WithLabelValues(c.slot, hash).Set(1)
		log.Printf("%s module loaded: %s", c.slot, hash)
	}
}

func (c *clientModule) get() ([]byte, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes, c.hash
}

// ---------- asset catalog ----------

type asset struct {
	name string
	hash string
	body []byte
}

type assetCatalog struct {
	mu    sync.Mutex
	byRef map[string]*asset // "name.hash" -> asset
	dir   string
}

func newAssetCatalog(dir string) *assetCatalog {
	c := &assetCatalog{byRef: map[string]*asset{}, dir: dir}
	if _, err := os.ReadDir(dir); err != nil {
		log.Printf("asset catalog: %v (no skins will load until it appears)", err)
	}
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
	for _, e := range entries {
		name := e.Name()
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

// ---------- certificates ----------

// rotatingCert regenerates the self-signed ECDSA cert at half-life so the
// serverCertificateHashes contract (<=14 days validity) holds for a
// long-running pod; /session always serves the current hash.
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
// change so cert-manager renewals land without a restart.
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

// ---------- database ----------

// databaseURL builds the journal DSN. DATABASE_URL wins (local dev); in
// the cluster the password arrives as a mounted Secret file so rotation
// rides the kubelet sync, keeping this Deployment reloader-free (the
// behavior hot-reload doctrine).
func databaseURL() (string, error) {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn, nil
	}
	pwFile := os.Getenv("PG_PASSWORD_FILE")
	if pwFile == "" {
		return "", fmt.Errorf("neither DATABASE_URL nor PG_PASSWORD_FILE set")
	}
	pw, err := os.ReadFile(pwFile)
	if err != nil {
		return "", err
	}
	host := envStr("PG_HOST", "postgres-products-rw.tenant-guardian-prod.svc:5432")
	db := envStr("PG_DATABASE", "mythra")
	user := envStr("PG_USER", "mythra")
	return fmt.Sprintf("postgresql://%s:%s@%s/%s?sslmode=require&pool_max_conns=4",
		user, url.QueryEscape(strings.TrimSpace(string(pw))), host, db), nil
}

func devTickRateHandler(registry *parks, allowedParks map[string]bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		park := r.URL.Query().Get("park")
		if !allowedParks[park] {
			http.NotFound(w, r)
			return
		}
		hz, err := strconv.Atoi(r.URL.Query().Get("hz"))
		if err != nil || hz < minTickHz || hz > maxTickHz {
			http.Error(w, fmt.Sprintf("hz must be an integer in %d..%d", minTickHz, maxTickHz), http.StatusBadRequest)
			return
		}
		a, ok := registry.current(park)
		if !ok {
			http.Error(w, "park has no live authority; connect the drill client first", http.StatusConflict)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := a.requestRate(ctx, hz); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]any{"park": park, "rateHz": hz})
	}
}

func runMythrad() {
	wtPort := envInt("WT_PORT", 4433)
	httpPort := envInt("HTTP_PORT", 9634)
	metricsPort := envInt("METRICS_PORT", 9633)
	behaviorDir := envStr("BEHAVIOR_DIR", "/etc/mythra/behavior")
	assetDir := envStr("ASSET_DIR", "/etc/mythra/assets")
	publicAddr := envStr("PUBLIC_ADDR", "") // "host:port" advertised to clients
	allowedOrigins := envStr("ALLOWED_ORIGINS", "")
	maxSessions := envInt("MAX_SESSIONS", 4000)
	// The desired startup tick rate: parks whose journaled rate differs
	// converge to it via a dark-phase rate_set on their next open. A local
	// drill can subsequently journal a live boundary for connected clients.
	tickHz := envInt("TICK_HZ", 24)
	if tickHz < minTickHz || tickHz > maxTickHz {
		log.Fatalf("TICK_HZ=%d outside supported range %d..%d", tickHz, minTickHz, maxTickHz)
	}
	issuer := envStr("OIDC_ISSUER", "https://auth.wakeupmythra.com/realms/wakeupmythra.com")
	jwksURL := envStr("OIDC_JWKS_URL", "")
	clientIDs := envStr("OIDC_CLIENT_IDS", "wake-up-mythra")
	requireEmail := envStr("REQUIRE_EMAIL_VERIFIED", "false") == "true"
	// Parks are a fixed registry: /session refuses names outside it, so an
	// authority (wazero runtime, goroutine, journal rows) only ever opens
	// for a park an operator declared.
	allowedParks := map[string]bool{}
	for _, p := range strings.Split(envStr("PARKS", "park-mythra"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			allowedParks[p] = true
		}
	}

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

	client := &clientModule{slot: "client"}
	client.set(defaultClientModule)
	parkMod := &clientModule{slot: "park"}
	parkMod.set(defaultParkModule)
	go watchDistributedModules(behaviorDir, client, parkMod)

	assets := newAssetCatalog(assetDir)

	// The journal comes up lazily: pool creation is offline, the schema
	// migration retries in the background, and hellos are refused with
	// "park unavailable" until the truth store is writable.
	dsn, err := databaseURL()
	if err != nil {
		log.Fatalf("journal database: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Fatalf("journal pool: %v", err)
	}
	j := journal.NewPg(pool)
	var journalReady atomic.Bool
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := j.Migrate(ctx)
			cancel()
			if err == nil {
				journalReady.Store(true)
				log.Print("journal ready")
				return
			}
			log.Printf("journal not ready (retrying in 5s): %v", err)
			time.Sleep(5 * time.Second)
		}
	}()

	traceShutdown, err := telemetry.Init(context.Background(), "mythrad",
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	if err != nil {
		log.Fatalf("tracing: %v", err)
	}
	defer traceShutdown(context.Background())

	mods := &modules{client: client, park: parkMod}
	registry := newParks(func() []byte { b, _ := parkMod.get(); return b }, fixtureTerrain, j, mods, timing{hz: tickHz})
	tickets, err := newTicketMint(os.Getenv("TICKET_KEY_FILE"))
	if err != nil {
		log.Fatalf("ticket key: %v", err)
	}
	handlers := &gameHandlers{
		parks: registry, tickets: tickets, maxSessions: maxSessions,
		allowedParks: allowedParks, anonMints: newAnonLimiter(),
	}
	gate := newOIDCGate(issuer, jwksURL, clientIDs, requireEmail)

	wtMux := http.NewServeMux()
	wt := webtransport.Server{
		H3: &http3.Server{
			Addr: fmt.Sprintf(":%d", wtPort),
			TLSConfig: &tls.Config{
				GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
					if c := fc.get(); c != nil {
						return c, nil
					}
					c, _ := rc.get()
					return &c, nil
				},
				NextProtos: []string{http3.NextProtoH3},
			},
			// Sessions carry an event log at human action rate plus tiny
			// datagrams: small flow-control windows bound per-session
			// buffer memory so the session cap, not buffer growth, is the
			// memory envelope. Snapshots stream fine through the maximums.
			QUICConfig: &quic.Config{
				EnableDatagrams:                true,
				MaxIncomingStreams:             16,
				MaxIncomingUniStreams:          16,
				InitialStreamReceiveWindow:     16 * 1024,
				MaxStreamReceiveWindow:         256 * 1024,
				InitialConnectionReceiveWindow: 32 * 1024,
				MaxConnectionReceiveWindow:     512 * 1024,
				MaxIdleTimeout:                 60 * time.Second,
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
		if !journalReady.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		sess, err := wt.Upgrade(w, r)
		if err != nil {
			mHandshakes.WithLabelValues("upgrade_failed").Inc()
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		go handlers.handleSession(sess)
	})

	// Ticket mint, game modules, and content-addressed assets ride the
	// Cloudflare-proxied ingress; only the QUIC dial goes direct to
	// PUBLIC_ADDR, pinned by the hash from /session while self-signed.
	certHash := func() (string, bool) {
		if fc.loaded() {
			return "", false
		}
		_, hash := rc.get()
		return base64.StdEncoding.EncodeToString(hash[:]), true
	}
	pageMux := http.NewServeMux()
	pageMux.HandleFunc("/session", handlers.handleSessionMint(gate, publicAddr, certHash))
	if os.Getenv("WUM_DEV_LIVE_TICK_RATE") == "true" {
		pageMux.HandleFunc("/dev/tick-rate", devTickRateHandler(registry, allowedParks))
		log.Print("development live tick-rate control enabled at POST /dev/tick-rate")
	}
	pageMux.HandleFunc("/wt-info", func(w http.ResponseWriter, r *http.Request) {
		addr := publicAddr
		if addr == "" {
			addr = "127.0.0.1:" + strconv.Itoa(wtPort)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, cw := client.get()
		_, pw := parkMod.get()
		info := map[string]any{"addr": addr, "clientWasm": cw, "parkWasm": pw}
		if hash, selfSigned := certHash(); selfSigned {
			info["certHashB64"] = hash
		}
		json.NewEncoder(w).Encode(info)
	})
	serveModule := func(mod *clientModule) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			module, hash := mod.get()
			if len(module) == 0 {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/wasm")
			w.Header().Set("ETag", `"`+hash+`"`)
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.Write(module)
		}
	}
	pageMux.HandleFunc("/behavior/client.wasm", serveModule(client))
	pageMux.HandleFunc("/behavior/park.wasm", serveModule(parkMod))
	// Terrain artifacts are immutable per URL: the embedded fixture serves
	// from memory, everything else from the journal's content store.
	fixtureID := terrainID(fixtureTerrain)
	pageMux.HandleFunc("/terrain/", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseUint(strings.TrimPrefix(r.URL.Path, "/terrain/"), 16, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		blob := fixtureTerrain
		if id != fixtureID {
			if !journalReady.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			var found bool
			blob, found, err = j.TerrainBlob(ctx, id)
			if err != nil {
				// transient store trouble is not "this terrain does not
				// exist" — a 404 would poison immutable caches
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
	})
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

	obsMux := http.NewServeMux()
	obsMux.Handle("/metrics", promhttp.Handler())
	obsMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	obsMux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if !journalReady.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(200)
	})

	log.Printf("mythrad: wt=:%d http=:%d metrics=:%d public=%s issuer=%s", wtPort, httpPort, metricsPort, publicAddr, issuer)
	go func() { log.Fatal(wt.ListenAndServe()) }()
	go func() {
		// /wt-info is polled by external monitors that send no traceparent;
		// real page boots always do (the fetch wrapper stamps one).
		handler := telemetry.Middleware(pageMux, telemetry.WithTraceparentOnly("/wt-info"))
		log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", httpPort), handler))
	}()
	go func() { log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", metricsPort), obsMux)) }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	log.Print("SIGTERM: closing sessions (clients rejoin the replacement by journal catch-up)")
	wt.Close()
}

func main() {
	switch filepath.Base(os.Args[0]) {
	case "chunkies-gateway":
		runChunkiesGateway()
	case "chunkies-park":
		runChunkiesPark()
	case "mythrad":
		runMythrad()
	default:
		log.Fatalf("unknown executable %q", filepath.Base(os.Args[0]))
	}
}
