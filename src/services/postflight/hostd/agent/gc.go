package agent

import (
	"context"
	"errors"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

// collectOrphans reclaims assignment-derived volumes only after the control
// plane acknowledges a terminal report by omitting it. Pool members are never
// inferred orphaned from omission: an explicit recycle state owns their fate.
func (a *Agent) collectOrphans(ctx context.Context, _ *vmView) {
	for id, record := range a.assignments {
		if !record.state.Terminal() || a.quarantinedJobs[id] {
			continue
		}
		if _, desired := a.desiredAssignments[id]; desired {
			continue
		}
		err := a.destroyAssignmentVolumes(ctx, zvol.AssignmentID(id))
		switch {
		case err == nil, errors.Is(err, zvol.ErrNotFound):
			delete(a.assignments, id)
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
	for id := range a.assignments {
		known[id] = true
	}
	for id := range a.desiredAssignments {
		known[id] = true
	}
	for _, workspace := range workspaces {
		assignmentID := workspaceAssignment(workspace.Name)
		if assignmentID == "" || known[assignmentID] || a.quarantinedJobs[assignmentID] {
			continue
		}
		err := a.destroyAssignmentVolumes(ctx, zvol.AssignmentID(assignmentID))
		if err == nil {
			a.metrics.OrphansDestroyed.Add(1)
		} else if !errors.Is(err, zvol.ErrNotFound) && !errors.Is(err, zvol.ErrBusy) {
			a.logger.Error("collecting orphan assignment volumes", "assignment_id", assignmentID, "err", err)
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
