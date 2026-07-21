package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/checkoutbundle"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

// Tick runs one convergence pass: advance every lease one observable step,
// execute reap verbs, reconcile the warm pool, and collect orphans. Every
// action is idempotent and advances only on observed substrate state, so a
// repeated or interrupted Tick is always safe.
func (a *Agent) Tick(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.synced {
		return
	}
	vms, err := a.listVMs(ctx)
	if err != nil {
		a.logger.Error("listing vms", "err", err)
		return
	}
	for _, id := range sortedLeaseIDs(a.leases) {
		a.stepLease(ctx, a.leases[id], vms)
	}
	a.reapGenerations(ctx)
	a.reconcilePool(ctx, vms)
	a.collectOrphans(ctx, vms)
}

// vmView indexes one List call so a Tick makes consistent decisions.
type vmView struct {
	byID      map[vm.ID]vm.Status
	byLease   map[string]vm.Status
	warm      map[vm.Class][]vm.ID
	countByCl map[vm.Class]int
}

func (a *Agent) listVMs(ctx context.Context) (*vmView, error) {
	statuses, err := a.vms.List(ctx)
	if err != nil {
		return nil, err
	}
	view := &vmView{
		byID:      map[vm.ID]vm.Status{},
		byLease:   map[string]vm.Status{},
		warm:      map[vm.Class][]vm.ID{},
		countByCl: map[vm.Class]int{},
	}
	for _, status := range statuses {
		view.byID[status.ID] = status
		view.countByCl[status.Class]++
		if status.Lease != "" {
			view.byLease[status.Lease] = status
		} else if status.Phase == vm.PhaseWarm {
			desiredImage := a.cfg.Images[status.Class]
			if desiredImage == "" || status.Image == desiredImage {
				view.warm[status.Class] = append(view.warm[status.Class], status.ID)
			}
		}
	}
	for _, ids := range view.warm {
		sortVMIDs(ids)
	}
	return view, nil
}

func (a *Agent) stepLease(ctx context.Context, record *lease, vms *vmView) {
	// A terminal lease that still holds a VM had its destroy fail earlier;
	// retry it every tick until the slot is actually free. Destroy is
	// idempotent, so this converges without leaking the slot.
	if record.state.Terminal() {
		if record.vmID != "" {
			a.destroyLeaseVM(ctx, record)
		}
		return
	}

	// A quarantined lease was named by the control plane but its spec was
	// rejected this sync. Leave it exactly as it is — neither advance nor
	// withdraw — so a transient validation failure never mutates it.
	if a.quarantined[record.spec.LeaseID] {
		return
	}

	now := a.now()
	if record.vmID != "" {
		if status, ok := vms.byID[vm.ID(record.vmID)]; ok {
			a.appendOriginTiming(record, status.Timing)
		}
	}

	// Cancellation wins over everything. The desired set is full state, so
	// a live lease the control plane no longer mentions has been withdrawn
	// — same as an explicit cancel, just less polite.
	if _, wanted := a.desired[record.spec.LeaseID]; record.spec.State == syncproto.DesiredCancel || !wanted {
		a.cancelLease(ctx, record)
		return
	}

	// A seal-only lease (its runner already exited on a prior life, or the
	// control plane is asking us to seal a workspace we still hold) must go
	// straight to sealing — it must never claim a fresh VM and re-attach the
	// workspace the control plane asked us to preserve unchanged. If a VM
	// still carries the lease, fall through and let it exit first.
	if _, hasVM := vms.byLease[record.spec.LeaseID]; record.spec.State == syncproto.DesiredSeal && !hasVM {
		if record.state != syncproto.StateExited {
			// Ensure the workspace record is populated for the seal step;
			// a fresh post-crash record has no device yet.
			record.enter(syncproto.StateExited, now)
		}
	}

	// Deadline enforcement: a lease stuck in any state releases its slot.
	if deadline, ok := stateDeadlines[record.state]; ok && now.Sub(record.since) > deadline {
		a.failLease(ctx, record, fmt.Sprintf("deadline exceeded in %s", record.state))
		return
	}

	switch record.state {
	case syncproto.StatePending:
		record.enter(syncproto.StateClaiming, now)

	case syncproto.StateClaiming:
		// Crash recovery first: a VM may already carry this lease.
		if status, ok := vms.byLease[record.spec.LeaseID]; ok {
			record.vmID = string(status.ID)
			if status.Assignment.RequestID != "" {
				if err := a.routeAssignment(record, status.Assignment); err != nil {
					a.failLease(ctx, record, "local assignment: "+err.Error())
					return
				}
			}
			switch status.Phase {
			case vm.PhaseAssigned:
				record.enter(syncproto.StateAssigning, now)
			case vm.PhaseBound:
				record.enter(syncproto.StateBinding, now)
			case vm.PhaseListening:
				record.enter(syncproto.StateListening, now)
			case vm.PhaseJobAssigned:
				if err := a.bindAssigned(ctx, record, status.ID); err != nil {
					a.failLease(ctx, record, "rendezvous: "+err.Error())
					return
				}
				record.enter(syncproto.StateBinding, now)
			case vm.PhaseWorkerReady:
				record.enter(syncproto.StateAuthorizing, now)
			case vm.PhaseHookBlocked:
				record.identity = identityReport(status.Identity)
				record.enter(syncproto.StateHookBlocked, now)
			case vm.PhaseReady:
				record.enter(syncproto.StateReady, now)
			default:
				record.enter(syncproto.StateBinding, now)
			}
			return
		}
		class := vm.Class(record.spec.RunnerClass)
		candidates := vms.warm[class]
		if len(candidates) == 0 {
			return // pool governor is refilling; deadline bounds the wait
		}
		id := candidates[0]
		vms.warm[class] = candidates[1:]
		// Claim the VM before preparing: an ambiguous delivery failure must
		// destroy this slot, never return it to the warm pool.
		record.vmID = string(id)
		if record.spec.JITConfig == "" {
			a.failLease(ctx, record, "prepare: listener credential was already consumed")
			return
		}
		if err := a.vms.Prepare(ctx, id, a.preparation(record)); err != nil {
			a.failLease(ctx, record, "prepare: "+err.Error())
			return
		}
		record.enter(syncproto.StateAssigning, now)

	case syncproto.StateBinding:
		status, ok := vms.byID[vm.ID(record.vmID)]
		if !ok || status.Phase == vm.PhaseGone {
			a.failLease(ctx, record, "vm disappeared")
			return
		}
		switch status.Phase {
		case vm.PhaseBound:
			hostAfter := time.Now().UnixNano()
			a.appendTrace(record, "mounts_ready", func(event *traceEvent) {
				event.Repo = record.executionSpec().RepositoryFullName
				event.GenerationSet = generationSet(record)
				event.Volumes = traceVolumes(record, true)
			})
			a.appendTrace(record, "clock_checked", func(event *traceEvent) {
				event.Repo = record.executionSpec().RepositoryFullName
				event.GenerationSet = generationSet(record)
				event.Clock = &traceClock{
					HostBeforeUnixNS: record.hostBeforeUnixNS, HostAfterUnixNS: hostAfter,
					GuestUnixNS: status.Clock.UnixNS, MaxSkewNS: int64(5 * time.Second),
					GuestSynchronized: status.Clock.Synchronized, Clocksource: status.Clock.Clocksource,
					AfterRestore: status.Clock.AfterRestore,
				}
			})
			if record.assignment == nil {
				a.failLease(ctx, record, "authorize: restored generation has no local assignment")
				return
			}
			if err := a.vms.Authorize(ctx, vm.ID(record.vmID), a.authorization(record)); err != nil {
				a.failLease(ctx, record, "authorize: "+err.Error())
				return
			}
			a.appendTrace(record, "worker_authorization_sent", func(event *traceEvent) {
				traceAssignment(record, event)
			})
			record.enter(syncproto.StateAuthorizing, now)
		case vm.PhaseExited:
			a.finishRunner(ctx, record, status)
		}

	case syncproto.StateAssigning:
		status, ok := vms.byID[vm.ID(record.vmID)]
		if !ok || status.Phase == vm.PhaseGone {
			a.failLease(ctx, record, "vm disappeared")
			return
		}
		switch status.Phase {
		case vm.PhaseListening:
			a.appendTrace(record, "pool_ready", func(event *traceEvent) {
				event.RunnerName = record.spec.LeaseID
				event.VMID = record.vmID
				event.Platform = a.platformEvidence()
			})
			record.enter(syncproto.StateListening, now)
		case vm.PhaseExited:
			a.finishRunner(ctx, record, status)
		}

	case syncproto.StateListening:
		status, ok := vms.byID[vm.ID(record.vmID)]
		if !ok || status.Phase == vm.PhaseGone {
			a.failLease(ctx, record, "vm disappeared")
			return
		}
		switch status.Phase {
		case vm.PhaseJobAssigned:
			if err := a.routeAssignment(record, status.Assignment); err != nil {
				a.failLease(ctx, record, "local assignment: "+err.Error())
				return
			}
			a.appendTrace(record, "assignment_observed", func(event *traceEvent) {
				traceAssignment(record, event)
			})
			if err := a.bindAssigned(ctx, record, status.ID); err != nil {
				a.failLease(ctx, record, "rendezvous: "+err.Error())
				return
			}
			record.enter(syncproto.StateBinding, now)
		case vm.PhaseExited:
			a.finishRunner(ctx, record, status)
		}

	case syncproto.StateHookBlocked:
		status, ok := vms.byID[vm.ID(record.vmID)]
		if !ok || status.Phase == vm.PhaseGone {
			a.failLease(ctx, record, "vm disappeared")
			return
		}
		if status.Phase == vm.PhaseExited {
			a.finishRunner(ctx, record, status)
			return
		}
		a.appendTrace(record, "job_hook_blocked", func(event *traceEvent) {
			traceIdentity(record, event)
		})
		a.appendTrace(record, "job_identity_reported", func(event *traceEvent) {
			traceIdentity(record, event)
			event.Repo = record.identity.Repository
		})
		if status.Phase == vm.PhaseReady {
			a.appendTrace(record, "job_hook_released", func(event *traceEvent) {
				traceIdentity(record, event)
				event.Repo = status.Identity.Repository
				event.GenerationSet = generationSet(record)
			})
			record.enter(syncproto.StateReady, now)
		}

	case syncproto.StateAuthorizing:
		status, ok := vms.byID[vm.ID(record.vmID)]
		if !ok || status.Phase == vm.PhaseGone {
			a.failLease(ctx, record, "vm disappeared")
			return
		}
		switch status.Phase {
		case vm.PhaseWorkerReady:
			a.appendTrace(record, "runner_worker_released", func(event *traceEvent) {
				traceAssignment(record, event)
			})
		case vm.PhaseHookBlocked:
			record.identity = identityReport(status.Identity)
			record.enter(syncproto.StateHookBlocked, now)
		case vm.PhaseReady:
			record.identity = identityReport(status.Identity)
			a.appendTrace(record, "job_hook_released", func(event *traceEvent) {
				traceIdentity(record, event)
				event.Repo = status.Identity.Repository
				event.GenerationSet = generationSet(record)
			})
			record.enter(syncproto.StateReady, now)
		case vm.PhaseExited:
			a.finishRunner(ctx, record, status)
		}

	case syncproto.StateReady:
		status, ok := vms.byID[vm.ID(record.vmID)]
		if !ok || status.Phase == vm.PhaseGone {
			a.failLease(ctx, record, "vm disappeared")
			return
		}
		if status.Phase == vm.PhaseExited {
			a.finishRunner(ctx, record, status)
		}

	case syncproto.StateExited:
		// A failed destroy at exit observation leaves the dead runner's VM
		// holding a slot; retry until it is actually gone.
		if record.vmID != "" {
			destroyedVMID := record.vmID
			a.appendTrace(record, "vm_destroy_started", func(event *traceEvent) {
				traceIdentity(record, event)
			})
			a.destroyLeaseVM(ctx, record)
			if record.vmID != "" {
				return
			}
			a.appendTrace(record, "vm_destroy_completed", func(event *traceEvent) {
				traceIdentity(record, event)
				event.VMID = destroyedVMID
			})
		}
		if record.spec.State != syncproto.DesiredSeal {
			return // waiting for the control plane's decision
		}
		if record.checkpoint == nil && record.spec.SealCheckpoint != nil {
			candidate := *record.spec.SealCheckpoint
			record.checkpoint = &candidate
		}
		if record.checkpoint == nil {
			a.failLease(ctx, record, "seal: process checkpoint artifact is missing")
			return
		}
		a.appendTrace(record, "snapshot_seal_started", func(event *traceEvent) {
			traceIdentity(record, event)
			event.GenerationSet = generationSet(record)
			event.Checkpoint = &traceCheckpoint{
				Digest: record.checkpoint.Digest, Version: record.checkpoint.Version,
			}
		})
		pair, err := a.zvols.SealPair(ctx,
			zvol.LeaseID(record.executionLeaseID()),
			zvol.GenerationID(record.spec.SealGeneration))
		if err != nil {
			a.failLease(ctx, record, "seal: "+err.Error())
			return
		}
		record.sealGen = string(pair.Workspace.Generation)
		a.appendTrace(record, "snapshot_seal_completed", func(event *traceEvent) {
			traceIdentity(record, event)
			event.GenerationSet = "workspace:" + string(pair.Workspace.Generation) + ",process:" + string(pair.Process.Generation)
			event.Checkpoint = &traceCheckpoint{
				Digest: record.checkpoint.Digest, Version: record.checkpoint.Version,
			}
		})
		record.enter(syncproto.StateSealed, now)
		a.metrics.SealedGenerations.Add(1)
	}
}

func (a *Agent) preparation(record *lease) vm.Preparation {
	return vm.Preparation{
		Lease: record.spec.LeaseID, JITConfig: record.spec.JITConfig,
		Env: map[string]string{
			"ACTIONS_RUNNER_HOOK_JOB_STARTED": "/usr/local/libexec/postflight-job-started.sh",
		},
	}
}

func (a *Agent) rendezvous(record *lease) vm.Rendezvous {
	execution := record.executionSpec()
	mountpoint := workspaceMountpoint(execution.RepositoryFullName)
	return vm.Rendezvous{
		Lease:               record.spec.LeaseID,
		WorkspaceDevice:     record.device,
		WorkspaceMountpoint: mountpoint,
		ProcessDevice:       record.processDevice,
		CheckpointDigest:    execution.Process.ExpectedDigest,
	}
}

func (a *Agent) authorization(record *lease) vm.Authorization {
	execution := record.executionSpec()
	token := checkoutbundle.DeriveCheckoutToken(a.hostSecret, execution.ExecutionID, execution.AttemptID)
	mountpoint := workspaceMountpoint(execution.RepositoryFullName)
	return vm.Authorization{
		Lease: record.spec.LeaseID, RequestID: record.assignment.RequestID,
		Identity: record.assignment.Identity,
		Env: map[string]string{
			"POSTFLIGHT_HOST_SERVICE_HTTP_ORIGIN": a.cfg.CheckoutGuestOrigin,
			"POSTFLIGHT_CHECKOUT_PATH":            a.cfg.CheckoutPath,
			"POSTFLIGHT_CHECKOUT_TOKEN":           token,
			"POSTFLIGHT_EXECUTION_ID":             execution.ExecutionID,
			"POSTFLIGHT_ATTEMPT_ID":               execution.AttemptID,
			"POSTFLIGHT_WORKSPACE_READY_FILE":     filepath.Join(mountpoint, guestproto.WorkspaceReadyMarker),
		},
	}
}

func identityReport(identity vm.JobIdentity) *syncproto.JobIdentityReport {
	return &syncproto.JobIdentityReport{
		RunID: identity.RunID, RunAttempt: identity.RunAttempt, RunnerName: identity.RunnerName,
		Repository: identity.Repository, WorkflowJob: identity.WorkflowJob,
	}
}

func (a *Agent) routeAssignment(record *lease, assignment vm.Assignment) error {
	if assignment.RequestID == "" || assignment.RunnerName != record.spec.LeaseID ||
		assignment.Identity.RunID == "" || assignment.Identity.RunAttempt <= 0 ||
		assignment.Identity.RunnerName != record.spec.LeaseID || assignment.Identity.Repository == "" ||
		assignment.JobDisplayName == "" {
		return errors.New("incomplete or mismatched listener assignment")
	}
	runID, err := strconv.ParseInt(assignment.Identity.RunID, 10, 64)
	if err != nil || runID <= 0 {
		return fmt.Errorf("invalid provider run id %q", assignment.Identity.RunID)
	}
	candidates := map[string]syncproto.DesiredLease{}
	staged := make([]string, 0, len(a.desired))
	for _, desired := range a.desired {
		staged = append(staged, fmt.Sprintf("%s(run=%d,attempt=%d,repo=%s,job=%s,class=%s,state=%s)",
			executionLeaseID(desired), desired.ProviderRunID, desired.ProviderRunAttempt,
			desired.RepositoryFullName, desired.JobDisplayName, desired.RunnerClass, desired.State))
		if desired.State != syncproto.DesiredRun || desired.RunnerClass != record.spec.RunnerClass ||
			desired.ProviderRunID != runID || desired.ProviderRunAttempt != assignment.Identity.RunAttempt ||
			desired.RepositoryFullName != assignment.Identity.Repository ||
			(desired.JobDisplayName != "" && desired.JobDisplayName != assignment.JobDisplayName) {
			continue
		}
		candidates[executionLeaseID(desired)] = desired
	}
	if len(candidates) != 1 {
		sort.Strings(staged)
		return fmt.Errorf("assignment run=%d attempt=%d repo=%s job=%s class=%s matched %d staged executions: %s",
			runID, assignment.Identity.RunAttempt, assignment.Identity.Repository, assignment.JobDisplayName,
			record.spec.RunnerClass, len(candidates), strings.Join(staged, "; "))
	}
	var selected syncproto.DesiredLease
	for _, candidate := range candidates {
		selected = candidate
	}
	selectedID := executionLeaseID(selected)
	for _, other := range a.leases {
		if other == record || other.assignment == nil {
			continue
		}
		if other.executionLeaseID() == selectedID {
			return fmt.Errorf("execution %s was already routed to listener %s", selectedID, other.spec.LeaseID)
		}
	}
	capturedAssignment := assignment
	capturedExecution := selected
	record.assignment = &capturedAssignment
	record.execution = &capturedExecution
	return nil
}

func (a *Agent) bindAssigned(ctx context.Context, record *lease, id vm.ID) error {
	if record.assignment == nil || record.execution == nil {
		return errors.New("assignment has not been routed")
	}
	a.appendTrace(record, "generation_materialization_started", func(event *traceEvent) {
		traceAssignment(record, event)
		event.Repo = record.execution.RepositoryFullName
	})
	if err := a.materialize(ctx, record, *record.execution); err != nil {
		return err
	}
	a.appendTrace(record, "generation_resolved", func(event *traceEvent) {
		event.Repo = record.execution.RepositoryFullName
		event.GenerationSet = generationSet(record)
		event.Volumes = traceVolumes(record, false)
	})
	record.hostBeforeUnixNS = time.Now().UnixNano()
	if err := a.vms.Rendezvous(ctx, id, a.rendezvous(record)); err != nil {
		return err
	}
	a.appendTrace(record, "rendezvous_dispatched", func(event *traceEvent) {
		traceAssignment(record, event)
		event.Repo = record.execution.RepositoryFullName
		event.GenerationSet = generationSet(record)
		event.Volumes = traceVolumes(record, true)
	})
	return nil
}

func (a *Agent) materialize(ctx context.Context, record *lease, execution syncproto.DesiredLease) error {
	volume, err := a.zvols.EnsureWorkspace(ctx,
		zvol.LeaseID(executionLeaseID(execution)),
		zvol.GenerationID(execution.Workspace.Generation), execution.Workspace.SizeBytes)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	processGeneration := zvol.GenerationID("")
	if execution.Process.ExpectedDigest != "" {
		processGeneration = zvol.GenerationID(execution.Process.Generation)
	}
	processSize := execution.Process.SizeBytes
	if processSize == 0 {
		processSize = defaultProcessVolumeSizeBytes
	}
	processVolume, err := a.zvols.EnsureProcess(ctx,
		zvol.LeaseID(executionLeaseID(execution)), processGeneration, processSize)
	if err != nil {
		return fmt.Errorf("process: %w", err)
	}
	record.device, record.volume = volume.Device, volume
	record.processDevice, record.processVolume = processVolume.Device, processVolume
	return nil
}

func (a *Agent) finishRunner(ctx context.Context, record *lease, status vm.Status) {
	record.exit = status.ExitCode
	record.enter(syncproto.StateExited, a.now())
	a.appendTrace(record, "runner_exit_observed", func(event *traceEvent) {
		traceIdentity(record, event)
		event.GenerationSet = generationSet(record)
	})
	if record.device != "" {
		a.appendTrace(record, "checkpoint_started", func(event *traceEvent) {
			traceIdentity(record, event)
			event.GenerationSet = generationSet(record)
		})
		artifact, err := a.vms.Quiesce(ctx, vm.ID(record.vmID))
		if err != nil {
			a.failLease(ctx, record, "quiesce: "+err.Error())
			return
		}
		a.appendOriginTiming(record, artifact.Timing)
		record.checkpoint = &syncproto.CheckpointArtifact{Digest: artifact.Digest, Version: artifact.Version}
		a.appendTrace(record, "checkpoint_completed", func(event *traceEvent) {
			traceIdentity(record, event)
			event.GenerationSet = generationSet(record)
			event.Checkpoint = &traceCheckpoint{Digest: artifact.Digest, Version: artifact.Version}
		})
	}
	destroyedVMID := record.vmID
	a.appendTrace(record, "vm_destroy_started", func(event *traceEvent) {
		traceIdentity(record, event)
	})
	a.destroyLeaseVM(ctx, record)
	if record.vmID == "" {
		a.appendTrace(record, "vm_destroy_completed", func(event *traceEvent) {
			traceIdentity(record, event)
			event.VMID = destroyedVMID
		})
	}
}

// runnerWorkRoot mirrors the golden image's runner install: the runner
// materializes GITHUB_WORKSPACE at _work/<repo>/<repo>, and the workspace
// zvol must already be mounted there when the runner starts.
const runnerWorkRoot = "/opt/actions-runner/_work"

// The single initial product class has 16 GiB RAM. CRIU images are often
// sparse, but the volume must accommodate a worst-case dump plus metadata.
const defaultProcessVolumeSizeBytes int64 = 24 << 30

func workspaceMountpoint(repositoryFullName string) string {
	name := repositoryFullName
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return runnerWorkRoot + "/" + name + "/" + name
}

func (a *Agent) cancelLease(ctx context.Context, record *lease) {
	a.destroyLeaseVM(ctx, record)
	record.enter(syncproto.StateCancelled, a.now())
}

func (a *Agent) failLease(ctx context.Context, record *lease, reason string) {
	a.destroyLeaseVM(ctx, record)
	record.reason = reason
	record.enter(syncproto.StateFailed, a.now())
	a.metrics.FailedLeases.Add(1)
	a.logger.Error("lease failed", "lease", record.spec.LeaseID, "reason", reason)
}

func (a *Agent) destroyLeaseVM(ctx context.Context, record *lease) {
	if record.vmID == "" {
		return
	}
	if err := a.vms.Destroy(ctx, vm.ID(record.vmID)); err != nil {
		a.logger.Error("destroying lease vm", "lease", record.spec.LeaseID, "err", err)
		return
	}
	record.vmID = ""
}

// reapGenerations executes the control plane's reap verbs. This is the only
// code path that destroys a sealed generation: node-local generations are
// the only copy, so deletion is never hostd's own idea.
func (a *Agent) reapGenerations(ctx context.Context) {
	referenced := map[zvol.GenerationID]bool{}
	for _, desired := range a.desired {
		if desired.Workspace.Generation != "" {
			referenced[zvol.GenerationID(desired.Workspace.Generation)] = true
		}
	}
	for _, generation := range a.reap {
		if referenced[generation] {
			continue
		}
		err := a.zvols.DestroyProcessGeneration(ctx, generation)
		if errors.Is(err, zvol.ErrNotFound) {
			err = nil
		}
		if err == nil {
			err = a.zvols.DestroyGeneration(ctx, generation)
		}
		switch {
		case err == nil:
			a.metrics.ReapedGenerations.Add(1)
		case errors.Is(err, zvol.ErrNotFound):
			// Already gone; the next inventory report confirms it.
		case errors.Is(err, zvol.ErrBusy):
			// A workspace still clones it; retry on a later tick once the
			// dependent lease is collected.
		default:
			a.logger.Error("reaping generation", "generation", generation, "err", err)
		}
	}
}

func sortedLeaseIDs(leases map[string]*lease) []string {
	ids := make([]string, 0, len(leases))
	for id := range leases {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func sortVMIDs(ids []vm.ID) {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
}

// workspaceLease extracts the lease ID a workspace volume name encodes, or
// "" if the name is not a workspace path.
func workspaceLease(name string) string {
	if i := strings.LastIndex(name, "/ws/"); i >= 0 {
		return name[i+len("/ws/"):]
	}
	return ""
}
