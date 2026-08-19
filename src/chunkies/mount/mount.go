// Package mount is the behavior mount: the distributed wasm modules a
// process serves and simulates with, hot-reloaded from a ConfigMap-backed
// directory. Bytes are immutable per content hash; a hash flip on a
// verdict is the client's update signal.
package mount

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var mInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "chunkies_behavior_script", Help: "1 for the currently loaded module hash per slot."}, []string{"slot", "hash"})

var mRefused = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "chunkies_behavior_refused_total", Help: "Mounted module bytes refused by the process's acceptance gate."}, []string{"slot"})

// Module tracks a distributed module's bytes plus content hash.
type Module struct {
	mu    sync.Mutex
	slot  string
	bytes []byte
	hash  string
	sum   [32]byte
}

// NewModule is born empty: the mounted behavior dir is a module's only
// source. The framework ships no game bytes of its own — a game arrives
// entirely as content — so a process that needs a module must WaitLoaded
// before serving rather than running on a stale built-in.
func NewModule(slot string) *Module {
	return &Module{slot: slot}
}

// Require is the boot-time gate: every named slot must already hold
// bytes. The mount exists before the process in every environment (a
// ConfigMap volume mounts before the container starts; dev points at
// committed files), so an empty slot is a misconfiguration to crash on
// loudly — never a condition to wait out.
func Require(modules ...*Module) error {
	for _, m := range modules {
		if b, _ := m.Get(); len(b) == 0 {
			return fmt.Errorf("behavior mount has no %q module", m.slot)
		}
	}
	return nil
}

func (m *Module) Set(module []byte) {
	sum := sha256.Sum256(module)
	hash := hex.EncodeToString(sum[:4])
	m.mu.Lock()
	changed := hash != m.hash
	m.bytes, m.hash, m.sum = module, hash, sum
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

// Sum is the module's full sha256 — the identity a checkpoint manifest
// pins (codec.Checkpoint CW/PW), computed once per Set, never per read.
func (m *Module) Sum() [32]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sum
}

// loadSlot adopts the dir's current bytes for one slot, once per content
// hash. accept, when non-nil, gates every new byte content before it
// becomes the slot's module — the process's defense against a mount that
// converged ahead of the process image (a refused module keeps the
// current one serving, counted by chunkies_behavior_refused_total). A nil
// accept takes everything: right for a process that only distributes
// bytes and never runs them, since the consumer's own boot gate decides
// there.
func loadSlot(dir, slot string, accept func(slot string, module []byte) error, m *Module, tried map[string]string) {
	module, err := os.ReadFile(filepath.Join(dir, slot+".wasm"))
	if err != nil || len(module) <= 8 {
		return
	}
	sum := sha256.Sum256(module)
	hash := hex.EncodeToString(sum[:4])
	if _, cur := m.Get(); cur == hash || tried[slot] == hash {
		return
	}
	tried[slot] = hash
	if accept != nil {
		if err := accept(slot, module); err != nil {
			log.Printf("%s module %s refused: %v", slot, hash, err)
			mRefused.WithLabelValues(slot).Inc()
			return
		}
	}
	m.Set(module)
}

// Load scans the mounted behavior dir once, synchronously — the boot-time
// pass, so a process can Require its content before serving.
func Load(dir string, accept func(slot string, module []byte) error, client, chunk *Module) {
	tried := map[string]string{}
	loadSlot(dir, "client", accept, client, tried)
	loadSlot(dir, "sim", accept, chunk, tried)
}

// Watch polls the mounted behavior dir; ConfigMap edits land on the mount
// within ~a minute of Flux applying them, with no pod restart.
func Watch(dir string, accept func(slot string, module []byte) error, client, chunk *Module) {
	tried := map[string]string{}
	for range time.Tick(2 * time.Second) {
		loadSlot(dir, "client", accept, client, tried)
		loadSlot(dir, "sim", accept, chunk, tried)
	}
}
