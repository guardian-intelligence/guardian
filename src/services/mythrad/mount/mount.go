// Package mount is the behavior mount: the distributed wasm modules a
// process serves and simulates with, hot-reloaded from a ConfigMap-backed
// directory. Bytes are immutable per content hash; a hash flip on a
// verdict is the client's update signal.
package mount

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

//go:embed behaviors/client.wasm
var DefaultClient []byte

//go:embed behaviors/park.wasm
var DefaultPark []byte

var mInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "mythra_behavior_script", Help: "1 for the currently loaded module hash per slot."}, []string{"slot", "hash"})

// Module tracks a distributed module's bytes plus content hash.
type Module struct {
	mu    sync.Mutex
	slot  string
	bytes []byte
	hash  string
}

func NewModule(slot string, module []byte) *Module {
	m := &Module{slot: slot}
	m.Set(module)
	return m
}

func (m *Module) Set(module []byte) {
	sum := sha256.Sum256(module)
	hash := hex.EncodeToString(sum[:4])
	m.mu.Lock()
	changed := hash != m.hash
	m.bytes, m.hash = module, hash
	m.mu.Unlock()
	if changed {
		mInfo.DeletePartialMatch(prometheus.Labels{"slot": m.slot})
		mInfo.WithLabelValues(m.slot, hash).Set(1)
		log.Printf("%s module loaded: %s", m.slot, hash)
	}
}

func (m *Module) Get() ([]byte, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bytes, m.hash
}

// Watch polls the mounted behavior dir; ConfigMap edits land on the mount
// within ~a minute of Flux applying them, with no pod restart.
func Watch(dir string, client, park *Module) {
	load := func() {
		if module, err := os.ReadFile(filepath.Join(dir, "client.wasm")); err == nil && len(module) > 8 {
			client.Set(module)
		}
		if module, err := os.ReadFile(filepath.Join(dir, "park.wasm")); err == nil && len(module) > 8 {
			park.Set(module)
		}
	}
	load()
	for range time.Tick(2 * time.Second) {
		load()
	}
}
