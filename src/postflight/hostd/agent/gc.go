package agent

import (
	"context"
	"errors"

	"github.com/guardian-intelligence/guardian/src/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/postflight/hostd/zvol"
)

// collectOrphans reclaims assignment-derived volumes only after the control
// plane acknowledges a terminal report by omitting it. Pool members are never
// inferred orphaned from omission: an explicit recycle state owns their fate.
func (a *Agent) collectOrphans(ctx context.Context, _ *vmView, assignments map[string]*assignment, desiredAssignments map[string]syncproto.DesiredAssignment, quarantinedJobs map[string]bool) {
	for id, record := range assignments {
		record.mu.Lock()
		if !record.state.Terminal() || quarantinedJobs[id] {
			record.mu.Unlock()
			continue
		}
		if _, desired := desiredAssignments[id]; desired {
			record.mu.Unlock()
			continue
		}
		err := a.destroyAssignmentVolumes(ctx, zvol.AssignmentID(id))
		record.mu.Unlock()
		switch {
		case err == nil, errors.Is(err, zvol.ErrNotFound):
			a.mu.Lock()
			if a.assignments[id] == record {
				if _, desired := a.desiredAssignments[id]; !desired && !a.quarantinedJobs[id] {
					delete(a.assignments, id)
				}
			}
			a.mu.Unlock()
		case errors.Is(err, zvol.ErrBusy):
		default:
			a.logger.Error("collecting assignment volumes", "assignment_id", id, "err", err)
		}
	}

	_, workspaces, err := a.zvols.Inventory(ctx)
	if err != nil {
		a.logger.Error("inventory for gc", "err", err)
		return
	}
	known := map[string]bool{}
	for id := range assignments {
		known[id] = true
	}
	for id := range desiredAssignments {
		known[id] = true
	}
	for _, workspace := range workspaces {
		assignmentID := workspaceAssignment(workspace.Name)
		if assignmentID == "" || known[assignmentID] || quarantinedJobs[assignmentID] {
			continue
		}
		err := a.destroyAssignmentVolumes(ctx, zvol.AssignmentID(assignmentID))
		if err == nil {
			a.metrics.OrphansDestroyed.Add(1)
		} else if !errors.Is(err, zvol.ErrNotFound) && !errors.Is(err, zvol.ErrBusy) {
			a.logger.Error("collecting orphan assignment volumes", "assignment_id", assignmentID, "err", err)
		}
	}

	a.collectTransfers(ctx, assignments, desiredAssignments)
}

// collectTransfers reclaims the transfer cache. Cached generations are
// derived state — re-fetchable from their owning host — so unlike resident
// generations they need no control-plane reap verb: any cache entry no live
// assignment routes at goes. ZFS keeps the sweep safe; an entry a sealed
// generation still descends from refuses destruction as busy.
func (a *Agent) collectTransfers(ctx context.Context, assignments map[string]*assignment, desiredAssignments map[string]syncproto.DesiredAssignment) {
	store, ok := a.zvols.(zvol.TransferStore)
	if !ok {
		return
	}
	transfers, err := store.ListTransfers(ctx)
	if err != nil {
		a.logger.Error("listing transfer cache for gc", "err", err)
		return
	}
	if len(transfers) == 0 {
		return
	}
	referenced := map[zvol.GenerationID]bool{}
	for _, record := range assignments {
		record.mu.Lock()
		if generation := record.spec.Workspace.Generation; generation != "" {
			referenced[zvol.GenerationID(generation)] = true
		}
		record.mu.Unlock()
	}
	for _, desired := range desiredAssignments {
		if generation := desired.Workspace.Generation; generation != "" {
			referenced[zvol.GenerationID(generation)] = true
		}
	}
	for _, generation := range transfers {
		if referenced[generation] {
			continue
		}
		err := store.DestroyTransfer(ctx, generation)
		if err == nil {
			a.metrics.TransfersCollected.Add(1)
		} else if !errors.Is(err, zvol.ErrNotFound) && !errors.Is(err, zvol.ErrBusy) {
			a.logger.Error("collecting transfer cache", "generation", generation, "err", err)
		}
	}
}

func (a *Agent) destroyAssignmentVolumes(ctx context.Context, id zvol.AssignmentID) error {
	if err := a.zvols.DestroyProcess(ctx, id); err != nil && !errors.Is(err, zvol.ErrNotFound) {
		return err
	}
	if err := a.zvols.DestroyTool(ctx, id); err != nil && !errors.Is(err, zvol.ErrNotFound) {
		return err
	}
	return a.zvols.DestroyWorkspace(ctx, id)
}
