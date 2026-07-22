// Package agent reconciles a host's pre-booted QEMU pool with immutable local
// GitHub assignments and durable generation selections from the control plane.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
	postflighttiming "github.com/guardian-intelligence/guardian/src/services/postflight/timing"
)

type Metrics struct {
	SyncFailures            atomic.Int64
	RejectedMembers         atomic.Int64
	RejectedAssignments     atomic.Int64
	ColdFallbacks           atomic.Int64
	FailedClosedAssignments atomic.Int64
	SealedGenerations       atomic.Int64
	ReapedGenerations       atomic.Int64
	OrphansDestroyed        atomic.Int64
}

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
	timing     *postflighttiming.Recorder
	traces     map[string]*traceState

	mu                 sync.Mutex
	assignments        map[string]*assignment
	desiredMembers     map[string]syncproto.DesiredPoolMember
	desiredAssignments map[string]syncproto.DesiredAssignment
	quarantinedMembers map[string]bool
	quarantinedJobs    map[string]bool
	reap               []zvol.GenerationID
	poolTargets        map[vm.Class]int
	synced             bool
}

type Options struct {
	Now        func() time.Time
	NewID      func() string
	Logger     *slog.Logger
	HTTPClient *http.Client
	Timing     *postflighttiming.Recorder
}

func New(cfg Config, zvols zvol.Driver, vms vm.Driver, credential string, hostSecret []byte, options Options) (*Agent, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if len(hostSecret) < 32 {
		return nil, fmt.Errorf("agent: host secret must be at least 32 bytes")
	}
	if credential == "" {
		return nil, fmt.Errorf("agent: control-plane credential is required")
	}
	a := &Agent{
		cfg: cfg, zvols: zvols, vms: vms, credential: credential,
		hostSecret: hostSecret, logger: options.Logger, httpClient: options.HTTPClient,
		now: options.Now, newID: options.NewID,
		assignments:        map[string]*assignment{},
		desiredMembers:     map[string]syncproto.DesiredPoolMember{},
		desiredAssignments: map[string]syncproto.DesiredAssignment{},
		quarantinedMembers: map[string]bool{}, quarantinedJobs: map[string]bool{},
		poolTargets: map[vm.Class]int{}, traces: map[string]*traceState{},
	}
	if a.logger == nil {
		a.logger = slog.Default()
	}
	if a.httpClient == nil {
		a.httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if a.now == nil {
		a.now = time.Now
	}
	if a.newID == nil {
		a.newID = randomID
	}
	if cfg.TraceDir != "" {
		if err := os.MkdirAll(cfg.TraceDir, 0o750); err != nil {
			return nil, fmt.Errorf("agent: create trace directory: %w", err)
		}
	}
	a.bootID = a.newID()
	a.timing = options.Timing
	if a.timing == nil {
		recorder, err := postflighttiming.New("hostd-agent:"+cfg.HostID, a.bootID)
		if err != nil {
			return nil, err
		}
		a.timing = recorder
	}
	return a, nil
}

func randomID() string {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(fmt.Sprintf("agent: reading randomness: %v", err))
	}
	return hex.EncodeToString(value[:])
}

func (a *Agent) Metrics() *Metrics { return &a.metrics }

func (a *Agent) Synced() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.synced
}

func (a *Agent) HandleSync(response syncproto.SyncResponse) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.applyDesired(response)
}

func (a *Agent) Report(ctx context.Context) (syncproto.SyncRequest, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.buildReport(ctx)
}

func (a *Agent) Run(ctx context.Context) error {
	defer a.closeTraceFiles()
	var updates <-chan vm.ID
	if source, ok := a.vms.(vm.UpdateSource); ok {
		updates = source.Updates()
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case id := <-updates:
			a.HandleVMUpdate(ctx, id)
			if status, err := a.vms.Status(ctx, id); err == nil && status.Assignment.RequestID != "" {
				// Assignment is the latency-critical edge. Immediately publish
				// it and consume the exact binding response instead of waiting for
				// the periodic repair interval.
				if _, err := a.syncOnce(ctx); err != nil {
					a.metrics.SyncFailures.Add(1)
					a.logger.Error("assignment sync", "vm", id, "err", err)
				}
			}
			a.Tick(ctx)
			resetTimer(timer, a.cfg.SyncInterval)
		case <-timer.C:
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
			timer.Reset(interval)
		}
	}
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

// HandleVMUpdate performs only local convergence. Run adds the immediate sync
// on assignment; tests can call this method deterministically.
func (a *Agent) HandleVMUpdate(ctx context.Context, id vm.ID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.synced {
		return
	}
	status, err := a.vms.Status(ctx, id)
	if err != nil {
		a.logger.Error("observing updated vm", "vm", id, "err", err)
		return
	}
	a.stepStatus(ctx, status)
}
