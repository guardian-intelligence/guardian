// Package agent is hostd's core: a single-threaded reconciler that syncs
// full observed state up to the control plane, receives full desired state
// down, and converges the local substrate (workspace zvols, sandbox VMs, the
// warm pool) toward it. The kubelet analogy is deliberate: the control plane
// decides what runs where; this package decides nothing, it makes it so.
//
// Everything stateful advances inside Tick, which is deliberately
// synchronous and driven by an injected clock — the sim harness executes the
// exact code paths production runs, one deterministic step at a time.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

// Metrics counts what the agent has done; all fields are safe for
// concurrent reads.
type Metrics struct {
	SyncFailures      atomic.Int64
	RejectedLeases    atomic.Int64
	FailedLeases      atomic.Int64
	SealedGenerations atomic.Int64
	ReapedGenerations atomic.Int64
	OrphansDestroyed  atomic.Int64
}

// Agent is the hostd core. Construct with New, drive with Run (production)
// or HandleSync/Tick directly (tests and the sim harness).
type Agent struct {
	cfg        Config
	zvols      zvol.Driver
	vms        vm.Driver
	logger     *slog.Logger
	httpClient *http.Client
	credential string
	hostSecret []byte
	now        func() time.Time
	newID      func() string
	bootID     string
	metrics    Metrics

	mu      sync.Mutex
	leases  map[string]*lease
	desired map[string]DesiredLease
	// quarantined holds lease IDs the control plane named in the last sync
	// but whose specs we rejected. They are neither advanced nor collected:
	// a validation failure must not read as a withdrawal.
	quarantined map[string]bool
	reap        []zvol.GenerationID
	poolTargets map[vm.Class]int
	// synced gates every destructive action: until the first successful
	// exchange with the control plane, a freshly restarted hostd must not
	// GC anything, because it cannot yet tell an orphan from a lease it
	// simply has not heard about.
	synced bool
}

// Options carries the injectable seams. Production uses the zero value;
// tests and the sim harness override.
type Options struct {
	Now    func() time.Time
	NewID  func() string
	Logger *slog.Logger
	// HTTPClient serves the sync exchange; unused when the harness calls
	// HandleSync directly.
	HTTPClient *http.Client
}

// New wires an Agent. The credential authenticates sync calls; hostSecret
// derives checkout tokens.
func New(cfg Config, zvols zvol.Driver, vms vm.Driver, credential string, hostSecret []byte, options Options) (*Agent, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	// A short secret makes checkout tokens forgeable by anyone who knows the
	// scheme and the lease IDs; 32 bytes matches the checkout server's floor.
	if len(hostSecret) < 32 {
		return nil, fmt.Errorf("agent: host secret must be at least 32 bytes")
	}
	if credential == "" {
		return nil, fmt.Errorf("agent: control-plane credential is required")
	}
	agent := &Agent{
		cfg:         cfg,
		zvols:       zvols,
		vms:         vms,
		logger:      options.Logger,
		httpClient:  options.HTTPClient,
		credential:  credential,
		hostSecret:  hostSecret,
		now:         options.Now,
		newID:       options.NewID,
		leases:      map[string]*lease{},
		desired:     map[string]DesiredLease{},
		quarantined: map[string]bool{},
		poolTargets: map[vm.Class]int{},
	}
	if agent.logger == nil {
		agent.logger = slog.Default()
	}
	if agent.httpClient == nil {
		agent.httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if agent.now == nil {
		agent.now = time.Now
	}
	if agent.newID == nil {
		agent.newID = randomID
	}
	agent.bootID = agent.newID()
	return agent, nil
}

func randomID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(fmt.Sprintf("agent: reading randomness: %v", err))
	}
	return hex.EncodeToString(buf[:])
}

// Metrics exposes the agent's counters.
func (a *Agent) Metrics() *Metrics { return &a.metrics }

// Synced reports whether the agent has completed a sync exchange in its
// current life. Until it has, Tick refuses every destructive action.
func (a *Agent) Synced() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.synced
}

// HandleSync applies one desired-state snapshot as if it arrived from the
// control plane. The sim harness and tests use it in place of syncOnce.
func (a *Agent) HandleSync(response SyncResponse) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.applyDesired(response)
}

// Report builds the sync request the agent would send right now.
func (a *Agent) Report(ctx context.Context) (SyncRequest, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.buildReport(ctx)
}

// Run drives the agent until the context ends: a sync exchange, then
// convergence ticks between exchanges.
func (a *Agent) Run(ctx context.Context) error {
	for {
		interval := a.cfg.SyncInterval
		if pollAfter, err := a.syncOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			a.metrics.SyncFailures.Add(1)
			a.logger.Error("sync", "err", err)
		} else if pollAfter > 0 {
			interval = pollAfter
		}

		a.Tick(ctx)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
