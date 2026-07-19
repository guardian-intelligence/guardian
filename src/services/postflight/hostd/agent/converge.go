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
			switch status.Phase {
			case vm.PhaseListening:
				record.enter(syncproto.StateListening, now)
			case vm.PhaseHookBlocked:
				record.identity = &syncproto.JobIdentityReport{
					RunID: status.Identity.RunID, RunAttempt: status.Identity.RunAttempt,
					RunnerName: status.Identity.RunnerName, Repository: status.Identity.Repository,
					WorkflowJob: status.Identity.WorkflowJob,
				}
				record.enter(syncproto.StateHookBlocked, now)
			case vm.PhaseReady:
				record.enter(syncproto.StateReady, now)
			default:
				record.enter(syncproto.StateAssigning, now)
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
		// Claim the VM before preparation: an ambiguous delivery failure must
		// destroy this listener, never return it to the warm pool.
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
		case vm.PhaseHookBlocked:
			record.identity = &syncproto.JobIdentityReport{
				RunID: status.Identity.RunID, RunAttempt: status.Identity.RunAttempt,
				RunnerName: status.Identity.RunnerName, Repository: status.Identity.Repository,
				WorkflowJob: status.Identity.WorkflowJob,
			}
			record.enter(syncproto.StateHookBlocked, now)
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
		if !record.spec.RendezvousAuthorized {
			return
		}
		if err := validateRendezvousIdentity(record); err != nil {
			a.failLease(ctx, record, "rendezvous identity: "+err.Error())
			return
		}
		a.appendTrace(record, "assignment_observed", func(event *traceEvent) {
			traceIdentity(record, event)
		})
		a.appendTrace(record, "job_hook_blocked", func(event *traceEvent) {
			traceIdentity(record, event)
		})
		a.appendTrace(record, "job_identity_reported", func(event *traceEvent) {
			traceIdentity(record, event)
			event.Repo = record.identity.Repository
		})
		volume, err := a.zvols.EnsureWorkspace(ctx,
			zvol.LeaseID(executionLeaseID(record.spec)),
			zvol.GenerationID(record.spec.Workspace.Generation),
			record.spec.Workspace.SizeBytes)
		if err != nil {
			a.failLease(ctx, record, "materialize: "+err.Error())
			return
		}
		record.device = volume.Device
		record.volume = volume
		a.appendTrace(record, "generation_resolved", func(event *traceEvent) {
			traceIdentity(record, event)
			event.Repo = record.identity.Repository
			event.GenerationSet = generationSet(record)
			event.Volumes = traceVolumes(record, false)
		})
		record.hostBeforeUnixNS = time.Now().UnixNano()
		if err := a.vms.Rendezvous(ctx, vm.ID(record.vmID), a.rendezvous(record)); err != nil {
			a.failLease(ctx, record, "rendezvous: "+err.Error())
			return
		}
		a.appendTrace(record, "rendezvous_bound", func(event *traceEvent) {
			traceIdentity(record, event)
			event.Repo = record.identity.Repository
			event.GenerationSet = generationSet(record)
			event.Volumes = traceVolumes(record, true)
		})
		record.enter(syncproto.StateBinding, now)

	case syncproto.StateBinding:
		status, ok := vms.byID[vm.ID(record.vmID)]
		if !ok || status.Phase == vm.PhaseGone {
			a.failLease(ctx, record, "vm disappeared")
			return
		}
		switch status.Phase {
		case vm.PhaseReady:
			hostAfter := time.Now().UnixNano()
			a.appendTrace(record, "mounts_ready", func(event *traceEvent) {
				traceIdentity(record, event)
				event.Repo = record.identity.Repository
				event.GenerationSet = generationSet(record)
				event.Volumes = traceVolumes(record, true)
			})
			a.appendTrace(record, "clock_checked", func(event *traceEvent) {
				traceIdentity(record, event)
				event.Repo = record.identity.Repository
				event.GenerationSet = generationSet(record)
				event.Clock = &traceClock{
					HostBeforeUnixNS: record.hostBeforeUnixNS, HostAfterUnixNS: hostAfter,
					GuestUnixNS: status.Clock.UnixNS, MaxSkewNS: int64(5 * time.Second),
					GuestSynchronized: status.Clock.Synchronized, Clocksource: status.Clock.Clocksource,
					AfterRestore: status.Clock.AfterRestore,
				}
			})
			a.appendTrace(record, "job_hook_released", func(event *traceEvent) {
				traceIdentity(record, event)
				event.Repo = record.identity.Repository
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
			a.destroyLeaseVM(ctx, record)
		}
		if record.spec.State != syncproto.DesiredSeal {
			return // waiting for the control plane's decision
		}
		snapshot, err := a.zvols.SealWorkspace(ctx,
			zvol.LeaseID(executionLeaseID(record.spec)),
			zvol.GenerationID(record.spec.SealGeneration))
		if err != nil {
			a.failLease(ctx, record, "seal: "+err.Error())
			return
		}
		record.sealGen = string(snapshot.Generation)
		record.enter(syncproto.StateSealed, now)
		a.metrics.SealedGenerations.Add(1)
	}
}

func (a *Agent) preparation(record *lease) vm.Preparation {
	return vm.Preparation{
		Lease: record.spec.LeaseID, JITConfig: record.spec.JITConfig,
		Env: map[string]string{
			"ACTIONS_RUNNER_HOOK_JOB_STARTED": "/usr/local/libexec/postflight-job-started.sh",
			"POSTFLIGHT_RENDEZVOUS_DIR":       "/run/postflight-rendezvous",
		},
	}
}

func (a *Agent) rendezvous(record *lease) vm.Rendezvous {
	token := checkoutbundle.DeriveCheckoutToken(a.hostSecret, record.spec.ExecutionID, record.spec.AttemptID)
	mountpoint := workspaceMountpoint(record.spec.RepositoryFullName)
	return vm.Rendezvous{
		Lease:               record.spec.LeaseID,
		WorkspaceDevice:     record.device,
		WorkspaceMountpoint: mountpoint,
		Env: map[string]string{
			"POSTFLIGHT_HOST_SERVICE_HTTP_ORIGIN": a.cfg.CheckoutGuestOrigin,
			"POSTFLIGHT_CHECKOUT_PATH":            a.cfg.CheckoutPath,
			"POSTFLIGHT_CHECKOUT_TOKEN":           token,
			"POSTFLIGHT_EXECUTION_ID":             record.spec.ExecutionID,
			"POSTFLIGHT_ATTEMPT_ID":               record.spec.AttemptID,
			"POSTFLIGHT_WORKSPACE_READY_FILE":     filepath.Join(mountpoint, guestproto.WorkspaceReadyMarker),
		},
	}
}

func validateRendezvousIdentity(record *lease) error {
	if record.identity == nil {
		return errors.New("blocked hook reported no identity")
	}
	identity := record.identity
	if identity.RunID != strconv.FormatInt(record.spec.ProviderRunID, 10) {
		return fmt.Errorf("run %s does not match %d", identity.RunID, record.spec.ProviderRunID)
	}
	if identity.RunAttempt != record.spec.ProviderRunAttempt {
		return fmt.Errorf("attempt %d does not match %d", identity.RunAttempt, record.spec.ProviderRunAttempt)
	}
	if identity.RunnerName != record.spec.AssignedRunnerName || identity.RunnerName != record.spec.LeaseID {
		return fmt.Errorf("runner %q does not match observed %q", identity.RunnerName, record.spec.AssignedRunnerName)
	}
	if identity.Repository != record.spec.RepositoryFullName {
		return fmt.Errorf("repository %q does not match %q", identity.Repository, record.spec.RepositoryFullName)
	}
	return nil
}

func (a *Agent) finishRunner(ctx context.Context, record *lease, status vm.Status) {
	record.exit = status.ExitCode
	record.enter(syncproto.StateExited, a.now())
	if record.device != "" {
		if err := a.vms.Quiesce(ctx, vm.ID(record.vmID)); err != nil {
			a.failLease(ctx, record, "quiesce: "+err.Error())
			return
		}
	}
	a.destroyLeaseVM(ctx, record)
}

// runnerWorkRoot mirrors the golden image's runner install: the runner
// materializes GITHUB_WORKSPACE at _work/<repo>/<repo>, and the workspace
// zvol must already be mounted there when the runner starts.
const runnerWorkRoot = "/opt/actions-runner/_work"

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
		err := a.zvols.DestroyGeneration(ctx, generation)
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
