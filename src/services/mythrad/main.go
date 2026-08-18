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
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
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

func main() {
	switch filepath.Base(os.Args[0]) {
	case "chunkies-gateway":
		runChunkiesGateway()
	case "chunkies-park":
		runChunkiesPark()
	default:
		log.Fatalf("unknown executable %q", filepath.Base(os.Args[0]))
	}
}
