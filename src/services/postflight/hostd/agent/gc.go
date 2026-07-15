package agent

import (
	"context"
	"errors"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

// collectOrphans reclaims derived state whose owner is gone. Three shapes:
// terminal leases the control plane has acknowledged (by omitting them from
// the desired set), VMs bound to leases hostd no longer knows, and workspace
// volumes on disk with no corresponding lease. Sealed generations are never
// touched here — reaping those is exclusively a control-plane verb.
//
// The synced gate in Tick means none of this runs before the first
// successful exchange after a restart, when "unknown" would still mean "not
// heard about yet" rather than "orphaned".
func (a *Agent) collectOrphans(ctx context.Context, vms *vmView) {
	// Terminal, acknowledged leases: destroy the workspace, forget the lease.
	for _, id := range sortedLeaseIDs(a.leases) {
		record := a.leases[id]
		if !record.state.Terminal() {
			continue
		}
		if _, stillDesired := a.desired[id]; stillDesired {
			continue
		}
		if a.quarantined[id] {
			// The control plane still names this lease; we just could not
			// parse the spec. Absence from the desired set is not an ack.
			continue
		}
		err := a.zvols.DestroyWorkspace(ctx, zvol.LeaseID(id))
		switch {
		case err == nil, errors.Is(err, zvol.ErrNotFound):
			delete(a.leases, id)
		case errors.Is(err, zvol.ErrBusy):
			// A VM still holds the volume open. stepLease retries the VM
			// destroy for terminal records every tick, so a later tick
			// frees the volume and this collection retries.
		default:
			a.logger.Error("collecting workspace", "lease", id, "err", err)
		}
	}

	// VMs whose lease hostd does not know: either a crash predates the
	// lease's terminal report, or the control plane forgot the lease while
	// the VM lived. The desired set is truth; unknown means destroy.
	for id, status := range vms.byID {
		if status.Lease == "" {
			continue // pool VMs belong to the governor
		}
		if _, known := a.leases[status.Lease]; known {
			continue
		}
		if a.quarantined[status.Lease] {
			continue // the control plane still wants it; we just can't parse the spec
		}
		if err := a.vms.Destroy(ctx, id); err != nil {
			a.logger.Error("collecting orphan vm", "vm", id, "err", err)
			continue
		}
		a.metrics.OrphansDestroyed.Add(1)
	}

	// Workspace volumes with no lease record: leftovers from a crash after
	// the control plane already forgot the lease.
	_, workspaces, err := a.zvols.Inventory(ctx)
	if err != nil {
		a.logger.Error("inventory for gc", "err", err)
		return
	}
	for _, workspace := range workspaces {
		leaseID := workspaceLease(workspace.Name)
		if leaseID == "" {
			continue
		}
		if _, known := a.leases[leaseID]; known {
			continue
		}
		if _, desired := a.desired[leaseID]; desired {
			continue
		}
		if a.quarantined[leaseID] {
			continue // rejected spec, not an orphan; preserve the workspace
		}
		err := a.zvols.DestroyWorkspace(ctx, zvol.LeaseID(leaseID))
		switch {
		case err == nil:
			a.metrics.OrphansDestroyed.Add(1)
		case errors.Is(err, zvol.ErrNotFound):
		case errors.Is(err, zvol.ErrBusy):
			// Nothing hostd knows about should hold an orphan open; a
			// dependent un-promoted clone from a crashed seal can. Surface it
			// — this leak is otherwise invisible to inventory.
			a.logger.Warn("orphan workspace is busy", "workspace", workspace.Name)
		default:
			a.logger.Error("collecting orphan workspace", "workspace", workspace.Name, "err", err)
		}
	}
}
