package gateway

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
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
