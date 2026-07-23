// Package agent reconciles a host's pre-booted QEMU pool with immutable local
// GitHub assignments and durable generation selections from the control plane.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
	postflighttiming "github.com/guardian-intelligence/guardian/src/services/postflight/timing"
)

type Metrics struct {
	SyncFailures             atomic.Int64
	RejectedMembers          atomic.Int64
	RejectedAssignments      atomic.Int64
	ColdFallbacks            atomic.Int64
	FailedClosedAssignments  atomic.Int64
	SealedGenerations        atomic.Int64
	ReapedGenerations        atomic.Int64
	OrphansDestroyed         atomic.Int64
	RejectedJobPlans         atomic.Int64
	JobPlanWatchFailures     atomic.Int64
	JobPlanMisses            atomic.Int64
	JobPlanResolveAttempts   atomic.Int64
	JobPlanResolveFailures   atomic.Int64
	JobPlanResolveSuccesses  atomic.Int64
	StorageAdmissionFailures atomic.Int64
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
	planMu             sync.RWMutex
	traceMu            sync.Mutex
	updateMu           sync.Mutex
	deferredUpdateMu   sync.Mutex
	updateWG           sync.WaitGroup
	updateWorkers      map[vm.ID]chan struct{}
	deferredUpdates    map[vm.ID]struct{}
	jobPlans           map[int64][]syncproto.JobPlan
	planCursor         string
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
		jobPlans:        map[int64][]syncproto.JobPlan{},
		updateWorkers:   map[vm.ID]chan struct{}{},
		deferredUpdates: map[vm.ID]struct{}{},
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
	return a.buildReport(ctx)
}

func (a *Agent) Run(ctx context.Context) error {
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		a.watchJobPlans(ctx)
	}()
	defer func() {
		<-watchDone
		a.updateWG.Wait()
		a.closeTraceFiles()
	}()
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
			a.dispatchVMUpdate(ctx, id)
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

func (a *Agent) dispatchVMUpdate(ctx context.Context, id vm.ID) {
	a.updateMu.Lock()
	worker := a.updateWorkers[id]
	if worker == nil {
		worker = make(chan struct{}, 1)
		a.updateWorkers[id] = worker
		a.updateWG.Add(1)
		go func() {
			defer a.updateWG.Done()
			for {
				select {
				case <-ctx.Done():
					a.updateMu.Lock()
					if a.updateWorkers[id] == worker {
						delete(a.updateWorkers, id)
					}
					a.updateMu.Unlock()
					return
				case <-worker:
					a.HandleVMUpdate(ctx, id)
					a.updateMu.Lock()
					if len(worker) == 0 && a.updateWorkers[id] == worker {
						delete(a.updateWorkers, id)
						a.updateMu.Unlock()
						return
					}
					a.updateMu.Unlock()
				}
			}
		}()
	}
	select {
	case worker <- struct{}{}:
	default:
	}
	a.updateMu.Unlock()
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

// HandleVMUpdate converges only the VM named by the event. Pool maintenance
// and control-plane synchronization run independently.
func (a *Agent) HandleVMUpdate(ctx context.Context, id vm.ID) {
	a.mu.Lock()
	synced := a.synced
	a.mu.Unlock()
	if !synced {
		a.deferVMUpdate(id)
		return
	}
	status, err := a.vms.Status(ctx, id)
	if err != nil {
		a.clearDeferredUpdate(id)
		a.mu.Lock()
		assignments := cloneMap(a.assignments)
		a.mu.Unlock()
		for _, record := range assignments {
			record.mu.Lock()
			if record.vmID != string(id) || record.state.Terminal() {
				record.mu.Unlock()
				continue
			}
			record.vmID = ""
			a.failClosed(ctx, record, "pool member disappeared after provider acquisition")
			record.mu.Unlock()
			return
		}
		a.logger.Error("observing updated vm", "vm", id, "err", err)
		return
	}
	if status.Phase == vm.PhaseJobAssigned {
		a.mu.Lock()
		owned := assignmentOwnsMember(a.assignments, status.Incarnation)
		a.mu.Unlock()
		if !owned {
			plan, ok := a.jobPlanFor(status)
			if !ok {
				a.metrics.JobPlanMisses.Add(1)
				a.metrics.JobPlanResolveAttempts.Add(1)
				trace, traceErr := a.traceFor(status.Incarnation, status.Assignment.RunnerName, string(status.ID))
				if traceErr != nil {
					a.logger.Error("opening plan-resolution evidence", "member_id", status.Incarnation, "vm", status.ID, "err", traceErr)
				}
				a.appendTrace(trace, nil, "job_plan_resolution_started", func(event *traceEvent) {
					event.CheckRunID = status.Assignment.CheckRunID
					event.Repo = status.Assignment.Identity.Repository
				})
				started := time.Now()
				a.logger.Info("postflight.hostd.job_plan.resolve_started", "vm", id, "member_id", status.Incarnation,
					"check_run_id", status.Assignment.CheckRunID, "run_id", status.Assignment.Identity.RunID)
				resolved, err := a.resolveJobPlan(ctx, status)
				if err != nil {
					a.metrics.JobPlanResolveFailures.Add(1)
					a.appendTrace(trace, nil, "job_plan_resolution_failed", func(event *traceEvent) {
						event.CheckRunID = status.Assignment.CheckRunID
						event.Repo = status.Assignment.Identity.Repository
						event.FailureReason = err.Error()
					})
					var resolutionErr *jobPlanResolveError
					if errors.As(err, &resolutionErr) && resolutionErr.status == http.StatusUnprocessableEntity {
						a.clearDeferredUpdate(id)
						a.logger.Error("postflight.hostd.job_plan.resolve_rejected", "vm", id, "member_id", status.Incarnation,
							"check_run_id", status.Assignment.CheckRunID, "duration_ns", time.Since(started).Nanoseconds())
						if destroyErr := a.vms.Destroy(ctx, id); destroyErr != nil {
							a.logger.Error("recycling rejected assignment vm", "vm", id, "member_id", status.Incarnation, "err", destroyErr)
						}
						return
					}
					a.deferVMUpdate(id)
					a.logger.Error("postflight.hostd.job_plan.resolve_failed", "vm", id, "member_id", status.Incarnation,
						"check_run_id", status.Assignment.CheckRunID, "duration_ns", time.Since(started).Nanoseconds(), "err", err)
					return
				}
				a.metrics.JobPlanResolveSuccesses.Add(1)
				a.appendTrace(trace, nil, "job_plan_resolution_completed", func(event *traceEvent) {
					event.CheckRunID = status.Assignment.CheckRunID
					event.JobID, _ = strconv.ParseInt(resolved.ExecutionID, 10, 64)
					event.Repo = status.Assignment.Identity.Repository
				})
				a.logger.Info("postflight.hostd.job_plan.resolve_completed", "vm", id, "member_id", status.Incarnation,
					"check_run_id", status.Assignment.CheckRunID, "plan_id", resolved.PlanID,
					"duration_ns", time.Since(started).Nanoseconds())
				plan, ok = resolved, true
			}
			a.clearDeferredUpdate(id)
			spec := desiredFromPlan(plan, status)
			if err := validateAssignment(spec); err != nil {
				a.metrics.RejectedAssignments.Add(1)
				a.logger.Error("rejecting locally bound job plan", "plan_id", plan.PlanID, "err", err)
				return
			}
			point := a.timing.Point("job_plan_bound_locally")
			trace, traceErr := a.traceFor(status.Incarnation, status.Assignment.RunnerName, string(status.ID))
			if traceErr != nil {
				a.logger.Error("opening locally bound assignment evidence", "member_id", status.Incarnation, "vm", status.ID, "err", traceErr)
			}
			record := &assignment{
				memberID: status.Incarnation,
				spec:     spec, state: syncproto.AssignmentObserved, since: a.now(),
				trace:    trace,
				observed: observedAssignment(status.Assignment),
				updateTiming: vm.TimingPoint{Event: point.Event, Source: point.Source, BootID: point.BootID,
					Sequence: point.Sequence, MonotonicNS: point.MonotonicNS, UnixNS: point.UnixNS},
			}
			a.mu.Lock()
			if assignmentOwnsMember(a.assignments, status.Incarnation) {
				a.mu.Unlock()
				return
			}
			a.assignments[spec.AssignmentID] = record
			a.desiredAssignments[spec.AssignmentID] = spec
			a.mu.Unlock()
			a.logger.Info("postflight.hostd.job_plan.bound", "plan_id", plan.PlanID, "member_id", status.Incarnation,
				"check_run_id", status.Assignment.CheckRunID)
		}
	}
	a.mu.Lock()
	assignments := cloneMap(a.assignments)
	quarantined := cloneMap(a.quarantinedJobs)
	a.mu.Unlock()
	a.stepStatus(ctx, status, assignments, quarantined)
}
