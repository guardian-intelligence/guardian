package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/checkoutbundle"
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
			view.warm[status.Class] = append(view.warm[status.Class], status.ID)
		}
	}
	for _, ids := range view.warm {
		sortVMIDs(ids)
	}
	return view, nil
}

func (a *Agent) stepLease(ctx context.Context, record *lease, vms *vmView) {
	if record.state.terminal() {
		return
	}
	now := a.now()

	// Cancellation wins over everything except states that hold nothing.
	if record.spec.State == DesiredCancel {
		a.cancelLease(ctx, record)
		return
	}

	// Deadline enforcement: a lease stuck in any state releases its slot.
	if deadline, ok := stateDeadlines[record.state]; ok && now.Sub(record.since) > deadline {
		a.failLease(ctx, record, fmt.Sprintf("deadline exceeded in %s", record.state))
		return
	}

	switch record.state {
	case StatePending:
		volume, err := a.zvols.EnsureWorkspace(ctx,
			zvol.LeaseID(record.spec.LeaseID),
			zvol.GenerationID(record.spec.Workspace.Generation),
			record.spec.Workspace.SizeBytes)
		if err != nil {
			// A missing clone source means the catalog and this host's
			// inventory disagree; failing (rather than silently running
			// cold) lets the control plane reconcile and reschedule.
			a.failLease(ctx, record, "materialize: "+err.Error())
			return
		}
		record.device = volume.Device
		record.enter(StateClaiming, now)

	case StateClaiming:
		// Crash recovery first: a VM may already carry this lease.
		if status, ok := vms.byLease[record.spec.LeaseID]; ok {
			record.vmID = string(status.ID)
			record.enter(StateAssigning, now)
			return
		}
		class := vm.Class(record.spec.RunnerClass)
		candidates := vms.warm[class]
		if len(candidates) == 0 {
			return // pool governor is refilling; deadline bounds the wait
		}
		id := candidates[0]
		vms.warm[class] = candidates[1:]
		if err := a.vms.Assign(ctx, id, a.assignment(record)); err != nil {
			a.failLease(ctx, record, "assign: "+err.Error())
			return
		}
		record.vmID = string(id)
		record.enter(StateAssigning, now)

	case StateAssigning, StateReady:
		status, ok := vms.byID[vm.ID(record.vmID)]
		if !ok || status.Phase == vm.PhaseGone {
			a.failLease(ctx, record, "vm disappeared")
			return
		}
		switch status.Phase {
		case vm.PhaseReady:
			if record.state == StateAssigning {
				record.enter(StateReady, now)
			}
		case vm.PhaseExited:
			record.exit = status.ExitCode
			record.enter(StateExited, now)
			// Destroy-and-refill: the slot frees as soon as the runner is
			// done; the workspace volume stays for a possible seal.
			a.destroyLeaseVM(ctx, record)
		}

	case StateExited:
		if record.spec.State != DesiredSeal {
			return // waiting for the control plane's decision
		}
		snapshot, err := a.zvols.SealWorkspace(ctx,
			zvol.LeaseID(record.spec.LeaseID),
			zvol.GenerationID(record.spec.SealGeneration))
		if err != nil {
			a.failLease(ctx, record, "seal: "+err.Error())
			return
		}
		record.sealGen = string(snapshot.Generation)
		record.enter(StateSealed, now)
		a.metrics.SealedGenerations.Add(1)
	}
}

// assignment builds what the guest needs: the workspace device, the JIT
// registration blob, and the checkout environment with a token derived from
// this host's secret — the token never exists anywhere but here and in the
// guest's environment.
func (a *Agent) assignment(record *lease) vm.Assignment {
	token := checkoutbundle.DeriveCheckoutToken(a.hostSecret, record.spec.ExecutionID, record.spec.AttemptID)
	return vm.Assignment{
		Lease:           record.spec.LeaseID,
		WorkspaceDevice: record.device,
		JITConfig:       record.spec.JITConfig,
		Env: map[string]string{
			"POSTFLIGHT_HOST_SERVICE_HTTP_ORIGIN": a.cfg.CheckoutGuestOrigin,
			"POSTFLIGHT_CHECKOUT_PATH":            a.cfg.CheckoutPath,
			"POSTFLIGHT_CHECKOUT_TOKEN":           token,
			"POSTFLIGHT_EXECUTION_ID":             record.spec.ExecutionID,
			"POSTFLIGHT_ATTEMPT_ID":               record.spec.AttemptID,
		},
	}
}

func (a *Agent) cancelLease(ctx context.Context, record *lease) {
	a.destroyLeaseVM(ctx, record)
	record.enter(StateCancelled, a.now())
}

func (a *Agent) failLease(ctx context.Context, record *lease, reason string) {
	a.destroyLeaseVM(ctx, record)
	record.reason = reason
	record.enter(StateFailed, a.now())
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
	for _, generation := range a.reap {
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
