// Package main is mythrad: the Wake Up Mythra game service. The netcode
// architecture is journaled deterministic simulation — every surface runs
// the identical fixed-point wasm sim and the server streams an ordered
// event journal, never state (docs/netcode.md is the contract).
//
// This binary is currently the doorman shell of that design: it terminates
// WebTransport, distributes the wasm modules and content-addressed assets,
// and serves /wt-info. Sessions are accepted and immediately closed with
// code 4503 while the journal protocol lands; the page renders a
// construction notice. The behavior distribution ladder (live/shadow slots,
// hot reload, client module hash) stays fully operational because module
// distribution is unchanged by the protocol.
//
//   - tier 1 (content): dog skins are content-addressed assets mounted from a
//     ConfigMap; clients stream them in on first sight. Shipping a new skin
//     is a data change.
//   - tier 2 (behavior): the live behavior module and an optional shadow
//     module (Rust -> wasm, executed by wazero) are mounted from a ConfigMap
//     and hot-reloaded without a restart. The client presentation module
//     rides the same mount, addressed by content hash.
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

// The park module is the complete game state machine (docs/netcode.md);
// the authority loop instantiates it as the journal's validator.
//
//go:embed behaviors/park.wasm
var defaultParkModule []byte

// closeRebuilding is the application error code sessions receive while the
// journal protocol is being built: the dial path (certs, UDP, H3 upgrade)
// stays verifiable end to end in prod even though no game session exists.
const closeRebuilding = 4503

var (
	mHandshakes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_handshakes_total", Help: "Session handshakes."}, []string{"result"})
	mBehaviorReloads = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mythra_behavior_reloads_total", Help: "Behavior module hot-reloads."}, []string{"slot", "result"})
	mBehaviorInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mythra_behavior_script", Help: "1 for the currently loaded module hash per slot."}, []string{"slot", "hash"})
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
// divergence by construction.
type behaviorSlot struct {
	mu   sync.Mutex
	name string // "live" | "shadow"
	hash string
	rt   wazero.Runtime
	mem  api.Memory
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
	mod, err := rt.Instantiate(ctx, module)
	if err != nil {
		rt.Close(ctx)
		mBehaviorReloads.WithLabelValues(b.name, "error").Inc()
		return fmt.Errorf("%s: %w", b.name, err)
	}
	if mod.Memory() == nil {
		rt.Close(ctx)
		mBehaviorReloads.WithLabelValues(b.name, "error").Inc()
		return fmt.Errorf("%s: module must export memory", b.name)
	}
	if b.rt != nil {
		b.rt.Close(ctx)
	}
	mBehaviorInfo.DeletePartialMatch(prometheus.Labels{"slot": b.name})
	mBehaviorInfo.WithLabelValues(b.name, hash).Set(1)
	mBehaviorReloads.WithLabelValues(b.name, "ok").Inc()
	b.hash, b.rt, b.mem = hash, rt, mod.Memory()
	log.Printf("behavior %s loaded: %s", b.name, hash)
	return nil
}

func (b *behaviorSlot) unload() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rt != nil {
		b.rt.Close(context.Background())
		b.rt, b.mem, b.hash = nil, nil, ""
		mBehaviorInfo.DeletePartialMatch(prometheus.Labels{"slot": b.name})
		log.Printf("behavior %s unloaded", b.name)
	}
}

func (b *behaviorSlot) loaded() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hash, b.rt != nil
}

// clientModule tracks the presentation module served to browsers: raw bytes
// plus a short content hash. Pages fetch by hash, so the bytes are
// immutable per URL and a hash flip is the update signal.
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
	mu    sync.Mutex
	byRef map[string]*asset // "name.hash" -> asset
	skin  string            // current default skin ref: "cell" or "name.hash"
	dir   string
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
	// so state can never reference an asset the server cannot serve.
	c.skin = "cell"
	if skin != "cell" {
		for ref, a := range c.byRef {
			if a.name == skin {
				c.skin = ref
			}
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
		mHandshakes.WithLabelValues("rebuilding").Inc()
		sess.CloseWithError(closeRebuilding, "rebuilding: journal protocol landing")
	})

	// Connect info, game modules, and content-addressed assets, served
	// through the normal Cloudflare-proxied ingress (the apex path-routes
	// these here; the page itself is the web app's job). The WebTransport
	// dial goes direct to PUBLIC_ADDR with the cert hash from /wt-info.
	pageMux := http.NewServeMux()
	pageMux.HandleFunc("/wt-info", func(w http.ResponseWriter, r *http.Request) {
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
	})
	pageMux.HandleFunc("/behavior/client.wasm", func(w http.ResponseWriter, r *http.Request) {
		module, hash := client.get()
		if len(module) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/wasm")
		w.Header().Set("ETag", `"`+hash+`"`)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Write(module)
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

	log.Printf("mythrad: wt=:%d http=:%d metrics=:%d public=%s", wtPort, httpPort, metricsPort, publicAddr)
	go func() { log.Fatal(wt.ListenAndServe()) }()
	go func() { log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", httpPort), pageMux)) }()
	go func() { log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", metricsPort), obsMux)) }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	log.Print("SIGTERM: closing sessions (clients redial against the replacement)")
	wt.Close()
}
