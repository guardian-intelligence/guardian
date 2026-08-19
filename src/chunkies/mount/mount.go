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

//go:embed behaviors/sim.wasm
var DefaultSim []byte

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
//
// accept, when non-nil, gates every new byte content before it becomes the
// slot's module — the process's defense against a mount that converged
// ahead of the process image (a refused module keeps the current one
// serving, counted by chunkies_behavior_refused_total). A nil accept takes
// everything: right for a process that only distributes bytes and never
// runs them, since the consumer's own boot gate decides there.
func Watch(dir string, accept func(slot string, module []byte) error, client, chunk *Module) {
	tried := map[string]string{}
	loadSlot := func(slot string, m *Module) {
		module, err := os.ReadFile(filepath.Join(dir, slot+".wasm"))
		if err != nil || len(module) <= 8 {
			return
		}
		sum := sha256.Sum256(module)
		hash := hex.EncodeToString(sum[:4])
		if tried[slot] == hash {
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
	load := func() {
		loadSlot("client", client)
		loadSlot("sim", chunk)
	}
	load()
	for range time.Tick(2 * time.Second) {
		load()
	}
}
